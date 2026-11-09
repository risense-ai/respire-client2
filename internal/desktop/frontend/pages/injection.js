import { $, all, h, api, action, notify, confirm, panel } from "../ui/core.js";
import { heading } from "./overview.js";
import { hostIcons } from "../icons.js";
import { currentLanguage } from "../ui/i18n.js";
import {
  agentResultMessage,
  agentStatus,
  targetDetected,
  targetsForScope,
} from "../ui/agent-install.js";

const modes = [
  ["normal", "正常（召回 + 写入）"],
  ["readonly", "只读（只召回，不写入）"],
  ["off", "关闭（记忆层停用）"],
];

const modeNames = {
  inject: "覆盖",
  block: "区块",
  reference: "引用",
  "ref-block": "引用区块",
  plugin: "插件",
};
const modeHints = {
  inject: "目标文件即提示词全文",
  block: "以标记包裹写入，用户内容保留",
  reference: "宿主配置引用提示词文件",
  "ref-block": "写入引用区块，指向提示词全文",
  plugin: "无独立提示词文件，仅释放插件",
};

const SCOPE_LABELS = {
  prompt: "提示词注入",
  plugin: "插件安装",
};

const SCOPE_NOTES = {
  prompt:
    "把 Respire 提示词写入可写提示词的 Agent 工具；内容统一管理，之后可随时更新或移除。",
  plugin:
    "为提供 Respire 插件的宿主准备并安装插件，获得更完整的宿主集成；仅适用于有对应插件的 Agent 工具。",
};

const SCOPE_EMPTY = {
  prompt:
    "没有已检测到且支持提示词注入的 Agent 工具；可显示其余目标或更换安装方式。",
  plugin:
    "没有已检测到且提供插件的 Agent 工具；可显示其余目标、更换安装方式或稍后配置。",
};

