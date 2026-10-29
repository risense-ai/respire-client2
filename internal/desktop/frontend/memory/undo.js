import { execute, raw, summary, notify } from "../ui/core.js";

let lastUndo = null;
let running = false;
const stamp = async () => {
  const status = summary(await execute("status"));
  return raw([status.data_dir, summary(await execute("memory_revision"))]);
};
export async function recordUndo({ kind, id, before }) {
  try {
    lastUndo = { kind, id, before, revision: await stamp() };
  } catch (error) {
    notify(
      `操作已保存，但未能记录撤销：${error.message}。请刷新核对，不要重复保存。`,
      true,
    );
  }
}
export async function undoLast() {
  if (running) throw new Error("撤销正在执行");
  const item = lastUndo;
  if (!item) throw new Error("没有可撤销的操作");
  running = true;
  try {
    if ((await stamp()) !== item.revision)
      throw new Error(
        "记忆库已在其他操作中改变，此次撤销已失效。请读取当前数据后手动调整。",
      );
    lastUndo = null;
    // Consume before submitting: an unknown write outcome must never be replayed.
    if (item.kind === "create") await execute("memory_delete", { id: item.id });
    else if (item.kind === "delete")
      await execute("memory_restore", { id: item.id });
    else if (item.kind === "move")
      await execute("organize_resort_apply", {
        spec: raw({
          ops: [{ id: item.id, parent: item.before.parent_id || "" }],
        }),
      });
    else {
      const old = item.before;
      await execute("memory_update", {
        id: item.id,
        title: old.title,
        content: old.content,
        kind: old.kind.toLowerCase(),
        tags: (old.tags || []).join(","),
        importance: old.importance,
      });
    }
    // Revision checks are not an atomic CAS; do not revalidate older operations
    // after a write that could have raced another client.
    return item.kind === "create" ? "" : item.id;
  } finally {
    running = false;
  }
}
