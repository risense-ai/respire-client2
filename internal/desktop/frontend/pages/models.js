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
} from "../ui/core.js";
import { heading, phase } from "./overview.js";
import { recallChecks } from "./recall.js";

const MIRROR_OPTIONS = [
  ["auto", "auto · 自动选择镜像"],
  ["https://hf-mirror.com", "hf-mirror.com"],
  ["https://hf-mirror.net", "hf-mirror.net"],
  ["https://huggingface.co", "huggingface.co"],
  ["custom", "自定义镜像…"],
];

export function models(root, ctx) {
  let agent = {},
    backend = {},
    engineState = { engine: "cpu", model: "quantized" },
    inventory = null,
    loaded = false,
    loading = false,
    sawMissingBaseline = false,
    baselinePrepared = false;
  root.innerHTML =
    heading("模型与索引", "查看真实加载与下载进度，管理推理引擎和检索方式。") +
    `<div data-live></div>` +
    panel(
      "推理引擎与模型",
      `<div class="panel-body stack"><p class="hint">BGE-M3 权重按需下载（多线程并行，0.5–1.2 GiB/个），下载完成前无法选择对应推理方式；应用引擎会立即重载 Runtime 模型并运行真实推理验证，失败自动回退 CPU。</p><form data-mirror class="stack"><div class="form-grid">${select(
        "mirror",
        "下载镜像",
        MIRROR_OPTIONS,
        "auto",
      )}<div data-mirror-custom hidden>${input(
        "mirror-custom",
        "自定义镜像源",
        "https://",
        { placeholder: "http/https 源站，仅主机名" },
      )}</div></div><div class="actions"><button type="submit">保存下载来源</button><span class="hint" data-mirror-state></span></div></form><div class="scroll-table" data-variants><p class="hint">正在读取模型目录…</p></div><div class="actions" data-baseline></div><form data-engine class="stack"><div class="form-grid">${select(
        "engine",
        "推理引擎",
        ctx.desktop?.platform?.startsWith("linux")
          ? [["cpu", "CPU"]]
          : [
              ["cpu", "CPU"],
              ["gpu", "GPU"],
              ["npu", "NPU"],
            ],
        "cpu",
      )}<label>权重文件<select name="variant" disabled><option>正在读取模型目录…</option></select></label></div><p class="hint">只能选择已下载的权重文件；CPU 默认 quantized，GPU/NPU 默认 fp16。应用引擎立即重载并验证，失败自动回退 CPU。</p><div class="actions"><button type="submit" class="primary">应用引擎（重载并验证）</button><button type="button" data-model="probe">运行实际推理验证</button><button type="button" data-model="providers">安装 NPU 引擎</button></div></form><div class="actions"><button data-model="cancel">取消当前任务</button><button data-model="uninstall" class="danger">卸载全部模型文件</button></div></div>`,
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
      "高质量召回服务",
      `<form data-api class="panel-body stack"><div class="form-grid">${input("base", "API 地址", "https://api.deepseek.com/v1", { required: true })}${input("model", "模型", "deepseek-flash", { required: true })}</div>${input("key", "API Key（留空保留已有密钥）", "", { type: "password" })}<p class="hint">密钥通过 CLI 写入系统凭据库，不保存在客户端设置中。</p><div class="actions"><button type="submit">保存服务配置</button><span data-key-state class="hint">正在读取配置…</span></div></form>`,
    ) +
    `<div data-result class="section-gap"></div>`;
  const live = $("[data-live]", root);
  function variantFromItem(item) {
    const match = /model_([a-z0-9]+)\.onnx/.exec(item || "");
    return match ? match[1] : "";
  }
  function currentMirror() {
    const value = $("[data-mirror] [name=mirror]", root).value;
    return value === "custom"
      ? $("[name=mirror-custom]", root).value.trim()
      : value;
  }
  function syncEngineForm(opActive) {
    const form = $("[data-engine]", root);
    if (!form || !inventory) return;
    const variantSel = $("[name=variant]", form);
    const wanted = variantSel.value || engineState.model;
    variantSel.replaceChildren(
      ...inventory.variants.map((variant) => {
        const option = new Option(
          `${variant.name} · ${bytes(variant.size)}${variant.installed ? "" : "（未下载）"}`,
          variant.name,
          false,
          variant.name === wanted,
        );
        option.disabled = !variant.installed;
        return option;
      }),
    );
    if (!variantSel.value || variantSel.selectedOptions[0]?.disabled) {
      const first = inventory.variants.find((variant) => variant.installed);
      variantSel.value = first ? first.name : "";
    }
    const anyInstalled = inventory.variants.some(
      (variant) => variant.installed,
    );
    const engineSel = $("[name=engine]", form);
    engineSel.disabled = !anyInstalled;
    variantSel.disabled = !anyInstalled;
    $("[type=submit]", form).disabled =
      !anyInstalled || !variantSel.value || opActive;
    $("[data-model=cancel]", root).disabled = !opActive || !ctx.status?.summary?.model_operation?.id;
    $("[data-model=providers]", root).disabled =
      !ctx.desktop?.platform?.startsWith("windows");
    $("[name=engine] option[value=gpu]", form)?.toggleAttribute(
      "disabled",
      ctx.desktop?.platform?.startsWith("linux"),
    );
    $("[name=engine] option[value=npu]", form)?.toggleAttribute(
      "disabled",
      !ctx.desktop?.platform?.startsWith("windows"),
    );
  }
  function variantStatus(variant, op, downloading, task) {
    const busy = downloading === variant.name;
    const current = engineState.model === variant.name;
    const pct = op.total
      ? Math.min(100, Math.round((op.done / op.total) * 100))
      : 0;
    // Client-side parallel download: progress rides the client task.
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
    node.innerHTML = `<table><thead><tr><th>权重文件</th><th>大小</th><th>状态</th><th>本机位置</th><th>操作</th></tr></thead><tbody>${inventory.variants
      .map((variant) => {
        const badge =
          variant.name === "quantized"
            ? "CPU 默认"
            : variant.name === "fp16"
              ? "GPU/NPU 默认"
              : "";
        return `<tr><td><span class="variant-name"><strong>${h(variant.name)}</strong>${badge ? `<span class="hint">${badge}</span>` : ""}</span></td><td>${h(bytes(variant.size))}</td><td data-status="${h(variant.name)}">${variantStatus(variant, op, downloading, ctx.desktop?.task)}</td><td><code title="${h(variant.path)}">${h(variant.path)}</code></td><td><button data-model="download" data-variant="${h(variant.name)}"${downloading === variant.name ? " disabled" : ""}>${variant.installed ? "重新下载" : "下载"}</button></td></tr>`;
      })
      .join("")}</tbody></table>`;
    renderBaseline();
    syncEngineForm(op.active);
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
      : `<button data-model="prepare" class="primary">下载并准备基础模型</button><span class="hint">首次使用需下载基础文件（quantized 权重 + 分词器，约 543 MiB）并构建索引。</span>`;
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
    }
    renderBaseline();
    syncEngineForm(op.active);
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
      const values = await Promise.allSettled([
        execute("agent_config"),
        execute("model_engine"),
        execute("classification_config", { backend: "ds" }),
        api("model-variants"),
      ]);
      if (!root.isConnected) return;
      if (values[0].status === "fulfilled") {
        agent = summary(values[0].value);
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
        $("[data-engine] [name=engine]", root).value = engineState.engine;
      }
      if (values[2].status === "fulfilled") backend = summary(values[2].value);
      if (values[3].status === "fulfilled") inventory = values[3].value;
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
        if (result.status === "rejected" && index !== 3)
          notify(result.reason.message, true);
      }
      renderVariants();
      update();
    } catch (error) {
      notify(error.message, true);
    } finally {
      loading = false;
    }
  }
  root.addEventListener("click", (event) => {
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
        await api("model-download", {
          variant: "baseline",
          ...(mirror && mirror !== "auto" ? { mirror } : {}),
        });
        sawMissingBaseline = true;
        notify("下载已开始，完成后自动构建索引");
        await ctx.refresh();
      } else if (operation === "download") {
        const variant = button.dataset.variant;
        const size = inventory?.variants?.find(
          (item) => item.name === variant,
        )?.size;
        if (
          !(await confirm(
            "下载权重文件",
            `将多线程并行下载 BGE-M3 ${variant} 权重（约 ${bytes(size)}）到 ${inventory?.model_dir || "本机模型目录"}；已验证的文件会跳过。`,
            "开始下载",
          ))
        )
          return;
        await api("model-download", {
          variant,
          ...(mirror && mirror !== "auto" ? { mirror } : {}),
        });
        notify("下载已开始，进度显示在状态列");
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
      } else if (operation === "probe") result = await execute("model_probe");
      else if (operation === "providers")
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
  $("[data-engine]", root).addEventListener("submit", (event) => {
    event.preventDefault();
    action($("[type=submit]", event.target), async () => {
      const data = new FormData(event.target);
      const envelope = await execute("model_engine_apply", {
        engine: data.get("engine"),
        model: data.get("variant"),
      });
      const result = summary(envelope);
      engineState = { engine: result.engine, model: result.model };
      $("[data-engine] [name=engine]", root).value = result.engine;
      if (result.fallback)
        notify(
          `所选引擎验证失败，已自动回退 CPU（${result.model}）；详情见应用结果。`,
          true,
        );
      else
        notify(
          `已切换到 ${String(result.engine).toUpperCase()} · ${result.model}，实际推理验证通过。`,
        );
      if (root.isConnected)
        $("[data-result]", root).innerHTML = resultView("应用结果", envelope);
      renderVariants();
      await ctx.refresh();
    });
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
  update();
  const dispose = recallChecks(root);
  if (ctx.desktop?.connected) load();
  return {
    dispose,
    update() {
      update();
      if (!loaded && ctx.desktop?.connected) load();
    },
    refresh: load,
  };
}
