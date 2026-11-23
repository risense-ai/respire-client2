import { $, all, api, execute, notify, number, rows } from "./ui/core.js";
import { overview } from "./pages/overview.js";
import { memoryWorkspace } from "./memory/workspace.js";
import { diary } from "./memory/diary.js";
import { organize } from "./pages/organize.js";
import { sessionImport } from "./pages/session-import.js";
import { models } from "./pages/models.js";
import {
  settings,
  updates,
  diagnostics,
  operations,
} from "./pages/settings.js";
import { accounts } from "./pages/accounts.js";
import { restorePendingRecoveryMaterials } from "./pages/authentication.js";
import { injection } from "./pages/injection.js";
import { applyTypography } from "./ui/typography.js";
import { grants } from "./pages/grants.js";
import {
  applyTranslations,
  currentLanguage,
  setLanguage,
  t,
} from "./ui/i18n.js";
import { createWizard } from "./onboarding/wizard.js";
applyTypography();
// The window is an app surface, not a browser page: no native context menu.
document.addEventListener("contextmenu", (event) => event.preventDefault());

const definitions = {
  overview: ["概览", overview],
  memories: ["记忆", memoryWorkspace],
  diary: ["日记", diary],
  organize: ["整理", organize],
  "session-import": ["会话导入", sessionImport],
  models: ["模型与索引", models],
  accounts: ["账户与空间", accounts],
  injection: ["配置Agent", injection],
  grants: ["访问授权", grants],
  settings: ["设置", settings],
  updates: ["更新", updates],
  operations: ["操作记录", operations],
  diagnostics: ["诊断", diagnostics],
};
const settingsPages = ["settings", "updates", "operations", "diagnostics"];
let current, view;
let refreshPromise, flushStatus = false, verifiedStatus, stagedStatus;
let checkFirstRun = false;
// Set once the topbar dropdowns are wired; appearance() runs earlier, so it
// only syncs the theme value when the dropdown already exists.
let themeDropdown = null;
const ctx = {
  desktop: null,
  status: null,
  statusError: "",
  statusPending: true,
  statusScope: null,
  displayStatus: null,
  api,
  execute,
  notify,
  navigate,
  refresh,
  changeLanguage,
  savePreferences,
};
const wizard = createWizard(ctx, {
  onFinish: () => route(),
});
ctx.openWizard = () => wizard.show({ reopen: true });
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
  themeDropdown?.set(theme);
  document.body.dataset.layout = prefs.layout || "sidebar";
  const toggle = $("#layout-toggle");
  const title = prefs.layout === "compact" ? "切换为侧栏布局" : "切换为紧凑布局";
  toggle.title = t(title);
  toggle.setAttribute("aria-label", t(title));
  const width = Number(prefs.sidebarWidth) || 0;
  document.documentElement.style.setProperty(
    "--sidebar-w",
    width ? `${width}px` : "",
  );
}
appearance();
systemTheme.addEventListener("change", appearance);
async function savePreferences(patch) {
  if (!ctx.desktop) throw new Error("客户端设置尚未读取");
  // Post only the patch: the bridge overlays present keys onto the stored
  // preferences, so a stale local mirror can never rewrite fields the
  // caller did not touch (this un-did checkbox toggles).
  await api("preferences", patch);
  if (ctx.desktop)
    ctx.desktop.preferences = { ...ctx.desktop.preferences, ...patch };
  if (patch.theme) localStorage.setItem("respire-theme", patch.theme);
  if (patch.language) languageDropdown.set(patch.language);
  if (ctx.desktop || patch.theme) appearance();
}
async function changeLanguage(value) {
  await savePreferences({ language: value });
  setLanguage(value);
  location.reload();
}
function navigate(page) {
  // While the onboarding wizard owns the screen, ordinary routes cannot be
  // reached; the wizard navigates through ctx.navigate only after pausing
  // itself (e.g. the updates entry), when isActive() is already false.
  if (wizard.isActive()) return;
  location.hash = page;
}
function route() {
  if (wizard.isActive()) return;
  const [page = "overview", query = ""] = (
    location.hash.slice(1) || "overview"
  ).split("?");
  ctx.params = new URLSearchParams(query);
  if (!definitions[page]) {
    navigate("overview");
    return;
  }
  current = page;
  view?.dispose?.();
  notify("");
  const root = document.createElement("section");
  root.className = "view-page";
  $("#view").replaceChildren(root);
  $("#breadcrumb").textContent = definitions[page][0];
  const inSettings = settingsPages.includes(page);
  if (inSettings) {
    const navigation = document.createElement("div");
    navigation.className = "tabs";
    navigation.setAttribute("role", "navigation");
    navigation.setAttribute("aria-label", "设置");
    settingsPages.forEach((key) => {
      const button = document.createElement("button");
      button.type = "button";
      button.textContent = definitions[key][0];
      button.classList.toggle("active", key === page);
      if (key === page) button.setAttribute("aria-current", "page");
      button.addEventListener("click", () => navigate(key));
      navigation.append(button);
    });
    root.before(navigation);
  }
  all("nav [data-page]").forEach((button) => {
    const active = button.dataset.page === page ||
      (button.dataset.page === "settings" && inSettings);
    button.classList.toggle("active", active);
    if (active)
      button.setAttribute("aria-current", "page");
    else button.removeAttribute("aria-current");
  });
  view = definitions[page][1](root, ctx);
  document.querySelector(".view-scroll").scrollTop = 0;
}
// Memories use status; the diary badge uses a lightweight date-filtered read.
// Organize suggestions are fetched by the Organize page, not by this timer.
const navCounts = { at: 0, running: false };
async function refreshNavCounts() {
  if (ctx.status?.summary?.unlocked !== true || !ctx.statusScope || navCounts.running) return;
  const scope = ctx.statusScope;
  if (Date.now() - navCounts.at < 60000) return;
  navCounts.running = true;
  navCounts.at = Date.now();
  try {
    const day = new Date().toISOString().slice(0, 10);
    const diaryEnv = await execute("diary_list", { from: day, to: day });
    if (!document.isConnected || ctx.statusScope !== scope || ctx.status?.summary?.unlocked !== true) return;
    $("#nav-diary-count").textContent = number(rows(diaryEnv).length);
  } catch {
    if (ctx.statusScope === scope) $("#nav-diary-count").textContent = "";
  } finally {
    navCounts.running = false;
  }
}
// Keep unchanged refresh labels' text nodes intact, including for accessibility.
// Compare the live DOM rather than cached values so external changes are repaired.
function setRefreshText(element, value) {
  const text = value == null ? "" : `${value}`;
  if (element.textContent !== text) element.textContent = text;
}
// Status carries a directory but not the Runtime RPC scope that produced it.
function healthScope(desktop) {
  const health = desktop?.health;
  if (!desktop?.connected || !health?.account_id || !health.instance_id ||
      !Number.isSafeInteger(health.generation) || health.generation < 1 || !health.data_dir)
    return null;
  return JSON.stringify([health.account_id, health.instance_id, health.generation, health.data_dir]);
}

