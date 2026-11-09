import {
  $,
  all,
  h,
  bytes,
  api,
  execute,
  confirm,
  notify,
} from "../ui/core.js";
import { setLanguage, t } from "../ui/i18n.js";
import {
  issuedRecoveryMaterial,
  recoveryMaterialJson,
  runAuthentication,
  validateAuthentication,
} from "../pages/authentication.js";
import {
  MODEL_CARDS,
  MODEL_OPTIONS,
  DEVICE_VARIANTS,
  DEVICES,
  engineAvailable,
  fetchInventory,
  isProfileModel,
  openDownloadModal,
  applyEngineChoice,
} from "../pages/model-kit.js";
// First-run onboarding wizard: a resumable step machine over real runtime
// state. Steps advance only on real receipts (auth envelopes, session
// reads, task state, profile state); step markers in preferences are just
// the resume hint. Recovery material lives in memory only and never
// reaches preferences, storage or logs.

const STEP_ORDER = [
  "language",
  "sync",
  "account",
  "recovery",
  "model",
  "device",
  "confirm",
  "execute",
  "agents",
];

const STEP_TITLES = {
  language: "语言",
  sync: "云端同步",
  account: "账户",
  recovery: "恢复材料",
  model: "模型",
  device: "计算设备",
  confirm: "确认",
  execute: "完成",
  agents: "Agent 工具",
};

const DEFAULT_SERVER = "https://api.rsrs.rs";

const AGENT_SCOPES = [
  {
    id: "all",
    title: "提示词 + 插件",
    detail: "写入 Respire 提示词，并为受支持的宿主安装 / 更新 Respire 插件。",
    badge: "推荐",
  },
  {
    id: "prompt",
    title: "仅提示词注入",
    detail: "只把 Respire 提示词写入所选 Agent 工具，不安装插件。",
  },
  {
    id: "plugin",
    title: "仅安装插件",
    detail: "只为受支持的宿主准备并安装 Respire 插件，不改写提示词。",
  },
];

const AGENT_STATE_NAMES = {
  fresh: "已注入且最新",
  stale: "已注入但过期",
  none: "未注入",
  absent: "应用未检测到",
};

function agentScopeEligible(scope, target) {
  if (scope === "prompt") return target.mode !== "plugin";
  if (scope === "plugin") return Boolean(target.plugin);
  return true;
}

// agentResultMessage mirrors the injection page's receipt rendering so the
// wizard reports the same per-target outcome wording.
function agentResultMessage(result) {
  if (result.message) return result.message;
  const parts = [];
  if (result.changed !== undefined)
    parts.push(
      result.changed
        ? result.enabled
          ? "提示词已写入"
          : "注入内容已移除"
        : "提示词已是最新，未改动",
    );
  if (result.plugin)
    parts.push(
      result.plugin.ok
        ? `插件：${result.plugin.message}`
        : `插件失败：${result.plugin.message}`,
    );
  return parts.join(" · ") || "已完成";
}

const EXECUTE_STAGES = [
  ["download", "下载"],
  ["verify", "SHA-256 校验"],
  ["load", "注册 / 加载模型"],
  ["probe", "真实推理验证"],
  ["index", "索引安排"],
];

function modelLabel(id) {
  return MODEL_OPTIONS.find(([key]) => key === id)?.[1] || id;
}

