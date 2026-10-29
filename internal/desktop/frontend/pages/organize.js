import {
  $,
  all,
  h,
  raw,
  input,
  select,
  dialog,
  execute,
  action,
  confirm,
  notify,
  summary,
  panel,
  resultView,
  showText,
} from "../ui/core.js";
import { heading } from "./overview.js";
import { report, entry } from "../memory/model.js";

export function organize(root, ctx) {
  let plan = null,
    planRevision = "",
    suggestions = [],
    backend = "ds";
  root.innerHTML =
    heading(
      "让记忆各归其位。",
      "先查看建议与方案，再执行移动或合并。所有变更交给 Runtime 校验。",
    ) +
    `<div class="actions organize-actions"><button data-analyze="suggestions">归纲建议</button><button data-analyze="duplicates">相似记忆</button><button data-analyze="audit">结构检查</button><button data-analyze="plan">生成本地修复方案</button></div>` +
    `<div data-report class="section-gap"><div class="panel empty">选择一种检查，读取当前记忆库的实际结果。</div></div>` +
    panel(
      "整理方案",
      `<div class="panel-body stack"><p class="hint">方案只包含明确选定的变更。预览后记忆库发生变化时，需要重新生成方案。</p><label>方案 JSON<textarea name="spec" rows="8" spellcheck="false" placeholder='{"ops":[{"id":"记忆 ID","parent":"父节点 ID"}]}'></textarea></label><div class="actions"><button data-plan-preview>校验并预览</button><button data-plan-apply class="primary" disabled>应用已预览方案</button><button data-plan-copy>复制方案</button></div></div>`,
    ) +
    panel(
      "AI 整理",
      `<form data-ai class="panel-body stack"><div class="form-grid">${select(
        "backend",
        "整理服务",
        [
          ["ds", "DS · OpenAI 兼容"],
          ["jev", "Jev"],
        ],
        "ds",
      )}${input("limit", "最多处理条数", "50", { type: "number", required: true })}${input("root", "限定子树 ID（可选）")}<label class="checkbox-label"><input type="checkbox" name="all">处理全库（覆盖条数限制）</label></div><p class="hint">会把选定范围的记忆材料发送给已配置的模型服务。生成预览不会自动执行变更。</p><div class="actions"><button type="submit">AI 方案预览</button><button type="button" data-ai-apply>AI 整理并执行</button><button type="button" data-provider>配置整理服务</button><span data-provider-state class="hint"></span></div></form>`,
    ) +
    `<div data-ai-result class="section-gap"></div>`;
  const area = $("[data-report]", root),
    specField = $("[name=spec]", root);
  async function revision() {
    return raw(summary(await execute("memory_revision")));
  }
  function setPlan(value, stamp) {
    plan = value;
    planRevision = stamp;
    specField.value = raw(value);
    $("[data-plan-apply]", root).disabled = true;
  }
  async function inspect(kind) {
    const names = {
      suggestions: "organize_suggestions",
      duplicates: "organize_duplicates",
      audit: "organize_audit",
      plan: "organize_plan",
    };
    const stamp = await revision();
    const result = await execute(
      names[kind],
      kind === "suggestions"
        ? { top: 50 }
        : kind === "duplicates"
          ? { min: 0.8, top: 30 }
          : {},
    );
    if (!root.isConnected) return;
    const value = report(result);
    if (kind === "suggestions") {
      if (!Array.isArray(value.suggests))
        throw new Error("Runtime 未返回归纲建议列表");
      suggestions = value.suggests;
      area.innerHTML = panel(
        "归纲建议",
        suggestions.length
          ? '<div class="panel-body actions"><button data-suggestions-all>预览全部建议</button></div>' + suggestions
              .map(
                (item, index) =>
                  `<div class="suggestion-card"><div class="row-copy"><strong>${h(item.orphan?.title || item.orphan?.id)}</strong><p>→ ${h(item.target?.title || item.target?.id)}</p><small>${h(item.target_tree?.title || "")}</small></div><button data-suggestion="${index}">预览移动</button></div>`,
              )
              .join("")
          : '<div class="empty">当前没有归纲建议</div>',
      );
    } else if (kind === "plan") {
      if (!Array.isArray(value.ops))
        throw new Error("Runtime 未返回有效的修复方案");
      setPlan({ ops: value.ops }, stamp);
      area.innerHTML = resultView("本地结构修复预览", result);
      notify("方案已生成，尚未修改记忆");
    } else
      area.innerHTML = resultView(
        kind === "duplicates" ? "相似记忆检查" : "结构检查",
        result,
      );
  }
  async function preview() {
    const parsed = JSON.parse(specField.value);
    if (!Array.isArray(parsed.ops)) throw new Error("方案需要包含 ops 数组");
    const stamp = await revision();
    const result = await execute("organize_resort_preview", {
      spec: raw(parsed),
    });
    plan = parsed;
    planRevision = stamp;
    area.innerHTML = resultView("方案预览", result);
    $("[data-plan-apply]", root).disabled = false;
    notify("预览已完成，确认后可应用。");
  }
  async function ai(apply) {
    const value = Object.fromEntries(new FormData($("[data-ai]", root)));
    const args = {
      backend: value.backend,
      limit: Number(value.limit),
      causal: true,
    };
    if (value.root.trim()) args.root = value.root.trim();
    if (value.all) args.all = true;
    if (apply) {
      if (
        !(await confirm(
          "执行 AI 整理",
          "所选记忆材料将发送给配置的模型服务，Runtime 会自动执行选定的整理变更。",
          "确认执行",
        ))
      )
        return;
      args.auto = true;
    }
    const stamp = await revision();
    const result = await execute("organize_classify", args, {
      onProgress: (job) => {
        if (root.isConnected)
          $("[data-ai-result]", root).innerHTML = panel(
            "整理进行中",
            `<div class="panel-body"><p>可切换页面，稍后从操作记录查询。</p><small>${h(job.id)}</small></div>`,
          );
      },
    });
    if (!root.isConnected) return;
    $("[data-ai-result]", root).innerHTML = resultView(
      apply ? "AI 整理结果" : "AI 整理预览",
      result,
    );
    if (!apply) {
      const value = report(result);
      if (Array.isArray(value.ops)) setPlan({ ops: value.ops }, stamp);
    }
    if (apply) await ctx.refresh();
  }
  root.addEventListener("click", (event) => {
    const button = event.target.closest("button");
    if (!button) return;
    if (button.dataset.analyze)
      action(button, () => inspect(button.dataset.analyze));
    if (button.dataset.suggestion !== undefined)
      action(button, async () => {
        const item = suggestions[Number(button.dataset.suggestion)];
        setPlan(
          { ops: [{ id: item.orphan.id, parent: item.target.id }] },
          await revision(),
        );
        await preview();
      });
    if (button.hasAttribute("data-suggestions-all"))
      action(button, async () => {
        setPlan({ ops: suggestions.map((item) => ({
          id: item.orphan.id, parent: item.target.id,
        })) }, await revision());
        await preview();
      });
    if (button.hasAttribute("data-plan-preview")) action(button, preview);
    if (button.hasAttribute("data-plan-copy"))
      action(button, async () => {
        await navigator.clipboard.writeText(specField.value);
        notify("方案已复制");
      });
    if (button.hasAttribute("data-plan-apply"))
      action(button, async () => {
        if (!plan || raw(JSON.parse(specField.value)) !== raw(plan))
          throw new Error("方案已改变，请重新预览");
        if ((await revision()) !== planRevision)
          throw new Error("记忆库已变化，请重新生成或校验方案");
        if (
          !(await confirm(
            "应用整理方案",
            `将应用 ${plan.ops.length} 项变更。请核对上面的预览结果。`,
            "应用方案",
          ))
        )
          return;
        const result = await execute("organize_resort_apply", {
          spec: raw(plan),
        });
        area.innerHTML = resultView("整理结果", result);
        $("[data-plan-apply]", root).disabled = true;
        notify("方案已应用");
        await ctx.refresh();
      });
    if (button.hasAttribute("data-ai-apply")) action(button, () => ai(true));
    if (button.hasAttribute("data-provider"))
      action(button, async () => {
        const current = summary(
          await execute("classification_config", {
            backend: $("[name=backend]", root).value,
          }),
        );
        dialog({
          title: "整理服务配置",
          body: `${select(
            "backend",
            "后端",
            [
              ["ds", "DS · OpenAI 兼容"],
              ["jev", "Jev"],
            ],
            current.backend || backend,
          )}${input("base", "DS API 地址", current.api_base || "https://api.deepseek.com/v1")}${input("model", "DS 模型", current.model || "deepseek-flash")}${input("key", "API Key（留空保留已有密钥）", "", { type: "password" })}<p class="hint">Jev 使用固定端点与模型；DS 可以使用 OpenAI 兼容服务。密钥写入系统凭据库。</p>`,
          onSubmit: async (values) => {
            const args = { backend: values.backend };
            if (values.backend === "ds") {
              args.base = values.base;
              args.model = values.model;
            }
            await execute("classification_save", args, { secret: values.key });
            backend = values.backend;
            notify("整理服务已保存");
            await loadProvider();
          },
        });
      });
  });
  specField.addEventListener("input", () => {
    $("[data-plan-apply]", root).disabled = true;
  });
  $("[data-ai]", root).addEventListener("submit", (event) => {
    event.preventDefault();
    action($("[type=submit]", event.target), () => ai(false));
  });
  async function loadProvider() {
    try {
      const value = summary(await execute("classification_config"));
      if (!root.isConnected) return;
      backend = value.backend || "ds";
      $("[name=backend]", root).value = backend;
      $("[data-provider-state]", root).textContent = value.has_key
        ? "已配置密钥"
        : "需要配置密钥";
    } catch (error) {
      if (root.isConnected)
        $("[data-provider-state]", root).textContent = error.message;
    }
  }
  if (ctx.desktop?.connected) loadProvider();
  return { refresh: () => inspect("suggestions") };
}
