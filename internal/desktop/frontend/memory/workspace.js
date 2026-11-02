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
import { heading } from "../pages/overview.js";
import { entry, list, inventory, kindNames } from "./model.js";
import { editor } from "./editor.js";
import { renderGraph } from "./graph.js";
import { recordUndo, undoLast } from "./undo.js";
import { memoryInteractions } from "./interactions.js";
import { memoryTools } from "./tools.js";
import { filterControls, filterRecords } from "./filters.js";
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
    searchCaption = "";
  let metadataCap = null;
  const checked = new Set();
  const graphState = {};
  let filters = {};
  root.innerHTML =
    heading(
      "你的记忆，清晰可见。",
      "浏览因果树、检索整库、编辑正文，或调整记忆之间的关系。",
      `<button data-new class="primary">新建记忆</button>`,
    ) +
    `<div class="memory-toolbar"><div class="search-field"><input data-search placeholder="筛选标题 / ID，或回车检索整库" aria-label="搜索记忆"><button data-search-submit>检索整库</button></div><div class="tabs view-tabs" role="tablist" aria-label="视图切换">${VIEW_MODES.map(
      ([value, label]) =>
        `<button role="tab" data-view="${value}" aria-selected="${value === mode}" class="${value === mode ? "active" : ""}">${label}</button>`,
    ).join("")}</div><button data-merge disabled>合并所选</button><button data-more>载入更多</button></div><div class="memory-caption"><span data-count>正在读取…</span><button data-clear-search hidden>返回目录</button></div><div class="memory-layout"><div class="memory-index" role="tree" aria-label="记忆树"></div><div id="memory-resizer" aria-hidden="true"></div><article class="memory-reader"><div class="empty">选择左侧记忆，查看完整内容。</div></article></div>`;
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
  $(".memory-toolbar", root).insertAdjacentHTML("afterend", filterControls());
  const filterForm = $("[data-filters]", root);
  filterForm.oninput = () => {
    filters = Object.fromEntries(new FormData(filterForm));
    listPage = 1;
    renderTree();
  };
  filterForm.onreset = () => {
    filters = {};
    listPage = 1;
    renderTree();
  };
  filterForm.onsubmit = (event) => event.preventDefault();
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
    const filtered = filterRecords(searchRows || records, filters);
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
      $("[data-merge]", root).disabled = checked.size < 2 || busy;
      $("[data-more]", root).disabled =
        busy ||
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
    all(".memory-row", tree).forEach((node) => (node.draggable = true));
    tree.scrollTop = keptScroll;
    finish();
  }
  function finish() {
    all(".memory-row", tree).forEach((node) => (node.draggable = true));
    count.textContent =
      searchCaption ||
      `已载入 ${number(records.length)} / ${number(ctx.status?.summary?.local_alive)} 条 · 已选 ${checked.size} 条`;
    $("[data-merge]", root).disabled = checked.size < 2 || busy;
    $("[data-more]", root).disabled =
      busy ||
      records.length >= Number(ctx.status?.summary?.local_alive || Infinity);
  }
  function locked() {
    reader.innerHTML = `<div class="empty"><strong>本机记忆库尚未解锁</strong>登录账户，或初始化本机离线记忆库。<div class="actions"><button data-accounts>账户与本机密钥</button></div></div>`;
    $("[data-accounts]", reader).addEventListener("click", () =>
      ctx.navigate("accounts"),
    );
    count.textContent = ctx.desktop?.connected
      ? "需要解锁记忆库"
      : "等待 Runtime 连接";
  }
  async function load() {
    if (busy) return;
    if (!ctx.desktop?.connected || ctx.status?.summary?.unlocked === false) {
      locked();
      return;
    }
    busy = true;
    let succeeded = false;
    count.textContent = "正在读取记忆目录…";
    try {
      if (metadataCap === null) {
        try {
          metadataCap =
            summary(await execute("capabilities")).capabilities
              ?.memory_list_metadata === true;
        } catch (error) {
          if (error.envelope?.summary?.reason !== "invalid_input")
            throw error;
          metadataCap = false;
          notify(
            "当前 CLI 使用分页全文目录；更新至 CLI 1.0.18 或兼容版本后可使用精简目录。",
          );
        }
      }
      const data = await execute("memory_list", {
        limit,
        ...(metadataCap ? { metadata: true } : {}),
      });
      if (!root.isConnected) return;
      // The compact catalog omits bodies; the paginated catalog provides them
      // and they feed card excerpts and text matching until the next reload.
      records = list(data).map((row) =>
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
          records.length < alive &&
          records.length >= fetched / 2 &&
          fetched < 100000
        ) {
          count.textContent = `已载入 ${number(records.length)} / ${number(alive)} 条，正在补全目录…`;
          fetched = Math.min(fetched * 2, 100000);
          const more = await execute("memory_list", {
            limit: fetched,
            metadata: true,
          });
          if (!root.isConnected) return;
          const rows = list(more);
          if (rows.length <= records.length) break; // server has no more
          records = rows.map((row) => ({ ...row, content: undefined }));
        }
        limit = Math.max(limit, fetched);
      }
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
      if (root.isConnected) {
        count.textContent = "读取失败";
        tree.innerHTML = `<div class="empty danger-text">${h(error.message)}</div>`;
      }
    } finally {
      busy = false;
      if (root.isConnected && succeeded) renderTree();
    }
  }
  // Body records carry literal 【前因】【行为】【后果】 markers; render each
  // marker section as its own labelled block instead of one dense paragraph.
  const CONTENT_MARKERS = /(【触发】|【前因】|【行为】|【后果】)/g;
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
    reader.innerHTML = `<div class="reader-heading"><div class="reader-path">${h(parent?.title || memory.parent_id || "顶层记忆")}</div><h2>${h(memory.title)}</h2><div class="reader-meta"><span>${h(kindNames[String(memory.kind || "context").toLowerCase()] || memory.kind)}</span><span>${memory.importance === "trivial" ? "日记" : "主区"}</span><span>${h(date(memory.updated_at || memory.created_at))}</span></div><div class="actions"><button data-edit>编辑</button><button data-child>新建后续记忆</button><button data-move>移动</button><button data-copy>复制</button><button data-share>分享</button><button data-delete class="danger">删除</button></div></div><div class="reading-content">${readingBlocks(memory.content) || h(memory.content || "")}</div><div class="reader-relations"><h3>上下游关系</h3>${parent ? `<button data-select="${h(parent.id)}">↑ ${h(parent.title)}</button>` : ""}${children.map((id) => `<button data-select="${h(id)}">↓ ${h(metadata.byId.get(id)?.title || id)}</button>`).join("")}${!parent && !children.length ? '<p class="hint">当前目录中没有上下游节点。</p>' : ""}<button data-chain>查看完整因果链</button></div><details class="technical"><summary>记忆信息</summary><pre>${h(raw({ ...memory, content: undefined }))}</pre></details>`;
  }
  async function choose(id, scroll = true) {
    const byId = inventory(records).byId,
      seen = new Set();
    let ancestor = byId.get(id);
    while (ancestor?.parent_id && !seen.has(ancestor.parent_id)) {
      seen.add(ancestor.parent_id);
      expanded.add(ancestor.parent_id);
      ancestor = byId.get(ancestor.parent_id);
    }
    selected = id;
    selectedEntry = null;
    const version = ++selectionVersion;
    renderTree();
    reader.innerHTML = '<div class="empty">正在读取完整内容…</div>';
    try {
      const memory = entry(await execute("memory_show", { id }));
      if (!root.isConnected || version !== selectionVersion) return;
      selectedEntry = memory;
      renderReader(memory);
      const listed = byId.get(id);
      $(".reader-meta", reader).insertAdjacentHTML(
        "beforeend",
        `<span>召回 ${h(listed?.recall_count ?? "—")} 次</span><span>${h(memory.project || "未指定项目")}</span><span>${h((memory.tags || []).join(" · "))}</span>`,
      );
      memoryTools(reader, memory, {
        changed: async () => {
          selected = "";
          selectedEntry = null;
          selectionVersion++;
          reader.innerHTML =
            '<div class="empty">操作已完成，请重新选择记忆。</div>';
          await load();
          await ctx.refresh();
        },
      });
      if (scroll) reader.scrollTop = 0;
    } catch (error) {
      if (root.isConnected && version === selectionVersion)
        reader.innerHTML = `<div class="empty danger-text">${h(error.message)}</div>`;
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
    count.textContent = "正在检索整库…";
    try {
      const result = await execute("memory_search", { query, limit: 50 });
      if (!root.isConnected) return;
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
    selected = await undoLast();
    selectedEntry = null;
    selectionVersion++;
    if (!selected)
      reader.innerHTML =
        '<div class="empty">新建记忆已撤销，请重新选择记忆。</div>';
    await load();
    await ctx.refresh();
    notify("已撤销");
  }
  function edit(options = {}) {
    editor({
      ...options,
      onSaved: async (id, undo) => {
        if (undo) await recordUndo({ ...undo, id });
        if (id) selected = id;
        await load();
        await ctx.refresh();
      },
    });
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
      edit({ parent: selected });
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
        const chosen = [];
        for (const id of checked)
          chosen.push(entry(await execute("memory_show", { id })));
        edit({
          memory: {
            ...chosen[0],
            content: chosen
              .map((item) => `## ${item.title}\n\n${item.content}`)
              .join("\n\n"),
          },
          merge: chosen.map((item) => item.id),
        });
      });
      return;
    }
    const memory = selectedEntry;
    if (!memory) return;
    if (button.hasAttribute("data-edit")) edit({ memory });
    if (button.hasAttribute("data-child")) edit({ parent: memory.id });
    if (button.hasAttribute("data-copy"))
      await action(button, async () => {
        await navigator.clipboard.writeText(memory.content || "");
        notify("正文已复制");
      });
    if (button.hasAttribute("data-move"))
      dialog({
        title: "移动记忆",
        body: input("parent", "新的父节点 ID", memory.parent_id || "", {
          required: true,
        }),
        onSubmit: async (value) => {
          await execute("memory_attach", {
            id: memory.id,
            parent: value.parent.trim(),
          });
          await recordUndo({ kind: "move", id: memory.id, before: memory });
          notify("已移动");
          await load();
        },
      });
    if (button.hasAttribute("data-share"))
      await action(button, async () => {
        const result = await execute("memory_share", { root: memory.id });
        const text = result.details?.payload;
        if (!text) throw new Error("Runtime 未返回分享材料");
        showText("分享子树", text);
      });
    if (button.hasAttribute("data-chain"))
      await action(button, async () => {
        const result = await execute("memory_chain", {
          id: memory.id,
          depth: 3,
        });
        const detail = result.details || {};
        const sequence = [
          ...(detail.ancestors || []),
          detail.entry,
          ...(detail.descendants || []),
        ]
          .filter(Boolean)
          .map((item) => item.entry || item);
        showText(
          "完整因果链",
          sequence
            .map((item) => `${item.title || item.id}\n\n${item.content || ""}`)
            .join("\n\n────────\n\n"),
        );
      });
    if (button.hasAttribute("data-delete"))
      await action(button, async () => {
        if (
          !(await confirm(
            "删除这条记忆",
            `将删除「${memory.title}」。删除会按同步设置传播，之后可以使用原 ID 恢复。`,
            "删除",
          ))
        )
          return;
        await execute("memory_delete", { id: memory.id });
        await recordUndo({ kind: "delete", id: memory.id, before: memory });
        selected = "";
        selectedEntry = null;
        selectionVersion++;
        checked.delete(memory.id);
        reader.innerHTML = `<div class="empty"><strong>记忆已删除</strong><button data-restore="${h(memory.id)}">恢复刚才删除的记忆</button></div>`;
        $("[data-restore]", reader).addEventListener("click", (event) =>
          action(event.currentTarget, async () => {
            await execute("memory_restore", { id: memory.id });
            selected = memory.id;
            await load();
            await ctx.refresh();
          }),
        );
        await load();
        await ctx.refresh();
      });
  });
  root.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" && event.key !== " ") return;
    const card = event.target.closest?.("[data-card]");
    if (card) {
      event.preventDefault();
      choose(card.dataset.card);
    }
  });
  function update() {
    const s = ctx.status?.summary;
    if (!s?.unlocked) {
      if (!loaded) locked();
      return;
    }
    if (document.querySelector("dialog[open]")) return;
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
    getSelected: () => selected,
    select: choose,
    search: () => $("[data-search]", root).focus(),
    newMemory: (subtree) => edit({ parent: subtree ? selected : "" }),
    edit: () => {
      if (selectedEntry) edit({ memory: selectedEntry });
    },
    remove: () => $("[data-delete]", reader)?.click(),
    undo,
    moved: async (before) => {
      await recordUndo({ kind: "move", id: before.id, before });
      selected = before.id;
      await load();
      await ctx.refresh();
      notify("已移动");
    },
  });
  return {
    update,
    refresh: load,
    dispose: () => {
      dispose();
      if (graphState.raf) cancelAnimationFrame(graphState.raf);
      graphState.raf = 0;
      graphState.dom = null;
    },
  };
}
