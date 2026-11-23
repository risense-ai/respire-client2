import {
  $,
  raw,
  execute,
  action,
  confirm,
  showText,
  notify,
  dialog,
  input,
} from "../ui/core.js";
import { recordUndo } from "./undo.js";
import { inspectGrants } from "../pages/grants.js";

export function memoryTools(reader, memory, { changed, requireCurrent, keepDialog }) {
  const area = document.createElement("div");
  area.className = "panel-body actions";
  area.innerHTML =
    '<button data-tool="promote">提升一级</button><button data-tool="access">查看访问授权</button><button data-tool="book">成书材料</button><button data-tool="portrait">画像材料</button><button data-tool="material">子树安装材料</button><button data-tool="purge" class="danger">永久清除密文</button>';
  reader.append(area);
  area.onclick = (event) => {
    const button = event.target.closest("[data-tool]");
    if (!button) return;
    action(button, async () => {
      requireCurrent();
      const kind = button.dataset.tool;
      if (kind === "access") return inspectGrants(memory, requireCurrent, keepDialog);
      if (kind === "purge") {
        keepDialog(dialog({
          title: "永久清除记忆密文",
          body:
            '<p class="warning-text">此操作不可撤销。会清除当前版本的记忆密文；Runtime 会按同步与冲突规则决定是否保留待处理修订。</p>' +
            input("id", "输入完整记忆 ID 以确认", "", { required: true }),
          submit: "永久清除",
          onSubmit: async (value) => {
            requireCurrent();
            if (value.id.trim() !== memory.id)
              throw Error("ID 不匹配，未执行清除");
            await execute("memory_purge", { id: memory.id });
            notify("密文清除请求已完成");
            await changed();
          },
        }));
        return;
      }
      if (kind === "promote") {
        if (
          !(await confirm(
            "提升记忆",
            "将此记忆连同子节点提升到上一级祖先之下。",
            "提升",
            keepDialog,
          ))
        )
          return;
        requireCurrent();
        await execute("memory_promote", { id: memory.id });
        notify("提升请求已完成");
        if (!requireCurrent.current()) return;
        await recordUndo({ kind: "move", id: memory.id, before: memory }, requireCurrent.current);
        await changed();
        return;
      }
      const name = {
        book: "book_material",
        portrait: "portrait_material",
        material: "memory_material",
      }[kind];
      const result = await execute(
        name,
        kind === "portrait" ? { limit: 40 } : { root: memory.id },
      );
      requireCurrent();
      keepDialog(showText(
        { book: "成书材料", portrait: "画像材料", material: "子树安装材料" }[
          kind
        ],
        raw(result.details || result.summary),
      ));
    });
  };
}
