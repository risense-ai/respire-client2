import { $, all, notify, confirm, execute } from "../ui/core.js";
import { entry, inventory } from "./model.js";

export function memoryInteractions(
  root,
  {
    getRecords,
    getSelected,
    select,
    newMemory,
    edit,
    remove,
    undo,
    search,
    moved,
  },
) {
  const keydown = (event) => {
    if (document.querySelector("dialog[open]")) return;
    const editing = event.target.closest(
      "input,textarea,select,[contenteditable=true]",
    );
    if (editing) return;
    const command = event.ctrlKey || event.metaKey;
    const consume = (fn) => {
      event.preventDefault();
      Promise.resolve()
        .then(fn)
        .catch((e) => notify(e.message, true));
    };
    if (command && event.key.toLowerCase() === "k") return consume(search);
    if (command && event.key.toLowerCase() === "n")
      return consume(() => newMemory(event.shiftKey));
    if (command && event.key.toLowerCase() === "z" && !event.shiftKey)
      return consume(undo);
    if (command && event.shiftKey && event.key.toLowerCase() === "c")
      return consume(async () => {
        const id = getSelected();
        if (id) {
          const link = new URL(location.href);
          link.hash = "memories?node=" + encodeURIComponent(id);
          await navigator.clipboard.writeText(link.href);
          notify("节点链接已复制");
        }
      });
    if (event.key === "F2") return consume(edit);
    if (event.key === "Backspace" || event.key === "Delete")
      return consume(remove);
    const controls = all(".memory-title", root),
      index = controls.findIndex((b) => b.dataset.select === getSelected());
    if (event.key === "ArrowDown" || event.key === "ArrowUp")
      return consume(() => {
        const i = Math.max(
          0,
          Math.min(
            controls.length - 1,
            index + (event.key === "ArrowDown" ? 1 : -1),
          ),
        );
        const c = controls[i];
        if (c) {
          c.focus();
          select(c.dataset.select);
        }
      });
    if (event.key === "ArrowRight" || event.key === "ArrowLeft")
      return consume(() => {
        const row = $(`.memory-row.selected`, root),
          toggle = row?.querySelector("[data-expand]");
        if (
          toggle &&
          !toggle.disabled &&
          row.getAttribute("aria-expanded") !==
            (event.key === "ArrowRight" ? "true" : "false")
        )
          toggle.click();
        else if (event.key === "ArrowLeft") {
          const p = getRecords().find((r) => r.id === getSelected())?.parent_id;
          if (p) select(p);
        }
      });
  };
  document.addEventListener("keydown", keydown);
  root.addEventListener("dragstart", (event) => {
    const row = event.target.closest(".memory-row");
    if (!row) return;
    event.dataTransfer.setData("application/x-respire-memory", row.dataset.id);
    event.dataTransfer.effectAllowed = "move";
  });
  root.addEventListener("dragover", (event) => {
    if (
      event.target.closest(".memory-row") &&
      [...event.dataTransfer.types].includes("application/x-respire-memory")
    ) {
      event.preventDefault();
      event.dataTransfer.dropEffect = "move";
    }
  });
  root.addEventListener("drop", async (event) => {
    const target = event.target.closest(".memory-row"),
      id = event.dataTransfer.getData("application/x-respire-memory");
    if (!target || !id) return;
    event.preventDefault();
    try {
      const parent = target.dataset.id,
        tree = inventory(getRecords()),
        seen = new Set();
      let cursor = parent;
      while (cursor && !seen.has(cursor)) {
        if (cursor === id) throw new Error("不能移动到自身或自己的子节点");
        seen.add(cursor);
        cursor = tree.byId.get(cursor)?.parent_id;
      }
      const before = entry(await execute("memory_show", { id }));
      if (before.parent_id === parent) return;
      if (
        !(await confirm(
          "移动记忆",
          `将「${before.title}」移动到「${tree.byId.get(parent)?.title || parent}」之下。`,
          "移动",
        ))
      )
        return;
      await execute("memory_attach", { id, parent });
      await moved(before);
    } catch (error) {
      notify(error.message, true);
    }
  });
  return () => document.removeEventListener("keydown", keydown);
}
