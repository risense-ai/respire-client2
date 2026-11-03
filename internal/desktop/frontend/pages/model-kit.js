import {
  $,
  h,
  bytes,
  summary,
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
];

export const MODEL_HINTS = {
  m3: "BGE-M3 权重按需下载（多线程并行，0.5–1.2 GiB/个），下载完成前无法选择对应推理方式；应用引擎会立即重载 Runtime 模型并运行真实推理验证，失败自动回退 CPU。",
  "bge-base-zh-v1.5":
    "BGE-base-zh-v1.5 权重按需下载（多线程并行，约 80–200 MiB/个）。下载 quantized 权重与分词器后，可在下方选择推理方式切换使用：Runtime 会注册模型档案、加载并运行真实推理验证，失败自动回退。",
  "bge-m3-distill-8l":
    "BGE-M3-Distill-8L 权重按需下载（多线程并行，约 0.3–1.1 GiB/个）。下载 quantized 权重与分词器后，可在下方选择推理方式切换使用：Runtime 会注册模型档案、加载并运行真实推理验证，失败自动回退。",
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
];

// Weight per device: CPU stays quantized; GPU/NPU prefer fp16.
export const DEVICE_VARIANTS = { cpu: "quantized", gpu: "fp16", npu: "fp16" };

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

export function variantFromItem(item) {
  const match = /model_([a-z0-9]+)\.onnx/.exec(item || "");
  return match ? match[1] : "";
}

// OS-level engine entry availability. This is not hardware detection: NPU and
// GPU still go through the runtime's real inference probe when applied.
export function engineAvailable(platform, engine) {
  if (engine === "cpu") return true;
  if (engine === "gpu") return !String(platform || "").startsWith("linux");
  if (engine === "npu") return String(platform || "").startsWith("windows");
  return false;
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

// The runtime reports the active selection in either details or summary;
// read both so either shape works.
export function readProfileState(state) {
  const progress = state?.progress || {};
  return progress.details || progress.summary || {};
}

export async function applyLegacyEngine(engine, variant) {
  const envelope = await execute("model_engine_apply", {
    engine,
    model: variant,
  });
  return { envelope, result: summary(envelope) };
}

export async function submitProfileSwitch(model, engine, variant) {
  await api("model-configure", { model, variant });
  await api("model-switch", { model, engine, variant });
}

// profileStateSettled reports whether a switch task reached a terminal
// state, and whether that state is success.
export function profileStateSettled(state) {
  const details = readProfileState(state);
  const text = String(
    details.state || details.phase || details.status || "",
  );
  if (/failed|失败/i.test(text)) return { settled: true, failed: true };
  if (/ready|完成/i.test(text)) return { settled: true, failed: false };
  return { settled: false, failed: false };
}

// waitForProfileSwitch polls model-profile-state until the switch settles or
// the deadline passes; it never reports success without a runtime receipt.
export async function waitForProfileSwitch({ onTick, timeoutMs = 15 * 60_000 } = {}) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const state = await fetchProfileState();
    onTick?.(state);
    const verdict = profileStateSettled(state);
    if (verdict.settled) return { ...verdict, details: readProfileState(state) };
    await new Promise((resolve) => setTimeout(resolve, 3000));
  }
  return { settled: true, failed: true, details: {}, timeout: true };
}

// Download progress modal: live bytes, pause / resume / cancel, result.
// Resolves `done` with true only when the task reached the completed stage.
export function openDownloadModal({
  model,
  variant,
  label,
  getMirror,
  onDone,
} = {}) {
  const element = document.createElement("dialog");
  element.className = "dialog";
  const subject = variant === "baseline" ? "基础文件（quantized + 分词器）" : variant;
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
  function start() {
    errorNode.hidden = true;
    setMode("downloading");
    const mirror = getMirror?.() || "auto";
    startPromise = api("model-download", {
      model,
      variant,
      ...(mirror && mirror !== "auto" ? { mirror } : {}),
    }).catch((error) => {
      // The bridge rejects with 409 when another task is running.
      stageNode.textContent = "无法开始下载";
      errorNode.textContent = error.message;
      errorNode.hidden = false;
      setMode("failed");
      throw error;
    });
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
    try {
      const state = await api("state");
      const task = state.task || {};
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
  start()
    .then(() => {
      timer = setInterval(poll, 700);
      poll();
    })
    .catch(() => {});
  return { element, done };
}

// notifyFallback surfaces the runtime's real post-probe engine when the
// selected device failed verification; the wizard and models page share it so
// a fallback can never be presented as success of the chosen device.
export function fallbackMessage(engine, result) {
  if (!result?.fallback) return "";
  return `${engine.toUpperCase()} 验证失败，已自动回退 CPU（${result.model}）；详情见应用结果。`;
}

export async function applyEngineChoice({ model, engine, variant }) {
  if (isProfileModel(model)) {
    await submitProfileSwitch(model, engine, variant);
    const verdict = await waitForProfileSwitch();
    return {
      engine,
      variant,
      fallback: false,
      failed: verdict.failed,
      warning: verdict.failed
        ? verdict.timeout
          ? "模型切换等待超时，请在模型页查看最终状态。"
          : "模型切换失败，Runtime 已回退原模型。"
        : "",
      details: verdict.details,
    };
  }
  const { envelope, result } = await applyLegacyEngine(engine, variant);
  return {
    engine: result.engine,
    variant: result.model,
    fallback: Boolean(result.fallback),
    failed: false,
    warning: fallbackMessage(engine, result),
    envelope,
  };
}
