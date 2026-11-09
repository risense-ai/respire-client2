import {
  $,
  h,
  raw,
  bytes,
  number,
  summary,
  panel,
  pairs,
  input,
  select,
  execute,
  action,
  confirm,
  notify,
  resultView,
  api,
  date,
} from "../ui/core.js";
import { heading, phase } from "./overview.js";
import { recallChecks } from "./recall.js";
import {
  MIRROR_OPTIONS,
  MODEL_OPTIONS,
  MODEL_HINTS,
  MODEL_MODES,
  isProfileModel,
  variantFromItem,
  engineAvailable,
  modeForVariant,
  performanceAvailable,
  detectBestEngine,
  fetchInventory,
  fetchProfileState,
  readProfileState,
  profileStateSettled,
  applyLegacyEngine,
  submitProfileSwitch,
  applyEngineChoice,
  fallbackMessage,
  openDownloadModal,
} from "./model-kit.js";

// Scenario-mode states: the page speaks in "can I use it now" terms instead of
// exposing the variant/engine matrix. FALLBACK and ERROR are sticky overrides
// on top of the computed state so a failed verification stays visible until
// the user retries or picks another mode.
const PAGE_STATES = {
  EMPTY: "empty",
  DOWNLOADING: "downloading",
  VERIFYING: "verifying",
  READY: "ready",
  FALLBACK: "fallback",
  ERROR: "error",
};

const STATE_CONFIG = {
  [PAGE_STATES.EMPTY]: {
    icon: "○",
    title: "需要下载模型文件",
    description: "选择运行模式后，点击下载即可启用 AI 功能",
    buttonText: "下载并启用",
    buttonClass: "primary",
  },
  [PAGE_STATES.DOWNLOADING]: {
    icon: "↓",
    title: "正在下载模型",
    description: "下载完成后将自动验证并启用",
    buttonText: "下载中…",
    buttonClass: "disabled",
  },
  [PAGE_STATES.VERIFYING]: {
    icon: "…",
    title: "正在验证",
    description: "正在加载模型并运行真实推理验证",
    buttonText: "验证中…",
    buttonClass: "disabled",
  },
  [PAGE_STATES.READY]: {
    icon: "✓",
    title: "AI 已就绪",
    description: "模型验证通过，可正常使用",
    buttonText: "已启用",
    buttonClass: "success",
  },
  [PAGE_STATES.FALLBACK]: {
    icon: "⚠",
    title: "已回退到兼容模式",
    description: "验证失败，已回退到兼容模式",
    buttonText: "重试",
    buttonClass: "primary",
  },
  [PAGE_STATES.ERROR]: {
    icon: "✗",
    title: "模型异常",
    description: "请重新下载或查看高级设置",
    buttonText: "重试",
    buttonClass: "primary",
  },
};

// Five-axis recall fusion (docs/client2gui-recall-fusion.md): the GUI only
// edits the persistent `recall_fusion` agent setting and shows the host
// receipt; the algorithm runs in the Runtime. Names must match the CLI.
const FUSION_MODES = [
  ["legacy", "原有融合（默认）"],
  ["attention", "五轴注意力融合"],
  ["attention_anchor", "相关性锚定融合"],
  ["auto", "自动选择"],
];
const FUSION_NUMERIC = [
  ["fusion_alpha", "先验总份额", 0.2],
  ["fusion_beta", "词法份额", 0.25],
  ["fusion_temperature", "注意力温度", 1],
  ["fusion_gate_recency", "普通查询时间门", 0.02],
  ["fusion_gate_recency_temporal", "时间查询时间门", 1],
  ["fusion_gate_hot", "热度门", 0.1],
  ["fusion_gate_tree", "树位置门", 0.02],
  ["fusion_temporal_alpha", "时间查询追加份额", 0.15],
];
const fusionLabel = (key) =>
  FUSION_NUMERIC.find(([name]) => name === key)?.[1] || key;
const fusionModeName = (mode) =>
  FUSION_MODES.find(([id]) => id === mode)?.[1] || mode;
// Runtime model_progress phases while a profile switch runs (model_progress.rs).
const SWITCH_PHASES = {
  queued: "排队中",
  "switch-verify": "验证权重",
  "switch-download": "下载权重",
  "switch-load": "加载模型",
  "switch-index": "重建记忆索引",
  "switch-diary": "重建日记索引",
};

