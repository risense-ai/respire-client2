// Tray quick panel: mirrors the overview page in a small flyout. Renders
// Chinese first; ui/i18n.js translates to English when the app language is en.
import {
  $,
  h,
  number,
  date,
  bytes,
  api,
  execute,
  notify,
  panel,
  pairs,
  metric,
} from "./ui/core.js";
import { applyTypography } from "./ui/typography.js";
import { applyTranslations, setLanguage } from "./ui/i18n.js";
applyTypography();
// App surface, not a browser page: no native context menu.
document.addEventListener("contextmenu", (event) => event.preventDefault());

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
const phase = (value) => phases[value] || value || "—";

const state = { desktop: null, status: null, statusError: "", syncing: false };

const systemTheme = matchMedia("(prefers-color-scheme: dark)");
function appearance() {
  const prefs = state.desktop?.preferences || {};
  const theme =
    prefs.theme || localStorage.getItem("respire-theme") || "system";
  const resolved =
    theme === "system" ? (systemTheme.matches ? "dark" : "light") : theme;
  document.documentElement.dataset.theme = resolved;
  $("#brand-icon").src =
    resolved === "dark" ? "brand/respire.svg" : "brand/respire-light.svg";
}
systemTheme.addEventListener("change", () => {
  appearance();
  render();
});

function render() {
  const s = state.status?.summary || {},
    sync = s.sync_live || {},
    index = s.retrieval_index || {},
    inference = state.desktop?.health?.inference || {},
    op = s.model_operation || {},
    session = s.session || {};
  const connected = Boolean(state.desktop?.connected);
  $("#qp-dot").classList.toggle("online", connected);
  $("#qp-connection-label").textContent = connected
    ? `Runtime ${state.desktop.health?.bin || "—"}`
    : "Runtime 未连接";
  $("#qp-body").innerHTML =
    `<div class="qp-metrics">${metric("本机记忆", number(s.local_alive), `总 ${number(s.local_total)} 条，包含已删除记录`)}${metric("云端记忆", number(sync.remote_alive), s.remote_configured ? "已配置云端" : "尚未连接云端")}${metric("待同步", number(sync.pending), `${number(sync.conflicts)} 项冲突`)}${metric("索引状态", phase(index.state), index.error || "")}</div>` +    panel(
      "同步",
      pairs([
        ["状态", phase(sync.phase)],
        ["自动同步", s.autosync === undefined ? "—" : s.autosync ? "已开启" : "已关闭"],
        ["已拉取 / 已推送", `${number(sync.pulled)} / ${number(sync.pushed)}`],
        [
          "最近成功",
          s.sync_scheduler?.last_success_unix
            ? date(s.sync_scheduler.last_success_unix * 1000)
            : "尚未报告",
        ],
        ["账户", session.user || "本机离线"],
        ["服务地址", s.server_addr],
      ]) + (sync.error ? `<div class="panel-body form-error">${h(sync.error)}</div>` : ""),
    ) +
    panel(
      "推理与索引",
      pairs([
        ["推理状态", phase(inference.phase)],
        ["推理队列", number(inference.queued)],
        [
          "后台索引",
          index.running ? "正在构建" : index.scheduled ? "等待执行" : phase(index.state),
        ],
        ["模型任务", op.active ? `${phase(op.phase)} · ${op.item || ""}` : "当前无任务"],
      ]) +
        (op.active
          ? `<div class="panel-body">${`<div class="progress-label"><span>${h(op.item || op.phase)}</span><span>${h(bytes(op.done))} / ${h(bytes(op.total))}</span></div><progress ${op.total ? `value="${h(op.done)}" max="${h(op.total)}"` : ""}></progress>`}</div>`
          : ""),
    ) +
    (state.statusError
      ? `<p class="form-error">状态读取失败：${h(state.statusError)}</p>`
      : "");
  $("#qp-sync").disabled = state.syncing || !connected;
  $("#qp-sync").textContent = state.syncing ? "同步中…" : "立即同步";
  applyTranslations();
  fit();
}

// Size the popup window to the content so nothing ever scrolls inside.
function fit() {
  const header = $(".qp-header"),
    notice = $("#notice"),
    main = $("#qp-body"),
    footer = $(".qp-footer");
  const zoom =
    parseFloat(document.documentElement.style.zoom || "1") || 1;
  // #qp-body sits on a 1fr grid track and stretches to the window height, so
  // its scrollHeight reports the stale window size once the content shrinks
  // and the panel would never shrink back. Measure the natural content
  // height instead; the inline height is restored before anything paints.
  main.style.height = "fit-content";
  const natural = main.scrollHeight;
  main.style.height = "";
  const height = Math.ceil(
    (header.offsetHeight +
      natural +
      footer.offsetHeight +
      (notice.hidden ? 0 : notice.offsetHeight) +
      2) *
      zoom,
  );
  api("panel-size", { height }).catch(() => {});
}

async function refresh() {
  try {
    state.desktop = await api("state");
    appearance();
    if (state.desktop.connected) {
      try {
        state.status = await api("runtime-status");
        state.statusError = "";
      } catch (error) {
        state.status = null;
        state.statusError = error.message;
      }
    } else {
      state.status = null;
      state.statusError = state.desktop.connectionError || "Runtime 未连接";
    }
  } catch (error) {
    state.statusError = error.message;
  }
  render();
}

$("#qp-refresh").addEventListener("click", () => refresh());
$("#qp-open").addEventListener("click", async (event) => {
  const button = event.currentTarget;
  button.disabled = true;
  try {
    await api("open-main", {});
  } catch (error) {
    notify(error.message, true);
  } finally {
    button.disabled = false;
  }
});
$("#qp-sync").addEventListener("click", async (event) => {
  const button = event.currentTarget;
  state.syncing = true;
  render();
  try {
    await execute("sync");
    notify("同步完成");
  } catch (error) {
    notify(error.message, true);
  } finally {
    state.syncing = false;
    render();
  }
  await refresh();
});
// Refresh whenever the flyout gains focus (tray click) and poll gently.
window.addEventListener("focus", () => refresh());
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) refresh();
});
setInterval(() => {
  if (!document.hidden) refresh();
}, 3000);

(async () => {
  try {
    state.desktop = await api("state");
    const preferred =
      state.desktop.preferences?.language ||
      localStorage.getItem("respire-language") ||
      "zh";
    setLanguage(preferred);
  } catch {
    setLanguage(localStorage.getItem("respire-language") || "zh");
  }
  appearance();
  await refresh();
})();