function healthObservation(desktop) {
  const scope = healthScope(desktop);
  if (!scope) return null;
  const health = desktop.health;
  return JSON.stringify([scope, health.credential_state, health.store_ready]);
}

function statusMatchesDesktop(status, desktop) {
  const summary = status?.summary;
  return status?.command === "status" && summary?.data_dir === desktop.health.data_dir &&
    typeof summary.unlocked === "boolean" &&
    !(desktop.health.credential_state === "required" && summary.unlocked);
}

function renderRefresh() {
  const desktop = ctx.desktop;
  if (desktop) appearance();
  $("#connection-dot").classList.toggle("online", Boolean(desktop?.connected));
  setRefreshText($("#connection-label"), desktop?.connected ? t("Runtime 已连接") : t("Runtime 未连接"));
  setRefreshText($("#client-version"), desktop ? t("客户端") + " " + desktop.version : "");
  setRefreshText($("#client-platform"), desktop?.platform);
  const s = ctx.displayStatus?.summary || {};
  const count = s.local_alive == null ? "" : number(s.local_alive);
  setRefreshText($("#nav-memory-count"), count);
  setRefreshText($("#status-summary"), !desktop?.connected
    ? t("Runtime 未连接")
    : count === ""
      ? t(ctx.statusError ? "暂时无法读取本机状态，请点击刷新。" : "正在读取本机状态")
      : currentLanguage() === "zh"
        ? `${count} 条记忆 · ${s.workspace || "normal"} · Runtime ${desktop.health?.bin || "—"}`
        : `${count} memories · ${s.workspace || "normal"} · Runtime ${desktop.health?.bin || "—"}`);
  const sync = s.sync_live;
  setRefreshText($("#sync-summary"), ctx.statusError
    ? t(ctx.displayStatus ? "正在重连，显示上次状态。" : "同步状态待确认")
    : !sync ? t("同步状态待确认")
      : currentLanguage() === "zh"
        ? `同步 ${sync.phase || "—"} · 待处理 ${number(sync.pending)} · 冲突 ${number(sync.conflicts)}`
        : `Sync ${sync.phase || "—"} · pending ${number(sync.pending)} · conflicts ${number(sync.conflicts)}`);
  // A page can fail to render independently of the verified snapshot.
  // Observe both synchronous throws and asynchronous rejection without
  // letting an unrelated page hold up the Runtime polling cadence.
  try {
    wizard.updateModelConfirmation();
    Promise.resolve(view?.update?.()).catch(reportRefreshError);
  } catch (error) {
    reportRefreshError(error);
  }
}