export function models(root, ctx) {
  let agent = {},
    backend = {},
    engineState = { engine: "cpu", model: "quantized" },
    inventory = null,
    loaded = false,
    loading = false,
    sawMissingBaseline = false,
    baselinePrepared = false,
    modelID = localStorage.getItem("respire-model") || "m3",
    profileState = null,
    switchInFlight = false,
    switchingVariant = "",
    switchTimer = null,
    selectedMode = localStorage.getItem("respire-model-mode") || "balanced",
    stateOverride = null,
    advancedExpanded = false,
    lastVerifiedAt = null,
    pageKey = "",
    switchTicks = 0,
    lastProbe = null,
    fusion = { runtime: null, draft: null, checked: false, loadFailed: false };
  root.innerHTML =
    heading("模型与索引", "选择运行模式，一键启用 AI 功能") +
    `<div data-live></div>` +
    panel(
      "推理引擎与模型",
      `<div class="panel-body stack"><div data-status-card></div><div class="mode-selector" data-mode-selector></div><div class="stack" data-primary-action></div><div data-advanced><div class="advanced-toggle" data-advanced-open><button type="button" class="link-btn" data-action="toggle-advanced">高级设置 ▼</button></div><div class="advanced-panel" hidden data-advanced-panel><div class="advanced-header"><h3>高级设置</h3><button type="button" class="link-btn" data-action="toggle-advanced">收起 ▲</button></div><div class="advanced-section"><h4>下载设置</h4><form data-mirror class="stack"><div class="form-grid">${select(
        "model",
        "模型",
        MODEL_OPTIONS,
        "m3",
      )}${select(
        "mirror",
        "下载镜像",
        MIRROR_OPTIONS,
        "auto",
      )}<div data-mirror-custom hidden>${input(
        "mirror-custom",
        "自定义镜像源",
        "https://",
        { placeholder: "http/https 源站，仅主机名" },
      )}</div></div><div class="actions"><button type="submit">保存下载来源</button><span class="hint" data-mirror-state></span></div></form></div><div class="advanced-section"><h4>权重文件管理</h4><p class="hint" data-model-hint></p><div class="actions" data-baseline></div><div class="scroll-table" data-variants><p class="hint">正在读取模型目录…</p></div></div><div class="advanced-section"><h4>推理引擎</h4><p class="hint" data-engine-hint>已下载的权重可在权重列表切换推理方式：应用立即重载 Runtime 模型并运行真实推理验证，失败自动回退 CPU。GPU 在 Linux 不可用，NPU 仅 Windows 支持。</p><div class="actions"><button type="button" data-model="probe">运行实际推理验证</button><button type="button" data-model="providers">安装 NPU 引擎</button></div></div><div class="advanced-section danger-zone"><h4>模型管理</h4><div class="actions"><button type="button" data-model="cancel">取消当前任务</button><button type="button" data-model="uninstall" class="danger">卸载全部模型文件</button></div></div></div></div></div>`,
    ) +
    panel(
      "检索与后台索引",
      `<form data-recall class="panel-body stack">${select(
        "mode",
        "召回方式",
        [
          ["fast", "快速 · 本机召回"],
          ["quality", "高质量 · 模型筛选候选标题"],
        ],
        "fast",
      )}<p class="hint">高质量模式会把查询和候选标题发送到配置的模型服务。服务失败时 Runtime 会报告状态并保留本机结果。</p><div class="actions"><button type="submit">保存召回方式</button><button type="button" data-model="index">安排后台索引</button><button type="button" data-model="rebuild">重新计算向量</button></div></form>`,
    ) +
    panel(
      "五轴注意力融合",
      `<div class="panel-body stack" data-fusion><p class="hint">对通过准入检查的向量候选融合语义、词法、时间、热度与树位置信号；算法在 Runtime 内执行，这里只保存设置并在下一次召回时生效。自动选择由 Runtime 按候选库大小决定，不承诺召回质量提升。</p><p class="hint" data-fusion-effective>正在读取配置…</p><form data-fusion-form class="stack" hidden>${select(
        "fusion",
        "融合模式",
        FUSION_MODES,
        "legacy",
      )}<details><summary>高级参数</summary><div class="form-grid">${FUSION_NUMERIC.map(
        ([key, label, value]) =>
          input(key, label, value, { type: "number", step: "any" }),
      ).join(
        "",
      )}</div><p class="hint">语义份额 = 1 − 先验总份额 − 词法份额；时间、热度与树位置在先验份额内按各自门值参与，时间查询会追加先验份额并受剩余份额限制。份额与门值取 0–1，注意力温度大于 0 且不大于 10。</p></details><div class="actions"><button type="submit">保存</button><button type="button" data-fusion-reset>恢复默认</button><span class="hint" data-fusion-state></span></div></form></div>`,
    ) +
    panel(
      "高质量召回服务",
      `<form data-api class="panel-body stack"><div class="form-grid">${input("base", "API 地址", "https://api.deepseek.com/v1", { required: true })}${input("model", "模型", "deepseek-flash", { required: true })}</div>${input("key", "API Key（留空保留已有密钥）", "", { type: "password" })}<p class="hint">密钥通过 CLI 写入系统凭据库，不保存在客户端设置中。</p><div class="actions"><button type="submit">保存服务配置</button><span data-key-state class="hint">正在读取配置…</span></div></form>`,
    ) +
    `<div data-result class="section-gap"></div>`;
  const live = $("[data-live]", root);
  function currentMirror() {
    const value = $("[data-mirror] [name=mirror]", root).value;
    return value === "custom"
      ? $("[name=mirror-custom]", root).value.trim()
      : value;
  }
  // Engine availability mirrors the old form: GPU is off on Linux, NPU only
  // on Windows; a running download or apply disables switching. Shared with
  // the wizard via model-kit; the OS check is an entry gate, not hardware
  // detection.
  function engineEnabled(engine, opActive) {
    if (opActive) return false;
    return engineAvailable(ctx.desktop?.platform, engine);
  }
  function engineChips(variant, opActive) {
    return ["cpu", "gpu", "npu"]
      .map((engine) => {
        const active =
          engineState.engine === engine && engineState.model === variant.name;
        return `<button class="engine-chip${active ? " active" : ""}" data-apply data-engine="${engine}" data-variant="${h(
          variant.name,
        )}"${engineEnabled(engine, opActive) ? "" : " disabled"} title="切换到 ${engine.toUpperCase()} · ${
          variant.name
        }：保存后立即重载并运行真实推理验证；失败会恢复切换前设置并说明原因">${engine.toUpperCase()}</button>`;
      })
      .join("");
  }
  // Live selection + availability line: what the runtime uses right now,
  // which entries this OS offers, and what a failed verification does.
  function renderEngineHint() {
    const node = $("[data-engine-hint]", root);
    if (!node) return;
    const platform = ctx.desktop?.platform || "";
    const entries = [
      `CPU 可用`,
      `GPU${engineAvailable(platform, "gpu") ? "可选" : "Linux 本版不可用"}`,
      `NPU${engineAvailable(platform, "npu") ? "可选" : "仅 Windows"}`,
    ];
    const label =
      MODEL_OPTIONS.find(([id]) => id === modelID)?.[1] || modelID;
    const probe = lastProbe
      ? `；最近验证：${lastProbe.selected}（${lastProbe.elapsed_ms}ms）`
      : "";
    const snapshotNote = ctx.desktop?.engine?.note
      ? ` ${ctx.desktop.engine.note}`
      : "";
    node.textContent = `当前使用 ${label} · ${engineState.model} 权重 · ${engineState.engine.toUpperCase()} 推理${probe}。本机入口：${entries.join(" / ")}。应用时会重载模型并运行真实推理验证，失败会恢复切换前设置并说明原因。${snapshotNote}`;
  }
  // One status renderer: switching state first, then client-side parallel
  // download, runtime download/verify, installed, or missing.
  function variantStatus(variant, op, downloading, task) {
    const busy = downloading === variant.name;
    const current = engineState.model === variant.name;
    if (switchInFlight && switchingVariant === variant.name) {
      if (!isProfileModel(modelID))
        return `<strong>切换中 · 正在重载并验证推理…</strong>`;
      const { operation } = readProfileState(profileState);
      const phase =
        SWITCH_PHASES[operation.phase] || operation.phase || "进行中";
      const pct = operation.total
        ? ` ${Math.round((operation.done / operation.total) * 100)}%`
        : "";
      return `<strong>切换中 · ${h(String(phase))}${pct}</strong>`;
    }
    const pct = op.total
      ? Math.min(100, Math.round((op.done / op.total) * 100))
      : 0;
    if (
      task?.busy &&
      String(task.stage || "").includes("model_" + variant.name + ".onnx")
    ) {
      const tPct = task.total
        ? Math.min(100, Math.round((task.done / task.total) * 100))
        : 0;
      return `<strong>并行下载中 ${tPct}%</strong><progress ${task.total ? `value="${h(task.done)}" max="${h(task.total)}"` : ""}></progress>`;
    }
    if (busy)
      return `<strong>${op.phase === "verify" ? "校验中" : "下载中"} ${pct}%</strong><progress ${op.total ? `value="${h(op.done)}" max="${h(op.total)}"` : ""}></progress>`;
    if (variant.installed)
      return `<span class="variant-status"><strong>${current ? "使用中 · 已下载" : "已下载"}</strong><span class="hint">${h(bytes(variant.bytes))} · 大小一致</span></span>`;
    return "未下载";
  }
  function renderVariants() {
    const node = $("[data-variants]", root);
    if (!node || !inventory) return;
    const op = ctx.status?.summary?.model_operation || {};
    const downloading = op.active ? variantFromItem(op.item) : "";
    const hint = $("[data-model-hint]", root);
    if (hint) hint.textContent = MODEL_HINTS[modelID] || MODEL_HINTS.m3;
    node.innerHTML = `<table><thead><tr><th>权重文件</th><th>大小</th><th>状态</th><th>本机位置</th><th>操作</th></tr></thead><tbody>${inventory.variants
      .map((variant) => {
        const badge =
          variant.name === "quantized"
            ? "CPU 默认"
            : variant.name === "fp16"
              ? "GPU/NPU 默认"
              : variant.name === "fp32"
                ? "完整精度"
                : "";
        const activeHere = engineState.model === variant.name;
        const ops = variant.installed
          ? `<div class="variant-ops">${engineChips(variant, Boolean(op.active) || switchInFlight)}</div><button data-model="download" data-variant="${h(variant.name)}"${downloading === variant.name ? " disabled" : ""}>${activeHere ? "重新下载" : "下载"}</button>`
          : `<button data-model="download" data-variant="${h(variant.name)}"${downloading === variant.name ? " disabled" : ""}>下载</button>`;
        return `<tr><td><span class="variant-name"><strong>${h(variant.name)}</strong>${badge ? `<span class="hint">${badge}${variant.name === "fp16" ? '<span class="warn-tip" title="资源占用较大，谨慎选择">*</span>' : ""}</span>` : ""}</span></td><td>${h(bytes(variant.size))}</td><td data-status="${h(variant.name)}">${variantStatus(variant, op, downloading, ctx.desktop?.task)}</td><td><span class="path-cell"><code title="${h(variant.path)}">${h(variant.path)}</code><button class="link-btn" data-open-dir="${h(variant.path)}">打开</button></span></td><td>${ops}</td></tr>`;
      })
      .join("")}</tbody></table>`;
    const cancelBtn = $("[data-model=cancel]", root);
    if (cancelBtn)
      cancelBtn.disabled =
        !op.active || !ctx.status?.summary?.model_operation?.id;
    const providersBtn = $("[data-model=providers]", root);
    if (providersBtn)
      providersBtn.disabled =
        !ctx.desktop?.platform?.startsWith("windows");
    renderBaseline();
  }
  function renderBaseline() {
    const area = $("[data-baseline]", root);
    if (!area || !inventory) return;
    const task = ctx.desktop?.task || {};
    const baseBusy =
      task.busy &&
      /下载 (tokenizer\.json|model_quantized\.onnx)/.test(task.stage || "");
    if (baseBusy) {
      const pct = task.total
        ? Math.min(100, Math.round((task.done / task.total) * 100))
        : 0;
      area.innerHTML = `<div class="stack" style="flex:1"><strong>并行下载基础文件 ${pct}%</strong><progress ${task.total ? `value="${h(task.done)}" max="${h(task.total)}"` : ""}></progress></div>`;
      return;
    }
    area.innerHTML = inventory.tokenizer_installed
      ? `<span class="hint">基础文件已就绪 · 模型目录 <code>${h(inventory.model_dir)}</code></span>`
      : `<button data-model="prepare" class="primary">下载并准备基础模型</button><span class="hint">首次使用需下载基础文件（quantized 权重 + 分词器，约 ${h(bytes(quantizedBytes()))}）并构建索引。</span>`;
  }
  function quantizedBytes() {
    const quantized = (inventory?.variants || []).find(
      (variant) => variant.name === "quantized",
    );
    return quantized?.size || 0;
  }
  // The client-side download task only counts as a model download when its
  // stage names model files; update or index tasks must not flip the page.
  function modelTaskBusy(task) {
    return (
      Boolean(task?.busy) &&
      /model_[a-z0-9]+\.onnx|tokenizer\.json/.test(String(task.stage || ""))
    );
  }
  function currentMode() {
    return (
      MODEL_MODES.find((mode) => mode.id === selectedMode) || MODEL_MODES[1]
    );
  }
  function modeVariant(mode) {
    return (inventory?.variants || []).find(
      (item) => item.name === mode.variant,
    );
  }
  // Default state from live data: downloading, missing weights, switching,
  // or ready for the selected mode.
  function computePageState() {
    const op = ctx.status?.summary?.model_operation || {};
    const task = ctx.desktop?.task || {};
    if (op.active || modelTaskBusy(task)) return PAGE_STATES.DOWNLOADING;
    const variant = modeVariant(currentMode());
    if (!variant?.installed || !inventory?.tokenizer_installed) return PAGE_STATES.EMPTY;
    if (switchInFlight) return PAGE_STATES.VERIFYING;
    if (engineState.model !== currentMode().variant) return PAGE_STATES.EMPTY;
    if (currentMode().requiresGPU && engineState.engine === 'cpu') return PAGE_STATES.EMPTY;
    if (!lastVerifiedAt && ctx.desktop?.health?.inference?.phase !== 'ready') return PAGE_STATES.EMPTY;
    return PAGE_STATES.READY;
  }
  function effectivePageState() {
    const computed = computePageState();
    // A sticky FALLBACK/ERROR survives idle recomputation (that is exactly
    // when it must stay visible); only a new download or switch — which
    // always computes as DOWNLOADING/VERIFYING — supersedes it.
    if (
      computed === PAGE_STATES.DOWNLOADING ||
      computed === PAGE_STATES.VERIFYING
    )
      stateOverride = null;
    return stateOverride || computed;
  }
  function renderStatusCard() {
    const node = $("[data-status-card]", root);
    if (!node) return;
    const state = effectivePageState();
    const config = STATE_CONFIG[state];
    const activeMode = MODEL_MODES.find(
      (mode) => mode.id === modeForVariant(engineState.model),
    );
    node.innerHTML = `<div class="status-card ${state}"><div class="status-icon">${config.icon}</div><div class="status-body"><strong>${h(config.title)}</strong><span class="hint">${h(config.description)}</span></div><div class="status-meta"><span class="mode-tag">${h(activeMode?.title || "—")} · ${h(engineState.engine.toUpperCase())}</span>${lastVerifiedAt ? `<span class="hint">${h("上次验证 " + date(lastVerifiedAt.toISOString()))}</span>` : ""}</div></div>`;
  }
  function renderModeSelector() {
    const node = $("[data-mode-selector]", root);
    if (!node) return;
    const accel = performanceAvailable(ctx.desktop?.platform);
    node.innerHTML = MODEL_MODES.map((mode) => {
      const variant = modeVariant(mode);
      const subtitle = variant?.size ? "≈ " + bytes(variant.size) : mode.subtitle;
      const noAccel = mode.requiresGPU && !accel;
      return `<button type="button" class="mode-card${selectedMode === mode.id ? " selected" : ""}${mode.recommended ? " recommended" : ""}" data-mode-select="${mode.id}">${mode.recommended ? '<span class="mode-badge">推荐</span>' : ""}<strong>${h(mode.title)}</strong><span class="hint">${h(subtitle)}</span><span class="mode-desc">${h(mode.description)}</span>${noAccel ? '<span class="hint">需 GPU/NPU</span>' : ""}</button>`;
    }).join("");
  }
  function renderPrimaryAction() {
    const node = $("[data-primary-action]", root);
    if (!node) return;
    const config = STATE_CONFIG[effectivePageState()];
    const idle =
      effectivePageState() === PAGE_STATES.DOWNLOADING ||
      effectivePageState() === PAGE_STATES.VERIFYING ||
      effectivePageState() === PAGE_STATES.READY;
    node.innerHTML = `<div data-mode-progress hidden></div><button type="button" class="${config.buttonClass}" data-mode-apply${idle ? " disabled" : ""}>${h(config.buttonText)}</button>`;
    updateModeProgress();
  }
  function updateModeProgress() {
    const node = $("[data-mode-progress]", root);
    if (!node) return;
    const op = ctx.status?.summary?.model_operation || {};
    const task = ctx.desktop?.task || {};
    if (modelTaskBusy(task)) {
      const pct = task.total
        ? Math.min(100, Math.round((task.done / task.total) * 100))
        : 0;
      node.hidden = false;
      node.innerHTML = `<div class="progress-label"><span>${h(String(task.stage || ""))}</span><span>${pct}%</span></div><progress ${task.total ? `value="${h(task.done)}" max="${h(task.total)}"` : ""}></progress>`;
    } else if (op.active) {
      const pct = op.total
        ? Math.min(100, Math.round((op.done / op.total) * 100))
        : 0;
      node.hidden = false;
      node.innerHTML = `<div class="progress-label"><span>${h(String(op.phase || ""))} · ${h(String(op.item || ""))}</span><span>${pct}%</span></div><progress ${op.total ? `value="${h(op.done)}" max="${h(op.total)}"` : ""}></progress>`;
    } else {
      node.hidden = true;
      node.innerHTML = "";
    }
  }
  function renderAdvancedVisibility() {
    const panelNode = $("[data-advanced-panel]", root);
    const openBar = $("[data-advanced-open]", root);
    if (panelNode) panelNode.hidden = !advancedExpanded;
    if (openBar) openBar.hidden = advancedExpanded;
  }
  // The selector and the primary button rebuild only when something they
  // display changed; polling every 3 seconds must not replace a button
  // mid-click.
  function renderDynamic(force = false) {
    const variant = modeVariant(currentMode());
    const key = [
      effectivePageState(),
      selectedMode,
      engineState.engine,
      engineState.model,
      variant ? `${variant.installed ? 1 : 0}:${variant.size}` : "none",
      lastVerifiedAt ? lastVerifiedAt.getTime() : 0,
      performanceAvailable(ctx.desktop?.platform) ? 1 : 0,
    ].join("|");
    if (!force && key === pageKey) {
      updateModeProgress();
      return;
    }
    pageKey = key;
    renderModeSelector();
    renderPrimaryAction();
    renderStatusCard();
  }
  function handleModeSelect(modeId) {
    if (!MODEL_MODES.some((mode) => mode.id === modeId)) return;
    selectedMode = modeId;
    localStorage.setItem("respire-model-mode", modeId);
    stateOverride = null;
    const mode = currentMode();
    if (
      mode.requiresGPU &&
      !performanceAvailable(ctx.desktop?.platform)
    )
      notify("当前设备不支持加速模式，将使用 CPU 运行", true);
    renderDynamic(true);
  }
  async function handlePrimaryAction() {
    const mode = currentMode();
    const engine =
      mode.engine === "auto"
        ? detectBestEngine(ctx.desktop?.platform)
        : mode.engine;
    if (!isProfileModel(modelID) && !inventory?.tokenizer_installed) {
      const completed = await openDownloadModal({
        model: modelID,
        variant: "tokenizer",
        label: mode.title,
        getMirror: currentMirror,
      }).done;
      if (!completed || !root.isConnected) return;
      inventory = await fetchInventory(modelID);
    }
    const variant = modeVariant(mode);
    if (!variant?.installed) {
      stateOverride = null;
      renderDynamic(true);
      // The modal resolves `done` true only when the download completed;
      // closing or cancelling never enables the mode on its own.
      const completed = await openDownloadModal({
        model: modelID,
        variant: mode.variant,
        label: mode.title,
        getMirror: currentMirror,
      }).done;
      const fresh = await fetchInventory(modelID).catch(() => null);
      if (fresh) inventory = fresh;
      if (!root.isConnected) return;
      if (completed || modeVariant(mode)?.installed)
        await applyEngineAfterDownload(mode, engine);
      else renderDynamic(true);
      return;
    }
    await applyEngineAfterDownload(mode, engine);
  }
  async function applyEngineAfterDownload(mode, engine) {
    try {
      switchInFlight = true;
      stateOverride = null;
      renderDynamic(true);
      const result = await applyEngineChoice({
        model: modelID,
        engine,
        variant: mode.variant,
      });
      if (result.fallback || result.failed) {
        // Reflect what the runtime actually fell back to; the mode tag and
        // the status card must not advertise the failed selection.
        if (isProfileModel(modelID)) {
          const active = result.details?.active;
          if (active) engineState = { engine: active.engine, model: active.variant };
        } else {
          engineState = { engine: result.engine, model: result.variant };
        }
        stateOverride = PAGE_STATES.FALLBACK;
        notify(result.warning || "验证失败，已回退到兼容模式", true);
      } else {
        engineState = { engine: result.engine, model: result.variant };
        lastVerifiedAt = new Date();
        notify(`已启用 ${mode.title} 模式`);
      }
      if (result.envelope && root.isConnected)
        $("[data-result]", root).innerHTML = resultView(
          "应用结果",
          result.envelope,
        );
    } catch (error) {
      stateOverride = PAGE_STATES.ERROR;
      notify(error.message, true);
    } finally {
      switchInFlight = false;
      renderDynamic(true);
      renderVariants();
      await ctx.refresh();
    }
  }
  // Fusion panel: the first line is what the Runtime will use next query
  // (defaults + saved settings + env overrides); the editor below shows the
  // saved draft. No receipt capability → editor stays hidden.
  function renderFusion() {
    const area = $("[data-fusion]", root);
    if (!area) return;
    const runtime = fusion.runtime;
    const editor = $("[data-fusion-form]", root);
    const state = $("[data-fusion-state]", root);
    if (!runtime?.effective) {
      editor.hidden = true;
      $("[data-fusion-effective]", root).textContent = fusion.loadFailed
        ? "融合配置读取失败，请刷新页面。"
        : fusion.checked
          ? "当前 CLI 不支持五轴融合设置，请升级 CLI 后再使用。"
          : "正在读取配置…";
      return;
    }
    editor.hidden = false;
    const overrides = Array.isArray(runtime.overrides) ? runtime.overrides : [];
    const configured = runtime.configured || {};
    let text = `当前生效模式：${fusionModeName(runtime.effective.fusion)} · 修改在下一次召回时生效。`;
    text += Object.keys(configured).length
      ? "编辑区显示已保存设置。"
      : "未保存自定义设置，当前按默认值运行。";
    if (overrides.length)
      text += ` 环境覆盖 ${overrides.length} 项：${overrides
        .map(
          (key) =>
            `${fusionLabel(key)} 生效 ${runtime.effective[key]}${
              configured[key] !== undefined
                ? `（已保存 ${configured[key]}）`
                : ""
            }`,
        )
        .join("；")}。保存不会清除 Runtime 环境变量。`;
    $("[data-fusion-effective]", root).textContent = text;
    state.textContent = "";
    const draft = fusion.draft || {};
    $("[name=fusion]", editor).value = draft.fusion || "legacy";
    for (const [key] of FUSION_NUMERIC) {
      const node = $(`[name=${key}]`, editor);
      if (node) node.value = draft[key] === undefined ? "" : String(draft[key]);
    }
  }
  // Collect + validate the draft. Only instant feedback: the host and the
  // Runtime re-validate the whole object on save.
  function fusionDraft() {
    const form = $("[data-fusion-form]", root);
    const draft = { fusion: $("[name=fusion]", form).value };
    for (const [key, label] of FUSION_NUMERIC) {
      const text = $(`[name=${key}]`, form).value.trim();
      if (!text) throw new Error(`${label}不能为空`);
      const value = Number(text);
      if (!Number.isFinite(value)) throw new Error(`${label}必须是有限数字`);
      if (key === "fusion_temperature") {
        if (value <= 0 || value > 10)
          throw new Error(`${label}需大于 0 且不大于 10`);
      } else if (value < 0 || value > 1) {
        throw new Error(`${label}取值范围为 0–1`);
      }
      draft[key] = value;
    }
    if (draft.fusion_alpha + draft.fusion_beta > 1)
      throw new Error("先验总份额与词法份额之和不能大于 1");
    return draft;
  }
  function adoptFusion(runtime) {
    fusion.checked = true;
    fusion.runtime = runtime || null;
    if (fusion.runtime?.effective)
      fusion.draft = { ...fusion.runtime.defaults, ...fusion.runtime.configured };
    renderFusion();
  }
  // Polling refreshes status cells in place; rebuilding the whole table would
  // replace buttons mid-click every 3 seconds.
  function updateVariantsProgress() {
    if (!inventory) return;
    const op = ctx.status?.summary?.model_operation || {};
    const task = ctx.desktop?.task || {};
    const downloading = op.active ? variantFromItem(op.item) : "";
    for (const variant of inventory.variants) {
      const cell = $(`[data-status="${CSS.escape(variant.name)}"]`, root);
      if (!cell) continue;
      const html = variantStatus(variant, op, downloading, task);
      if (cell.innerHTML !== html) cell.innerHTML = html;
      const button = $(
        `[data-variant="${CSS.escape(variant.name)}"]`,
        root,
      );
      const clientBusy =
        task.busy &&
        String(task.stage || "").includes("model_" + variant.name + ".onnx");
      if (button)
        button.disabled =
          downloading === variant.name || Boolean(op.active) || clientBusy;
      for (const chip of document.querySelectorAll(
        `[data-apply][data-variant="${CSS.escape(variant.name)}"]`,
      )) {
        const active =
          engineState.engine === chip.dataset.engine &&
          engineState.model === variant.name;
        chip.classList.toggle("active", active);
        chip.disabled = !engineEnabled(chip.dataset.engine, Boolean(op.active) || switchInFlight);
      }
    }
    renderBaseline();
  }
  function update() {
    const s = ctx.status?.summary || {},
      health = ctx.desktop?.health || {},
      inference = health.inference || {},
      index = s.retrieval_index || {},
      op = s.model_operation || {};
    live.innerHTML = `<div class="grid">${panel(
      "运行状态",
      pairs([
        ["推理状态", phase(inference.phase)],
        [
          "推理工作进程",
          inference.worker_active === undefined
            ? "—"
            : inference.worker_active
              ? "已就绪"
              : "未启动",
        ],
        [
          "执行中 / 等待",
          `${number(inference.foreground?.active ?? inference.active)} / ${number(inference.queued)}`,
        ],
        ["索引状态", phase(index.state)],
        [
          "索引任务",
          index.running ? "正在构建" : index.scheduled ? "已安排" : "空闲",
        ],
      ]),
    )}${panel("模型任务", `<div class="panel-body"><p>${h(op.active ? `${phase(op.phase)} · ${op.item || ""}` : "当前没有模型任务")}</p>${op.active ? `<div class="progress-label"><span>${h(bytes(op.done))} / ${h(bytes(op.total))}</span><span>${h(op.elapsed_secs !== undefined ? op.elapsed_secs + " 秒" : "")}</span></div><progress ${op.total ? `value="${h(op.done)}" max="${h(op.total)}"` : ""}></progress>` : ""}${index.error ? `<p class="form-error section-gap">${h(index.error)}</p>` : ""}</div><details class="technical"><summary>推理与索引详情</summary><pre>${h(raw({ inference, index, model: op }))}</pre></details>`)}</div>`;
    updateVariantsProgress();
    renderDynamic();
    renderEngineHint();
    // The client downloaded the baseline files: have the Runtime build the
    // index once, right after the tokenizer shows up.
    if (inventory) {
      if (!inventory.tokenizer_installed) sawMissingBaseline = true;
      else if (sawMissingBaseline && !baselinePrepared) {
        baselinePrepared = true;
        const mirror = currentMirror();
        execute("model_prepare", {
          mirror: mirror && mirror !== "auto" ? mirror : "auto",
        })
          .then(() => notify("基础模型已就绪，索引已安排"))
          .catch((error) => notify(error.message, true));
      }
    }
  }
  async function load() {
    if (loading || !ctx.desktop?.connected) return;
    loading = true;
    loaded = true;
    try {
      const savedModel = localStorage.getItem("respire-model");
      if (savedModel && MODEL_OPTIONS.some(([id]) => id === savedModel))
        modelID = savedModel;
      const values = await Promise.allSettled([
        execute("agent_config"),
        execute("model_engine"),
        execute("classification_config", { backend: "ds" }),
        fetchInventory(modelID),
        fetchProfileState(),
      ]);
      if (!root.isConnected) return;
      if (values[0].status === "fulfilled") {
        agent = summary(values[0].value);
        fusion.loadFailed = false;
        adoptFusion(agent.recall_fusion_runtime);
        $("[data-recall] [name=mode]", root).value =
          agent.recall_mode || "fast";
        const mirror = agent.model_mirror || "auto";
        const mirrorSel = $("[data-mirror] [name=mirror]", root);
        if ([...mirrorSel.options].some((option) => option.value === mirror))
          mirrorSel.value = mirror;
        else {
          mirrorSel.value = "custom";
          $("[data-mirror-custom]", root).hidden = false;
          $("[name=mirror-custom]", root).value = mirror;
        }
        $("[data-mirror-state]", root).textContent = "当前来源：" + mirror;
      }
      if (values[1].status === "fulfilled") {
        const state = summary(values[1].value);
        engineState = {
          engine: state.engine || "cpu",
          model:
            state.model ||
            (state.engine === "gpu" || state.engine === "npu"
              ? "fp16"
              : "quantized"),
        };
      }
      if (values[2].status === "fulfilled") backend = summary(values[2].value);
      if (values[3].status === "fulfilled") {
        inventory = values[3].value;
        if (inventory.model && MODEL_OPTIONS.some(([id]) => id === inventory.model)) {
          modelID = inventory.model;
        }
      }
      if (values[4].status === "fulfilled") {
        profileState = values[4].value;
        // The runtime's catalog carries what is actually active; the selector
        // keeps an explicit local choice when one exists.
        const { active, switchState } = readProfileState(profileState);
        const activeModel = active?.model || "";
        if (activeModel && !switchInFlight) {
          const known = MODEL_OPTIONS.some(([id]) => id === activeModel);
          if (!savedModel && known) modelID = activeModel;
          if (modelID === activeModel) {
            if (modelID !== (inventory?.model || "")) {
              inventory = await fetchInventory(modelID);
            }
            engineState = {
              engine: active.engine || "cpu",
              model: active.variant || "quantized",
            };
          }
        }
        // A switch left pending by a previous page session keeps being
        // watched so its outcome still reaches the notice.
        if (
          isProfileModel(modelID) &&
          !switchInFlight &&
          switchState.state === "pending"
        ) {
          switchInFlight = true;
          switchingVariant = "";
          switchTicks = 0;
          if (!switchTimer)
            switchTimer = setInterval(() => refreshProfileState(), 3000);
          notify("检测到未完成的模型切换任务，继续等待 Runtime 结果…");
        }
      } else {
        profileState = null;
      }
      // Existing installations map back to a mode card; a stored preference
      // always wins over the mapping.
      const savedMode = localStorage.getItem("respire-model-mode");
      selectedMode =
        savedMode && MODEL_MODES.some((mode) => mode.id === savedMode)
          ? savedMode
          : modeForVariant(engineState.model);
      if (values[0].status === "rejected") {
        fusion.loadFailed = true;
        renderFusion();
      }
      $("[data-mirror] [name=model]", root).value = modelID;
      $("[data-api] [name=base]", root).value =
        agent.recall_api_base ||
        backend.api_base ||
        "https://api.deepseek.com/v1";
      $("[data-api] [name=model]", root).value =
        agent.recall_model || backend.model || "deepseek-flash";
      $("[data-key-state]", root).textContent =
        values[2].status === "rejected"
          ? "读取配置失败，请刷新重试"
          : backend.has_key
            ? "系统凭据库已配置密钥"
            : "未检测到所选服务的密钥";
      for (const [index, result] of values.entries()) {
        if (result.status === "rejected" && index !== 1 && index !== 3 && index !== 4)
          notify(result.reason.message, true);
      }
      renderVariants();
      renderDynamic(true);
      update();
    } catch (error) {
      notify(error.message, true);
    } finally {
      loading = false;
    }
  }
  // Model switcher: persist the choice and reload the weight table for it.
  $("[data-mirror] [name=model]", root).addEventListener("change", (event) => {
    const next = event.target.value;
    if (!MODEL_OPTIONS.some(([id]) => id === next) || next === modelID) return;
    modelID = next;
    localStorage.setItem("respire-model", modelID);
    inventory = null;
    profileState = null;
    const node = $("[data-variants]", root);
    if (node) node.innerHTML = '<p class="hint">正在读取模型目录…</p>';
    (async () => {
      try {
        inventory = await fetchInventory(modelID);
        await refreshProfileState();
      } catch (error) {
        notify(error.message, true);
        renderVariants();
      }
    })();
  });
  async function refreshProfileState() {
    if (!isProfileModel(modelID)) {
      profileState = null;
      renderVariants();
      return;
    }
    profileState = await fetchProfileState();
    const { active } = readProfileState(profileState);
    if (active?.model === modelID) {
      engineState = {
        engine: active.engine || "cpu",
        model: active.variant || switchingVariant || "quantized",
      };
    }
    if (root.isConnected) {
      renderVariants();
      renderDynamic();
    }
    if (switchInFlight) {
      const verdict = profileStateSettled(profileState);
      switchTicks += 1;
      // 3s × 600 = 30 min: never poll forever; the runtime itself abandons a
      // switch after 30 minutes.
      const expired = switchTicks > 600;
      if (verdict.settled || expired) {
        const { switchState } = readProfileState(profileState);
        switchInFlight = false;
        switchingVariant = "";
        clearInterval(switchTimer);
        switchTimer = null;
        if (!verdict.failed && !expired) lastVerifiedAt = new Date();
        switchTicks = 0;
        notify(
          expired
            ? "模型切换状态等待超时，请到本页查看最终结果。"
            : verdict.failed
              ? `模型切换失败${switchState.error ? `：${switchState.error}` : ""}；Runtime 已恢复原模型。`
              : `已切换使用 ${modelID} · ${engineState.model}，真实推理验证通过。`,
          expired || verdict.failed,
        );
        if (root.isConnected) {
          renderVariants();
          renderDynamic(true);
          await ctx.refresh();
        }
      }
    }
  }
  root.addEventListener("click", (event) => {
    const toggle = event.target.closest("[data-action=toggle-advanced]");
    if (toggle) {
      advancedExpanded = !advancedExpanded;
      renderAdvancedVisibility();
      return;
    }
    const modeCard = event.target.closest("[data-mode-select]");
    if (modeCard) {
      handleModeSelect(modeCard.dataset.modeSelect);
      return;
    }
    const modeApply = event.target.closest("[data-mode-apply]");
    if (modeApply) {
      action(modeApply, () => handlePrimaryAction());
      return;
    }
    const apply = event.target.closest("[data-apply]");
    if (apply) {
      action(apply, async () => {
        const engine = apply.dataset.engine;
        const variant = apply.dataset.variant;
        if (isProfileModel(modelID)) {
          switchingVariant = variant;
          switchInFlight = true;
          switchTicks = 0;
          if (!switchTimer)
            switchTimer = setInterval(() => refreshProfileState(), 3000);
          try {
            await submitProfileSwitch(modelID, engine, variant);
            notify(`切换任务已提交：${modelID} · ${engine.toUpperCase()} · ${variant}；Runtime 正在验证并加载，完成后在此提示。`);
          } catch (error) {
            switchInFlight = false;
            switchingVariant = "";
            clearInterval(switchTimer);
            switchTimer = null;
            throw error;
          }
          await refreshProfileState();
          return;
        }
        // Legacy m3: one host operation saves the selection, reloads the
        // model and probes it; on failure the host restores the previous
        // selection and the receipt carries the cause.
        switchingVariant = variant;
        switchInFlight = true;
        renderVariants();
        notify(`正在切换到 ${engine.toUpperCase()} · ${variant}：重载模型并运行真实推理验证，期间推理可能短暂停顿…`);
        try {
          const { envelope, result } = await applyLegacyEngine(engine, variant);
          engineState = { engine: result.engine, model: result.model };
          if (!result.fallback) lastVerifiedAt = new Date();
          const probe = result.probe || {};
          if (typeof probe.elapsed_ms === "number" && probe.selected)
            lastProbe = { selected: probe.selected, elapsed_ms: probe.elapsed_ms };
          if (result.fallback)
            notify(fallbackMessage(engine, result), true);
          else
            notify(
              `已切换到 ${engine.toUpperCase()} · ${result.model}，实际推理验证通过${probe.selected ? `（${probe.selected} · ${probe.elapsed_ms}ms）` : ""}。`,
            );
          if (root.isConnected)
            $("[data-result]", root).innerHTML = resultView("应用结果", envelope);
        } finally {
          switchInFlight = false;
          switchingVariant = "";
          if (root.isConnected) {
            renderVariants();
            renderDynamic(true);
          }
          await ctx.refresh();
        }
      });
      return;
    }
    const reveal = event.target.closest("[data-open-dir]");
    if (reveal) {
      action(reveal, async () => {
        notify(await api("open-model-dir", { path: reveal.dataset.openDir }));
      });
      return;
    }
    const button = event.target.closest("[data-model]");
    if (!button) return;
    action(button, async () => {
      const operation = button.dataset.model;
      let result;
      const mirror = currentMirror();
      if (operation === "prepare") {
        if (
          !(await confirm(
            "下载基础模型",
            "将多线程并行下载 quantized 权重与分词器（约 543 MiB）到本机模型目录；完成后自动构建索引。已校验的文件会跳过。",
            "开始下载",
          ))
        )
          return;
        sawMissingBaseline = true;
        openDownloadModal({
          model: modelID,
          variant: "baseline",
          label: "基础文件（quantized + 分词器）",
          getMirror: currentMirror,
          onDone: () => load(),
        });
        await ctx.refresh();
      } else if (operation === "download") {
        const variant = button.dataset.variant;
        const size = inventory?.variants?.find(
          (item) => item.name === variant,
        )?.size;
        const label =
          MODEL_OPTIONS.find(([id]) => id === modelID)?.[1] || modelID;
        if (
          !(await confirm(
            "下载权重文件",
            `将多线程并行下载 ${label} 的 ${variant} 权重（约 ${bytes(size)}）到 ${inventory?.model_dir || "本机模型目录"}；已验证的文件会跳过。`,
            "开始下载",
          ))
        )
          return;
        openDownloadModal({
          model: modelID,
          variant,
          label,
          getMirror: currentMirror,
          onDone: () => load(),
        });
        await ctx.refresh();
      } else if (operation === "cancel") {
        const id = ctx.status?.summary?.model_operation?.id;
        if (!id) throw new Error("没有正在运行的模型任务");
        result = await execute("model_cancel", { id });
      } else if (operation === "uninstall") {
        if (
          !(await confirm(
            "卸载全部模型文件",
            "删除本机模型目录中的 BGE-M3 文件后，语义召回需要重新下载模型。",
            "卸载",
          ))
        )
          return;
        result = await execute("model_uninstall");
      } else if (operation === "probe") {
        // Real inference on the active settings: the receipt names the
        // execution provider the runtime actually selected.
        result = await execute("model_probe");
        const probe = summary(result) || {};
        if (probe.selected && typeof probe.elapsed_ms === "number")
          lastProbe = { selected: probe.selected, elapsed_ms: probe.elapsed_ms };
        notify(
          `实际推理验证通过：${probe.selected || "当前引擎"} · ${probe.elapsed_ms ?? "—"}ms · cosine ${typeof probe.cosine === "number" ? probe.cosine.toFixed(4) : "—"}`,
        );
      } else if (operation === "providers")
        result = await execute("model_install_engines");
      else if (operation === "index") result = await execute("index_prepare");
      else if (operation === "rebuild") {
        if (
          !(await confirm(
            "重新计算向量",
            "Runtime 会为记忆重新生成检索向量，过程可能较长。",
            "开始重建",
          ))
        )
          return;
        result = await execute("index_rebuild");
      }
      if (result && root.isConnected)
        $("[data-result]", root).innerHTML = resultView("操作结果", result);
      if (operation !== "probe")
        notify("请求已处理；模型与索引状态会继续刷新。");
      await ctx.refresh();
    });
  });
  $("[data-mirror]", root).addEventListener("submit", (event) => {
    event.preventDefault();
    action($("[type=submit]", event.target), async () => {
      const mirror = currentMirror();
      await execute("agent_set", {
        setting: "model_mirror=" + (mirror || "auto"),
      });
      $("[data-mirror-state]", root).textContent =
        "当前来源：" + (mirror || "auto");
      notify("下载来源已保存，之后的下载任务使用该镜像。");
    });
  });
  $("[data-mirror] [name=mirror]", root).addEventListener("change", (event) => {
    $("[data-mirror-custom]", root).hidden = event.target.value !== "custom";
  });
  $("[data-recall]", root).addEventListener("submit", (event) => {
    event.preventDefault();
    action($("[type=submit]", event.target), async () => {
      await execute("agent_set", {
        setting: "recall_mode=" + new FormData(event.target).get("mode"),
      });
      notify("召回方式已保存");
      await ctx.refresh();
    });
  });
  $("[data-api]", root).addEventListener("submit", (event) => {
    event.preventDefault();
    action($("[type=submit]", event.target), async () => {
      const value = Object.fromEntries(new FormData(event.target));
      if (value.key) {
        const previous = summary(
          await execute("classification_config"),
        ).backend;
        await execute(
          "classification_save",
          { backend: "ds", base: value.base, model: value.model },
          { secret: value.key },
        );
        $("[name=key]", event.target).value = "";
        if (previous && previous !== "ds")
          await execute("classification_save", { backend: previous });
      }
      await execute("agent_set", { setting: "recall_api_base=" + value.base });
      await execute("agent_set", { setting: "recall_model=" + value.model });
      notify("召回服务配置已保存");
      await load();
    });
  });
  // Save the whole recall_fusion object in one agent_set, then read it back:
  // a mismatch means the stored config changed under us, a failed readback
  // means the write outcome is unconfirmed — never auto-retry the write.
  $("[data-fusion-form]", root).addEventListener("submit", (event) => {
    event.preventDefault();
    action($("[type=submit]", event.target), async () => {
      if (!fusion.runtime?.effective)
        throw new Error("请先升级支持五轴融合设置的 CLI");
      const draft = fusionDraft();
      await execute("agent_set", {
        setting: "recall_fusion=" + JSON.stringify(draft),
      });
      let readBack;
      try {
        readBack = summary(await execute("agent_config")).recall_fusion_runtime;
      } catch {
        notify("写入已提交，但回读失败：结果待确认，请刷新设置。", true);
        return;
      }
      if (readBack?.effective) adoptFusion(readBack);
      if (
        !readBack?.configured ||
        Object.keys(draft).some((key) => readBack.configured[key] !== draft[key])
      )
        throw new Error("保存后的回读与草稿不一致，配置可能已变化，请刷新设置");
      const overrides = readBack.overrides || [];
      notify(
        overrides.length
          ? `已保存；但这些字段由 Runtime 环境覆盖：${overrides
              .map(fusionLabel)
              .join("、")}，保存值不会生效。`
          : "五轴融合设置已保存，下一次召回生效。",
        overrides.length > 0,
      );
    });
  });
  // Restoring defaults only rewrites the draft; nothing persists until 保存.
  $("[data-fusion-reset]", root).addEventListener("click", (event) => {
    action(event.target, async () => {
      if (!fusion.runtime?.defaults)
        throw new Error("请先升级支持五轴融合设置的 CLI");
      fusion.draft = { ...fusion.runtime.defaults };
      renderFusion();
      $("[data-fusion-state]", root).textContent = "已恢复默认草稿；保存后生效。";
    });
  });
  update();
  const dispose = recallChecks(root);
  if (ctx.desktop?.connected) load();
  return {
    dispose() {
      // An in-flight switch keeps polling (bounded by switchTicks): the page
      // is gone but the outcome must still reach the global notice instead of
      // failing silently in the background. switchInFlight stays set so the
      // settle branch in refreshProfileState can fire.
      dispose();
    },
    update() {
      update();
      if (!loaded && ctx.desktop?.connected) load();
    },
    refresh: load,
  };
}
