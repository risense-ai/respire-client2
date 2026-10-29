import {
  $,
  h,
  raw,
  summary,
  rows,
  execute,
  action,
  notify,
  panel,
  confirm,
  resultView,
} from "../ui/core.js";
import { heading } from "./overview.js";

export function injection(root, ctx) {
  let revision = "",
    selected = "",
    remove = false,
    attempted = false,
    loading = false;
  root.innerHTML =
    heading(
      "让上下文，连接到工具。",
      "查看本机注入目标并确认应用。Codex 支持文件预览与修订校验。",
    ) +
    `<div class="grid">${panel("注入目标", '<div data-targets class="empty">等待读取</div>')}${panel("预览与应用", '<div class="panel-body stack"><p data-target-name class="hint">选择左侧目标</p><label class="checkbox-label"><input type="checkbox" data-remove>移除此目标中的注入内容</label><div class="actions"><button data-preview disabled>预览变更</button><button data-apply class="primary" disabled>应用已预览变更</button></div><p data-preview-note class="hint"></p><div data-preview-result></div></div>')}</div>`;
  async function load() {
    if (loading) return;
    loading = true;
    attempted = true;
    try {
      const result = await execute("inject_targets");
      if (!root.isConnected) return;
      const targets = rows(result, "targets");
      $("[data-targets]", root).className = "";
      $("[data-targets]", root).innerHTML = targets.length
        ? `<ul class="clean">${targets.map((item) => `<li class="row"><div class="row-copy"><strong>${h(item.name || item.id)}</strong><small>${h(item.state || "—")}</small></div><button data-target="${h(item.id)}" data-name="${h(item.name || item.id)}">选择</button></li>`).join("")}</ul>`
        : '<div class="empty">没有返回注入目标</div>';
    } catch (error) {
      if (root.isConnected)
        $("[data-targets]", root).textContent = error.message;
    } finally {
      loading = false;
    }
  }
  root.addEventListener("click", (event) => {
    const button = event.target.closest("button");
    if (!button) return;
    if (button.dataset.target) {
      selected = button.dataset.target;
      revision = "";
      $("[data-target-name]", root).textContent = button.dataset.name;
      $("[data-preview]", root).disabled = selected !== "codex";
      $("[data-apply]", root).disabled = selected === "codex";
      $("[data-apply]", root).textContent =
        selected === "codex" ? "应用已预览变更" : "确认应用";
      $("[data-preview-note]", root).textContent =
        selected === "codex"
          ? "先预览；目标文件改变后需要重新预览。"
          : "当前 Runtime 不支持此目标的文件预览。确认后由 CLI 更新本机配置。";
      $("[data-preview-result]", root).textContent = "";
    }
    if (button.hasAttribute("data-preview"))
      action(button, async () => {
        const result = await execute("inject_preview", {
          id: selected,
          remove,
        });
        const value = {
          ...summary(result),
          ...(result.details && !Array.isArray(result.details)
            ? result.details
            : {}),
        };
        revision = value.revision || value.expected || "";
        $("[data-preview-result]", root).innerHTML = resultView(
          "注入预览",
          result,
        );
        $("[data-apply]", root).disabled = !revision;
        if (!revision)
          notify("Runtime 未返回可应用的修订版本，未开放写入。", true);
      });
    if (button.hasAttribute("data-apply"))
      action(button, async () => {
        if (selected === "codex" && !revision) throw new Error("请先预览");
        if (
          !(await confirm(
            "应用注入变更",
            `将${remove ? "移除" : "更新"}目标「${selected}」的本机配置文件。`,
            "应用",
          ))
        )
          return;
        const result = await execute("inject_apply", {
          id: selected,
          ...(selected === "codex" ? { revision } : {}),
          remove,
        });
        $("[data-preview-result]", root).innerHTML = resultView(
          "注入结果",
          result,
        );
        revision = "";
        button.disabled = true;
        await load();
      });
  });
  $("[data-remove]", root).addEventListener("change", (event) => {
    remove = event.target.checked;
    revision = "";
    $("[data-apply]", root).disabled = !selected || selected === "codex";
  });
  if (ctx.desktop?.connected) load();
  return {
    refresh: load,
    update() {
      if (!attempted && ctx.desktop?.connected) load();
    },
  };
}
