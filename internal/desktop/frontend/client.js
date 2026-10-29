import { $, all, api, execute, notify, number } from "./ui/core.js";
import { overview } from "./pages/overview.js";
import { memoryWorkspace } from "./memory/workspace.js";
import { diary } from "./memory/diary.js";
import { organize } from "./pages/organize.js";
import { models } from "./pages/models.js";
import {
  settings,
  updates,
  diagnostics,
  operations,
} from "./pages/settings.js";
import { accounts } from "./pages/accounts.js";
import { injection } from "./pages/injection.js";

const definitions = {
  overview: ["概览", overview],
  memories: ["记忆", memoryWorkspace],
  diary: ["日记", diary],
  organize: ["整理", organize],
  models: ["模型与索引", models],
  accounts: ["账户与空间", accounts],
  injection: ["上下文注入", injection],
  settings: ["设置", settings],
  updates: ["更新", updates],
  operations: ["操作记录", operations],
  diagnostics: ["诊断", diagnostics],
};
let current,
  view,
  refreshing = false;
const ctx = {
  desktop: null,
  status: null,
  statusError: "",
  api,
  execute,
  notify,
  navigate,
  refresh,
  savePreferences,
};
const systemTheme = matchMedia("(prefers-color-scheme: dark)");
function appearance() {
  const prefs = ctx.desktop?.preferences || {};
  const theme =
    prefs.theme || localStorage.getItem("respire-theme") || "system";
  const resolved =
    theme === "system" ? (systemTheme.matches ? "dark" : "light") : theme;
  document.documentElement.dataset.theme = resolved;
  $("#brand-icon").src =
    resolved === "dark" ? "brand/respire.svg" : "brand/respire-light.svg";
  $("#theme").value = theme;
  document.body.dataset.layout = prefs.layout || "sidebar";
  $("#layout-toggle").textContent =
    prefs.layout === "compact" ? "侧栏布局" : "紧凑布局";
}
appearance();
systemTheme.addEventListener("change", appearance);
async function savePreferences(patch) {
  if (!ctx.desktop) throw new Error("客户端设置尚未读取");
  const preferences = { ...ctx.desktop.preferences, ...patch };
  await api("preferences", preferences);
  ctx.desktop.preferences = preferences;
  localStorage.setItem("respire-theme", preferences.theme);
  appearance();
}
function navigate(page) {
  location.hash = page;
}
function route() {
  const page = location.hash.slice(1) || "overview";
  if (!definitions[page]) {
    navigate("overview");
    return;
  }
  current = page;
  view?.dispose?.();
  const root = document.createElement("section");
  root.className = "view-page";
  $("#view").replaceChildren(root);
  $("#breadcrumb").textContent = definitions[page][0];
  all("nav [data-page]").forEach((button) => {
    button.classList.toggle("active", button.dataset.page === page);
    if (button.dataset.page === page)
      button.setAttribute("aria-current", "page");
    else button.removeAttribute("aria-current");
  });
  view = definitions[page][1](root, ctx);
  document.querySelector(".view-scroll").scrollTop = 0;
}
async function refresh() {
  if (refreshing) return;
  refreshing = true;
  try {
    ctx.desktop = await api("state");
    appearance();
    $("#connection-dot").classList.toggle("online", ctx.desktop.connected);
    $("#connection-label").textContent = ctx.desktop.connected
      ? "Runtime 已连接"
      : "Runtime 未连接";
    $("#client-version").textContent = "客户端 " + ctx.desktop.version;
    $("#client-platform").textContent = ctx.desktop.platform;
    if (ctx.desktop.connected) {
      try {
        ctx.status = await api("runtime-status");
        ctx.statusError = "";
      } catch (error) {
        ctx.status = null;
        ctx.statusError = error.message;
      }
    } else {
      ctx.status = null;
      ctx.statusError = ctx.desktop.connectionError || "Runtime 未连接";
    }
    const s = ctx.status?.summary || {};
    $("#nav-memory-count").textContent = number(s.local_alive);
    $("#status-summary").textContent = ctx.desktop.connected
      ? `${number(s.local_alive)} 条记忆 · ${s.workspace || "normal"} · Runtime ${ctx.desktop.health?.bin || "—"}`
      : "Runtime 未连接";
    const sync = s.sync_live || {};
    $("#sync-summary").textContent = ctx.statusError
      ? "状态读取失败"
      : `同步 ${sync.phase || "—"} · 待处理 ${number(sync.pending)} · 冲突 ${number(sync.conflicts)}`;
    view?.update?.();
  } catch (error) {
    notify(error.message, true);
  } finally {
    refreshing = false;
  }
}
all("nav [data-page]").forEach((button) =>
  button.addEventListener("click", () => navigate(button.dataset.page)),
);
$("#theme").addEventListener("change", async (event) => {
  try {
    await savePreferences({ theme: event.target.value });
  } catch (error) {
    notify(error.message, true);
    appearance();
  }
});
$("#layout-toggle").addEventListener("click", async () => {
  try {
    await savePreferences({
      layout:
        ctx.desktop.preferences.layout === "compact" ? "sidebar" : "compact",
    });
  } catch (error) {
    notify(error.message, true);
  }
});
$("#refresh").addEventListener("click", async () => {
  await refresh();
  await view?.refresh?.();
});
window.addEventListener("hashchange", route);
document.addEventListener("respire-operation", () => refresh());
route();
async function poll() {
  await refresh();
  setTimeout(poll, document.hidden ? 15000 : 3000);
}
poll();