function reportRefreshError(error) {
  console.error("Client refresh failed", error);
}

function failRefresh(error) {
  reportRefreshError(error);
  stagedStatus = null;
  try {
    publishStatus(null, { error: t("暂时无法读取本机状态，请点击刷新。"), invalidate: true });
  } catch (renderError) {
    reportRefreshError(renderError);
  }
}

function publishStatus(desktop, { checking = false, error = "", invalidate = false } = {}) {
  const observation = healthObservation(desktop);
  if (invalidate || !observation || observation !== verifiedStatus?.observation)
    verifiedStatus = null;
  ctx.desktop = desktop;
  const scope = invalidate ? null : healthScope(desktop);
  const clearDiary = scope !== ctx.statusScope || desktop?.health?.credential_state === "required";
  ctx.statusScope = scope;
  ctx.displayStatus = verifiedStatus?.status || null;
  ctx.status = checking || error ? null : ctx.displayStatus;
  ctx.statusPending = checking;
  ctx.statusError = error;
  if (clearDiary) {
    navCounts.at = 0;
    setRefreshText($("#nav-diary-count"), "");
  }
  renderRefresh();
}

function refresh({ immediate = true, initial } = {}) {
  flushStatus ||= immediate;
  if (immediate && ctx.status) {
    try { publishStatus(ctx.desktop, { checking: true }); }
    catch (error) { failRefresh(error); }
  }
  if (refreshPromise) return refreshPromise;
  refreshPromise = refreshStatus(initial).finally(() => {
    refreshPromise = null;
    flushStatus = false;
  }).then(() => {
    Promise.resolve(refreshNavCounts()).catch(reportRefreshError);
    maybeShowFirstRun();
  }).catch(failRefresh);
  return refreshPromise;
}

