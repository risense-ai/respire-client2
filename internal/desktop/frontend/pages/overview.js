import {
  $,
  all,
  h,
  number,
  date,
  bytes,
  metric,
  panel,
  pairs,
  execute,
  action,
  isPending,
  notify,
} from "../ui/core.js";
import {
  MODEL_OPTIONS,
  fetchProfileState,
  readProfileState,
} from "./model-kit.js";

import { stats } from "./stats.js";

const phases = {
  idle: "空闲",
  running: "正在同步",
  ok: "已完成",
  err: "同步失败",
  scheduled: "已安排",
  ready: "就绪",
  failed: "失败",
  loading: "加载中",
  unavailable: "不可用",
};
export const phase = (value) => phases[value] || value || "—";
export function heading(title, description, actions = "") {
  return `<div class="page-heading"><div><div class="eyebrow">RESPIRE / LOCAL WORKSPACE</div><h1>${h(title)}</h1><p>${h(description)}</p></div><div class="heading-actions">${actions}</div></div>`;
}
const ENGINE_NAMES = { cpu: "CPU", gpu: "GPU", npu: "NPU" };
export function overview(root, ctx) {
  // Polls update status around these controls without replacing focused buttons.
  root.innerHTML =
    '<div data-overview-summary></div>' +
    '<section class="section-gap" data-overview-stats></section>' +
    '<div class="section-gap" data-overview-details></div>' +
    panel(
      "Runtime",
      '<div data-runtime-status></div><div class="panel-body"><div class="actions"><button data-host="start">启动 Runtime</button><button data-host="stop">停止 Runtime</button><button data-go="updates">安装与更新</button></div><div data-runtime-error></div></div>',
    ) +
    '<div data-overview-onboarding></div>';
  const summaryRoot = $("[data-overview-summary]", root);
  const statsView = stats($("[data-overview-stats]", root), ctx);
  const detailsRoot = $("[data-overview-details]", root);
  const runtimeStatus = $("[data-runtime-status]", root);
  const runtimeError = $("[data-runtime-error]", root);
  const startButton = $('[data-host="start"]', root);
  const stopButton = $('[data-host="stop"]', root);
  const onboarding = $("[data-overview-onboarding]", root);
  runtimeStatus.closest(".panel").classList.add("section-gap");
  // Onboarding: prompt/plugin readiness is checked once per page visit.
  let promptsReady = null;
  // Runtime active selection (profile models); refreshed with the page.
  let profileSelection = null;
  let hostPending = false;
  async function loadProfileSelection() {
    if (!ctx.desktop?.connected) return;
    try {
      const { active } = readProfileState(await fetchProfileState());
      profileSelection = active;
    } catch {
      profileSelection = null;
    }
    if (root.isConnected) update();
  }
  function update() {
    statsView.update();
    const state = ctx.desktop || {},
      s = ctx.status?.summary || {},
      health = state.health || {},
      sync = s.sync_live || {},
      index = s.retrieval_index || {},
      inference = health.inference || {},
      op = s.model_operation || {},
      session = s.session || {};
    const nowTask = state.task || {};
    if (promptsReady === null && state.connected)
      ctx
        .api("inject-targets")
        .then((r) => {
          promptsReady = (r.targets || []).some(
            (t) => t.state === "fresh" || !!t.plugin?.source,
          );
          if (root.isConnected) update();
        })
        .catch(() => {
          promptsReady = false;
        });
    // What inference runs on right now: profile models override the legacy
    // inference.json snapshot the client reads locally.
    const selection = profileSelection;
    const snapshot = state.engine || {};
    const engine = String(
      selection?.engine || snapshot.engine || "cpu",
    ).toLowerCase();
    const engineName = ENGINE_NAMES[engine] || engine.toUpperCase();
    const modelID = selection?.model || snapshot.model_profile || "m3";
    const modelName =
      MODEL_OPTIONS.find(([id]) => id === modelID)?.[1] || modelID;
    const variant = selection?.variant || snapshot.variant || "quantized";
    const file = selection?.variant ? null : snapshot.file;
    const note = selection ? "" : snapshot.note || "";
    summaryRoot.innerHTML =
      heading(
        "记忆，保持就绪。",
        "本机数据、云端同步、模型与索引，集中查看。",
        `<button data-go="memories" class="primary">查看记忆</button>`,
      ) +
      `<div class="metrics">${metric("本机记忆", number(s.local_alive), `总计 ${number(s.local_total)} 条，包含已删除记录`)}${metric("云端记忆", number(sync.remote_alive), s.remote_configured ? "已配置云端" : "尚未连接云端")}${metric("待同步", number(sync.pending), `${number(sync.conflicts)} 项冲突`)}${metric("索引状态", phase(index.state), index.error || "")}</div>`;
    detailsRoot.innerHTML = `<div class="grid">${panel(
        "同步",
        pairs([
          ["状态", phase(sync.phase)],
          [
            "自动同步",
            s.autosync === undefined ? "—" : s.autosync ? "已开启" : "已关闭",
          ],
          [
            "已拉取 / 已推送",
            `${number(sync.pulled)} / ${number(sync.pushed)}`,
          ],
          [
            "最近成功",
            s.sync_scheduler?.last_success_unix
              ? date(s.sync_scheduler.last_success_unix * 1000)
              : "尚未报告",
          ],
          ["账户", session.user || "本机离线"],
          ["服务地址", s.server_addr],
        ]) +
          `<div class="panel-body"><button data-command="sync" ${isPending("sync") ? "disabled" : ""}>${isPending("sync") ? "同步中…" : "立即同步"}</button>${sync.error ? `<p class="form-error section-gap">${h(sync.error)}</p>` : ""}</div>`,
      )}
      ${panel(
        "模型与索引",
        pairs([
          ["当前模型", `${modelName} · ${variant}`],
          ["推理方式", engineName],
          [
            "向量文件",
            selection
              ? "档案权重已加载"
              : file
                ? file
                : `${variant}（${engineName} 默认）`,
          ],
          ["推理状态", phase(inference.phase)],
          ["推理队列", number(inference.queued)],
          [
            "后台索引",
            index.running
              ? "正在构建"
              : index.scheduled
                ? "等待执行"
                : phase(index.state),
          ],
          [
            "模型任务",
            op.active ? `${phase(op.phase)} · ${op.item || ""}` : "当前无任务",
          ],
        ]) +
          `<div class="model-progress">${note ? `<p class="hint">${h(note)}</p>` : ""}${op.active ? `<div class="progress-label"><span>${h(op.item || op.phase)}</span><span>${h(bytes(op.done))} / ${h(bytes(op.total))}</span></div><progress ${op.total ? `value="${h(op.done)}" max="${h(op.total)}"></progress>` : ""}` : ""}<div class="actions"><button data-go="models">管理模型与索引</button></div></div>`,
      )}</div>`;
    runtimeStatus.innerHTML =
      pairs([
        ["连接", state.connected ? "已连接" : "未连接"],
        ["版本", health.bin || "—"],
        [
          "管理方式",
          state.managed
            ? "客户端管理"
            : state.connected
              ? "外部 CLI 管理"
              : "未连接",
        ],
        [
          "运行中 / 排队",
          `${number(health.rpc?.executing_until_reply)} / ${number(health.rpc?.queued_or_cancelled_pending_removal)}`,
        ],
        ["客户端当前操作", nowTask.stage || "—"],
      ]) +
        `${nowTask.busy && nowTask.total ? `<div class="model-progress"><div class="progress-label"><span>${h(nowTask.stage)}</span><span>${h(bytes(nowTask.done))} / ${h(bytes(nowTask.total))}</span></div><progress value="${h(nowTask.done)}" max="${h(nowTask.total)}"></progress></div>` : ""}`;
    startButton.disabled = hostPending || Boolean(nowTask.busy);
    stopButton.disabled = hostPending || Boolean(nowTask.busy || !state.connected);
    runtimeError.innerHTML = nowTask.error
      ? `<div class="panel-body form-error">${h(nowTask.error)}</div>`
      : "";
    // First-run checklist: each step clears by real state, and the whole card
    // disappears once everything is done.
    const steps = [
      {
        done: state.connected,
        title: "连接 Runtime",
        desc: nowTask.busy
          ? nowTask.stage
          : state.connected
            ? "本机 Runtime 已就绪"
            : "客户端会自动下载并启动本机 Runtime",
        action: nowTask.busy
          ? null
          : { go: "updates", label: "安装与更新" },
      },
      {
        done: Boolean(session.user),
        title: "登录或注册账户",
        desc: "云端同步与多设备共享从这里开始",
        action: { go: "accounts", label: "前往账户" },
      },
      {
        done: Boolean(session.has_local_keys),
        title: "保存本机安全密钥",
        desc: "密钥只留在本机，用于加密记忆库",
        action: { go: "accounts", label: "前往账户" },
      },
      {
        done: Boolean(promptsReady),
        title: "写入提示词 / 安装插件",
        desc: "按 Agent 启用记忆提示词，并释放对应插件",
        action: { go: "injection", label: "前往安装" },
      },
    ];
    onboarding.innerHTML = !steps.every((step) => step.done)
      ? `<div class="panel section-gap"><div class="panel-heading"><h2>开始使用</h2></div><div class="panel-body stack">${steps
          .map(
            (step) =>
              `<div class="row spread"><div class="row-copy"><strong><span class="dot ${
                step.done ? "online" : ""
              }" style="margin-right:8px"></span>${h(step.title)}</strong><small>${h(step.desc)}</small></div>${
                step.done
                  ? '<small>已完成</small>'
                  : step.action
                    ? `<button data-go="${h(step.action.go)}">${h(step.action.label)}</button>`
                    : ""
              }</div>`,
          )
          .join("")}</div></div>`
      : "";
    if (ctx.statusError)
      onboarding.insertAdjacentHTML(
        "beforeend",
        `<p class="form-error section-gap">状态读取失败：${h(ctx.statusError)}</p>`,
      );
  }
  root.addEventListener("click", (event) => {
    const button = event.target.closest("button");
    if (!button) return;
    if (button.dataset.go) ctx.navigate(button.dataset.go);
    if (button.dataset.command)
      action(button, async () => {
        await execute(button.dataset.command);
        notify("同步完成");
        await ctx.refresh();
      });
    if (button.dataset.host)
      action(button, async () => {
        hostPending = true;
        update();
        try {
          await ctx.api(button.dataset.host, {});
          await ctx.refresh();
        } finally {
          hostPending = false;
        }
      }).finally(() => {
        if (root.isConnected) update();
      });
  });
  update();
  loadProfileSelection();
  return {
    update,
    refresh() {
      update();
      return statsView.refresh();
    },
  };
}
