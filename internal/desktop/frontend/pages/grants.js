import {
  $,
  h,
  raw,
  execute,
  action,
  confirm,
  dialog,
  input,
  panel,
  showText,
  notify,
  rows,
} from "../ui/core.js";
import { heading } from "./overview.js";
import { entry } from "../memory/model.js";

export async function inspectGrants(memory) {
  const ancestors = new Set(), seen = new Set([memory.id]);
  let parent = memory.parent_id;
  while (parent && !seen.has(parent)) {
    if (seen.size >= 256) throw Error("祖先链超过检查限制，请先检查树结构");
    seen.add(parent);
    ancestors.add(parent);
    parent = entry(await execute("memory_show", { id: parent })).parent_id;
  }
  const grants = rows(await execute("grant_list"), "grants");
  const relevant = grants.filter((grant) => !grant.revoked &&
    (grant.root_id === memory.id || ancestors.has(grant.root_id)));
  showText("当前节点的只读授权", raw({
    node: memory.id,
    note: "按当前祖先链与有效授权计算；移动节点后范围可能改变。",
    grants: relevant.map((grant) => ({ ...grant,
      relationship: grant.root_id === memory.id ? "直接授权" : "继承自祖先" })),
  }));
}

export function grants(root, ctx) {
  let loading = false,
    attempted = false;
  root.innerHTML =
    heading(
      "控制记忆的访问范围。",
      "为本机工具创建只读子树授权。授权包括子节点，撤销后令牌失效。",
      '<button data-create class="primary">新建只读授权</button>',
    ) + "<div data-grants></div>";
  async function load() {
    if (loading || !ctx.desktop?.connected) return;
    loading = true;
    attempted = true;
    try {
      const result = await execute("grant_list");
      if (!root.isConnected) return;
      const records = rows(result, "grants");
      $("[data-grants]", root).innerHTML = panel(
        "现有授权",
        records.length
          ? `<ul class="clean">${records.map((item) => `<li class="row"><div class="row-copy"><strong>${h(item.label || item.id)}</strong><small>子树 ${h(item.root_id || item.root)} · ${h(item.revoked ? "已撤销" : "只读")}</small></div>${item.revoked ? "" : `<button class="danger" data-revoke="${h(item.id)}">撤销</button>`}</li>`).join("")}</ul>`
          : '<p class="empty">暂无只读授权</p>',
      );
    } catch (e) {
      if (root.isConnected) $("[data-grants]", root).textContent = e.message;
    } finally {
      loading = false;
    }
  }
  $("[data-create]", root).onclick = () =>
    dialog({
      title: "新建只读子树授权",
      body:
        input("root", "子树根节点 ID", "", { required: true }) +
        input("label", "工具或用途", "", { required: true }) +
        '<p class="hint">只授予该节点及其后代的读取权限。令牌具有访问能力，请仅交给可信工具。</p>',
      onSubmit: async (values) => {
        const result = await execute("grant_create", values);
        showText("保存只读授权材料", raw(result.details || result.summary));
        await load();
      },
    });
  root.onclick = (event) => {
    const button = event.target.closest("[data-revoke]");
    if (button)
      action(button, async () => {
        if (
          !(await confirm(
            "撤销只读授权",
            "使用此令牌的工具将无法继续读取授权子树。",
            "撤销",
          ))
        )
          return;
        await execute("grant_revoke", { id: button.dataset.revoke });
        notify("授权已撤销");
        await load();
      });
  };
  load();
  return {
    refresh: load,
    update() {
      if (!attempted) load();
    },
  };
}
