import {
  $,
  h,
  bytes,
  execute,
  action,
  notify,
  api,
} from "../ui/core.js";

// Shared model business rules for the models page and the onboarding wizard:
// catalog metadata, device availability, the download dialog, and engine
// application. Both surfaces must use these; neither copies its own version.

export const MIRROR_OPTIONS = [
  ["auto", "auto · 自动选择镜像"],
  ["https://hf-mirror.com", "hf-mirror.com"],
  ["https://hf-mirror.net", "hf-mirror.net"],
  ["https://huggingface.co", "huggingface.co"],
  ["custom", "自定义镜像…"],
];

export const MODEL_OPTIONS = [
  ["m3", "BGE-M3 · 1024 维 · 多语（默认）"],
  ["bge-base-zh-v1.5", "BGE-base-zh-v1.5 · 768 维 · 中文"],
  ["bge-m3-distill-8l", "BGE-M3-Distill-8L · 1024 维 · 轻量多语"],
  ["granite-311m-r2", "Granite 311M R2 · 768 维 · 量化"],
  ["granite-97m-r2", "Granite 97M R2 · 384 维"],
];

export const MODEL_HINTS = {
  m3: "BGE-M3 权重按需下载（多线程并行，0.5–1.2 GiB/个），下载完成前无法选择对应推理方式；应用引擎会立即重载 Runtime 模型并运行真实推理验证，失败自动回退 CPU。",
  "bge-base-zh-v1.5":
    "BGE-base-zh-v1.5 权重按需下载（多线程并行，约 80–200 MiB/个）。下载 quantized 权重与分词器后，可在下方选择推理方式切换使用：Runtime 会注册模型档案、加载并运行真实推理验证，失败自动回退。",
  "bge-m3-distill-8l":
    "BGE-M3-Distill-8L 权重按需下载（多线程并行，约 0.3–1.1 GiB/个）。下载 quantized 权重与分词器后，可在下方选择推理方式切换使用：Runtime 会注册模型档案、加载并运行真实推理验证，失败自动回退。",
  "granite-311m-r2": "下载量化权重和分词器，使用 CPU 运行。",
  "granite-97m-r2": "下载原始权重和分词器，使用 CPU 运行。",
};

// Wizard card copy: positioning only; download sizes come from the live
// inventory so numbers never go stale.
export const MODEL_CARDS = [
  {
    id: "m3",
    title: "BGE-M3",
    detail: "多语 · 1024 维 · 质量与兼容性优先（默认）",
  },
  {
    id: "bge-base-zh-v1.5",
    title: "BGE-base-zh-v1.5",
    detail: "中文为主 · 768 维 · 较小下载与资源占用",
  },
  {
    id: "bge-m3-distill-8l",
    title: "BGE-M3-Distill-8L",
    detail: "多语 · 1024 维 · 较轻量，适合资源受限设备",
  },
  { id: "granite-311m-r2", title: "Granite 311M R2", detail: "多语 · 768 维 · 量化" },
  { id: "granite-97m-r2", title: "Granite 97M R2", detail: "多语 · 384 维 · 原始权重" },
];

export const graniteVariant = (model) =>
  ({ "granite-311m-r2": "quantized", "granite-97m-r2": "standard" })[model];

// Weight per device: CPU stays quantized; GPU/NPU prefer fp16.
export const DEVICE_VARIANTS = { cpu: "quantized", gpu: "fp16", npu: "fp16" };

// Scenario modes for the models page: intent cards instead of the raw
// variant/engine matrix. Subtitles are static fallbacks; the page overrides
// them with live inventory sizes so numbers never go stale.
export const MODEL_MODES = [
  {
    id: "balanced",
    title: "均衡",
    subtitle: "~540 MB",
    description: "推荐：质量与速度平衡",
    variant: "quantized",
    engine: "cpu",
    requiresGPU: false,
    recommended: true,
  },
  {
    id: "performance",
    title: "高性能",
    subtitle: "~1 GB",
    description: "GPU/NPU 加速，需兼容硬件",
    variant: "fp16",
    engine: "auto",
    requiresGPU: true,
  },
];