async function refreshStatus(initial) {
  let desktop;
  try {
    desktop = initial || await api("state");
  } catch (error) {
    stagedStatus = null;
    publishStatus(null, { error: error.message, invalidate: true });
    return;
  }
  const observation = healthObservation(desktop);
  if (!observation) {
    stagedStatus = null;
    publishStatus(desktop, { error: desktop.connectionError ||
      t(desktop.connected ? "Runtime 身份尚未确认，请点击刷新。" : "Runtime 未连接"), invalidate: true });
    return;
  }
  // The regular next Health observation closes the preceding sample. This
  // brackets Status without a second expensive Desktop-state read per poll.
  if (stagedStatus?.observation === observation)
    verifiedStatus = stagedStatus;
  stagedStatus = null;
  const hadVerified = verifiedStatus?.observation === observation;
  if (!hadVerified || flushStatus || ctx.statusError)
    publishStatus(desktop, { checking: true });
  else
    publishStatus(desktop); // Ordinary polling retains its last *verified* pair.
  let status;
  try {
    status = await api("runtime-status");
  } catch (error) {
    publishStatus(desktop, { error: error.message });
    return;
  }
  if (!statusMatchesDesktop(status, desktop)) {
    publishStatus(desktop, { error: t("Runtime 身份尚未确认，请点击刷新。"), invalidate: true });
    return;
  }
  const lockChanged = hadVerified &&
    verifiedStatus.status.summary.unlocked !== status.summary.unlocked;
  if (lockChanged) publishStatus(desktop, { checking: true, invalidate: true });
  if (hadVerified && !lockChanged && !flushStatus && !ctx.statusPending) {
    stagedStatus = { observation, status };
    return;
  }
  // On first load, an identity change, transport recovery or a user retry,
  // flush now. A manual request arriving during a poll upgrades that poll.
  let end;
  try {
    end = await api("state");
  } catch (error) {
    publishStatus(null, { error: error.message, invalidate: true });
    return;
  }
  if (healthObservation(end) !== observation) {
    publishStatus(end, { error: t("Runtime 身份尚未确认，请点击刷新。"), invalidate: true });
    return;
  }
  verifiedStatus = { observation, status };
  publishStatus(end);
}

all("nav [data-page]").forEach((button) =>
  button.addEventListener("click", () => navigate(button.dataset.page)),
);
// The local-workspace card is a shortcut to the accounts page.
$("#workspace-entry").addEventListener("click", () => navigate("accounts"));
// Self-drawn dropdowns: native selects render with dated system chrome on
// some platforms, so the topbar controls draw their own listbox.
function initDropdown(dropdown, onChange) {
  const toggle = $(".dropdown-toggle", dropdown),
    menu = $(".dropdown-menu", dropdown),
    value = $(".dropdown-value", dropdown);
  const options = all("[role=option]", menu);
  const close = () => {
    menu.hidden = true;
    toggle.setAttribute("aria-expanded", "false");
  };
  const pick = (option) => {
    options.forEach((item) =>
      item.setAttribute("aria-selected", String(item === option)),
    );
    value.textContent = option.textContent;
    close();
    onChange(option.dataset.value);
  };
  toggle.addEventListener("click", () => {
    menu.hidden = !menu.hidden;
    toggle.setAttribute("aria-expanded", String(!menu.hidden));
    if (!menu.hidden)
      (
        options.find((item) => item.getAttribute("aria-selected") === "true") ||
        options[0]
      )?.focus();
  });
  menu.addEventListener("click", (event) => {
    const option = event.target.closest("[role=option]");
    if (option) pick(option);
  });
  menu.addEventListener("keydown", (event) => {
    const index = options.indexOf(document.activeElement);
    if (event.key === "Escape") {
      close();
      toggle.focus();
    } else if (event.key === "ArrowDown") {
      event.preventDefault();
      options[Math.min(options.length - 1, index + 1)]?.focus();
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      options[Math.max(0, index - 1)]?.focus();
    } else if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      const option = document.activeElement?.closest("[role=option]");
      if (option) pick(option);
    }
  });
  document.addEventListener("click", (event) => {
    if (!dropdown.contains(event.target)) close();
  });
  return {
    set(next) {
      const option = options.find((item) => item.dataset.value === next);
      if (!option) return;
      options.forEach((item) =>
        item.setAttribute("aria-selected", String(item === option)),
      );
      value.textContent = option.textContent;
    },
  };
}
themeDropdown = initDropdown($("#theme-dropdown"), async (value) => {
  try {
    await savePreferences({ theme: value });
  } catch (error) {
    notify(error.message, true);
    appearance();
  }
});
const languageDropdown = initDropdown(
  $("#language-dropdown"),
  async (value) => {
    try {
      await changeLanguage(value);
    } catch (error) {
      notify(error.message, true);
      languageDropdown.set(currentLanguage());
    }
  },
);
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
const SIDEBAR_MIN = 170,
  SIDEBAR_MAX = 380;
