export function entry(value) {
  const candidate = value?.details?.entry || value?.entry || value;
  if (!candidate || typeof candidate.id !== "string")
    throw new Error("Runtime 未返回有效记忆");
  return candidate;
}
export function list(envelope) {
  if (!Array.isArray(envelope?.details))
    throw new Error("Runtime 记忆列表格式不受支持");
  return envelope.details.map((value) => entry(value));
}
export function inventory(records) {
  const byId = new Map(records.map((row) => [row.id, row]));
  const children = new Map();
  for (const row of records) {
    const parent = byId.has(row.parent_id) ? row.parent_id : "";
    if (!children.has(parent)) children.set(parent, []);
    children.get(parent).push(row.id);
  }
  return { byId, children };
}
export function flatten(records, expanded, query = "") {
  const tree = inventory(records),
    visible = [],
    visited = new Set();
  const matching = new Set();
  const needle = query.trim().toLocaleLowerCase();
  const reachable = new Set(),
    pending = [...(tree.children.get("") || [])];
  while (pending.length) {
    const id = pending.pop();
    if (reachable.has(id)) continue;
    reachable.add(id);
    pending.push(...(tree.children.get(id) || []));
  }
  if (needle)
    for (const row of records) {
      if (!`${row.title || ""} ${row.id}`.toLocaleLowerCase().includes(needle))
        continue;
      let current = row;
      const path = new Set();
      while (current && !path.has(current.id)) {
        path.add(current.id);
        matching.add(current.id);
        current = tree.byId.get(current.parent_id);
      }
    }
  function walk(id, depth) {
    const pending = [{ id, depth }];
    while (pending.length) {
      const current = pending.pop();
      if (visited.has(current.id)) continue;
      visited.add(current.id);
      if (needle && !matching.has(current.id)) continue;
      const row = tree.byId.get(current.id),
        kids = tree.children.get(current.id) || [];
      visible.push({
        row,
        depth: current.depth,
        kids: kids.length,
        expanded: needle || expanded.has(current.id),
      });
      if (needle || expanded.has(current.id))
        for (let i = kids.length - 1; i >= 0; i--)
          pending.push({ id: kids[i], depth: current.depth + 1 });
    }
  }
  (tree.children.get("") || []).forEach((id) => walk(id, 0));
  // Cyclic or incomplete inputs stay visible; only Runtime may repair their links.
  for (const row of records)
    if (
      !reachable.has(row.id) &&
      !visited.has(row.id) &&
      (!needle || matching.has(row.id))
    )
      walk(row.id, 0);
  return visible;
}
export const kindNames = {
  knowledge: "知识",
  context: "上下文",
  decision: "决定",
  preference: "偏好",
  task: "任务",
  emotion: "情绪",
  time: "时间",
  skill: "技能",
};
export function report(envelope) {
  return {
    ...(envelope?.summary || {}),
    ...(envelope?.details && !Array.isArray(envelope.details)
      ? envelope.details
      : {}),
  };
}