// Map an active weight back to the mode card that represents it; used when
// an existing installation has no stored mode preference yet.
export function modeForVariant(variant) {
  return variant === "fp16" ? "performance" : "balanced";
}

export function performanceAvailable(platform, capabilities) {
  return engineAvailable(platform, "gpu", capabilities) || engineAvailable(platform, "npu", capabilities);
}

export function detectBestEngine(platform, capabilities) {
  if (engineAvailable(platform, "gpu", capabilities)) return "gpu";
  return "cpu";
}

export const DEVICES = [
  {
    id: "cpu",
    title: "CPU",
    detail: "兼容性最好、无需额外驱动，速度相对慢。",
  },
  {
    id: "gpu",
    title: "GPU",
    detail: "适合频繁或批量向量计算，速度和显存占用通常更高；Linux 本版不可用。",
  },
  {
    id: "npu",
    title: "NPU",
    detail: "面向支持的 Windows 硬件，强调能效；需兼容硬件和引擎，启用时将做实际推理验证。",
  },
];

export const isProfileModel = (model) => model !== "m3";

export function variantFromItem(item, variants = []) {
  const name = String(item || "").toLowerCase();
  for (const variant of variants) {
    const filename = String(variant.path || "").toLowerCase().split(/[\\/]/).pop();
    if (filename && name.includes(filename)) return variant.name;
  }
  return /model_([a-z0-9]+)(?:[.][a-z0-9-]+)?[.](?:onnx|ort)\b/.exec(name)?.[1] || "";
}

// The legacy GPU choice asks Runtime to probe; only explicit denial removes
// that choice. An NPU, unlike GPU, is never inferred from the OS.
export function engineAvailable(platform, engine, capabilities) {
  if (engine === "cpu") return true;
  const os = String(platform || "");
  const advertised = capabilities?.[engine];
  const available = typeof advertised === "boolean" ? advertised : advertised?.available;
  if (engine === "gpu") return Boolean(os) && !os.startsWith("linux") && available !== false;
  if (engine === "npu") return os.startsWith("windows") && available === true;
  return false;
}

export function runtimeModelScope(ctx) {
  const health = ctx.desktop?.health;
  return ctx.desktop?.connected && health?.credential_state !== "required" &&
    health?.account_id && health.instance_id && health.generation && health.data_dir
    ? JSON.stringify([health.account_id, health.instance_id, health.generation, health.data_dir])
    : null;
}

export function modelSwitchBlock(ctx) {
  if (runtimeModelScope(ctx)) return "";
  if (!ctx.desktop?.connected) return "Runtime 未连接。连接后才能启用模型。";
  if (ctx.desktop.health?.credential_state === "required")
    return "Runtime 账户已锁定。解锁后才能启用模型。";
  return "Runtime 账户尚未确认。请刷新状态，确认账户后再启用模型。";
}

export function engineCapabilities(ctx) {
  return ctx.status?.summary?.model_engine_capabilities ?? ctx.desktop?.engine?.capabilities;
}

export function localModelDownload(task) {
  return task?.busy === true &&
    /下载(?:模型|基础文件)|合并文件|模型文件校验|tokenizer[.]json|[a-z0-9_.-]+[.](?:onnx|ort)\b/i.test(String(task.stage || ""));
}

export function modelActivity({ task, operation, index, switching } = {}) {
  if (switching) return "switching";
  if (localModelDownload(task)) return "downloading";
  if (operation?.active) {
    const phase = String(operation.phase || "").toLowerCase();
    if (phase.startsWith("switch-") || /load|probe/.test(phase)) return "switching";
    if (operation.background || /index|embed/.test(phase) || index?.running) return "indexing";
    if (/download|fetch|merge|verify|check|hash/.test(phase)) return "downloading";
  }
  if (index?.running || index?.scheduled) return "indexing";
  return "idle";
}

