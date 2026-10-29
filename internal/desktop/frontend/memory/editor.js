import {
  $,
  all,
  h,
  input,
  select,
  dialog,
  confirm,
  execute,
  rows,
  summary,
  notify,
} from "../ui/core.js";
import { entry, kindNames } from "./model.js";

export function editor({ memory = null, parent = "", merge = [], onSaved }) {
  const isEdit = Boolean(memory?.id);
  const element = dialog({
    title: merge.length ? "合并记忆" : isEdit ? "编辑记忆" : "记录一条记忆",
    wide: true,
    submit: merge.length ? "合并并保存" : "保存",
    body:
      `<div class="form-grid">${input("title", "标题", memory?.title || "", { required: true })}${select("type", "类型", Object.entries(kindNames), String(memory?.kind || "context").toLowerCase())}${select(
        "importance",
        "存储区域",
        [
          ["important", "主区 · 重要记忆"],
          ["trivial", "日记 · 琐事"],
        ],
        memory?.importance || "important",
      )}${!isEdit || merge.length ? input("parent", "父节点 ID", memory?.parent_id || parent) : ""}</div>` +
      `<label>正文<textarea name="content" class="reading-text" required rows="10">${h(memory?.content || "")}</textarea></label><div class="actions"><button type="button" data-template>三段模板</button><button type="button" data-candidates>查找相近记忆</button><span class="hint">主区用于长期记忆，琐事按 Runtime 规则并入当日日记。</span></div>` +
      `${input("tags", "标签（逗号分隔）", Array.isArray(memory?.tags) ? memory.tags.join(",") : memory?.tags || "")}<div data-candidates-list></div>` +
      (!isEdit
        ? `<label class="checkbox-label"><input type="checkbox" name="force">明确新建，跳过判重（请先查看相近记忆）</label>`
        : "") +
      (merge.length
        ? `<p class="warning-text">将合并 ${merge.length} 条记忆。旧条目会被替换，请核对完整正文。未选中的子节点会按 Runtime 规则提升到仍存在的祖先，不会自动挂到新条目。</p>`
        : ""),
    onSubmit: async (values) => {
      const args = {
        title: values.title.trim(),
        content: values.content,
        type: values.type,
        importance: values.importance,
        tags: values.tags,
      };
      let envelope;
      if (isEdit && !merge.length) {
        const current = entry(await execute("memory_show", { id: memory.id }));
        if (current.updated_at !== memory.updated_at)
          throw new Error("记忆已在其他入口修改，请重新读取后再编辑。");
        delete args.type;
        args.kind = values.type;
        args.id = memory.id;
        envelope = await execute("memory_update", args);
      } else {
        args.parent = values.parent.trim();
        if (!args.parent) delete args.parent;
        if (values.force) args.force = true;
        if (merge.length) {
          if (
            !(await confirm(
              "确认合并",
              `将用当前正文合并 ${merge.length} 条记忆；请确认需要保留的内容都已写入。未选中的子节点会提升到仍存在的祖先。`,
              "确认合并",
            ))
          )
            return false;
          args.merge_ids = merge.join(",");
        }
        envelope = await execute("memory_create", args);
        const status = envelope?.status;
        if (status === "candidates" || !summary(envelope).id)
          throw new Error(
            "尚未写入。存在待确认候选，请查看相近记忆，选择挂载或明确新建。",
          );
      }
      notify(merge.length ? "合并已完成" : "记忆已保存");
      await onSaved?.(
        summary(envelope).id || memory?.id,
        merge.length
          ? null
          : { kind: isEdit ? "edit" : "create", before: memory },
      );
    },
  });
  $("[data-template]", element).addEventListener("click", () => {
    const field = $("[name=content]", element);
    if (!field.value.trim()) field.value = "【前因】\n\n【行为】\n\n【后果】";
    field.focus();
  });
  $("[data-candidates]", element).addEventListener("click", async (event) => {
    const button = event.currentTarget,
      content = $("[name=content]", element).value.trim();
    if (!content) {
      notify("请先填写正文", true);
      return;
    }
    button.disabled = true;
    const area = $("[data-candidates-list]", element);
    area.innerHTML = '<p class="hint">正在查找相近记忆…</p>';
    try {
      const result = await execute("memory_candidates", { content });
      const report = result.details || {};
      const source = Array.isArray(result.details)
        ? result.details
        : [...(report.merge || []), ...(report.parent || [])];
      const candidates = source
        .map((value) => {
          try {
            return entry(value);
          } catch {
            return null;
          }
        })
        .filter(
          (item, index, items) =>
            item && items.findIndex((other) => other?.id === item.id) === index,
        );
      if (!candidates.length) {
        area.innerHTML =
          '<p class="hint">没有返回可直接挂载的候选。保存时仍以 Runtime 的判重结果为准。</p>';
        return;
      }
      area.innerHTML = `<div class="candidate-list">${candidates.map((row) => `<div><div><strong>${h(row.title)}</strong><small>${h(row.id)}</small></div><button type="button" data-parent="${h(row.id)}">挂到此条</button><button type="button" data-read="${h(row.id)}">查看</button></div>`).join("")}</div>`;
      all("[data-parent]", area).forEach((control) =>
        control.addEventListener("click", () => {
          const parentField = $("[name=parent]", element);
          if (parentField) parentField.value = control.dataset.parent;
          else notify("调整父节点请使用记忆详情中的“移动”操作。");
        }),
      );
      all("[data-read]", area).forEach((control) =>
        control.addEventListener("click", async () => {
          try {
            const item = entry(
              await execute("memory_show", { id: control.dataset.read }),
            );
            dialog({
              title: item.title,
              body: `<div class="reading-content">${h(item.content)}</div>`,
              submit: "关闭",
              onSubmit: () => {},
              wide: true,
            });
          } catch (error) {
            notify(error.message, true);
          }
        }),
      );
    } catch (error) {
      area.textContent = error.message;
      area.classList.add("form-error");
    } finally {
      button.disabled = false;
    }
  });
  return element;
}
