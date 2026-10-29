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
import { entry, list, flatten, inventory, kindNames } from "./model.js";
import { editor } from "./editor.js";
import { renderGraph } from "./graph.js";
import { recordUndo, undoLast } from "./undo.js";
import { memoryInteractions } from "./interactions.js";
import { memoryTools } from "./tools.js";
import { filterControls, filterRecords } from "./filters.js";

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
    selectionVersion = 0,
    revision = "",
    loaded = false;
  let metadataCap=null;
  const checked = new Set();
  const graphState = {};
  let filters = {};
  root.innerHTML =
    heading(
      "你的记忆，清晰可见。",
      "浏览因果树、检索整库、编辑正文，或调整记忆之间的关系。",
      `<button data-new class="primary">新建记忆</button>`,
    ) +
    `<div class="memory-toolbar"><div class="search-field"><input data-search placeholder="筛选标题 / ID，或回车检索整库" aria-label="搜索记忆"><button data-search-submit>检索整库</button></div><button data-mode>列表视图</button><button data-merge disabled>合并所选</button><button data-more>载入更多</button></div><div class="memory-caption"><span data-count>正在读取…</span><button data-clear-search hidden>返回目录</button></div><div class="memory-layout"><div class="memory-index" role="tree" aria-label="记忆树"></div><article class="memory-reader"><div class="empty">选择左侧记忆，查看完整内容。</div></article></div>`;
  const tree = $(".memory-index", root),
    reader = $(".memory-reader", root),
    count = $("[data-count]", root);
  const undoButton = document.createElement("button");
  undoButton.textContent = "撤销";
  undoButton.dataset.undo = "";
  undoButton.title = "Ctrl / Command Z：撤销未发生冲突的操作";
  $(".memory-toolbar", root).append(undoButton);
  $(".memory-toolbar", root).insertAdjacentHTML("afterend", filterControls());
  const filterForm = $("[data-filters]", root);
  filterForm.oninput = () => {
    filters = Object.fromEntries(new FormData(filterForm));
    renderTree();
  };
  filterForm.onreset = () => {
    filters = {};
    renderTree();
  };
  filterForm.onsubmit = (event) => event.preventDefault();
  function renderTree(searchRows) {
    const filtered = filterRecords(searchRows || records, filters);
    root.classList.toggle("graph-mode", mode === "graph");
    if (mode === "graph") {
      const values =
        filtered.filter((row) =>
          `${row.title} ${row.id}`.toLowerCase().includes(filter.toLowerCase()),
        );
      renderGraph(tree, values, selected, graphState, (id) => choose(id));
      count.textContent = `图谱显示 ${values.length} 个已载入节点`;
      return;
    }
    const visible = searchRows
      ? filtered.map((row) => ({ row, depth: 0, kids: 0 }))
      : mode === "list"
        ? filtered
            .filter((row) =>
              `${row.title || ""} ${row.id}`
                .toLowerCase()
                .includes(filter.toLowerCase()),
            )
            .map((row) => ({ row, depth: 0, kids: 0 }))
        : flatten(filtered, expanded, filter);
    tree.innerHTML =
      visible
        .slice(0, shown)
        .map(
          ({ row, depth, kids, expanded: open }) =>
            `<div class="memory-row ${row.id === selected ? "selected" : ""}" data-id="${h(row.id)}" data-level="${Math.min(depth, 8)}" role="treeitem" aria-selected="${row.id === selected}" ${kids ? `aria-expanded="${Boolean(open)}"` : ""}><input type="checkbox" data-check="${h(row.id)}" ${checked.has(row.id) ? "checked" : ""} aria-label="选择 ${h(row.title)}"><button class="tree-toggle" data-expand="${h(row.id)}" ${kids ? "" : "disabled"} aria-label="展开或折叠">${kids ? (open ? "▾" : "▸") : "·"}</button><button class="memory-title" data-select="${h(row.id)}" title="${h(row.title)}">${h(row.title || row.id.slice(0, 8))}</button></div>`,
        )
        .join("") +
      (visible.length > shown
        ? `<button class="tree-load" data-render-more>再显示 ${Math.min(200, visible.length - shown)} 条</button>`
        : "");
    if (!visible.length)
      tree.innerHTML = '<div class="empty">暂无匹配记忆</div>';
    all(".memory-row", tree).forEach((node) => (node.draggable = true));
    count.textContent = searchRows
      ? `检索返回 ${visible.length} 条；搜索覆盖整库`
      : `已载入 ${number(records.length)} / ${number(ctx.status?.summary?.local_alive)} 条 · 已选 ${checked.size} 条`;
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
      if(metadataCap===null){
        try { metadataCap=summary(await execute('capabilities')).capabilities?.memory_list_metadata===true; }
        catch(error){if(error.envelope?.summary?.reason!=='invalid_input')throw error;metadataCap=false;notify('当前 CLI 使用分页全文目录；更新至 CLI 1.0.18 或兼容版本后可使用精简目录。');}
      }
      const data = await execute("memory_list", { limit,...(metadataCap?{metadata:true}:{}) });
      if (!root.isConnected) return;
      records = list(data).map((row) => ({ ...row, content: undefined }));
      loaded = true;
      succeeded = true;
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
  function renderReader(memory) {
    const metadata = inventory(records),
      parent = metadata.byId.get(memory.parent_id),
      children = metadata.children.get(memory.id) || [];
    reader.innerHTML = `<div class="reader-heading"><div class="reader-path">${h(parent?.title || memory.parent_id || "顶层记忆")}</div><h2>${h(memory.title)}</h2><div class="reader-meta"><span>${h(kindNames[String(memory.kind || "context").toLowerCase()] || memory.kind)}</span><span>${memory.importance === "trivial" ? "日记" : "主区"}</span><span>${h(date(memory.updated_at || memory.created_at))}</span></div><div class="actions"><button data-edit>编辑</button><button data-child>新建后续记忆</button><button data-move>移动</button><button data-copy>复制</button><button data-share>分享</button><button data-delete class="danger">删除</button></div></div><div class="reading-content">${h(memory.content || "")}</div><div class="reader-relations"><h3>上下游关系</h3>${parent ? `<button data-select="${h(parent.id)}">↑ ${h(parent.title)}</button>` : ""}${children.map((id) => `<button data-select="${h(id)}">↓ ${h(metadata.byId.get(id)?.title || id)}</button>`).join("")}${!parent && !children.length ? '<p class="hint">当前目录中没有上下游节点。</p>' : ""}<button data-chain>查看完整因果链</button></div><details class="technical"><summary>记忆信息</summary><pre>${h(raw({ ...memory, content: undefined }))}</pre></details>`;
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
      $(".reader-meta", reader).insertAdjacentHTML("beforeend",
        `<span>召回 ${h(listed?.recall_count ?? "—")} 次</span><span>${h(memory.project || "未指定项目")}</span><span>${h((memory.tags || []).join(" · "))}</span>`);
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
    if (!selected) reader.innerHTML = '<div class="empty">新建记忆已撤销，请重新选择记忆。</div>';
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
      renderTree();
    }
  });
  $("[data-search]", root).addEventListener("keydown", (event) => {
    if (event.key === "Enter") {
      event.preventDefault();
      search();
    }
  });
  root.addEventListener("change", (event) => {
    if (event.target.dataset.check) {
      const id = event.target.dataset.check;
      if (event.target.checked) checked.add(id);
      else checked.delete(id);
      renderTree();
    }
  });
  root.addEventListener("click", async (event) => {
    const button = event.target.closest("button");
    if (!button) return;
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
    if (button.hasAttribute("data-mode")) {
      mode = mode === "tree" ? "list" : mode === "list" ? "graph" : "tree";
      button.textContent = {
        tree: "列表视图",
        list: "图谱视图",
        graph: "树形视图",
      }[mode];
      renderTree();
      return;
    }
    if (button.hasAttribute("data-more")) {
      limit = Math.min(100000, limit * 2);
      await load();
      return;
    }
    if (button.hasAttribute("data-clear-search")) {
      filter = "";
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
  return { update, refresh: load, dispose };
}
