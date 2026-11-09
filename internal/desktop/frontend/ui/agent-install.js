// Shared install-mode rules for the two install surfaces (onboarding wizard
// and the injection page). One wording and qualification source keeps the
// goal lists, statuses and receipts from drifting apart.
export const AGENT_STATE_NAMES = {
  fresh: "已注入且最新",
  stale: "已注入但过期",
  none: "未注入",
  absent: "应用未检测到",
};

// Scope support: prompt targets need their own prompt file; plugin targets
// need a matching Respire plugin. Prompt-only copies of plugin-only hosts
// are excluded here, never by the backend.
export function scopeSupports(scope, target) {
  if (scope === "prompt") return target.mode !== "plugin";
  if (scope === "plugin") return Boolean(target.plugin);
  return false;
}

// "Detected" gates the default goal list on both surfaces; the install
// page's 显示其余 is the only explicit bypass, and it still keeps the
// scope filter above.
export function targetDetected(target) {
  return target.likelyInstalled === true;
}

export function targetsForScope(
  targets,
  scope,
  { detectedOnly = false } = {},
) {
  return (targets || []).filter(
    (target) =>
      scopeSupports(scope, target) && (!detectedOnly || targetDetected(target)),
  );
}

// Status follows the active scope: a dual-capability target reports its
// prompt state on the prompt side and its plugin state on the plugin side.
export function agentStatus(target, scope = "prompt") {
  if (scope === "plugin" ? target.plugin : target.mode === "plugin")
    return target.plugin?.source ? "插件源目录已就绪" : "插件未安装";
  return AGENT_STATE_NAMES[target.state] || target.state || "未知";
}

// agentResultMessage mirrors inject-set receipts so the wizard and the
// install page report the same per-target outcome wording.
export function agentResultMessage(result) {
  if (result.message) return result.message;
  const parts = [];
  if (result.changed !== undefined)
    parts.push(
      result.changed
        ? result.enabled
          ? "提示词已写入"
          : "注入内容已移除"
        : "提示词已是最新，未改动",
    );
  if (result.plugin)
    parts.push(
      result.plugin.ok
        ? `插件：${result.plugin.message}`
        : `插件失败：${result.plugin.message}`,
    );
  return parts.join(" · ") || "已完成";
}
