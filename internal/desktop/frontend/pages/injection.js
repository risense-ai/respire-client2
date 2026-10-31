import {
  $,
  h,
  summary,
  rows,
  execute,
  api,
  action,
  notify,
  panel,
  confirm,
  resultView,
} from "../ui/core.js";
import { heading } from "./overview.js";

const modes = [
  ["normal", "正常（召回 + 写入）"],
  ["readonly", "只读（只召回，不写入）"],
  ["off", "关闭（记忆层停用）"],
];

export function injection(root, ctx) {
  let selected = "",
    remove = false,
    revision = "",
    attempted = false,
    loading = false;

  root.innerHTML =
    heading(
      "安装提示词与插件。",
      "先设置提示词模式并分发到已检测的应用，再安装各宿主的 Respire 插件。",
    ) +
    `<div class="grid">${panel(
      "注入提示词",
      `<div class="panel-body stack"><div class="facts"><div><dt>当前模式</dt><dd data-mode-name>等待读取</dd></div></div><label>提示词模式<select data-mode>${modes
        .map(([value, text]) => `<option value="${value}">${text}</option>`)
        .join("")}</select></label><div class="actions"><button class="primary" data-save-mode>保存并分发</button><button data-refresh-targets>重新检测</button></div><div data-mode-result></div></div>`,
    )}${panel(
      "注入目标",
      '<div data-targets class="empty">等待读取</div><div class="panel-body actions"><button data-apply-all>注入 / 更新全部</button></div>',
    )}${panel(
      "预览与应用",
      '<div class="panel-body stack"><p data-target-name class="hint">选择左侧目标</p><label class="checkbox-label"><input type="checkbox" data-remove>移除此目标中的注入内容</label><div class="actions"><button data-preview disabled>预览变更</button><button data-apply class="primary" disabled>应用已预览变更</button></div><p data-preview-note class="hint"></p><div data-preview-result></div></div>',
    )}</div>${panel(
      "Respire 插件",
      '<div class="panel-body stack"><p class="hint">自动安装会先准备本机源目录，再调用宿主命令。界面安装型宿主会准备目录并显示下一步。</p><div data-plugins class="empty">等待读取</div></div>',
    )}`;

  const stateText = (state) =>
    ({
      fresh: "已注入且最新",
      stale: "已注入但过期",
      none: "未注入",
      absent: "应用未检测到",
    })[state] || state || "未知";
  const modeText = (value) => modes.find(([key]) => key === value)?.[1] || value;

  async function load() {
    if (loading) return;
    loading = true;
    attempted = true;
    await Promise.allSettled([loadTargets(), loadMode(), loadPlugins()]);
    loading = false;
  }

  async function loadTargets() {
    if (!ctx.desktop?.connected) throw new Error("Runtime 未连接");
    const result = await execute("inject_targets");
    if (!root.isConnected) return;
    const targets = rows(result, "targets");
    $("[data-targets]", root).className = "";
    $("[data-targets]", root).innerHTML = targets.length
      ? `<ul class="clean">${targets
          .map(
            (item) =>
              `<li class="row"><div class="row-copy"><strong>${h(item.name || item.id)}</strong><small>${h(stateText(item.state))}</small></div><button data-target="${h(item.id)}" data-name="${h(item.name || item.id)}">选择</button></li>`,
          )
          .join("")}</ul>`
      : '<div class="empty">没有返回注入目标</div>';
  }

  async function loadMode() {
    if (!ctx.desktop?.connected) throw new Error("Runtime 未连接");
    const result = await execute("agent_config");
    if (!root.isConnected) return;
    const mode = summary(result).workspace_mode || "normal";
    $("[data-mode]", root).value = mode;
    $("[data-mode-name]", root).textContent = modeText(mode);
    $("[data-mode-result]", root).innerHTML = "";
  }

  async function loadPlugins() {
    const plugins = await api("plugins");
    if (!root.isConnected) return;
    $("[data-plugins]", root).className = "";
    $("[data-plugins]", root).innerHTML = plugins
      .map(
        (item) =>
          `<div class="row"><div class="row-copy"><strong>${h(item.name)}</strong><small>${h(
            [
              item.auto ? "自动安装" : "界面安装",
              item.command ? "宿主命令已找到" : "宿主命令未找到",
              item.source ? "源目录已准备" : "源目录未准备",
            ].join(" · "),
          )}${item.profiles ? ` · ${h(item.profiles)} profile` : ""}</small><small class="hint">${h(item.manual)}</small></div><div class="actions"><button class="primary" data-install="${h(item.id)}">${item.auto ? "安装 / 更新" : "准备源目录"}</button><button data-open="${h(item.id)}">仓库</button><button data-open-mirror="${h(item.id)}">镜像</button></div></div>`,
      )
      .join("");
  }

  async function saveMode() {
    const mode = $("[data-mode]", root).value;
    await execute("agent_set", { setting: `workspace_mode=${mode}` });
    const result = await execute("inject_all");
    $("[data-mode-name]", root).textContent = modeText(mode);
    $("[data-mode-result]", root).innerHTML = resultView(
      "提示词分发结果",
      result,
    );
    await loadTargets();
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
    if (button.hasAttribute("data-save-mode"))
      action(button, async () => {
        if (!(await confirm("分发提示词", "将按所选模式更新所有已检测的应用。", "分发")))
          return;
        await saveMode();
      });
    if (button.hasAttribute("data-refresh-targets"))
      action(button, loadTargets);
    if (button.hasAttribute("data-apply-all"))
      action(button, async () => {
        if (!(await confirm("注入全部目标", "将更新所有已检测应用中的 Respire 提示词。", "注入")))
          return;
        const result = await execute("inject_all");
        $("[data-mode-result]", root).innerHTML = resultView(
          "全部注入结果",
          result,
        );
        await loadTargets();
      });
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
        await loadTargets();
      });
    if (button.hasAttribute("data-install"))
      action(button, async () => {
        const id = button.dataset.install;
        if (
          !(await confirm(
            "安装插件",
            "将准备本机插件源目录，并在支持自动安装时调用宿主命令。",
            "安装",
          ))
        )
          return;
        const message = await api("plugin-install", { target: id });
        notify(message);
        await loadPlugins();
      });
    if (button.hasAttribute("data-open"))
      action(button, () =>
        api("open-releases", { target: "plugin-" + button.dataset.open }),
      );
    if (button.hasAttribute("data-open-mirror"))
      action(button, () =>
        api("open-releases", {
          target: "plugin-mirror-" + button.dataset.openMirror,
        }),
      );
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