export async function fetchModelDownloadTask(legacyTask) {
  try {
    return (await api("models/progress"))?.task || {};
  } catch (error) {
    // The previous desktop host rejects unknown GET routes with this exact
    // response. A transport error does not prove the isolated slot is absent.
    const legacy = /^(?:desktop API request rejected|unknown desktop action|请求失败 \((?:404|405|501)\))$/i;
    if (!legacy.test(error.message)) throw error;
    return typeof legacyTask === "function" ? legacyTask() : (await api("state"))?.task || {};
  }
}

export function watchModelDownloadTask(onTask, {
  read = fetchModelDownloadTask,
  schedule = setTimeout,
  cancel = clearTimeout,
} = {}) {
  let disposed = false, timer, last = {};
  async function tick() {
    let snapshot;
    try {
      snapshot = await read();
    } catch {
      // Keep the last isolated snapshot during a failed read. It is not
      // replaced by unrelated desktop work from the legacy global slot.
    }
    if (disposed) return;
    if (snapshot) {
      last = snapshot;
      onTask(snapshot);
    }
    timer = schedule(tick, localModelDownload(last) ? 700 : 3000);
  }
  void tick();
  return () => {
    disposed = true;
    cancel(timer);
  };
}

export async function cancelModelWork(task, operation) {
  if (localModelDownload(task))
    return api("model-download-cancel", { pause: false });
  if (operation?.active && operation.id) return execute("model_cancel", { id: operation.id });
  throw new Error("没有正在运行的模型任务");
}

// Bind the click to what the page showed. If work changes while obtaining a
// fresh snapshot, never reinterpret a local cancel as Runtime cancel (or the
// reverse), and never cancel a replacement Runtime operation.
export async function cancelDisplayedModelWork(displayedTask, displayedOperation, {
  read = fetchModelDownloadTask, currentOperation, onTask,
}) {
  const local = localModelDownload(displayedTask);
  const task = await read();
  onTask?.(task);
  if (local) {
    if (localModelDownload(task)) return cancelModelWork(task);
  } else {
    const current = currentOperation();
    if (!localModelDownload(task) && displayedOperation?.active && displayedOperation.id &&
        current?.active && current.id === displayedOperation.id)
      return cancelModelWork(null, current);
  }
  throw new Error("模型任务已变化，请重新确认后取消。");
}

export async function fetchInventory(model) {
  return api("model-variants?model=" + encodeURIComponent(model));
}

export async function fetchProfileState() {
  try {
    return await api("model-profile-state");
  } catch {
    return null;
  }
}

// The runtime reports a switch in progress.summary: `switch` carries the
// terminal state (pending / ready / failed + error), `operation` the live
// phase, and catalog.active the running selection. Read that shape in one
// place so the models page, the wizard and the overview never diverge.
export function readProfileState(state) {
  const progressEnvelope = state?.progress || {};
  const progress = progressEnvelope.details || progressEnvelope.summary || {};
  const catalogEnvelope = state?.catalog || {};
  const catalog = catalogEnvelope.details || catalogEnvelope.summary || {};
  return {
    switchState: progress.switch || {},
    operation: progress.operation || {},
    active: catalog.active || null,
  };
}

export function switchID(value) {
  const id = value?.id ?? value?.task_id ?? value?.details?.id ?? value?.summary?.id;
  return typeof id === "string" && id.trim() ? id : null;
}

async function requestProfileSwitch(model, engine, variant) {
  let response, data;
  try {
    response = await fetch("/api/model-switch", {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Respire-Client": "1" },
      body: JSON.stringify({ model, engine, variant, receipt: true }),
    });
    data = await response.json();
  } catch (cause) {
    throw Object.assign(new Error("未收到模型切换回执。", { cause }), { switchUnconfirmed: true });
  }
  if (!response.ok || data?.ok !== true) {
    const message = data?.error || `请求失败 (${response.status})`;
    const switchUnconfirmed = response.status >= 500 || typeof data?.ok !== "boolean" ||
      (typeof message === "string" && /^请求 [^\s]+ 的结果尚未确认/.test(message));
    throw Object.assign(new Error(message), { switchUnconfirmed });
  }
  return data.result;
}