export function injection(root, ctx) {
  let targets = [],
    mode = "normal",
    scope = "prompt",
    openIds = new Set(),
    showAll = false,
    busy = false,
    attempted = false;

  root.innerHTML =
    heading(
      "安装提示词与插件。",
      "两种安装方式相互独立：在对应标签页中只执行当前方式的操作；已有提示词与已装插件一律保留，切换方式不会移除它们。",
    ) +
    panel(
      "提示词模式",
      `<div class="panel-body ag-modebar"><label>提示词模式<select data-mode>${modes
        .map(([value, text]) => `<option value="${value}">${text}</option>`)
        .join("")}</select></label><div class="actions"><button class="primary" data-apply-mode>应用并更新已启用的提示词</button><button data-refresh>重新检测</button></div><small class="hint">切换模式后，已启用 Agent 的提示词会按新模式重写；插件不受影响。</small></div>`,
    ) +
    panel(
      "Agent",
      `<div class="panel-body hint" data-scope-note>${h(
        SCOPE_NOTES[scope],
      )}</div><div class="wizard-tabs" role="tablist" aria-label="安装方式"><button type="button" role="tab" data-scope-tab="prompt" aria-selected="true">${h(
        SCOPE_LABELS.prompt,
      )}</button><button type="button" role="tab" data-scope-tab="plugin" aria-selected="false">${h(
        SCOPE_LABELS.plugin,
      )}</button></div><div data-agents class="ag-list"><div class="empty">等待读取</div></div>`,
    );

  const monogram = (name) => (String(name).trim()[0] || "·").toUpperCase();

  const iconHTML = (t) => {
    const icon = hostIcons[t.id];
    if (!icon) return h(monogram(t.name));
    if (icon.img) return `<img src="${h(icon.img)}" alt="">`;
    return icon.svg;
  };

  // Scoped actions inside the details follow the active tab: the prompt tab
  // only writes or removes prompts, the plugin tab only installs plugins.
  const detailActionsHTML = (t) => {
    const buttons = [];
    if (scope === "prompt") {
      if (t.state === "fresh")
        buttons.push(
          `<button class="danger" data-scope="${h(t.id)}" data-enable="false" data-only="prompt">移除注入</button>`,
        );
      else
        buttons.push(
          `<button class="primary" data-scope="${h(t.id)}" data-enable="true" data-only="prompt">${
            t.state === "stale" ? "更新提示词" : "注入提示词"
          }</button>`,
        );
    }
    if (scope === "plugin" && t.plugin) {
      buttons.push(
        `<button class="primary" data-scope="${h(t.id)}" data-enable="true" data-only="plugin">安装 / 更新插件</button>`,
      );
      buttons.push(
        `<button data-repo="${h(t.plugin.id)}">仓库</button>`,
      );
      // The gh-proxy mirror is a China-only convenience.
      if (currentLanguage() === "zh")
        buttons.push(
          `<button data-mirror="${h(t.plugin.id)}">镜像</button>`,
        );
    }
    return buttons.length ? `<div class="actions">${buttons.join("")}</div>` : "";
  };

  const statusLine = (t) => {
    const parts = [agentStatus(t, scope)];
    if (scope === "plugin" && t.plugin) {
      parts.push(t.plugin.command ? "宿主命令已找到" : "宿主命令未找到");
    } else if (t.path) {
      parts.push(t.path);
    }
    return parts.join(" · ");
  };

  const factsHTML = (t) => {
    const pluginSide = scope === "plugin";
    // Only plugin-only hosts carry the plugin directory in path/realPath; a
    // dual host's row shows the prompt file there, so its plugin side
    // reports the prepared state instead of a wrong path.
    const sourceValue = pluginSide
      ? t.mode === "plugin"
        ? t.realPath || t.path
        : t.plugin?.source
          ? "源目录已就绪"
          : "源目录未准备"
      : t.realPath || t.path;
    const lines = [
      [pluginSide ? "插件源目录" : "注入文件", sourceValue],
      [
        pluginSide ? "安装方式" : "写入方式",
        pluginSide
          ? t.plugin?.auto
            ? "自动调用宿主命令"
            : "在宿主插件界面添加源目录"
          : `${modeNames[t.mode] || t.mode} · ${modeHints[t.mode] || ""}`,
      ],
    ];
    if (t.plugin) {
      if (t.plugin.profiles) lines.push(["Profile", t.plugin.profiles]);
      if (t.plugin.manual) lines.push(["说明", t.plugin.manual]);
    }
    return `<dl class="facts">${lines
      .map(
        ([label, value]) =>
          `<div><dt>${h(label)}</dt><dd>${h(value ?? "—")}</dd></div>`,
      )
      .join("")}</dl>`;
  };

  const rowHTML = (t) => {
    const promptSide = scope === "prompt";
    const on = promptSide ? t.state === "fresh" : !!t.plugin?.source;
    const active = promptSide ? on || t.state === "stale" : on;
    const open = openIds.has(t.id);
    return `<div class="ag-row${promptSide && t.state === "stale" ? " drifted" : ""}${
      targetDetected(t) ? "" : " ag-folded"
    }" data-row="${h(t.id)}">
<span class="ag-icon" aria-hidden="true">${iconHTML(t)}</span>
<div class="ag-who">
<div class="ag-name"><strong>${h(t.name)}</strong><span class="ag-tag">${
      modeNames[t.mode] || h(t.mode)
    }</span></div>
<div class="ag-line"><span class="ag-dot ${
      on ? "on" : active ? "stale" : ""
    }"></span><small>${h(statusLine(t))}</small></div>
</div>
<button class="ag-detail" data-detail="${h(t.id)}" aria-expanded="${
      open ? "true" : "false"
    }">详情<span class="ag-chev" aria-hidden="true">${
      open ? "︿" : "﹀"
    }</span></button>
<label class="ag-switch"><input type="checkbox" data-toggle="${h(
      t.id,
    )}" data-kind="${h(t.mode)}"${on ? " checked" : ""}><span class="ag-slider"></span><span class="sr-only">${
      promptSide
        ? `注入 ${h(t.name)} 提示词`
        : `安装 ${h(t.name)} 插件`
    }</span></label>
<div class="ag-open" data-open="${h(t.id)}"${
      open ? "" : " hidden"
    }>${factsHTML(t)}${detailActionsHTML(t)}</div>
</div>`;
  };

  function render() {
    all("[data-scope-tab]", root).forEach((tab) =>
      tab.setAttribute("aria-selected", String(tab.dataset.scopeTab === scope)),
    );
    const note = $("[data-scope-note]", root);
    if (note) note.textContent = SCOPE_NOTES[scope];
    const list = $("[data-agents]", root);
    if (!targets.length) {
      list.innerHTML = '<div class="empty">没有返回注入目标</div>';
      return;
    }
    // The tab filter always applies; undetected targets stay behind 显示其余.
    const supported = targetsForScope(targets, scope);
    const inUse = supported.filter((t) => targetDetected(t));
    const rest = supported.filter((t) => !inUse.includes(t));
    const shown = showAll ? supported : inUse;
    list.classList.toggle("busy", busy);
    list.innerHTML =
      (shown.length
        ? shown.map(rowHTML).join("")
        : `<div class="empty">${h(SCOPE_EMPTY[scope])}</div>`) +
      (rest.length
        ? `<div class="ag-fold"><button data-fold>${
            showAll ? "收起" : "显示其余"
          } ${h(rest.length)} 个</button></div>`
        : "");
  }

  let loading = false;
  async function load() {
    if (loading) return;
    loading = true;
    attempted = true;
    if (!ctx.desktop?.connected) {
      $("[data-agents]", root).innerHTML =
        '<div class="empty">Runtime 未连接，无法读取注入目标</div>';
      return;
    }
    try {
      const result = await api("inject-targets");
      if (!root.isConnected) return;
      mode = result.mode || "normal";
      targets = result.targets || [];
      $("[data-mode]", root).value = mode;
      render();
    } catch (error) {
      if (root.isConnected) {
        $("[data-agents]", root).innerHTML = `<div class="empty">${h(
          error.message,
        )}</div>`;
      }
    } finally {
      loading = false;
    }
  }

  const resultMessage = (data) => {
    if (data.mode) {
      return `模式已保存为${modes.find(([key]) => key === data.mode)?.[1] || data.mode}，已更新 ${data.updated ?? 0} 个目标的提示词`;
    }
    return agentResultMessage(data);
  };

  // Every apply on this page carries the active tab's scope; the combined
  // scope=all switch is no longer exposed here.
  async function setTarget(id, enable, opScope = scope) {
    const target = targets.find((t) => t.id === id);
    if (!target) return;
    if (!enable && opScope === "plugin") {
      notify("该插件需要在宿主的插件界面移除，Client2 不自动卸载。", true);
      render();
      return;
    }
    const what =
      opScope === "plugin"
        ? "将准备本机插件源目录，并在支持时调用宿主命令完成安装。"
        : enable
          ? `将${target.state === "stale" ? "按当前模式更新" : "写入"} ${target.name} 的提示词。`
          : `将移除 ${target.name} 中的注入内容。`;
    const label =
      opScope === "plugin"
        ? "安装插件"
        : enable
          ? `启用 ${target.name}`
          : "移除注入";
    if (
      !(await confirm(
        label,
        what,
        opScope === "plugin" ? "安装" : enable ? "启用" : "移除",
      ))
    ) {
      render();
      return;
    }
    busy = true;
    render();
    try {
      const result = await api("inject-set", { id, enable, scope: opScope });
      notify(resultMessage(result));
    } catch (error) {
      notify(error.message, true);
    }
    busy = false;
    await load();
  }

  root.addEventListener("change", (event) => {
    const input = event.target.closest("[data-toggle]");
    if (!input) return;
    if (busy) {
      input.checked = !input.checked;
      return;
    }
    setTarget(input.dataset.toggle, input.checked, scope);
  });

  root.addEventListener("click", (event) => {
    const button = event.target.closest("button");
    if (!button) return;
    if (button.dataset.scopeTab) {
      if (busy || scope === button.dataset.scopeTab) return;
      // Switching tabs keeps page state but never carries the other tab's
      // fold/results into this one's actions.
      scope = button.dataset.scopeTab;
      showAll = false;
      render();
      return;
    }
    if (button.dataset.detail) {
      const id = button.dataset.detail;
      if (openIds.has(id)) openIds.delete(id);
      else openIds.add(id);
      render();
    }
    if (button.hasAttribute("data-fold")) {
      showAll = !showAll;
      render();
    }
    if (button.hasAttribute("data-refresh")) action(button, load);
    if (button.hasAttribute("data-apply-mode"))
      action(button, async () => {
        if (
          !(await confirm(
            "应用提示词模式",
            "将保存所选模式，并按新模式重写所有已启用 Agent 的提示词。",
            "应用",
          ))
        )
          return;
        busy = true;
        render();
        try {
          const result = await api("inject-mode", {
            mode: $("[data-mode]", root).value,
          });
          notify(resultMessage(result));
        } finally {
          busy = false;
        }
        await load();
      });
    if (button.dataset.scope)
      setTarget(
        button.dataset.scope,
        button.dataset.enable === "true",
        button.dataset.only,
      );
    if (button.dataset.repo)
      action(button, () =>
        api("open-releases", { target: "plugin-" + button.dataset.repo }),
      );
    if (button.dataset.mirror)
      action(button, () =>
        api("open-releases", {
          target: "plugin-mirror-" + button.dataset.mirror,
        }),
      );
  });

  // Disconnected Runtime: an honest explanation, never cached or fake
  // targets; re-detect becomes available once connected again.
  if (ctx.desktop?.connected) load();
  else
    $("[data-agents]", root).innerHTML =
      '<div class="empty">Runtime 未连接，无法读取注入目标</div>';
  return {
    refresh: load,
    update() {
      if (!attempted && ctx.desktop?.connected) load();
    },
  };
}