export function createWizard(ctx) {
  const root = document.getElementById("onboarding");
  let state = null;
  let busy = false;
  let pill = null;
  let executeStarted = false;

  function active() {
    return Boolean(state) && !state.done && root.hidden === false;
  }

  function connected() {
    return Boolean(ctx.desktop?.connected);
  }

  function session() {
    return ctx.status?.summary?.session || {};
  }

  async function saveStep(step, extra = {}) {
    state.step = step;
    try {
      await ctx.savePreferences({ onboardingStep: step, ...extra });
    } catch (error) {
      notify(error.message, true);
    }
  }

  async function goto(step, extra = {}) {
    if (busy) return;
    state.error = "";
    await saveStep(step, extra);
    render();
  }

  function stepIndex(step) {
    const index = STEP_ORDER.indexOf(step);
    return index < 0 ? STEP_ORDER.length : index;
  }

  // ---------------------------------------------------------------- render

  function render() {
    root.hidden = false;
    root.className = "onboarding";
    // A re-run opened from Settings can be backed out; the first run has no
    // exit by design.
    root.innerHTML = `<div class="wizard-shell" role="dialog" aria-modal="true" aria-label="初始化向导"><div class="wizard-card">${state.reRun ? '<button type="button" class="wizard-exit" data-wizard-exit title="退出向导" aria-label="退出向导">×</button>' : ""}<ol class="wizard-steps">${STEP_ORDER.map(
      (step, index) => {
        const current = stepIndex(state.step);
        const cls =
          index < current ? " done" : index === current ? " current" : "";
        return `<li class="wizard-step${cls}" ${index === current ? 'aria-current="step"' : ""}><span class="wizard-step-dot"></span><span class="wizard-step-label">${h(
          STEP_TITLES[step],
        )}</span></li>`;
      },
    ).join("")}</ol><div class="wizard-body"></div></div></div>`;
    const body = $(".wizard-body", root);
    const exit = $("[data-wizard-exit]", root);
    if (exit)
      exit.addEventListener("click", () => {
        if (!state || state.agentBusy) return;
        // Backing out of a re-run keeps everything already applied and
        // writes nothing; completion markers are untouched.
        destroy();
      });
    // Back buttons are shared across steps; delegate so every re-render
    // keeps them wired.
    body.addEventListener("click", (event) => {
      const back = event.target.closest("[data-back]");
      if (back && !busy) goto(back.dataset.back);
      const skip = event.target.closest("[data-skip-wizard]");
      if (skip && !busy && !state.agentBusy) skipWizard();
    });
    const render = {
      language: renderLanguage,
      sync: renderSync,
      account: renderAccount,
      recovery: renderRecovery,
      model: renderModel,
      device: renderDevice,
      confirm: renderConfirm,
      execute: renderExecute,
      agents: renderAgents,
    }[state.step];
    (render || renderSync)(body);
  }

  function frame(title, subtitle, inner, actions = "", { skip = true } = {}) {
    // First run has no exit button, so every step carries an explicit skip
    // that ends the wizard for good; a re-run already exits from the card
    // corner without writing anything.
    const skipButton = state.reRun
      ? ""
      : `<button type="button" data-skip-wizard>跳过向导</button>`;
    return `<h1>${h(title)}</h1>${subtitle ? `<p class="wizard-sub">${h(subtitle)}</p>` : ""}${inner}<div class="wizard-actions">${skip ? skipButton : ""}${actions}</div><p class="form-error" role="alert" data-wizard-error ${state.error ? "" : "hidden"}>${h(state.error || "")}</p>`;
  }

  function card(
    label,
    detail,
    value,
    { disabled = false, badge = "", active = false } = {},
  ) {
    return `<button type="button" class="wizard-choice${active ? " active" : ""}" data-choice="${h(value)}"${disabled ? " disabled" : ""}><span class="wizard-choice-copy"><strong>${h(label)}${badge ? `<span class="wizard-badge">${h(badge)}</span>` : ""}</strong><small>${h(detail)}</small></span></button>`;
  }

  function backButton(step) {
    return `<button type="button" data-back="${h(step)}">上一步</button>`;
  }

  async function setError(message) {
    state.error = message || "";
    const node = $("[data-wizard-error]", root);
    if (node) {
      node.textContent = message || "";
      node.hidden = !message;
    }
    if (message) notify(message, true);
  }

  // ----------------------------------------------------------------- steps

  function renderLanguage(body) {
    if (!state.language)
      state.language =
        ctx.desktop?.preferences?.language ||
        localStorage.getItem("respire-language") ||
        "";
    body.innerHTML = frame(
      "选择语言",
      "之后可以在设置或右上角切换。",
      `<div class="wizard-choices">${card("English", "界面使用英文", "en", { active: state.language === "en" })}${card("中文", "界面、记忆和日志使用中文", "zh", { active: state.language === "zh" })}</div>`,
      `<button type="button" class="primary" data-next-language${state.language ? "" : " disabled"}>下一步</button>`,
    );
    all("[data-choice]", body).forEach((choice) =>
      choice.addEventListener("click", () => {
        if (busy) return;
        state.language = choice.dataset.choice;
        applyLanguage(state.language);
        render();
      }),
    );
    $("[data-next-language]", body).addEventListener("click", () => {
      if (busy || !state.language) return;
      goto("sync");
    });
  }

  // Selecting a card applies the language at once so the wizard and the UI
  // behind it retranslate; 下一步 advances without re-saving.
  function applyLanguage(value) {
    setLanguage(value);
    ctx.savePreferences({ language: value }).catch((error) => {
      notify(error.message, true);
    });
  }

  function renderSync(body) {
    body.innerHTML = frame(
      "要在其他设备上使用你的记忆吗？",
      "",
      `<div class="wizard-choices">${card(
        "启用云端同步（推荐）",
        "多设备访问，端到端加密同步；需要注册或登录账户。",
        "cloud",
        { badge: "推荐", active: state.syncChoice === "cloud" },
      )}${card(
        "仅在此设备使用",
        "不同步；仍可本机记录记忆和语义检索，稍后可在「账户与空间」启用。",
        "offline",
        { active: state.syncChoice === "offline" },
      )}</div>`,
      backButton("language") +
        `<button type="button" class="primary" data-next-sync${state.syncChoice ? "" : " disabled"}>下一步</button>`,
    );
    all("[data-choice]", body).forEach((choice) =>
      choice.addEventListener("click", () => {
        if (busy) return;
        state.syncChoice = choice.dataset.choice;
        render();
      }),
    );
    $("[data-next-sync]", body).addEventListener("click", () => {
      if (busy || !state.syncChoice) return;
      chooseSync(state.syncChoice);
    });
  }

  async function chooseSync(value) {
    if (busy) return;
    busy = true;
    try {
      state.syncChoice = value;
      state.error = "";
      if (value === "cloud") {
        await saveStep("account", { onboardingSyncChoice: value });
        state.step = "account";
      } else {
        await saveStep("model", { onboardingSyncChoice: value });
        state.step = "model";
      }
      render();
    } finally {
      busy = false;
    }
  }

  function renderAccount(body) {
    if (state.accountPhase === "autosync") return renderAutosync(body);
    if (!connected()) return renderAccountOffline(body);
    const mode = state.accountMode;
    const server = ctx.status?.summary?.server_addr || DEFAULT_SERVER;
    body.innerHTML = frame(
      "注册或登录",
      "凭据通过 CLI 宿主 RPC 提交，不进入进程命令行或客户端设置。",
      `<div class="wizard-tabs" role="tablist"><button type="button" role="tab" data-mode="register" aria-selected="${mode === "register"}">创建账户</button><button type="button" role="tab" data-mode="login" aria-selected="${mode === "login"}">已有账户</button></div><form class="wizard-form stack" id="wizard-auth-form"><label>服务器<input name="addr" value="${h(server)}" required autocomplete="off"></label><label>账户名称<input name="user" required autocomplete="username"></label><label>登录密码<input name="password" type="password" required autocomplete="new-password"></label>${
        mode === "register"
          ? '<label>再次输入密码<input name="confirmation" type="password" required autocomplete="new-password"></label>'
          : '<label>超级密码（已有本机密钥时可留空）<input name="super" type="password" autocomplete="new-password"></label><label>两步验证代码（启用时填写 6 位代码）<input name="totp" type="password" autocomplete="one-time-code"></label>'
      }</form>`,
      `<button type="button" data-back="sync">上一步</button><button type="submit" class="primary" form="wizard-auth-form">${mode === "register" ? "创建账户" : "登录"}</button>`,
    );
    all("[data-mode]", body).forEach((tab) =>
      tab.addEventListener("click", () => {
        state.accountMode = tab.dataset.mode;
        state.error = "";
        render();
      }),
    );
    $("#wizard-auth-form", body).addEventListener("submit", (event) => {
      event.preventDefault();
      submitAuthentication(Object.fromEntries(new FormData(event.target)));
    });
  }

  // Runtime/CLI unavailable: offer the updates page or an explicit,
  // re-confirmed offline fallback. A failed auth must never be recorded as
  // a configured cloud account.
  function renderAccountOffline(body) {
    body.innerHTML = frame(
      "Runtime 尚未就绪",
      "注册和登录需要客户端管理的 Runtime 或兼容的 CLI（1.0.18 或更高）。当前没有可连接的 Runtime。",
      "",
      `<button type="button" data-open-updates>打开更新页面</button><button type="button" data-later-offline>稍后配置，先离线使用</button><button type="button" data-recheck>重新检查</button>${backButton("sync")}`,
    );
    $("[data-open-updates]", body).addEventListener("click", () => {
      pause();
      ctx.navigate("updates");
    });
    $("[data-recheck]", body).addEventListener("click", async () => {
      await ctx.refresh();
      render();
    });
    $("[data-later-offline]", body).addEventListener("click", () => {
      deferOffline();
    });
  }

  async function deferOffline() {
    if (
      !(await confirm(
        "稍后配置云端",
        "将跳过账户设置，先以本机模式使用；云端同步可在之后于「账户与空间」启用。确定先离线使用吗？",
        "先离线使用",
      ))
    )
      return;
    state.syncChoice = "offline";
    await goto("model", { onboardingSyncChoice: "offline" });
  }

  // One busy control: spinner + disabled label so an in-flight remote call
  // is visible; the button restores itself on failure without re-rendering
  // (a re-render would wipe the typed credentials).
  function setButtonBusy(button, busyText) {
    if (!button) return () => {};
    const original = button.innerHTML;
    button.disabled = true;
    button.classList.add("wizard-busy");
    button.innerHTML = `<span class="wizard-spinner" aria-hidden="true"></span>${h(busyText)}`;
    return () => {
      button.disabled = false;
      button.classList.remove("wizard-busy");
      button.innerHTML = original;
    };
  }

  async function submitAuthentication(values) {
    if (busy) return;
    busy = true;
    await setError("");
    const restore = setButtonBusy(
      $('button[form="wizard-auth-form"]', root),
      state.accountMode === "register" ? t("正在创建账户…") : t("正在登录…"),
    );
    try {
      validateAuthentication(state.accountMode, values);
      const envelope = await runAuthentication(state.accountMode, values);
      // Judge by the reported session, not the step marker; a concurrent
      // refresh may still be in flight, so poll briefly for the receipt.
      for (let i = 0; i < 10 && !session().has_token; i++) {
        await new Promise((resolve) => setTimeout(resolve, 300));
        await ctx.refresh();
      }
      if (!session().has_token)
        throw Error("登录状态未确认，请稍后重试或在「账户与空间」查看。");
      state.material = issuedRecoveryMaterial(envelope);
      state.accountPhase = "autosync";
      render();
    } catch (error) {
      setError(error.message);
      restore();
    } finally {
      busy = false;
    }
  }

  function renderAutosync(body) {
    const keys = session().has_local_keys;
    body.innerHTML = frame(
      "自动同步",
      state.accountMode === "register"
        ? "账户已创建。开启后记忆会在本机与云端之间自动同步。"
        : "登录成功。开启后记忆会在本机与云端之间自动同步。",
      `<label class="checkbox-label"><input type="checkbox" name="autosync" ${state.autoSync ? "checked" : ""}>启用自动同步（推荐）</label><p class="hint">${t("本机密钥")}：${keys ? t("已就绪") : t("未初始化")}。${t("云端的记忆正文始终加密后同步。")}</p>`,
      `<button type="button" class="primary" data-autosync-continue>继续</button>`,
    );
    $("[data-autosync-continue]", body).addEventListener("click", (event) => {
      saveAutosync($("[name=autosync]", body).checked, event.currentTarget);
    });
  }

  async function saveAutosync(enabled, button) {
    if (busy) return;
    busy = true;
    await setError("");
    const restore = setButtonBusy(button, t("正在保存…"));
    try {
      state.autoSync = enabled;
      await execute("autosync_set", { enabled });
      // Poll briefly: the reported autosync value races the in-flight
      // status refresh after the write.
      let reported;
      for (let i = 0; i < 10; i++) {
        await new Promise((resolve) => setTimeout(resolve, 300));
        await ctx.refresh();
        reported = ctx.status?.summary?.autosync;
        if (reported === undefined || Boolean(reported) === enabled) break;
      }
      if (reported !== undefined && Boolean(reported) !== enabled)
        throw Error("自动同步设置未确认，请稍后在「账户与空间」重试。");
      state.accountPhase = "auth";
      if (state.material) {
        await saveStep("recovery");
        state.step = "recovery";
      } else {
        await saveStep("model");
        state.step = "model";
      }
      render();
    } catch (error) {
      setError(error.message);
      restore();
    } finally {
      busy = false;
    }
  }

  function renderRecovery(body) {
    if (!state.material) {
      // A reload clears the in-memory material; never fake this step.
      body.innerHTML = frame(
        "保存恢复材料",
        "",
        '<p class="hint">没有待确认的新恢复材料。你可以在「账户与空间」导出密钥查看。</p>',
        `<button type="button" class="primary" data-recovery-next>下一步</button>`,
      );
      $("[data-recovery-next]", body).addEventListener("click", () =>
        goto("model"),
      );
      return;
    }
    body.innerHTML = frame(
      "保存 Super Password",
      "登录密码不能替代恢复材料。丢失后服务端无法帮助恢复本机密钥。",
      `<div class="wizard-recovery"><label>恢复材料<textarea readonly rows="7" data-recovery-text>${h(
        recoveryMaterialJson(state.material),
      )}</textarea></label><div class="actions"><button type="button" data-copy-material>复制恢复材料</button></div><label class="checkbox-label"><input type="checkbox" data-saved-material>我已将恢复材料保存到安全位置</label></div>`,
      `<button type="button" class="primary" data-recovery-next disabled>下一步</button>`,
    );
    $("[data-copy-material]", body).addEventListener("click", async () => {
      await navigator.clipboard.writeText(
        recoveryMaterialJson(state.material),
      );
      notify("已复制");
    });
    const next = $("[data-recovery-next]", body);
    $("[data-saved-material]", body).addEventListener("change", (event) => {
      next.disabled = !event.target.checked;
    });
    next.addEventListener("click", () => {
      // The material is acknowledged: drop it from memory immediately.
      state.material = null;
      goto("model");
    });
  }

  async function ensureInventory(model) {
    if (!state.inventories[model]) {
      state.inventories[model] = await fetchInventory(model);
    }
    return state.inventories[model];
  }

  function renderModel(body) {
    const benefit =
      "本地嵌入模型把记忆转为向量，用于语义召回、相似内容判断和整理；计算在本机完成，不需要把记忆正文发送给云端模型服务。下载需要网络并占用磁盘；跳过则部分检索和整理功能不可用，之后可在「模型与索引」补装。";
    body.innerHTML = frame(
      "选择模型",
      "",
      `<p class="hint">${h(benefit)}</p><div class="wizard-choices">${MODEL_CARDS.map(
        (model, index) =>
          card(model.title, model.detail, model.id, {
            badge: index === 0 ? "默认" : "",
            active: state.model === model.id,
          }),
      ).join("")}</div>`,
      backButton(state.syncChoice === "cloud" ? "account" : "sync") +
        `<button type="button" data-skip-model>暂不下载</button><button type="button" class="primary" data-next-model>下一步</button>`,
    );
    all("[data-choice]", body).forEach((choice) =>
      choice.addEventListener("click", () => {
        if (state.model !== choice.dataset.choice) {
          state.model = choice.dataset.choice;
          state.variantTouched = false;
          state.variant = "";
        }
        render();
      }),
    );
    $("[data-next-model]", body).addEventListener("click", () => {
      if (busy) return;
      goto("device");
    });
    $("[data-skip-model]", body).addEventListener("click", async () => {
      if (
        await confirm(
          "暂不下载模型",
          "不依赖向量的功能仍可使用；语义召回、相似判断和部分整理操作将不可用，之后可在「模型与索引」补装。确定暂不下载吗？",
          "暂不下载",
        )
      )
        complete();
    });
  }

  function renderDevice(body) {
    if (!connected()) {
      body.innerHTML = frame(
        "选择计算设备",
        "",
        '<p class="hint">等待 Runtime 连接后才能读取模型目录和下载量。</p>',
        backButton("model"),
      );
      return;
    }
    const platform = ctx.desktop?.platform || "";
    // OS-level entry gates from the shared kit; real capability is decided
    // by the runtime's inference probe at enable time.
    const gpuEntry = engineAvailable(platform, "gpu");
    const npuEntry = engineAvailable(platform, "npu");
    body.innerHTML = frame(
      "选择计算设备",
      "设备与权重的真实可用性以启用时的实际推理验证为准。",
      `<div class="wizard-choices">${DEVICES.map((device) =>
        card(device.title, device.detail, device.id, {
          disabled:
            (device.id === "gpu" && !gpuEntry) ||
            (device.id === "npu" && !npuEntry),
          badge: device.id === "cpu" ? "默认" : "",
          active: state.device === device.id,
        }),
      ).join("")}</div>${
        state.device === "npu" && npuEntry
          ? '<div class="actions"><button type="button" data-install-engines>安装 NPU 引擎</button></div>'
          : ""
      }<details class="wizard-advanced"><summary>高级：选择权重</summary><div class="stack" data-weights><p class="hint">正在读取模型目录…</p></div></details><p class="hint" data-download-estimate></p>`,
      backButton("model") +
        `<button type="button" class="primary" data-device-next>下一步</button>`,
    );
    all("[data-choice]", body).forEach((choice) =>
      choice.addEventListener("click", () => {
        state.device = choice.dataset.choice;
        state.variantTouched = false;
        render();
      }),
    );
    const install = $("[data-install-engines]", body);
    if (install)
      install.addEventListener("click", () =>
        execute("model_install_engines")
          .then(() => notify("NPU 引擎安装任务已提交"))
          .catch((error) => setError(error.message)),
      );
    loadWeights(body);
  }

  // Weights follow the device: CPU defaults to quantized, GPU/NPU to fp16;
  // the advanced section may override. Download estimate always reflects the
  // files actually missing on disk.
  async function loadWeights(body) {
    let inventory;
    try {
      inventory = await ensureInventory(state.model);
    } catch (error) {
      setError(error.message);
      return;
    }
    if (!root.isConnected || state.step !== "device") return;
    if (!state.variantTouched || !state.variant)
      state.variant = DEVICE_VARIANTS[state.device] || "quantized";
    const weights = $("[data-weights]", body);
    if (weights) {
      const options = inventory.variants
        .map(
          (variant) =>
            `<option value="${h(variant.name)}" ${variant.name === state.variant ? "selected" : ""}>${h(variant.name)} · ${h(bytes(variant.size))}${variant.installed ? " · 已下载" : ""}</option>`,
        )
        .join("");
      weights.innerHTML = `<label>权重<select name="variant">${options}</select></label><p class="hint">分词器随权重一起下载；已校验通过的文件不会重复下载。</p>`;
      const select = $("select[name=variant]", weights);
      select.addEventListener("change", () => {
        state.variant = select.value;
        state.variantTouched = true;
        updateEstimate(body, inventory);
      });
    }
    const next = $("[data-device-next]", body);
    if (next) {
      next.addEventListener("click", () => goto("confirm"));
    }
    updateEstimate(body, inventory);
  }

  function pendingDownloadBytes(inventory) {
    const variant = inventory.variants.find(
      (item) => item.name === state.variant,
    );
    return variant?.installed ? 0 : variant?.size || 0;
  }

  function updateEstimate(body, inventory) {
    const node = $("[data-download-estimate]", body);
    if (!node) return;
    const size = pendingDownloadBytes(inventory);
    const tokenizer = inventory.tokenizer_installed ? "" : t("，另含分词器");
    node.textContent = `${t("预计下载")}${size ? h(bytes(size)) : t("无需新文件")}${tokenizer} · ${t("保存位置")} ${h(inventory.model_dir)}`;
  }

  async function renderConfirm(body) {
    if (!connected()) {
      body.innerHTML = frame(
        "确认并开始",
        "",
        '<p class="hint">等待 Runtime 连接后才能提交下载与启用。</p>',
        backButton("device"),
      );
      return;
    }
    let inventory;
    try {
      inventory = await ensureInventory(state.model);
    } catch (error) {
      body.innerHTML = frame(
        "确认并开始",
        "",
        `<p class="form-error">${h(error.message)}</p>`,
        backButton("device"),
      );
      return;
    }
    const cloud =
      state.syncChoice === "cloud"
        ? `${t("云端同步")}（${h(session().user || "")}） · ${t("自动同步")}${state.autoSync ? t("已开启") : t("未开启")}`
        : t("仅本机使用");
    const size = pendingDownloadBytes(inventory);
    body.innerHTML = frame(
      "确认并开始",
      "",
      `<dl class="facts"><div><dt>${t("云端")}</dt><dd>${cloud}</dd></div><div><dt>${t("模型")}</dt><dd>${h(modelLabel(state.model))}</dd></div><div><dt>${t("计算设备")}</dt><dd>${h(state.device.toUpperCase())}</dd></div><div><dt>${t("权重")}</dt><dd>${h(state.variant)}</dd></div><div><dt>${t("预计下载")}</dt><dd>${size ? h(bytes(size)) : t("无需新文件")}</dd></div><div><dt>${t("保存位置")}</dt><dd>${h(inventory.model_dir)}</dd></div></dl><p class="hint">${t("确认前不会触发下载或引擎变更。启用时会运行真实推理验证；GPU/NPU 验证失败会自动回退 CPU 并如实展示。")}</p>`,
      backButton("device") +
        `<button type="button" data-later-download>稍后下载</button><button type="button" class="primary" data-start-execute>下载并启用</button>`,
    );
    $("[data-later-download]", body).addEventListener("click", () =>
      complete(),
    );
    $("[data-start-execute]", body).addEventListener("click", () => {
      executeStarted = false;
      goto("execute");
    });
  }

  function renderExecute(body) {
    const stages = state.stages || {};
    body.innerHTML = frame(
      "正在启用模型",
      "",
      `<ol class="wizard-stagelist">${EXECUTE_STAGES.map(
        ([key, label]) => {
          const cls = stages[key] || "pending";
          return `<li class="wizard-stage ${cls}"><span class="wizard-stage-dot"></span>${h(label)}<span class="wizard-stage-note" data-stage-note="${h(key)}"></span></li>`;
        },
      ).join("")}</ol><div class="wizard-execute-status" data-execute-status></div><div class="actions" data-execute-actions></div>`,
    );
    if (!executeStarted && !state.executing) {
      executeStarted = true;
      runExecute("download");
    }
  }

  function setStage(stage, status, note = "") {
    if (!state) return; // wizard closed by skip mid-execute
    state.stages = state.stages || {};
    state.stages[stage] = status;
    const node = root.querySelector(
      `[data-stage-note="${CSS.escape(stage)}"]`,
    );
    const li = node?.closest(".wizard-stage");
    if (li) {
      li.classList.remove("pending", "active", "done", "failed");
      li.classList.add(status);
    }
    if (node) node.textContent = note;
  }

  function executeStatus(text, error = false) {
    const node = $("[data-execute-status]", root);
    if (node) {
      node.textContent = text;
      node.classList.toggle("form-error", error);
    }
  }

  function executeActions(buttons) {
    const area = $("[data-execute-actions]", root);
    if (!area) return;
    area.innerHTML = buttons
      .map(
        ([label, action, primary]) =>
          `<button type="button" ${primary ? 'class="primary" ' : ""}data-exec="${h(action)}">${h(label)}</button>`,
      )
      .join("");
    all("[data-exec]", area).forEach((button) =>
      button.addEventListener("click", () => {
        const action = button.dataset.exec;
        if (action === "retry-download") runExecute("download");
        else if (action === "retry-enable") runExecute("enable");
        else if (action === "retry-index") runExecute("index");
        else if (action === "exit-models") exitToModels();
      }),
    );
  }

  function failure(stage, message) {
    setStage(stage, "failed");
    executeStatus(message, true);
    const retry =
      stage === "download"
        ? ["重试下载", "retry-download"]
        : stage === "index"
          ? ["重试索引", "retry-index"]
          : ["重试启用", "retry-enable"];
    executeActions([
      [retry[0], retry[1], true],
      ["保存选择，退出到模型页", "exit-models"],
    ]);
  }

  async function runExecute(startStage) {
    if (state.executing) return;
    state.executing = true;
    // The in-memory device/weight selection may be missing on a resumed
    // run; derive the device default so the download never uses a blank
    // variant name.
    if (!state.variant)
      state.variant = DEVICE_VARIANTS[state.device] || "quantized";
    executeActions([]);
    try {
      if (startStage === "download") {
        if (!(await runDownloadStage())) {
          if (state) state.executing = false;
          return;
        }
      }
      if (startStage === "download" || startStage === "enable") {
        if (!(await runEnableStage())) {
          if (state) state.executing = false;
          return;
        }
      }
      await runIndexStage();
      if (state) {
        state.executing = false;
        renderExecuteSuccess();
      }
    } catch (error) {
      if (state) state.executing = false;
      failure(startStage === "index" ? "index" : "probe", error.message);
    }
  }

  async function runDownloadStage() {
    setStage("download", "active");
    executeStatus("正在检查模型文件…");
    let inventory;
    try {
      inventory = await ensureInventory(state.model);
    } catch (error) {
      failure("download", error.message);
      return false;
    }
    const variant = inventory.variants.find(
      (item) => item.name === state.variant,
    );
    const needed = !inventory.tokenizer_installed || !variant?.installed;
    if (!needed) {
      // Existing verified weights: skip straight to enabling.
      setStage("download", "done", "文件已存在，跳过下载");
      setStage("verify", "done", "已校验");
      return true;
    }
    executeStatus("正在下载模型文件…");
    const { done } = openDownloadModal({
      model: state.model,
      variant: state.variant,
      label: modelLabel(state.model),
      getMirror: () => "auto",
    });
    const ok = await done;
    const stage = ctx.desktop?.task || {};
    if (!ok && stage.stage !== "完成") {
      // Download still running after the modal was closed: the wizard keeps
      // tracking it from the task snapshot without resubmitting.
      const tracked = await trackRunningDownload();
      if (!tracked) {
        failure("download", "下载未完成。");
        return false;
      }
    }
    try {
      inventory = await fetchInventory(state.model);
      state.inventories[state.model] = inventory;
    } catch (error) {
      failure("verify", error.message);
      return false;
    }
    const installed = inventory.variants.find(
      (item) => item.name === state.variant,
    );
    if (!inventory.tokenizer_installed || !installed?.installed) {
      failure("verify", "模型文件校验未通过。");
      return false;
    }
    setStage("download", "done");
    setStage("verify", "done", "校验通过");
    return true;
  }

  // Reopen-resume path: a download may still be running after the wizard or
  // the modal was closed; follow the client task state to its end.
  async function trackRunningDownload() {
    for (let rounds = 0; rounds < 7200; rounds++) {
      if (!state) return false; // wizard closed by skip: stop tracking
      await ctx.refresh();
      const task = ctx.desktop?.task || {};
      if (!task.busy) return task.stage === "完成";
      executeStatus(
        `${task.stage || "下载中"}${task.total ? ` · ${bytes(task.done)} / ${bytes(task.total)}` : ""}`,
      );
      await new Promise((resolve) => setTimeout(resolve, 1500));
    }
    return false;
  }

  async function runEnableStage() {
    setStage("load", "active");
    executeStatus("正在注册并加载模型…");
    let outcome;
    try {
      outcome = await applyEngineChoice({
        model: state.model,
        engine: state.device,
        variant: state.variant,
      });
    } catch (error) {
      failure("load", error.message);
      return false;
    }
    if (outcome.failed) {
      failure("probe", outcome.warning || "真实推理验证失败。");
      return false;
    }
    if (outcome.fallback) {
      // Report the engine the host restored after the failed probe: never
      // present the selected device as successful when verification fell back.
      setStage("load", "done");
      setStage("probe", "done", `${outcome.engine.toUpperCase()} · ${t("已恢复原设置")}`);
      executeStatus(outcome.warning, true);
    } else {
      setStage("load", "done");
      setStage("probe", "done", `${outcome.engine.toUpperCase()} · ${t("验证通过")}`);
    }
    state.enableOutcome = outcome;
    return true;
  }

  async function runIndexStage() {
    setStage("index", "active");
    executeStatus("正在安排索引…");
    try {
      if (isProfileModel(state.model))
        await execute("index_prepare");
      else
        await execute("model_prepare", { mirror: "auto" });
    } catch (error) {
      failure("index", error.message);
      return false;
    }
    setStage("index", "done");
    return true;
  }

  function renderExecuteSuccess() {
    if (!state) return; // wizard closed by skip mid-execute
    const outcome = state.enableOutcome || {};
    const body = $(".wizard-body", root);
    const actualEngine = outcome.engine || "cpu";
    const note = outcome.fallback
      ? `${h(state.device.toUpperCase())} ${t("验证未通过，已恢复 ")}${h(actualEngine.toUpperCase())} · ${h(outcome.variant)}。${h(outcome.warning || "")}`
      : `${t("模型已可用")}：${h(modelLabel(state.model))} · ${h(actualEngine.toUpperCase())} · ${h(outcome.variant || state.variant)}。`;
    body.innerHTML = frame(
      "开始使用 Respire",
      "",
      `<p>${note}</p><p class="hint">${t("模型已可用；后台索引可以继续构建，「全部索引完成」以「模型与索引」页的索引状态为准。")}</p>`,
      `<button type="button" data-skip-agents>直接开始</button><button type="button" class="primary" data-next-agents>下一步：配置 Agent 工具</button>`,
      { skip: false },
    );
    $("[data-next-agents]", body).addEventListener("click", () => goto("agents"));
    $("[data-skip-agents]", body).addEventListener("click", () => complete());
  }

  // ------------------------------------------------------- agent tools step

  function agentStatus(t) {
    if (t.mode === "plugin")
      return t.plugin?.source ? "插件源目录已就绪" : "插件未安装";
    return AGENT_STATE_NAMES[t.state] || t.state || "未知";
  }

  function defaultAgentSelection() {
    const selected = new Set();
    for (const target of state.agentTargets || [])
      if (agentScopeEligible(state.agentScope, target)) selected.add(target.id);
    return selected;
  }

  async function renderAgents(body) {
    const finishButton = `<button type="button" class="primary" data-finish>开始使用 Respire</button>`;
    const wireFinish = () =>
      $("[data-finish]", body).addEventListener("click", () => complete());
    if (!connected()) {
      body.innerHTML = frame(
        "配置 Agent 工具",
        "把 Respire 提示词写入已安装的 Agent 工具，或为其安装 Respire 插件；之后可在「配置Agent」页随时调整。",
        '<p class="hint">等待 Runtime 连接后才能读取已安装的 Agent 工具。</p>',
        finishButton,
      );
      wireFinish();
      return;
    }
    if (!state.agentTargets) {
      body.innerHTML = frame(
        "配置 Agent 工具",
        "把 Respire 提示词写入已安装的 Agent 工具，或为其安装 Respire 插件；之后可在「配置Agent」页随时调整。",
        '<p class="hint">正在读取已安装的 Agent 工具…</p>',
        finishButton,
      );
      wireFinish();
      try {
        const result = await api("inject-targets");
        state.agentTargets = result.targets || [];
      } catch (error) {
        state.agentTargets = [];
        state.agentResults = [
          { name: "读取注入目标", ok: false, message: error.message },
        ];
      }
      if (root.isConnected && state.step === "agents") render();
      return;
    }
    const eligible = (state.agentTargets || []).filter((target) =>
      agentScopeEligible(state.agentScope, target),
    );
    if (!state.agentSelected) state.agentSelected = defaultAgentSelection();
    const results = state.agentResults || [];
    body.innerHTML = frame(
      "配置 Agent 工具",
      "选择配置方式，再勾选要配置的已安装 Agent 工具；应用后逐个写入并回报结果。",
      `<div class="wizard-choices">${AGENT_SCOPES.map(
        (scope) =>
          `<button type="button" class="wizard-choice${
            state.agentScope === scope.id ? " active" : ""
          }" data-agent-scope="${h(scope.id)}"${
            state.agentBusy ? " disabled" : ""
          }><span class="wizard-choice-copy"><strong>${h(scope.title)}${
            scope.badge ? `<span class="wizard-badge">${h(scope.badge)}</span>` : ""
          }</strong><small>${h(scope.detail)}</small></span></button>`,
      ).join("")}</div><div class="stack" data-agent-list>${
        eligible.length
          ? eligible
              .map(
                (target) =>
                  `<label class="wizard-agent-row"><input type="checkbox" data-agent-target="${h(
                    target.id,
                  )}"${
                    state.agentSelected.has(target.id) ? " checked" : ""
                  }${state.agentBusy ? " disabled" : ""}><span class="wizard-agent-copy"><strong>${h(
                    target.name,
                  )}</strong><small>${h(agentStatus(target))}</small></span></label>`,
              )
              .join("")
          : '<p class="hint">没有支持该方式的已安装 Agent 工具；可更换配置方式或稍后在「配置Agent」页处理。</p>'
      }</div>${
        results.length
          ? `<div class="wizard-agent-results">${results
              .map(
                (item) =>
                  `<div class="row"><span class="dot ${
                    item.ok ? "online" : ""
                  }"></span><strong>${h(item.name)}</strong><small>${h(
                    item.message,
                  )}</small></div>`,
              )
              .join("")}</div>`
          : ""
      }`,
      (state.agentBusy
        ? ""
        : `<button type="button" data-agent-apply${
            state.agentSelected.size ? "" : " disabled"
          }>应用到所选</button>`) +
        finishButton,
    );
    all("[data-agent-scope]", body).forEach((button) =>
      button.addEventListener("click", () => {
        if (state.agentBusy || state.agentScope === button.dataset.agentScope)
          return;
        state.agentScope = button.dataset.agentScope;
        state.agentSelected = defaultAgentSelection();
        state.agentResults = null;
        render();
      }),
    );
    all("[data-agent-target]", body).forEach((input) =>
      input.addEventListener("change", () => {
        if (input.checked) state.agentSelected.add(input.dataset.agentTarget);
        else state.agentSelected.delete(input.dataset.agentTarget);
        const apply = $("[data-agent-apply]", root);
        if (apply) apply.disabled = !state.agentSelected.size;
      }),
    );
    const apply = $("[data-agent-apply]", body);
    if (apply)
      apply.addEventListener("click", () => applyAgents());
    wireFinish();
  }

  async function applyAgents() {
    if (state.agentBusy || !state.agentSelected.size) return;
    state.agentBusy = true;
    state.agentResults = [];
    render();
    for (const id of [...state.agentSelected]) {
      const target = (state.agentTargets || []).find((item) => item.id === id);
      const name = target?.name || id;
      try {
        const result = await api("inject-set", {
          id,
          enable: true,
          scope: state.agentScope,
        });
        state.agentResults.push({
          name,
          ok: true,
          message: agentResultMessage(result),
        });
      } catch (error) {
        state.agentResults.push({ name, ok: false, message: error.message });
      }
      const area = $("[data-agent-results]", root);
      if (area)
        area.innerHTML = state.agentResults
          .map(
            (item) =>
              `<div class="row"><span class="dot ${
                item.ok ? "online" : ""
              }"></span><strong>${h(item.name)}</strong><small>${h(
                item.message,
              )}</small></div>`,
          )
          .join("");
    }
    state.agentBusy = false;
    try {
      const result = await api("inject-targets");
      state.agentTargets = result.targets || [];
    } catch {}
    state.agentSelected = defaultAgentSelection();
    render();
  }

  async function exitToModels() {
    await complete();
    ctx.navigate("models");
  }

  // Skip ends the wizard on any first-run step: onboardingCompleted is set,
  // so neither the wizard nor the resume pill ever comes back on its own.
  // Applied settings are kept; an in-flight model task keeps running in the
  // background and stays visible on the models page.
  async function skipWizard() {
    const tail = state.executing
      ? "正在执行的模型任务会在后台继续，可在「模型与索引」页查看进度。"
      : "之后可以在「设置 → 关于」重新运行向导。";
    if (
      !(await confirm(
        "跳过初始化向导",
        `剩余步骤不再执行，已应用的设置保留。${tail}`,
        "跳过向导",
      ))
    )
      return;
    complete();
  }

  async function complete() {
    state.done = true;
    try {
      await ctx.savePreferences({
        onboardingCompleted: true,
        onboardingStep: "done",
      });
    } catch (error) {
      notify(error.message, true);
    }
    destroy();
  }

  // ------------------------------------------------------- pause & resume

  function pause() {
    root.hidden = true;
    if (!pill) {
      pill = document.createElement("button");
      pill.type = "button";
      pill.className = "wizard-resume-pill";
      pill.textContent = "继续初始化向导";
      pill.addEventListener("click", () => show());
      document.body.append(pill);
    }
    pill.hidden = false;
  }

  function show({ reopen = false } = {}) {
    if (reopen || !state) start(reopen);
    if (!state || state.done) return;
    if (pill) pill.hidden = true;
    // Re-read live state so a resume judges by reality, not by markers.
    resumeFromState();
    render();
  }

  // Runtime-state recovery: trust receipts over step markers.
  function resumeFromState() {
    if (!state) return;
    const step = state.step;
    if (step === "account" || step === "recovery") {
      const token = session().has_token;
      if (token && state.material) state.step = "recovery";
      else if (token || (step === "recovery" && !state.material))
        state.step = "model";
    }
  }

  function start(reopen = false) {
    const prefs = ctx.desktop?.preferences || {};
    const completed = Boolean(prefs.onboardingCompleted);
    state = {
      step: prefs.onboardingStep || "language",
      syncChoice: prefs.onboardingSyncChoice || "",
      language: "",
      accountMode: "register",
      accountPhase: "auth",
      autoSync: true,
      material: null,
      model: "m3",
      device: "cpu",
      variant: "",
      variantTouched: false,
      inventories: {},
      stages: {},
      agentScope: "all",
      agentTargets: null,
      agentSelected: null,
      agentResults: null,
      agentBusy: false,
      error: "",
      done: false,
      // Re-run (Settings entry or an already-completed install): starts from
      // the top and may be exited without writing anything.
      reRun: Boolean(reopen || completed),
    };
    if (!STEP_ORDER.includes(state.step)) state.step = "language";
    // Model/device/weight choices are deliberately not persisted; after a
    // reload the user re-selects them and already-verified files are skipped
    // by the download stage.
    if (state.step === "confirm" || state.step === "execute")
      state.step = "model";
    executeStarted = false;
    // A first run skips the language card when a language is already known;
    // a re-run always starts from the top so every step is reconfigurable.
    if (!state.reRun && state.step === "language" &&
      (prefs.language || localStorage.getItem("respire-language")))
      state.step = "sync";
  }

  function destroy() {
    root.hidden = true;
    root.innerHTML = "";
    if (pill) {
      pill.remove();
      pill = null;
    }
    state = null;
    executeStarted = false;
  }

  return {
    show,
    destroy,
    isActive: active,
    isDone: () => !state || state.done,
  };
}
