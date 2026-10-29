import {
  $,
  api,
  input,
  panel,
  execute,
  action,
  confirm,
  notify,
  summary,
} from "../ui/core.js";

export function storagePanel(root, ctx) {
  const section = document.createElement("section");
  section.className = "section-gap";
  section.innerHTML = panel(
    "Runtime 数据目录",
    `<form class="panel-body stack">${input("dir", "数据目录", ctx.status?.summary?.data_dir || "", { required: true })}<p class="hint">切换由 CLI 完成。原目录保留；不会自动搬迁记忆或复制密钥。使用 RSRS_DATA_DIR 的隔离环境还需遵循 Runtime 的路径限制。</p><div class="actions"><button type="button" data-folder>选择目录</button><button type="submit">切换数据目录</button></div></form>`,
  );
  root.append(section);
  $("[data-folder]", section).onclick = (event) =>
    action(event.currentTarget, async () => {
      const dir = await api("file-dialog", { kind: "directory" });
      if (dir) $("[name=dir]", section).value = dir;
    });
  $("form", section).onsubmit = (event) => {
    event.preventDefault();
    action($("[type=submit]", section), async () => {
      const dir = $("[name=dir]", section).value.trim();
      if (
        !(await confirm(
          "切换 Runtime 数据目录",
          `将切换到「${dir}」。原目录的数据保留，新目录需要自己的会话和密钥。请先完成当前操作并备份。`,
          "切换",
        ))
      )
        return;
      const result = await execute("data_dir_set", { dir });
      await ctx.refresh();
      const expected = summary(result).data_dir,
        actual = ctx.status?.summary?.data_dir;
      const normalize = (s) =>
        navigator.userAgent.includes("Windows")
          ? String(s).replaceAll("\\", "/").replace(/\/+$/, "").toLowerCase()
          : String(s).replace(/\/+$/, "");
      if (!actual || !expected || normalize(actual) !== normalize(expected))
        throw Error(
          "目录设置已保存，但尚未确认 Runtime 已切换。请刷新诊断核对，不要重复提交。",
        );
      notify("Runtime 已切换数据目录");
    });
  };
}
