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
