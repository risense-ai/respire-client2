import {
  $,
  all,
  h,
  number,
  date,
  raw,
  api,
  execute,
  action,
  notify,
  input,
  dialog,
  confirm,
  showText,
  rows,
  summary,
} from "../ui/core.js";
import { t } from "../ui/i18n.js";
import { heading } from "../pages/overview.js";
import { entry, list, inventory, kindNames } from "./model.js";
import { editor } from "./editor.js";
import { renderGraph } from "./graph.js";
import { recordUndo, undoLast } from "./undo.js";
import { memoryInteractions } from "./interactions.js";
import { memoryTools } from "./tools.js";
import { buildIndex, visibleRows, searchForceIds } from "./tree-model.js";

const VIEW_MODES = [
  ["tree", "树形"],
  ["list", "列表"],
  ["card", "卡片"],
  ["graph", "图谱"],
];
// Rows per page for the list and card pagers, matching the console dashboard.
const PAGE_SIZE = 24;

const kindLabel = (row) =>
  kindNames[String(row.kind || "context").toLowerCase()] || row.kind;
const whenText = (row) => date(row.updated_at || row.created_at);
const tagList = (row) =>
  Array.isArray(row.tags) ? row.tags.filter(Boolean) : [];

export function memoryWorkspace(root, ctx) {
  let records = [],
    selected = ctx.params?.get("node") || "",
    selectedEntry = null,
    renderedEntry = null,
    renderedContext = null,
    readerToken = null,
    expanded = new Set(),
    filter = "",
    mode = "tree",
    busy = false,
    limit = 500,
    shown = 200,
    listPage = 1,
    selectionVersion = 0,
    revision = "",
    autoReloadAt = 0,
    loaded = false,
    catalogContext = null,
    statusUnavailable = false,
    actionEpoch = 0,
    searchCaption = "";
  let metadataCap = null;
  const checked = new Set();
  const ownedDialogs = new Set();
  function keepDialog(element) {
    if (!element) return;
    ownedDialogs.add(element);
    element.addEventListener("close", () => ownedDialogs.delete(element), { once: true });
    return element;
  }
  function closeMemoryDialogs() {
    for (const element of ownedDialogs) element.close();
    ownedDialogs.clear();
  }
  const graphState = {};
  root.innerHTML =
    heading(
      "你的记忆，清晰可见。",
      "浏览因果树、检索整库、编辑正文，或调整记忆之间的关系。",
      `<button data-new class="primary">新建记忆</button>`,
    ) +
    `<div class="memory-toolbar"><div class="search-field"><input data-search placeholder="筛选标题 / ID，或回车检索整库" aria-label="搜索记忆"><button data-search-submit>检索整库</button></div><div class="tabs view-tabs" role="tablist" aria-label="视图切换">${VIEW_MODES.map(
      ([value, label]) =>
        `<button role="tab" data-view="${value}" aria-selected="${value === mode}" class="${value === mode ? "active" : ""}">${label}</button>`,
    ).join("")}</div><button data-merge disabled>合并所选</button><button data-more>载入更多</button></div><div class="memory-caption"><span data-count>正在读取…</span><button data-clear-search hidden>返回目录</button><button data-focus title="铺满窗口" aria-label="铺满窗口" aria-pressed="false"><svg class="icon-expand" width="14" height="14" viewBox="0 0 16 16" aria-hidden="true"><path d="M2 6V2h4M10 2h4v4M14 10v4h-4M6 14H2v-4" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg><svg class="icon-restore" width="14" height="14" viewBox="0 0 16 16" aria-hidden="true"><path d="M6 2v4H2M14 6h-4V2M10 14v-4h4M2 10h4v4" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg></button></div><div class="memory-layout"><div class="memory-index" role="tree" aria-label="记忆树"></div><div id="memory-resizer" aria-hidden="true"></div><article class="memory-reader"><div class="empty">选择左侧记忆，查看完整内容。</div></article></div>`;
  const tree = $(".memory-index", root),
    reader = $(".memory-reader", root),
    count = $("[data-count]", root);
  // Draggable split between the tree index and the reader; width persists in
  // preferences (0 keeps the proportional default).
  const INDEX_MIN = 185,
    INDEX_MAX = 640;
  const savedIndexWidth = Number(ctx.desktop?.preferences?.memoryIndexWidth) || 0;
  if (savedIndexWidth >= INDEX_MIN)
    document.documentElement.style.setProperty(
      "--memory-index-w",
      `${savedIndexWidth}px`,
    );
  $("#memory-resizer", root).addEventListener("mousedown", (event) => {
    if (root.classList.contains("graph-mode")) return;
    event.preventDefault();
    document.body.classList.add("resizing");
    const apply = (clientX) => {
      const layout = $(".memory-layout", root).getBoundingClientRect();
      const width = Math.round(
        Math.min(INDEX_MAX, Math.max(INDEX_MIN, clientX - layout.left)),
      );
      document.documentElement.style.setProperty(
        "--memory-index-w",
        `${width}px`,
      );
      return width;
    };
    const move = (e) => apply(e.clientX);
    const up = (e) => {
      window.removeEventListener("mousemove", move);
      window.removeEventListener("mouseup", up);
      document.body.classList.remove("resizing");
      const width = apply(e.clientX);
      ctx
        .savePreferences({ memoryIndexWidth: width })
        .catch((error) => notify(error.message, true));
    };
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
  });
  const undoButton = document.createElement("button");
  undoButton.textContent = "撤销";
  undoButton.dataset.undo = "";
  undoButton.title = "Ctrl / Command Z：撤销未发生冲突的操作";
  $(".memory-toolbar", root).append(undoButton);
  // Focus mode: the tree and reader fill the window until toggled back.
  function setFocusMode(on) {
    root.classList.toggle("focus-mode", on);
    document.body.classList.toggle("memory-focus", on);
    const button = $("[data-focus]", root);
    button.title = on ? "退出铺满窗口" : "铺满窗口";
    button.setAttribute("aria-label", button.title);
    button.setAttribute("aria-pressed", String(on));
  }
  $("[data-focus]", root).addEventListener("click", () =>
    setFocusMode(root.classList.contains("focus-mode") ? false : true),
  );
  const textMatch = (row) =>
    `${row.title || ""} ${row.id}`.toLowerCase().includes(filter.trim().toLowerCase());
  const paged = (values) => {
    const pages = Math.max(1, Math.ceil(values.length / PAGE_SIZE));
    listPage = Math.min(Math.max(1, listPage), pages);
    return {
      slice: values.slice((listPage - 1) * PAGE_SIZE, listPage * PAGE_SIZE),
      pages,
      page: listPage,
      total: values.length,
    };
  };
  const pager = ({ pages, page, total }) =>
    pages > 1
      ? `<nav class="pager" aria-label="分页"><button data-page-set="-1" ${page <= 1 ? "disabled" : ""}>上一页</button><span class="pager-state">第 ${page} / ${pages} 页 · 共 ${number(total)} 条</span><button data-page-set="1" ${page >= pages ? "disabled" : ""}>下一页</button></nav>`
      : "";
  const memoryRow = ({ row, depth = 0, kids = 0, open = false }, folder) =>
    `<div class="memory-row ${row.id === selected ? "active" : ""}${checked.has(row.id) ? " selected" : ""}" data-id="${h(row.id)}" data-level="${Math.min(depth, 8)}" role="treeitem" aria-selected="${checked.has(row.id) || row.id === selected}" ${kids ? `aria-expanded="${Boolean(open)}"` : ""}><button class="tree-toggle" data-expand="${h(row.id)}" ${kids ? "" : "disabled"} aria-label="展开或折叠">${kids ? (open ? "▾" : "▸") : "·"}</button><button class="memory-title" data-select="${h(row.id)}" title="${h(row.title)}">${h(row.title || row.id.slice(0, 8))}</button></div>`;
  const listRow = (row) =>
    `<div class="memory-row list-row ${row.id === selected ? "active" : ""}${checked.has(row.id) ? " selected" : ""}" data-id="${h(row.id)}" role="row"><span class="memory-cell cell-kind"><span class="badge">${h(kindLabel(row))}</span></span><button class="memory-title cell-title" data-select="${h(row.id)}" title="${h(row.title)}">${h(row.title || row.id.slice(0, 8))}</button><span class="memory-cell cell-project">${h(row.project || "—")}</span><span class="memory-cell cell-updated">${h(whenText(row))}</span><span class="memory-cell cell-tags">${h(tagList(row).join(", ") || "—")}</span></div>`;
  const memoryCard = (row) => {
    const tags = tagList(row);
    return `<div class="memory-card ${row.id === selected ? "active" : ""}${checked.has(row.id) ? " selected" : ""}" data-id="${h(row.id)}" data-card="${h(row.id)}" data-select="${h(row.id)}" data-kind="${h(String(row.kind || "context").toLowerCase())}" tabindex="0" role="button" aria-selected="${row.id === selected || checked.has(row.id)}"><span class="badge">${h(kindLabel(row))}</span><strong>${h(row.title || row.id.slice(0, 8))}</strong>${
      row.content ? `<p>${h(String(row.content).replace(/\s+/g, " ").slice(0, 140))}</p>` : ""
    }<small><span>${h(row.project || "—")}</span><span>${h(whenText(row))}</span>${
      tags.length ? `<span class="hit-tags">${h(tags.join(", "))}</span>` : ""
    }</small></div>`;
  };
  function renderTree(searchRows) {
    const keptScroll = tree.scrollTop;
    // The catalog filter panel is gone; keep the previous default order
    // (most recently updated first) for siblings in every view.
    const filtered = (searchRows || records)
      .slice()
      .sort((a, b) =>
        String(b.updated_at || b.created_at || "").localeCompare(
          String(a.updated_at || a.created_at || ""),
        ),
      );
    root.classList.toggle("graph-mode", mode === "graph");
    root.classList.toggle("wide-mode", mode === "list" || mode === "card");
    tree.classList.toggle("graph-shell", mode === "graph");
    all("[data-view]", root).forEach((tab) => {
      const active = tab.dataset.view === mode;
      tab.classList.toggle("active", active);
      tab.setAttribute("aria-selected", String(active));
    });
    if (mode === "graph") {
      const values = filtered.filter(textMatch);
      renderGraph(tree, values, selected, graphState, (id) => choose(id));
      count.textContent = searchRows
        ? `检索返回 ${values.length} 条；搜索覆盖整库`
        : `已载入 ${number(records.length)} / ${number(ctx.status?.summary?.local_alive)} 条 · 已选 ${checked.size} 条`;
      $("[data-merge]", root).disabled = checked.size < 2 || busy || !readableLibrary();
      $("[data-more]", root).disabled =
        busy || !readableLibrary() ||
        records.length >= Number(ctx.status?.summary?.local_alive || Infinity);
      return;
    }
    if (mode === "card") {
      const values = searchRows ? filtered : filtered.filter(textMatch);
      const view = paged(values);
      tree.innerHTML = `<div class="memory-cards">${view.slice.map(memoryCard).join("") || (values.length ? "" : '<div class="empty">暂无匹配记忆</div>')}</div>${searchRows ? "" : pager(view)}`;
      finish();
      return;
    }
    if (mode === "list") {
      const values = searchRows ? filtered : filtered.filter(textMatch);
      const view = paged(values);
      tree.innerHTML = `<div class="memory-list" role="table" aria-label="全部记忆"><div class="memory-row memory-row-head" role="row" aria-hidden="true"><span>类型</span><span>标题</span><span>项目</span><span>更新时间</span><span>标签</span></div>${view.slice.map(listRow).join("")}</div>${searchRows ? "" : pager(view)}`;
      if (!values.length)
        tree.innerHTML = '<div class="empty">暂无匹配记忆</div>';
      finish();
      return;
    }
    // Tree view: the console's model — trivial (diary) rows group on the diary
    // page, empty leaves and pure category notes hide, search keeps matches
    // and their ancestors visible with every branch opened.
    const needle = searchRows ? "" : filter.trim();
    let source = filtered;
    if (needle && !searchRows) {
      const keep = searchForceIds(source, needle.toLowerCase());
      source = source.filter((row) => keep.has(row.id));
    }
    const index = buildIndex(source);
    index.forceIds = needle ? searchForceIds(source, needle.toLowerCase()) : new Set();
    const visible = searchRows
      ? filtered.map((row) => ({ row, depth: 0, kids: 0, open: false }))
      : visibleRows(index, expanded, Boolean(needle));
    tree.innerHTML =
      visible
        .slice(0, shown)
        .map((item) =>
          memoryRow(item, item.row.kind === "folder" || item.kids > 0),
        )
        .join("") +
      (visible.length > shown
        ? `<button class="tree-load" data-render-more>再显示 ${Math.min(200, visible.length - shown)} 条</button>`
        : "");
    if (!visible.length)
      tree.innerHTML = '<div class="empty">暂无匹配记忆</div>';
    tree.scrollTop = keptScroll;
    finish();
  }
  function finish() {
    const canRead = Boolean(readableLibrary());
    all(".memory-row", tree).forEach((node) => (node.draggable = canRead));
    count.textContent =
      unavailableMessage() || searchCaption ||
      `已载入 ${number(records.length)} / ${number(ctx.status?.summary?.local_alive)} 条 · 已选 ${checked.size} 条`;
    $("[data-merge]", root).disabled = checked.size < 2 || busy || !canRead;
    $("[data-more]", root).disabled =
      busy || !canRead ||
      records.length >= Number(ctx.status?.summary?.local_alive || Infinity);
  }
  function readableLibrary() {
    if (!ctx.desktop?.connected || ctx.status?.summary?.unlocked !== true ||
        ctx.desktop.health?.credential_state === "required") return null;
    return ctx.statusScope;
  }
  function readableGuard() {
    const library = readableLibrary(), epoch = actionEpoch;
    return scopedGuard(
      () => Boolean(library && root.isConnected && epoch === actionEpoch && library === readableLibrary()),
      "当前记忆或账户状态尚未确认，请刷新后重试。",
    );
  }
  function scopedGuard(current, message) {
    const guard = () => { if (!current()) throw new Error(message); };
    guard.current = current;
    return guard;
  }
  function requireReadable() {
    const guard = readableGuard();
    guard();
    return guard;
  }
  function activeReaderEntry() {
    return selectedEntry?.id === selected && renderedContext !== null &&
      renderedContext === readableLibrary() ? selectedEntry : null;
  }
  function requireCurrentReader(memory) {
    const token = readerToken, requireLibrary = requireReadable();
    const current = () => Boolean(token && token === readerToken && activeReaderEntry()?.id === memory.id);
    const guard = () => {
      requireLibrary();
      if (!current()) throw new Error("当前记忆或账户已变化，请重新选择后操作。");
    };
    guard.current = () => requireLibrary.current() && current();
    return guard;
  }
  // A guard failure BEFORE a request prevents it. After a confirmed write
  // the same observation can only veto local UI work, not the success receipt.
  async function afterCommit(guard, update) {
    if (!guard.current()) return;
    try {
      await update();
    } catch (error) {
      console.error("Committed memory action: view refresh failed", error);
    }
  }
  function discardReader() {
    selectedEntry = null;
    renderedEntry = null;
    renderedContext = null;
    readerToken = null;
    ++selectionVersion;
  }
  function confirmedLock() {
    return ctx.desktop?.connected && (ctx.status?.summary?.unlocked === false ||
      ctx.desktop.health?.credential_state === "required");
  }
  function unavailableMessage() {
    if (confirmedLock()) return "需要解锁记忆库";
    if (!ctx.desktop?.connected)
      return ctx.statusError ? "正在重连，请点击刷新重试。" : "等待 Runtime 连接";
    if (readableLibrary()) return "";
    if (ctx.statusError) return "暂时无法读取记忆状态，请点击刷新重试。";
    return "正在读取记忆目录…";
  }
  let catalogEpoch = 0;
  function showUnavailable() {
    const message = unavailableMessage();
    const lock = confirmedLock();
    const scope = ctx.statusScope;
    if (catalogContext && (catalogContext !== scope || lock)) {
      ++catalogEpoch;
      ++actionEpoch;
      revision = "";
      records = [];
      checked.clear();
      expanded.clear();
      selectionAnchor = null;
      lastFoldClick = { id: "", at: 0 };
      filter = "";
      $("[data-search]", root).value = "";
      $("[data-clear-search]", root).hidden = true;
      searchCaption = "";
      selected = "";
      loaded = false;
      catalogContext = null;
      metadataCap = null;
      discardReader();
      closeMemoryDialogs();
      renderTree();
      reader.innerHTML = '<div class="empty">账户已变化，请重新选择记忆。</div>';
    }
    if (message && !statusUnavailable) {
      ++actionEpoch;
      ++catalogEpoch;
      ++selectionVersion;
      selectedEntry = null;
      readerToken = null;
    }
    statusUnavailable = Boolean(message);
    const actionsAvailable = !message && Boolean(activeReaderEntry());
    all("button[data-edit],button[data-child],button[data-move],button[data-share],button[data-delete],button[data-tool],button[data-chain]", reader)
      .forEach((button) => { button.disabled = !actionsAvailable; });
    all("button[data-restore]", reader).forEach((button) => { button.disabled ||= Boolean(message); });
    $("[data-new]", root).disabled = Boolean(message);
    $("[data-merge]", root).disabled = Boolean(message) || checked.size < 2 || busy;
    if (!message) return false;
    revision = "";
    count.textContent = message;
    if (lock) {
      reader.innerHTML = `<div class="empty"><strong>本机记忆库尚未解锁</strong>登录账户，或初始化本机离线记忆库。<div class="actions"><button data-accounts>账户与本机密钥</button></div></div>`;
    } else if (!loaded || !renderedEntry) {
      reader.innerHTML = `<div class="empty">${h(t(message))}${ctx.statusError ? `<div class="actions"><button data-retry-status>${h(t("刷新"))}</button></div>` : ""}</div>`;
    }
    return true;
  }
  async function load() {
    if (busy || showUnavailable()) return;
    const version = catalogEpoch;
    const library = readableLibrary();
    catalogContext = library;
    busy = true;
    let succeeded = false;
    count.textContent = "正在读取记忆目录…";
    try {
      const current = () => root.isConnected && version === catalogEpoch && library === readableLibrary();
      if (metadataCap === null) {
        try {
          const capabilities = summary(await execute("capabilities")).capabilities;
          if (!current()) return;
          metadataCap = capabilities?.memory_list_metadata === true;
        } catch (error) {
          if (!current()) return;
          if (error.envelope?.summary?.reason !== "invalid_input")
            throw error;
          metadataCap = false;
          notify(
            "当前 CLI 使用分页全文目录；更新至 CLI 1.0.18 或兼容版本后可使用精简目录。",
          );
        }
      }
      if (!current()) return;
      const data = await execute("memory_list", {
        limit,
        ...(metadataCap ? { metadata: true } : {}),
      });
      if (!current() || showUnavailable()) return;
      // The compact catalog omits bodies; the paginated catalog provides them
      // and they feed card excerpts and text matching until the next reload.
      let nextRecords = list(data).map((row) =>
        metadataCap ? { ...row, content: undefined } : row,
      );
      // A paginated catalog flattens subtrees whose parent is not loaded yet:
      // buildIndex drops orphans to the top level. The metadata edition is
      // cheap, so top it up until the whole library is in memory.
      if (metadataCap) {
        const alive = Number(ctx.status?.summary?.local_alive) || 0;
        let fetched = limit;
        while (
          alive &&
          nextRecords.length < alive &&
          nextRecords.length >= fetched / 2 &&
          fetched < 100000
        ) {
          count.textContent = `已载入 ${number(nextRecords.length)} / ${number(alive)} 条，正在补全目录…`;
          fetched = Math.min(fetched * 2, 100000);
          const more = await execute("memory_list", {
            limit: fetched,
            metadata: true,
          });
          if (!current() || showUnavailable()) return;
          const rows = list(more);
          if (rows.length <= nextRecords.length) break; // server has no more
          nextRecords = rows.map((row) => ({ ...row, content: undefined }));
        }
        limit = Math.max(limit, fetched);
      }
      records = nextRecords;
      loaded = true;
      succeeded = true;
      // A reader still showing the pre-unlock lock screen resets to the idle hint.
      if (!selected && !reader.querySelector(".reader-heading"))
        reader.innerHTML =
          '<div class="empty">选择左侧记忆，查看完整内容。</div>';
      if (!expanded.size)
        records
          .filter((row) => !row.parent_id)
          .slice(0, 15)
          .forEach((row) => expanded.add(row.id));
      renderTree();
      if (selected) await choose(selected, false);
    } catch (error) {
      if (root.isConnected && version === catalogEpoch && !showUnavailable()) {
        revision = "";
        selectedEntry = null;
        readerToken = null;
        showUnavailable();
        count.textContent = t("暂时无法刷新记忆目录，请稍后重试。");
        if (!loaded) tree.innerHTML = `<div class="empty">${h(t("暂时无法刷新记忆目录，请稍后重试。"))}<button data-retry-status>${h(t("刷新"))}</button></div>`;
      }
    } finally {
      busy = false;
      if (root.isConnected && version !== catalogEpoch && readableLibrary()) update();
      else if (root.isConnected && succeeded) renderTree();
    }
  }
  // Body records carry literal 【前因】【行为】【后果】 markers; render each
  // marker section as its own labelled block instead of one dense paragraph.
  // Body records carry literal 【前因】【行为】【后果】 markers (Chinese
  // legacy) or the English 【Cause】【Action】【Result】 set the current CLI
  // writes; render each marker section as its own labelled block.
  const CONTENT_MARKERS =
    /(【触发】|【前因】|【行为】|【后果】|【Trigger】|【Cause】|【Action】|【Result】)/g;
  function readingBlocks(text) {
    const source = String(text || "");
    CONTENT_MARKERS.lastIndex = 0;
    const marks = [...source.matchAll(CONTENT_MARKERS)];
    if (!marks.length) return "";
    const sections = [];
    const intro = source.slice(0, marks[0].index).trim();
    if (intro) sections.push({ label: "", body: intro });
    marks.forEach(([marker], i) => {
      const end = i + 1 < marks.length ? marks[i + 1].index : source.length;
      const body = source.slice(marks[i].index + marker.length, end).trim();
      if (body) sections.push({ label: marker.slice(1, -1), body });
    });
    return sections
      .map(
        (section) =>
          `<section class="content-block${section.label ? "" : " plain"}">${section.label ? `<h4>${h(section.label)}</h4>` : ""}<p>${h(section.body)}</p></section>`,
      )
      .join("");
  }
  function renderReader(memory) {
    const metadata = inventory(records),
      parent = metadata.byId.get(memory.parent_id),
      children = metadata.children.get(memory.id) || [];
    reader.innerHTML = `<div class="reader-heading"><div class="reader-path">${h(parent?.title || memory.parent_id || "顶层记忆")}</div><h2>${h(memory.title)}</h2><div class="reader-meta"><span>${h(kindNames[String(memory.kind || "context").toLowerCase()] || memory.kind)}</span><span>${memory.importance === "trivial" ? "日记" : "主区"}</span><span>${h(date(memory.updated_at || memory.created_at))}</span></div><div class="actions"><button data-edit>编辑</button><button data-child>新建后续记忆</button><button data-move>移动</button><button data-copy>复制</button><button data-share>分享</button><button data-delete class="danger">删除</button></div></div><div class="reading-content">${readingBlocks(memory.content) || h(memory.content || "")}</div><div class="reader-relations"><h3>上下游关系</h3>${parent ? `<button data-select="${h(parent.id)}">↑ ${h(parent.title)}</button>` : ""}${children.map((id) => `<button data-select="${h(id)}">↓ ${h(metadata.byId.get(id)?.title || id)}</button>`).join("")}${!parent && !children.length ? '<p class="hint">当前目录中没有上下游节点。</p>' : ""}<button data-chain>查看完整因果链</button></div><details class="technical"><summary>记忆信息</summary><div class="technical-bar"><button type="button" data-tech-full>铺满窗口</button></div><pre>${h(raw({ ...memory, content: undefined }))}</pre></details>`;
    const technical = $(".technical", reader);
    const techButton = $("[data-tech-full]", technical);
    techButton.addEventListener("click", () => {
      const on = technical.classList.toggle("fullscreen");
      if (on) technical.open = true;
      techButton.textContent = on ? t("退出铺满窗口") : t("铺满窗口");
    });
  }
  async function choose(id, scroll = true) {
    if (showUnavailable()) return;
    const byId = inventory(records).byId,
      seen = new Set();
    let ancestor = byId.get(id);
    while (ancestor?.parent_id && !seen.has(ancestor.parent_id)) {
      seen.add(ancestor.parent_id);
      expanded.add(ancestor.parent_id);
      ancestor = byId.get(ancestor.parent_id);
    }
    const library = readableLibrary();
    // Only the same record in the same verified library may retain
    // its display through a transient read failure.
    const viewing = library !== null && renderedContext === library && renderedEntry?.id === id;
    const previous = viewing ? (selectedEntry || renderedEntry) : null;
    if (!viewing) {
      discardReader();
      reader.innerHTML = '<div class="empty">正在读取完整内容…</div>';
    } else if (scroll) {
      selectedEntry = null;
    }
    selected = id;
    const version = ++selectionVersion;
    renderTree();
    const stillCurrent = () => root.isConnected && version === selectionVersion &&
      !showUnavailable() && library === readableLibrary();
    try {
      const memory = entry(await execute("memory_show", { id }));
      if (!stillCurrent()) return;
      if (!memory || memory.id !== id)
        throw Object.assign(new Error("Runtime 返回的记忆与当前选择不一致。"), { envelope: {} });
      selectedEntry = memory;
      renderedContext = library;
      const rebuild = !readerToken || !(viewing && previous.updated_at === memory.updated_at);
      readerToken ||= {};
      if (rebuild) {
        renderReader(memory);
        const listed = byId.get(id);
        $(".reader-meta", reader).insertAdjacentHTML(
          "beforeend",
          `<span>召回 ${h(listed?.recall_count ?? "—")} 次</span><span>${h(memory.project || "未指定项目")}</span><span>${h((memory.tags || []).join(" · "))}</span>`,
        );
        const requireCurrent = requireCurrentReader(memory);
        memoryTools(reader, memory, {
          requireCurrent,
          keepDialog,
          changed: () => afterCommit(requireCurrent, async () => {
            selected = "";
            discardReader();
            reader.innerHTML =
              '<div class="empty">操作已完成，请重新选择记忆。</div>';
            await load();
            await ctx.refresh();
          }),
        });
        if (scroll) reader.scrollTop = 0;
      }
      renderedEntry = memory;
      showUnavailable();
    } catch (error) {
      if (!stillCurrent()) return;
      // Keep only the display on a background transport error, not
      // reader actions or a definitively denied/deleted record.
      if (viewing && !scroll && !error.envelope) {
        selectedEntry = null;
        readerToken = null;
        showUnavailable();
        revision = "";
        return;
      }
      discardReader();
      reader.innerHTML = error.envelope
        ? `<div class="empty danger-text">${h(error.message)}</div>`
        : `<div class="empty">${h(t("暂时无法读取记忆状态，请点击刷新重试。"))}<button data-retry-status>${h(t("刷新"))}</button></div>`;
    }
  }
  async function search() {
    const query = $("[data-search]", root).value.trim();
    if (!query) {
      filter = "";
      searchCaption = "";
      $("[data-clear-search]", root).hidden = true;
      renderTree();
      return;
    }
    if (showUnavailable()) return;
    const version = catalogEpoch;
    count.textContent = "正在检索整库…";
    try {
      const result = await execute("memory_search", { query, limit: 50 });
      if (!root.isConnected || version !== catalogEpoch || showUnavailable()) return;
      const matches = rows(result)
        .map((value) => {
          try {
            return entry(value);
          } catch {
            return null;
          }
        })
        .filter(Boolean);
      searchCaption = `检索返回 ${matches.length} 条；搜索覆盖整库`;
      renderTree(matches);
      $("[data-clear-search]", root).hidden = false;
    } catch (error) {
      notify(error.message, true);
    }
  }
  async function undo() {
    const guard = requireReadable();
    const id = await undoLast(guard);
    notify("已撤销");
    await afterCommit(guard, async () => {
      selected = id;
      selectedEntry = null;
      selectionVersion++;
      if (!selected)
        reader.innerHTML =
          '<div class="empty">新建记忆已撤销，请重新选择记忆。</div>';
      await load();
      if (guard.current()) await ctx.refresh();
    });
  }
  function edit(options = {}) {
    const guard = options.requireCurrent || requireReadable();
    guard();
    keepDialog(editor({
      ...options,
      requireCurrent: guard,
      keepDialog,
      onSaved: (id, undo) => afterCommit(guard, async () => {
        if (undo) await recordUndo({ ...undo, id }, guard.current);
        if (!guard.current()) return;
        if (id) selected = id;
        await load();
        if (guard.current()) await ctx.refresh();
      }),
    }));
  }
  root.addEventListener("input", (event) => {
    if (event.target.matches("[data-search]")) {
      filter = event.target.value;
      shown = 200;
      listPage = 1;
      searchCaption = "";
      renderTree();
    }
  });
  $("[data-search]", root).addEventListener("keydown", (event) => {
    if (event.key === "Enter") {
      event.preventDefault();
      search();
    }
  });
  // Anchor for Shift range selection: last row chosen with a plain click.
  let selectionAnchor = null;
  // Double-click folding: the first click re-renders the tree, so the
  // browser never synthesizes a dblclick across the replaced node — detect
  // two clicks on the same row ourselves instead.
  let lastFoldClick = { id: "", at: 0 };
  const isDoubleTap = (id) => {
    const now = Date.now();
    const double = lastFoldClick.id === id && now - lastFoldClick.at < 500;
    lastFoldClick = { id, at: now };
    return double;
  };
  const foldToggle = (row) => {
    const toggle = row.querySelector("[data-expand]");
    if (!toggle || toggle.disabled) return;
    const id = toggle.dataset.expand;
    if (expanded.has(id)) expanded.delete(id);
    else expanded.add(id);
    renderTree();
  };
  // Select a whole row: plain click resets to just this row, Ctrl/Cmd toggles
  // it in the multi-select set, Shift selects the inclusive visible range
  // between the anchor and this row.
  function selectRow(row, { ctrl = false, shift = false } = {}) {
    const id = row?.dataset.id;
    if (!id) return;
    if (shift && selectionAnchor) {
      const rows = all(".memory-row[data-id], .memory-card[data-id]", tree);
      const from = rows.findIndex((n) => n.dataset.id === selectionAnchor);
      const to = rows.findIndex((n) => n.dataset.id === id);
      if (from >= 0 && to >= 0) {
        if (!ctrl) checked.clear();
        const [a, b] = from < to ? [from, to] : [to, from];
        for (let i = a; i <= b; i++) checked.add(rows[i].dataset.id);
        selectionAnchor = id;
        renderTree();
        return;
      }
    }
    if (ctrl) {
      if (checked.has(id)) checked.delete(id);
      else checked.add(id);
    } else {
      checked.clear();
      checked.add(id);
    }
    selectionAnchor = id;
    renderTree();
  }
  root.addEventListener("click", async (event) => {
    const button = event.target.closest("button");
    const card = event.target.closest("[data-card]");
    if (card) {
      if (button) {
        // Buttons inside the card (none today) keep dedicated handling.
        choose(card.dataset.card);
        return;
      }
      // Ctrl/Cmd or Shift on a card multi/range-selects; a plain card click
      // still opens the memory in the reader.
      if (event.ctrlKey || event.metaKey || event.shiftKey) {
        selectRow(card, { ctrl: event.ctrlKey || event.metaKey, shift: event.shiftKey });
        return;
      }
      selectRow(card, { ctrl: false, shift: false });
      choose(card.dataset.card);
      return;
    }
    const row = event.target.closest(".memory-row[data-id]");
    if (row) {
      if (button && button.classList.contains("memory-title")) {
        if (event.ctrlKey || event.metaKey || event.shiftKey) {
          selectRow(row, { ctrl: event.ctrlKey || event.metaKey, shift: event.shiftKey });
          return;
        }
        // A plain title click selects the row and opens it in the reader;
        // a double-click on an expandable row folds it instead of reopening.
        selectRow(row, { ctrl: false, shift: false });
        if (isDoubleTap(row.dataset.id)) {
          foldToggle(row);
          return;
        }
        if (event.detail <= 1) choose(row.dataset.id);
        return;
      }
      if (!button) {
        // Clicking anywhere else on the row selects the whole row, and a
        // double-click folds or unfolds an expandable row.
        selectRow(row, { ctrl: event.ctrlKey || event.metaKey, shift: event.shiftKey });
        if (isDoubleTap(row.dataset.id)) foldToggle(row);
        return;
      }
      // Other buttons (tree expand toggles) fall through to their handlers.
    }
    if (!button) return;
    if (button.hasAttribute("data-accounts")) {
      ctx.navigate("accounts");
      return;
    }
    if (button.hasAttribute("data-retry-status")) {
      await action(button, async () => { await ctx.refresh(); await load(); });
      return;
    }
    if (button.dataset.view) {
      mode = button.dataset.view;
      listPage = 1;
      renderTree();
      return;
    }
    if (button.dataset.pageSet) {
      listPage += Number(button.dataset.pageSet);
      renderTree();
      return;
    }
    if (button.dataset.select) {
      choose(button.dataset.select);
      return;
    }
    if (button.dataset.expand) {
      const id = button.dataset.expand;
      if (expanded.has(id)) expanded.delete(id);
      else expanded.add(id);
      renderTree();
      return;
    }
    if (button.hasAttribute("data-render-more")) {
      shown += 200;
      renderTree();
      return;
    }
    if (button.hasAttribute("data-new")) {
      if (!showUnavailable()) edit({ parent: selected });
      return;
    }
    if (button.hasAttribute("data-undo")) {
      await action(button, undo);
      return;
    }
    if (button.hasAttribute("data-more")) {
      limit = Math.min(100000, limit * 2);
      await load();
      return;
    }
    if (button.hasAttribute("data-clear-search")) {
      filter = "";
      searchCaption = "";
      $("[data-search]", root).value = "";
      button.hidden = true;
      renderTree();
      return;
    }
    if (button.hasAttribute("data-search-submit")) {
      await action(button, search);
      return;
    }
    if (button.hasAttribute("data-merge")) {
      await action(button, async () => {
        const chosen = [], chosenIDs = [...checked], guard = requireReadable();
        for (const id of chosenIDs) {
          guard();
          chosen.push(entry(await execute("memory_show", { id })));
        }
        guard();
        edit({
          memory: {
            ...chosen[0],
            content: chosen
              .map((item) => `## ${item.title}\n\n${item.content}`)
              .join("\n\n"),
          },
          merge: chosen.map((item) => item.id),
          requireCurrent: guard,
        });
      });
      return;
    }
    const memory = activeReaderEntry();
    if (!memory) return;
    const requireCurrent = requireCurrentReader(memory);
    if (button.hasAttribute("data-edit")) edit({ memory, requireCurrent });
    if (button.hasAttribute("data-child")) edit({ parent: memory.id, requireCurrent });
    if (button.hasAttribute("data-copy"))
      await action(button, async () => {
        await navigator.clipboard.writeText(memory.content || "");
        notify("正文已复制");
      });
    if (button.hasAttribute("data-move"))
      keepDialog(dialog({
        title: "移动记忆",
        body: input("parent", "新的父节点 ID", memory.parent_id || "", {
          required: true,
        }),
        onSubmit: async (value) => {
          requireCurrent();
          await execute("memory_attach", {
            id: memory.id,
            parent: value.parent.trim(),
          });
          notify("已移动");
          await afterCommit(requireCurrent, async () => {
            await recordUndo({ kind: "move", id: memory.id, before: memory }, requireCurrent.current);
            if (requireCurrent.current()) await load();
          });
        },
      }));
    if (button.hasAttribute("data-share"))
      await action(button, async () => {
        const result = await execute("memory_share", { root: memory.id });
        if (!requireCurrent.current()) {
          notify("分享请求已完成。账户或选择已变化，未显示分享材料。");
          return;
        }
        const text = result.details?.payload;
        if (!text) {
          notify("分享请求已完成，但 Runtime 未返回分享材料，请检查操作记录。");
          return;
        }
        await afterCommit(requireCurrent, () => keepDialog(showText("分享子树", text)));
      });
    if (button.hasAttribute("data-chain"))
      await action(button, async () => {
        const result = await execute("memory_chain", {
          id: memory.id,
          depth: 3,
        });
        requireCurrent();
        const detail = result.details || {};
        const sequence = [
          ...(detail.ancestors || []),
          detail.entry,
          ...(detail.descendants || []),
        ]
          .filter(Boolean)
          .map((item) => item.entry || item);
        keepDialog(showText(
          "完整因果链",
          sequence
            .map((item) => `${item.title || item.id}\n\n${item.content || ""}`)
            .join("\n\n────────\n\n"),
        ));
      });
    if (button.hasAttribute("data-delete"))
      await action(button, async () => {
        if (
          !(await confirm(
            "删除这条记忆",
            `将删除「${memory.title}」。删除会按同步设置传播，之后可以使用原 ID 恢复。`,
            "删除",
            keepDialog,
          ))
        )
          return;
        requireCurrent();
        const restoreGuard = requireReadable();
        await execute("memory_delete", { id: memory.id });
        notify("记忆已删除");
        await afterCommit(requireCurrent, async () => {
          await recordUndo({ kind: "delete", id: memory.id, before: memory }, requireCurrent.current);
          if (!requireCurrent.current()) return;
          selected = "";
          discardReader();
          checked.delete(memory.id);
          reader.innerHTML = `<div class="empty"><strong>记忆已删除</strong><button data-restore="${h(memory.id)}">恢复刚才删除的记忆</button></div>`;
          $("[data-restore]", reader).addEventListener("click", (event) =>
            action(event.currentTarget, async () => {
              restoreGuard();
              if (selected)
                throw new Error("当前记忆或账户已变化，请重新选择后操作。");
              await execute("memory_restore", { id: memory.id });
              notify("记忆已恢复");
              await afterCommit(restoreGuard, async () => {
                selected = memory.id;
                await load();
                if (restoreGuard.current()) await ctx.refresh();
              });
            }),
          );
          await load();
          if (restoreGuard.current()) await ctx.refresh();
        });
      });
  });
  root.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      const technical = $(".technical.fullscreen", reader);
      if (technical) {
        technical.classList.remove("fullscreen");
        const techButton = $("[data-tech-full]", technical);
        if (techButton) techButton.textContent = t("铺满窗口");
      }
      if (root.classList.contains("focus-mode")) setFocusMode(false);
      return;
    }
    if (event.key !== "Enter" && event.key !== " ") return;
    const card = event.target.closest?.("[data-card]");
    if (card) {
      event.preventDefault();
      choose(card.dataset.card);
    }
  });
  function update() {
    if (showUnavailable() || document.querySelector("dialog[open]")) return;
    const s = ctx.status.summary;
    const stamp = `${s.data_dir}/${s.local_total}/${s.max_updated_at}`;
    if (stamp !== revision && !busy) {
      revision = stamp;
      load();
      return;
    }
    // Final-consistency fallback: cloud sync lag or a missed stamp must not
    // freeze the catalog — quietly re-read it every 30s like the dashboard.
    const now = Date.now();
    if (!busy && now >= autoReloadAt) {
      autoReloadAt = now + 30000;
      load();
    }
  }
  update();
  const dispose = memoryInteractions(root, {
    getRecords: () => records,
    getSelected: () => readableLibrary() ? selected : "",
    requireReadable,
    keepDialog,
    select: choose,
    search: () => $("[data-search]", root).focus(),
    newMemory: (subtree) => edit({ parent: subtree ? selected : "" }),
    edit: () => {
      const memory = activeReaderEntry();
      if (memory) edit({ memory, requireCurrent: requireCurrentReader(memory) });
    },
    remove: () => $("[data-delete]", reader)?.click(),
    undo,
    moved: (before, guard) => {
      notify("已移动");
      return afterCommit(guard, async () => {
        await recordUndo({ kind: "move", id: before.id, before }, guard.current);
        if (!guard.current()) return;
        selected = before.id;
        await load();
        if (guard.current()) await ctx.refresh();
      });
    },
  });
  return {
    update,
    refresh: load,
    dispose: () => {
      dispose();
      closeMemoryDialogs();
      document.body.classList.remove("memory-focus");
      if (graphState.raf) cancelAnimationFrame(graphState.raf);
      graphState.raf = 0;
      graphState.dom = null;
    },
  };
}
