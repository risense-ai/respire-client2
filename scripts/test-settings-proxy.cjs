const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const root = path.resolve(__dirname, '..');
const frontend = path.join(root, 'internal/desktop/frontend');
const read = (file) => fs.readFileSync(path.join(frontend, file), 'utf8');

// The settings page renders the proxy snapshot the desktop bridge reports
// (/api/state → proxy). Execute the production row builder, not a copy.
const proxy = new Function(
  read('ui/proxy.js').replace(/^export /gm, '') + '\nreturn {proxyRows};',
)();

const cases = [
  {
    snapshot: { mode: 'system', source: 'system', http: 'http://127.0.0.1:7897', https: 'http://127.0.0.1:7897', noProxy: 'localhost,127.0.0.1' },
    expect: [['代理来源', '系统代理'], ['HTTP 代理', 'http://127.0.0.1:7897'], ['绕过列表', 'localhost,127.0.0.1']],
  },
  {
    snapshot: { mode: 'system', source: 'environment', https: 'http://env:3128', noProxy: 'corp.example' },
    expect: [['代理来源', '代理环境变量'], ['HTTPS 代理', 'http://env:3128'], ['绕过列表', 'corp.example']],
  },
  {
    snapshot: { mode: 'direct', source: 'off' },
    expect: [['代理来源', '代理已关闭']],
    count: 1,
  },
  {
    snapshot: { mode: 'system', source: 'direct', note: '系统代理使用 PAC 自动配置，客户端无法解析；已直连' },
    expect: [['代理来源', '未配置代理'], ['代理说明', '系统代理使用 PAC 自动配置，客户端无法解析；已直连']],
  },
  {
    snapshot: {},
    expect: [['代理来源', '正在读取代理']],
    count: 1,
  },
];

for (const item of cases) {
  const rows = proxy.proxyRows(item.snapshot);
  for (const [label, value] of item.expect) {
    assert.ok(rows.some(([k, v]) => k === label && v === value), `missing ${label}: ${value} in ${JSON.stringify(rows)}`);
  }
  if (item.count) assert.equal(rows.length, item.count, `row count for ${JSON.stringify(item.snapshot)}`);
}

// The preference control and its status container must stay on the page.
const settings = read('pages/settings.js');
assert.match(settings, /data-pref="proxyMode"/, 'proxy mode selector missing');
assert.match(settings, /data-proxy/, 'proxy status container missing');
assert.match(settings, /proxyRows\(/, 'proxy rows must be rendered from the shared builder');

// Every label the panel renders must exist in the Chinese→English table, or
// the English UI would keep Chinese proxy rows.
const i18n = read('ui/i18n.js');
const labels = new Set([...cases.flatMap((item) => item.expect.map(([label]) => label)), '代理', '直连']);
for (const label of labels) {
  assert.ok(i18n.includes(`["${label}",`), `missing i18n entry for ${label}`);
}

console.log(`settings proxy panel: ${cases.length} cases passed`);
