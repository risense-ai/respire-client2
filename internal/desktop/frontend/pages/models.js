import {
  $,
  all,
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
} from "../ui/core.js";
import { heading, phase } from "./overview.js";
import { recallChecks } from "./recall.js";

export function models(root, ctx) {
  let agent = {},
    backend = {},
    engine = "cpu",
    loaded = false,
    loading = false;
  root.innerHTML =
    heading("模型与索引", "查看真实加载与下载进度，管理推理引擎和检索方式。") +
    `<div data-live></div><div class="grid section-gap">${panel("本机模型", `<div class="panel-body stack"><p class="hint">BGE-M3 量化模型约 543 MiB。文件下载、校验和索引构建由 Runtime 执行。</p>${input("mirror", "下载来源", "auto", { placeholder: "auto 或镜像地址" })}<div class="actions"><button data-model="mirror">保存下载来源</button><button data-model="prepare" class="primary">下载并准备模型</button><button data-model="restart">取消并重新准备</button><button data-model="cancel">取消当前任务</button><button data-model="uninstall" class="danger">卸载模型</button></div></div>`)}
    ${panel(
      "推理引擎",
      `<form data-engine class="panel-body stack">${select(
        "engine",
        "引擎",
        ctx.desktop?.platform?.startsWith("linux")
          ? [["cpu", "CPU"]]
          : [
              ["cpu", "CPU"],
              ["gpu", "GPU"],
              ["npu", "NPU"],
            ],
        "cpu",
      )}<p class="hint">默认使用 CPU。切换失败会报告错误，不自动改用其他引擎。</p><div class="actions"><button type="submit">保存引擎</button><button type="button" data-model="probe">运行实际推理验证</button><button type="button" data-model="providers">安装 NPU 引擎</button></div></form>`,
    )}</div>` +
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
    $("[data-model=cancel]", root).disabled = !op.active || !op.id;
    $("[data-model=providers]", root).disabled =
      !ctx.desktop?.platform?.startsWith("windows");
    $("[name=engine] option[value=gpu]", root)?.toggleAttribute(
      "disabled",
      ctx.desktop?.platform?.startsWith("linux"),
    );
    $("[name=engine] option[value=npu]", root)?.toggleAttribute(
      "disabled",
      !ctx.desktop?.platform?.startsWith("windows"),
    );
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
      ]);
      if (!root.isConnected) return;
      if (values[0].status === "fulfilled") {
        agent = summary(values[0].value);
        $("[data-recall] [name=mode]", root).value =
          agent.recall_mode || "fast";
        $("[name=mirror]", root).value = agent.model_mirror || "auto";
      }
      if (values[1].status === "fulfilled") {
        engine = summary(values[1].value).engine;
        $("[data-engine] [name=engine]", root).value = engine || "";
      }
      if (values[2].status === "fulfilled") backend = summary(values[2].value);
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
      for (const result of values) {
        if (result.status === "rejected") notify(result.reason.message, true);
      }
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
      if (operation === "mirror") {
        result = await execute("agent_set", {
          setting:
            "model_mirror=" + ($("[name=mirror]", root).value.trim() || "auto"),
        });
      } else if (operation === "restart") {
        if (
          !(await confirm(
            "重新准备模型",
            "将取消当前任务，再使用所选来源准备模型。已完成的文件会保留。",
            "重新准备",
          ))
        )
          return;
        const id = ctx.status?.summary?.model_operation?.id;
        if (id) await execute("model_cancel", { id });
        result = await execute("model_prepare", {
          mirror: $("[name=mirror]", root).value.trim() || "auto",
        });
      } else if (operation === "prepare") {
        if (
          !(await confirm(
            "准备本机模型",
            "首次准备需要下载约 543 MiB，并由 Runtime 构建索引。已校验的文件会保留。",
            "开始准备",
          ))
        )
          return;
        result = await execute("model_prepare", {
          mirror: $("[name=mirror]", root).value.trim() || "auto",
        });
      } else if (operation === "cancel") {
        const id = ctx.status?.summary?.model_operation?.id;
        if (!id) throw new Error("没有正在运行的模型任务");
        result = await execute("model_cancel", { id });
      } else if (operation === "uninstall") {
        if (
          !(await confirm(
            "卸载模型",
            "删除用户安装的 BGE-M3 文件后，语义召回需要重新准备模型。",
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
  $("[data-engine]", root).addEventListener("submit", (event) => {
    event.preventDefault();
    action($("[type=submit]", event.target), async () => {
      await execute("model_engine_set", {
        engine: new FormData(event.target).get("engine"),
      });
      notify("引擎已保存，下次加载模型生效");
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