$("#sidebar-resizer").addEventListener("mousedown", (event) => {
  if (document.body.dataset.layout === "compact") return;
  event.preventDefault();
  document.body.classList.add("resizing");
  const apply = (clientX) =>
    document.documentElement.style.setProperty(
      "--sidebar-w",
      `${Math.min(SIDEBAR_MAX, Math.max(SIDEBAR_MIN, clientX))}px`,
    );
  const move = (e) => apply(e.clientX);
  const up = (e) => {
    window.removeEventListener("mousemove", move);
    window.removeEventListener("mouseup", up);
    document.body.classList.remove("resizing");
    apply(e.clientX);
    const width = Math.round(
      Math.min(SIDEBAR_MAX, Math.max(SIDEBAR_MIN, e.clientX)),
    );
    savePreferences({ sidebarWidth: width }).catch((error) =>
      notify(error.message, true),
    );
  };
  window.addEventListener("mousemove", move);
  window.addEventListener("mouseup", up);
});
$("#refresh").addEventListener("click", async () => {
  await refresh();
  await view?.refresh?.();
});
// While the wizard is active it owns the screen: hash edits from the
// keyboard or extensions are rolled back instead of re-routing.
window.addEventListener("hashchange", () => {
  if (wizard.isActive()) {
    if (location.hash.slice(1).split("?")[0] !== current)
      location.hash = current;
    return;
  }
  route();
});
document.addEventListener("respire-operation", () => refresh({ immediate: false }));
let translationScheduled = false;
new MutationObserver(() => {
  if (translationScheduled) return;
  translationScheduled = true;
  requestAnimationFrame(() => {
    translationScheduled = false;
    applyTranslations();
  });
}).observe(document.documentElement, {
  childList: true,
  subtree: true,
  characterData: true,
  attributes: true,
  attributeFilter: ["title", "placeholder", "aria-label"],
});
function maybeShowFirstRun() {
  if (!checkFirstRun || !ctx.desktop) return;
  if (!ctx.desktop.preferences?.onboardingCompleted && ctx.desktop.connected && !ctx.status)
    return; // A transient status failure does not establish a new account.
  checkFirstRun = false;
  if (ctx.desktop.preferences?.onboardingCompleted || ctx.desktop.localSessionHasToken ||
      ctx.status?.summary?.session?.has_token) return;
  wizard.show(); // A clean install may need the wizard to install/start Runtime.
}
async function poll(initial) {
  try {
    await refresh({ immediate: false, initial });
  } catch (error) {
    reportRefreshError(error);
  } finally {
    setTimeout(poll, document.hidden ? 15000 : 3000);
  }
}
(async () => {
  let initial;
  try {
    initial = await api("state");
    ctx.desktop = initial; // The first route needs host preferences even before Status.
    const preferred =
      initial.preferences?.language ||
      localStorage.getItem("respire-language") ||
      "";
    if (preferred) {
      setLanguage(preferred);
      languageDropdown.set(preferred);
    }
  } catch {
    const preferred = localStorage.getItem("respire-language");
    if (preferred) setLanguage(preferred);
  }
  route();
  // First run: the wizard replaces the old first-language layer and covers
  // every step from language to model enablement. The auto-show waits for
  // the first status read and never opens when this machine already has a
  // local account; the entry in Settings → About still opens a full re-run.
  const firstPoll = poll(initial);
  try {
    await restorePendingRecoveryMaterials();
  } catch (error) {
    notify(error.message, true);
  }
  await firstPoll;
  checkFirstRun = true;
  maybeShowFirstRun();
})().catch(reportRefreshError);