function ensureCurrentSwitch(isCurrent) {
  if (!isCurrent()) throw new Error("当前账户或模型选择已变化，未继续提交模型切换。");
}

export async function submitProfileSwitch(model, engine, variant, isCurrent = () => true) {
  ensureCurrentSwitch(isCurrent);
  const snapshot = await fetchProfileState();
  ensureCurrentSwitch(isCurrent);
  const prior = readProfileState(snapshot).switchState;
  if ((prior.state ?? prior.status) === "pending") throw new Error("已有模型切换任务，等待原任务完成后再试。");
  if (isProfileModel(model)) await api("model-configure", { model, variant });
  ensureCurrentSwitch(isCurrent);
  const receipt = await requestProfileSwitch(model, engine, variant);
  return { expectedId: switchID(receipt) };
}

function matchingSwitchState(state, tracking) {
  const progress = state?.progress;
  if (progress?.command !== "model.progress" || progress.status !== "ok" || !tracking?.expectedId)
    return null;
  const details = readProfileState(state);
  return switchID(details.switchState) === tracking.expectedId ? details : null;
}

function switchVerdict(details) {
  const switchState = details?.switchState;
  if (!switchState) return { settled: false, failed: false };
  const phase = String(switchState.state || switchState.status || "").toLowerCase();
  const failed = ["failed", "cancelled", "canceled"].includes(phase);
  return { settled: failed || phase === "ready", failed };
}

export function profileStateSettled(state, tracking) {
  return switchVerdict(matchingSwitchState(state, tracking));
}

export function switchProgressLabel(state) {
  const { operation: progress, switchState } = readProfileState(state);
  const id = switchID(switchState);
  const operation = id && id === switchID(progress) ? progress : {};
  const phase = {
    queued: "排队中",
    "switch-verify": "验证权重",
    "switch-download": "下载权重",
    "switch-load": "加载模型",
    "switch-index": "重建记忆索引",
    "switch-diary": "重建日记索引",
  }[operation.phase] || (switchState.state === "pending" ? "等待 Runtime 完成切换" : "");
  return phase && operation.total
    ? `${phase} · ${Math.min(100, Math.round((operation.done / operation.total) * 100))}%`
    : phase;
}

export function switchFailure(state) {
  const { switchState } = state || {};
  const rollback = switchState?.rollback?.state ?? switchState?.rollback ?? switchState?.recovery ?? switchState?.semantic;
  const detail = {
    previous_ready: "切换前模型已重新验证可用。",
    unchanged: "原模型未被更改。",
    unverified: "原模型是否可用尚未确认。",
  }[rollback] || "请查看 Runtime 的当前模型状态。";
  return `模型切换失败${switchState?.error ? `：${switchState.error}` : ""}；${detail}`;
}

export async function waitForProfileSwitch({ tracking, onTick, timeoutMs = 15 * 60_000,
  isCurrent = () => true } = {}) {
  const unknown = { settled: false, failed: false, unconfirmed: true, details: {} };
  if (!tracking?.expectedId) return unknown;
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline && isCurrent()) {
    const state = await fetchProfileState();
    if (!isCurrent()) return unknown;
    const details = matchingSwitchState(state, tracking);
    if (details) onTick?.(state);
    const verdict = switchVerdict(details);
    if (verdict.settled) return { ...verdict, details };
    await new Promise((resolve) => setTimeout(resolve, 3000));
  }
  return { ...unknown, timeout: isCurrent() };
}

