// Pure presentation of the proxy snapshot returned by the desktop bridge
// (/api/state → proxy). Kept free of DOM and i18n imports so the settings
// page and its Node test can share exactly the same row builder.
//
// Labels are deliberately specific ("代理来源" rather than "来源") so the
// shared translation table cannot accidentally rewrite unrelated UI text.
const sourceNames = {
  environment: "代理环境变量",
  system: "系统代理",
  direct: "未配置代理",
  off: "代理已关闭",
};

export function proxyRows(proxy) {
  const info = proxy || {};
  const rows = [["代理来源", sourceNames[info.source] || "正在读取代理"]];
  if (info.http) rows.push(["HTTP 代理", info.http]);
  if (info.https) rows.push(["HTTPS 代理", info.https]);
  if (info.noProxy) rows.push(["绕过列表", info.noProxy]);
  if (info.note) rows.push(["代理说明", info.note]);
  return rows;
}
