import { $, all, h, api, action, notify, confirm, panel } from "../ui/core.js";
import { heading } from "./overview.js";
import { hostIcons } from "../icons.js";
import { currentLanguage } from "../ui/i18n.js";

const modes = [
  ["normal", "正常（召回 + 写入）"],
  ["readonly", "只读（只召回，不写入）"],
  ["off", "关闭（记忆层停用）"],
];

const stateNames = {
  fresh: "已注入且最新",
  stale: "已注入但过期",
  none: "未注入",
  absent: "应用未检测到",
};
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

export function injection(root, ctx) {
  let targets = [],
    mode = "normal",
    openIds = new Set(),
    showAll = false,
    busy = false,
    attempted = false;

  root.innerHTML =
    heading(
      "安装提示词与插件。",
      "按 Agent 逐一启用：开启后 Client2 直接写入提示词，并释放该宿主的 Respire 插件；关闭即移除注入内容。",
    ) +
    panel(
      "提示词模式",
      `<div class="panel-body ag-modebar"><label>提示词模式<select data-mode>${modes
        .map(([value, text]) => `<option value="${value}">${text}</option>`)
        .join("")}</select></label><div class="actions"><button class="primary" data-apply-mode>应用并更新已启用的提示词</button><button data-refresh>重新检测</button></div><small class="hint">切换模式后，已启用 Agent 的提示词会按新模式重写；插件不受影响。</small></div>`,
    ) +
    panel(
      "Agent",
      `<div class="panel-body hint">开启 = 写入提示词 + 安装 / 更新该宿主插件；关闭 = 移除注入内容（插件保留）。</div><div data-agents class="ag-list"><div class="empty">等待读取</div></div>`,
    );

  const monogram = (name) => (String(name).trim()[0] || "·").toUpperCase();

  const iconHTML = (t) => {
    const icon = hostIcons[t.id];
    if (!icon) return h(monogram(t.name));
    if (icon.img) return `<img src="${h(icon.img)}" alt="">`;
    return icon.svg;
  };

  // Scoped actions inside the details: prompt write/remove and plugin install
  // stay available separately next to the row's combined switch.
  const detailActionsHTML = (t) => {
    const buttons = [];
    if (t.mode !== "plugin") {
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
    if (t.plugin)
      buttons.push(
        `<button data-scope="${h(t.id)}" data-enable="true" data-only="plugin">安装 / 更新插件</button>`,
      );
    if (t.plugin) {
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

  const statusText = (t) =>
    t.mode === "plugin"
      ? t.plugin?.source
        ? "插件源目录已就绪"
        : "插件未安装"
      : (stateNames[t.state] || t.state || "未知");

  const statusLine = (t) => {
    const parts = [statusText(t)];
    if (t.mode === "plugin" && t.plugin) {
      parts.push(t.plugin.command ? "宿主命令已找到" : "宿主命令未找到");
    } else if (t.path) {
      parts.push(t.path);
    }
    return parts.join(" · ");
  };

  const factsHTML = (t) => {
    const lines = [
      [t.mode === "plugin" ? "插件源目录" : "注入文件", t.realPath || t.path],
      [
        t.mode === "plugin" ? "安装方式" : "写入方式",
        t.mode === "plugin"
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
    const on =
      t.mode === "plugin" ? !!t.plugin?.source : t.state === "fresh";
    const active = on || t.state === "stale" || !!t.plugin?.source;
    const known =
      t.likelyInstalled || t.state !== "absent" || !!t.plugin?.source;
    const open = openIds.has(t.id);
    return `<div class="ag-row${t.state === "stale" ? " drifted" : ""}${
      known ? "" : " ag-folded"
    }" data-row="${h(t.id)}">
<span class="ag-icon" aria-hidden="true">${iconHTML(t)}</span>
<div class="ag-who">
<div class="ag-name"><strong>${h(t.name)}</strong><span class="ag-tag">${
      modeNames[t.mode] || h(t.mode)
    }</span></div>
<div class="ag-line"><span class="ag-dot ${
      on ? "on" : t.state === "stale" || active ? "stale" : ""
    }"></span><small>${h(statusLine(t))}</small></div>
</div>
<button class="ag-detail" data-detail="${h(t.id)}" aria-expanded="${
      open ? "true" : "false"
    }">详情<span class="ag-chev" aria-hidden="true">${
      open ? "︿" : "﹀"
    }</span></button>
<label class="ag-switch"><input type="checkbox" data-toggle="${h(
      t.id,
    )}" data-kind="${h(t.mode)}"${on ? " checked" : ""}${
      t.mode === "plugin" && !t.plugin ? " disabled" : ""
    }><span class="ag-slider"></span><span class="sr-only">启用 ${h(
      t.name,
    )}</span></label>
<div class="ag-open" data-open="${h(t.id)}"${
      open ? "" : " hidden"
    }>${factsHTML(t)}${detailActionsHTML(t)}</div>
</div>`;
  };

  function render() {
    const list = $("[data-agents]", root);
    if (!targets.length) {
      list.innerHTML = '<div class="empty">没有返回注入目标</div>';
      return;
    }
    const inUse = targets.filter(
      (t) => t.likelyInstalled || t.state !== "absent" || !!t.plugin?.source,
    );
    const rest = targets.filter((t) => !inUse.includes(t));
    const shown = showAll ? targets : inUse;
    list.classList.toggle("busy", busy);
    list.innerHTML =
      shown.map(rowHTML).join("") +
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
    if (data.message) return data.message; // plugin-only hosts report the install result directly
    const parts = [];
    if (data.changed !== undefined)
      parts.push(
        data.changed
          ? data.enabled
            ? "提示词已写入"
            : "注入内容已移除"
          : "提示词已是最新，未改动",
      );
    if (data.plugin)
      parts.push(
        data.plugin.ok ? `插件：${data.plugin.message}` : `插件失败：${data.plugin.message}`,
      );
    return parts.join(" · ");
  };

  async function setTarget(id, enable, scope = "all") {
    const target = targets.find((t) => t.id === id);
    if (!target) return;
    if (!enable && (target.mode === "plugin" || scope === "plugin")) {
      notify("该插件需要在宿主的插件界面移除，Client2 不自动卸载。", true);
      render();
      return;
    }
    const promptText = target.mode === "plugin" ? "" :
      scope === "prompt"
        ? enable
          ? `将${target.state === "stale" ? "按当前模式更新" : "写入"} ${target.name} 的提示词。`
          : `将移除 ${target.name} 中的注入内容。`
        : enable
          ? `将${target.plugin ? "写入提示词，并安装 / 更新该宿主的 Respire 插件" : "写入提示词"}。`
          : `将移除 ${target.name} 中的注入内容${target.plugin ? "；插件保留，可在详情中查看。" : "。"}`;
    const what =
      scope === "plugin"
        ? "将准备本机插件源目录，并在支持时调用宿主命令完成安装。"
        : promptText;
    const label =
      scope === "plugin" ? "安装插件" : enable ? `启用 ${target.name}` : `停用 ${target.name}`;
    if (what && !(await confirm(label, what, enable ? "启用" : scope === "prompt" && !enable ? "移除" : "停用"))) {
      render();
      return;
    }
    busy = true;
    render();
    try {
      const result = await api("inject-set", { id, enable, scope });
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
    setTarget(input.dataset.toggle, input.checked);
  });

  root.addEventListener("click", (event) => {
    const button = event.target.closest("button");
    if (!button) return;
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
        button.dataset.only || "all",
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

  if (ctx.desktop?.connected) load();
  return {
    refresh: load,
    update() {
      if (!attempted && ctx.desktop?.connected) load();
    },
  };
}