// Download progress modal: live bytes, pause / resume / cancel, result.
// Resolves `done` with true only when the task reached the completed stage.
export function openDownloadModal({
  model,
  engine,
  variant,
  label,
  getMirror,
  onDone,
} = {}) {
  const element = document.createElement("dialog");
  element.className = "dialog";
  const subject = variant === "baseline" ? "基础模型文件" : variant;
  element.innerHTML = `<form method="dialog"><div class="dialog-heading"><h2>下载权重文件</h2><button type="button" class="icon-button" data-close aria-label="关闭">×</button></div><div class="dialog-body"><p class="hint">${h(label)} · ${h(subject)}</p><div class="progress-label"><span data-dl-stage>正在连接下载源…</span><span data-dl-bytes></span></div><progress data-dl-bar style="width:100%"></progress><p class="form-error" role="alert" hidden data-dl-error></p></div><footer><button type="button" data-dl="pause" hidden>暂停</button><button type="button" data-dl="resume" class="primary" hidden>继续下载</button><button type="button" data-dl="cancel" hidden>取消下载</button><button type="button" data-dl="retry" class="primary" hidden>重新下载</button><button type="button" data-dl="dismiss" class="primary" hidden>关闭</button></footer></form>`;
  document.body.append(element);
  element.showModal();
  const stageNode = $("[data-dl-stage]", element),
    bytesNode = $("[data-dl-bytes]", element),
    bar = $("[data-dl-bar]", element),
    errorNode = $("[data-dl-error]", element);
  const buttons = {
    pause: $('[data-dl="pause"]', element),
    resume: $('[data-dl="resume"]', element),
    cancel: $('[data-dl="cancel"]', element),
    retry: $('[data-dl="retry"]', element),
    dismiss: $('[data-dl="dismiss"]', element),
  };
  // The wizard awaits this; the models page ignores it and refreshes on close.
  let resolveDone;
  const done = new Promise((resolve) => {
    resolveDone = resolve;
  });
  function setMode(mode) {
    for (const [name, button] of Object.entries(buttons))
      button.hidden = true;
    if (mode === "downloading") buttons.pause.hidden = false, buttons.cancel.hidden = false;
    else if (mode === "paused") buttons.resume.hidden = false, buttons.cancel.hidden = false;
    else if (mode === "done") buttons.dismiss.hidden = false;
    else if (mode === "failed") buttons.retry.hidden = false, buttons.dismiss.hidden = false;
    else if (mode === "cancelled") buttons.retry.hidden = false, buttons.dismiss.hidden = false;
  }
  let timer = null;
  let startPromise = Promise.resolve();
  // While our POST is in flight a terminal task snapshot may still describe
  // the previous task, so poll() must not treat it as our result.
  let submitting = false;
  let polling = false;
  let generation = 0;
  function start() {
    errorNode.hidden = true;
    setMode("downloading");
    const mirror = getMirror?.() || "auto";
    submitting = true;
    generation += 1;
    startPromise = api("model-download", {
      model,
      variant,
      ...(["cpu", "gpu"].includes(engine) ? { engine } : {}),
      ...(mirror && mirror !== "auto" ? { mirror } : {}),
    }).then(
      () => {
        submitting = false;
      },
      (error) => {
        submitting = false;
        // The bridge rejects with 409 when another task is running.
        stageNode.textContent = "无法开始下载";
        errorNode.textContent = error.message;
        errorNode.hidden = false;
        setMode("failed");
        throw error;
      },
    );
    return startPromise;
  }
  function applyTask(task) {
    stageNode.textContent = task.stage || "";
    bytesNode.textContent = task.total
      ? `${bytes(task.done)} / ${bytes(task.total)}`
      : "";
    if (task.total) {
      bar.value = task.done;
      bar.max = task.total;
    }
  }
  async function poll() {
    if (submitting || polling || !element.open) return;
    polling = true;
    const started = generation;
    try {
      const task = await fetchModelDownloadTask();
      if (started !== generation || submitting || !element.open) return;
      applyTask(task);
      if (task.busy) return;
      // The download ended: success, paused, cancelled or failed.
      clearInterval(timer);
      timer = null;
      if (task.stage === "已暂停") {
        setMode("paused");
        stageNode.textContent = "已暂停 · 已下载部分保留，可继续";
      } else if (task.stage === "完成") {
        setMode("done");
        stageNode.textContent = "下载完成，校验通过";
        bar.value = bar.max = task.total || 1;
        resolveDone(true);
        onDone?.();
      } else if (task.stage === "已取消") {
        setMode("cancelled");
        stageNode.textContent = "已取消";
        resolveDone(false);
      } else {
        setMode("failed");
        stageNode.textContent = "下载失败";
        errorNode.textContent = task.error || "下载失败，请重试";
        errorNode.hidden = false;
        resolveDone(false);
      }
    } catch {
      // transient state poll failure: keep the modal as-is
    } finally {
      polling = false;
    }
  }
  const resumeOrRetry = (button) =>
    action(button, async () => {
      setMode("downloading");
      await start();
      if (!timer) timer = setInterval(poll, 700);
    });
  element.addEventListener("click", (event) => {
    const button = event.target.closest("[data-dl]");
    if (!button || button.hidden) return;
    const act = button.dataset.dl;
    if (act === "pause")
      action(button, async () => {
        await api("model-download-cancel", { pause: true });
      });
    else if (act === "resume" || act === "retry") resumeOrRetry(button);
    else if (act === "cancel")
      action(button, async () => {
        await api("model-download-cancel", { pause: false });
        element.close();
      });
    else if (act === "dismiss") element.close();
  });
  element.addEventListener(
    "close",
    () => {
      if (timer) {
        clearInterval(timer);
        timer = null;
      }
      // Closing the modal does not stop the download; an unresolved `done`
      // settles false so the awaiting wizard can recover from task state.
      resolveDone(false);
      onDone?.();
    },
    { once: true },
  );
  setMode("downloading");
  // Poll from the start, not after the POST resolves: the server answers as
  // soon as the task is registered, so progress streams in while it runs.
  timer = setInterval(poll, 700);
  start().catch(() => { clearInterval(timer); timer = null; });
  return { element, done };
}

// notifyFallback surfaces the runtime's real post-probe engine when the
// selected device failed verification; the wizard and models page share it so
// a fallback can never be presented as success of the chosen device. The
// probe failure cause travels in result.warning — always show it.
export function fallbackMessage(engine, result) {
  if (!result?.fallback) return "";
  const reason = result.warning ? `（${result.warning}）` : "";
  const restored = result.engine
    ? `${result.engine.toUpperCase()} · ${result.model}`
    : `CPU（${result.model}）`;
  return `${engine.toUpperCase()} 验证失败${reason}，已恢复切换前的 ${restored}；详情见应用结果。`;
}

const unconfirmedModelSwitches = new WeakMap();
const switchUnconfirmedNotice = "模型切换请求的结果尚未确认。请刷新查看 Runtime 当前状态；不要重复提交。";

export function modelSwitchUnconfirmed(ctx) {
  const scope = runtimeModelScope(ctx);
  return Boolean(scope && scope === unconfirmedModelSwitches.get(ctx));
}

export async function applyEngineChoice({ model, engine, variant, ctx, onTick, isCurrent = () => true }) {
  const scope = ctx && runtimeModelScope(ctx);
  const unconfirmed = () => {
    if (scope) unconfirmedModelSwitches.set(ctx, scope);
    return { engine, variant, fallback: false, failed: false, unconfirmed: true, warning: switchUnconfirmedNotice };
  };
  if (scope && modelSwitchUnconfirmed(ctx)) return unconfirmed();
  try {
    const tracking = await submitProfileSwitch(model, engine, variant, isCurrent);
    const verdict = await waitForProfileSwitch({ tracking, onTick, isCurrent });
    if (verdict.unconfirmed) return unconfirmed();
    // Published typed terminal results identify their own selection;
    // catalog.active may already reflect a subsequent unrelated request.
    const selection = verdict.details.switchState?.result?.selection;
    return {
      engine: selection?.engine || engine,
      variant: selection?.variant || variant,
      fallback: false,
      failed: verdict.failed,
      warning: verdict.failed ? switchFailure(verdict.details) : "",
      details: verdict.details,
    };
  } catch (error) {
    if (error.switchUnconfirmed) return unconfirmed();
    throw error;
  }
}
