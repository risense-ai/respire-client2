const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const root = path.resolve(__dirname, '..');
const read = file => fs.readFileSync(path.join(root, 'internal/desktop/frontend', file), 'utf8');
const source = read('client.js');
const section = source.slice(source.indexOf('function setRefreshText('), source.indexOf('\nall("nav [data-page]")', source.indexOf('async function refresh()')));
assert.match(section, /async function refresh\(\)/);
assert.equal((section.match(/setRefreshText\(\$\(/g) || []).length, 6);
// Execute the production helper and refresh body, not a rewritten algorithm.
// This deliberately models text-node replacement, not a browser or Windows UIA.
class Element {
  constructor() { this.node = { nodeValue: '' }; this.writes = 0; this.classList = { toggle() {} }; }
  get textContent() { return this.node.nodeValue; }
  set textContent(value) { this.writes++; this.node = { nodeValue: value == null ? '' : `${value}` }; }
}
const elements = Object.fromEntries(['connection-dot', 'connection-label', 'client-version', 'client-platform', 'nav-memory-count', 'status-summary', 'sync-summary'].map(id => [`#${id}`, new Element()]));
const document = {
  documentElement: {}, body: { querySelectorAll: () => [] },
  createTreeWalker() {
    const nodes = Object.values(elements).map(e => e.node); let index = 0;
    return { nextNode() { this.currentNode = nodes[index++]; return !!this.currentNode; } };
  },
};
const i18n = new Function('document', 'NodeFilter', 'localStorage', read('ui/i18n.js').replace(/^export /gm, '') + '\nreturn {t, currentLanguage, setLanguage, applyTranslations};')(document, { SHOW_TEXT: 4 }, { setItem() {} });
const core = new Function(read('ui/core.js').replace(/^export /gm, '') + '\nreturn {number};')();
let desktop, status, failure, stateFailure, stateWait;
const events = [], notices = [];
const ctx = {};
const bindings = {
  ctx, $: selector => elements[selector], ...i18n, ...core,
  async api(name) {
    events.push(name);
    if (name === 'state') { if (stateFailure) throw Error('state failed'); if (stateWait) await stateWait; return desktop; }
    assert.equal(name, 'runtime-status');
    if (failure) throw Error('status failed'); return status;
  },
  appearance() { events.push('appearance'); },
  view: { update() { events.push('view.update'); } },
  refreshNavCounts() { events.push('nav'); },
  notify(...args) { notices.push(args); },
};
const { refresh, setRefreshText } = new Function(...Object.keys(bindings), 'let refreshing = false;\n' + section + '\nreturn {refresh, setRefreshText};')(...Object.values(bindings));
const fields = Object.values(elements).slice(1);
const nodes = () => fields.map(e => e.node);
const writes = () => fields.map(e => e.writes);
let count = 0;
async function test(name, fn) { await fn(); console.log(`PASS ${++count}: ${name}`); }
(async () => {
  desktop = { connected: true, version: '2.0.0-dev.test', platform: 'windows/amd64', health: { bin: 'fixture' } };
  status = { summary: { local_alive: 12, workspace: 'normal', sync_live: { phase: 'idle', pending: 0, conflicts: 0 } } };
  await test('initial English values and existing call order', async () => {
    await refresh();
    assert.deepEqual(events, ['state', 'appearance', 'runtime-status', 'view.update', 'nav']);
    assert.equal(elements['#connection-label'].textContent, 'Runtime connected');
    assert.equal(elements['#client-platform'].textContent, 'windows/amd64');
    assert.equal(elements['#nav-memory-count'].textContent, '12');
    assert.equal(elements['#status-summary'].textContent, '12 memories · normal · Runtime fixture');
    assert.equal(elements['#sync-summary'].textContent, 'Sync idle · pending 0 · conflicts 0');
  });
  await test('100 repeated polls preserve nodes without suppressing view updates', async () => {
    const before = nodes(), counts = writes(); events.length = 0;
    for (let n = 0; n < 100; n++) { await refresh(); i18n.applyTranslations(); }
    assert.deepEqual(nodes(), before); fields.forEach((e, n) => assert.equal(e.node, before[n]));
    assert.deepEqual(writes(), counts);
    assert.equal(events.filter(e => e === 'view.update').length, 100);
  });
  await test('real status count version platform and sync changes propagate', async () => {
    desktop.version = '2.0.0-dev.next'; desktop.platform = 'linux/amd64';
    status.summary.local_alive = 13; status.summary.workspace = 'readonly'; status.summary.sync_live.pending = 2;
    await refresh();
    assert.equal(elements['#client-version'].textContent, 'Client 2.0.0-dev.next');
    assert.equal(elements['#client-platform'].textContent, 'linux/amd64');
    assert.equal(elements['#nav-memory-count'].textContent, '13');
    assert.equal(elements['#status-summary'].textContent, '13 memories · readonly · Runtime fixture');
    assert.equal(elements['#sync-summary'].textContent, 'Sync idle · pending 2 · conflicts 0');
  });
  await test('Chinese language change and repeated poll', async () => {
    i18n.setLanguage('zh'); await refresh();
    assert.equal(elements['#connection-label'].textContent, 'Runtime 已连接');
    assert.equal(elements['#client-version'].textContent, '客户端 2.0.0-dev.next');
    assert.match(elements['#status-summary'].textContent, /^13 条记忆/);
    assert.equal(elements['#sync-summary'].textContent, '同步 idle · 待处理 2 · 冲突 0');
    const before = writes(); await refresh(); assert.deepEqual(writes(), before);
  });
  await test('English language change restores dynamic labels', async () => {
    i18n.setLanguage('en'); await refresh();
    assert.equal(elements['#client-version'].textContent, 'Client 2.0.0-dev.next');
    assert.match(elements['#status-summary'].textContent, /^13 memories/);
  });
  await test('external DOM drift repaired despite unchanged backend', async () => {
    const expected = fields.map(e => e.textContent);
    fields.forEach(e => { e.textContent = 'external drift'; }); await refresh();
    assert.deepEqual(fields.map(e => e.textContent), expected);
  });
  await test('DOM coercion and nullish platform values remain equivalent', async () => {
    for (const value of [null, undefined, 0, 7, false, true, '', ['x'], { toString: () => 'fixture' }]) {
      const e = new Element(); setRefreshText(e, value);
      assert.equal(e.textContent, value == null ? '' : `${value}`);
      const before = e.writes; setRefreshText(e, value); assert.equal(e.writes, before);
    }
    for (const value of [null, undefined, 0, false]) { desktop.platform = value; await refresh(); assert.equal(elements['#client-platform'].textContent, value == null ? '' : `${value}`); }
    assert.throws(() => setRefreshText(new Element(), Symbol('invalid DOMString')), TypeError);
  });
  await test('missing element still throws instead of hiding selector failures', async () => {
    assert.throws(() => setRefreshText(null, 'value'), TypeError);
  });
  await test('status error retains existing observer translation behavior', async () => {
    failure = true; await refresh(); assert.equal(ctx.statusError, 'status failed');
    assert.equal(elements['#sync-summary'].textContent, '状态读取失败');
    i18n.applyTranslations(); assert.equal(elements['#sync-summary'].textContent, 'Status read failed');
    // Known existing English-error rewrite is intentionally not changed here.
    const before = elements['#sync-summary'].writes; await refresh();
    assert.equal(elements['#sync-summary'].writes, before + 1);
    i18n.applyTranslations(); assert.equal(elements['#sync-summary'].textContent, 'Status read failed');
    failure = false; await refresh(); assert.equal(ctx.statusError, '');
  });
  await test('disconnect skips runtime request and reconnect updates', async () => {
    desktop.connected = false; events.length = 0; await refresh();
    assert.deepEqual(events, ['state', 'appearance', 'view.update', 'nav']);
    assert.equal(elements['#connection-label'].textContent, 'Runtime disconnected');
    assert.equal(elements['#status-summary'].textContent, 'Runtime disconnected');
    desktop.connected = true; await refresh(); assert.equal(elements['#connection-label'].textContent, 'Runtime connected');
  });
  await test('state failure notification and next refresh recovery', async () => {
    stateFailure = true; await refresh(); assert.deepEqual(notices.pop(), ['state failed', true]);
    stateFailure = false; events.length = 0; await refresh(); assert.ok(events.includes('view.update'));
  });
  await test('overlapping refresh still guarded', async () => {
    let release; stateWait = new Promise(resolve => { release = resolve; }); events.length = 0;
    const pending = refresh(); await refresh(); assert.deepEqual(events, ['state']);
    release(); await pending; stateWait = null;
    assert.deepEqual(events, ['state', 'appearance', 'runtime-status', 'view.update', 'nav']);
  });
  await test('core configuration notices require the original applied receipt', async () => {
    const models = read('pages/models.js');
    const start = models.indexOf('  function fusionApplicationNotice(');
    const notice = new Function(models.slice(start, models.indexOf('\n  // Polling', start)) + '\nreturn fusionApplicationNotice;')();
    for (const state of [undefined, null, {state:'unknown'}, {state:'applied'}, {state:'pending'}]) {
      const value = notice(state);
      assert.equal(value.warning, true);
      assert.doesNotMatch(value.text, /已保存|核心已应用/);
    }
    assert.match(notice({state:'pending', reason:'runtime_not_running'}).text, /核心待应用/);
    assert.equal(notice({state:'applied', applies:'next_query', revision:2,
      scope:{account_id:'synthetic', instance_id:'original', generation:1}, effective:{recall_fusion:{}}}).warning, false);
  });
  await test('core configuration Unknown queries the original desktop request without another setter', async () => {
    const models = read('pages/models.js');
    const start = models.indexOf('  // Keep an uncertain operation');
    const listeners = {}, calls = [], notices = [];
    const fusion = {runtime:{effective:{}}, requestId:null};
    let pending, queriedState = 'unconfirmed';
    const save = {disabled:false};
    const nodes = {};
    const $ = selector => selector === '[type=submit]' ? save : (nodes[selector] ||= {
      addEventListener:(_, callback) => {listeners[selector] = callback;},
    });
    const renderStart = models.indexOf('  function renderFusion()');
    const renderFusion = new Function('$', 'root', 'fusion', 'FUSION_NUMERIC',
      models.slice(renderStart, models.indexOf('  // Collect + validate', renderStart)) + '\nreturn renderFusion;')($, {}, fusion, []);
    const coreSource = read('ui/core.js');
    const productionAction = new Function('notify', coreSource.slice(coreSource.indexOf('export async function action('),
      coreSource.indexOf('export function panel(')).replace(/^export /gm, '') + '\nreturn action;')(
      (...args) => notices.push(args));
    const bindings = {root:{}, fusion, summary:envelope => envelope.summary,
      $, action:(button, fn) => (pending = productionAction(button, fn)),
      fusionDraft:() => ({fusion:'legacy'}), renderFusion,
      notify:(...args) => notices.push(args),
      async execute(name) {
        calls.push(name);
        const error = Error('unknown'); error.requestId = 'desktop-original';
        error.operation = {state:'unconfirmed'}; throw error;
      },
      async api(name, args) {
        calls.push([name, args]);
        return {state:queriedState, envelope:{summary:{core_application:{state:'applied'}}}};
      },
      fusionApplicationNotice:() => ({text:'original receipt', warning:false}),
    };
    new Function(...Object.keys(bindings), models.slice(start, models.indexOf('  // Restoring defaults', start)))(...Object.values(bindings));
    const submit = {preventDefault() {}, target:{}};
    const saving = listeners['[data-fusion-form]'](submit);
    assert.equal(save.disabled, true);
    const duplicate = listeners['[data-fusion-form]'](submit);
    assert.equal(save.disabled, true);
    await duplicate;
    await saving;
    await pending;
    assert.equal(fusion.requestId, 'desktop-original');
    assert.equal(save.disabled, true);
    await listeners['[data-fusion-form]'](submit);
    assert.equal(save.disabled, true);
    save.disabled = false; // Even a direct submission cannot bypass the original-request guard.
    await listeners['[data-fusion-form]'](submit);
    assert.equal(save.disabled, true);
    assert.match(notices.at(-1)[0], /查询原请求/);
    listeners['[data-fusion-query]']({currentTarget:{}}); await pending;
    assert.equal(fusion.requestId, 'desktop-original');
    assert.equal(save.disabled, true);
    queriedState = 'done';
    listeners['[data-fusion-query]']({currentTarget:{}}); await pending;
    assert.equal(fusion.requestId, null);
    assert.equal(save.disabled, false);
    assert.deepEqual(calls, ['agent_set', ['operation-recover', {id:'desktop-original'}], ['operation-recover', {id:'desktop-original'}]]);
  });
  await test('memory workspace keeps connection and status failures distinct from a confirmed lock', () => {
    const workspace = fs.readFileSync('internal/desktop/frontend/memory/workspace.js', 'utf8');
    const start = workspace.indexOf('  function unavailableMessage()');
    const end = workspace.indexOf('  async function load()', start);
    assert.ok(start >= 0 && end > start);
    const ctx = {desktop: {connected: false}, status: null, statusError: ''};
    const reader = {innerHTML: ''}, counter = {textContent: ''};
    const showUnavailable = new Function('ctx', 'reader', 'count', '$', 't', 'h',
      'const checked = new Set(), expanded = new Set(); function renderTree() {} let records = [], selected, searchCaption, loaded = false, revision = "", renderedEntry, selectedEntry, selectionVersion = 0;\n' +
      workspace.slice(start, end) +
      '\nreturn (alreadyLoaded = false) => { loaded = alreadyLoaded; return showUnavailable(); };'
    )(ctx, reader, counter, () => ({addEventListener() {}}), text => text, text => text);
    assert.equal(showUnavailable(), true);
    assert.equal(counter.textContent, '等待 Runtime 连接');
    assert.doesNotMatch(reader.innerHTML, /data-accounts/);

    ctx.desktop.connected = true;
    assert.equal(showUnavailable(), true);
    assert.equal(counter.textContent, '正在读取记忆目录…');
    reader.innerHTML = 'previously loaded entry';
    ctx.statusError = 'status failed';
    assert.equal(showUnavailable(true), true);
    assert.equal(counter.textContent, '暂时无法读取记忆状态，请稍后刷新。');
    assert.equal(reader.innerHTML, 'previously loaded entry');

    ctx.status = {summary: {unlocked: false}};
    assert.equal(showUnavailable(true), true);
    assert.equal(counter.textContent, '需要解锁记忆库');
    assert.match(reader.innerHTML, /data-accounts/);
    ctx.status = {summary: {unlocked: true}};
    assert.equal(showUnavailable(true), false);
  });
  // Reuse the real workspace module for memory readback and refresh rules.
  const workspace = read('memory/workspace.js');
  const segment = (from, to) => workspace.slice(workspace.indexOf(from), workspace.indexOf(to));
  const build = new Function('ctx', 'execute', `
  const root = { isConnected: true }, checked = new Set(), expanded = new Set();
  const reader = { innerHTML: 'old reader', querySelector: () => true };
  const tree = { innerHTML: 'old tree' }, count = { textContent: '' };
  let loaded = true, revision = 'loaded', busy = false, limit = 4, metadataCap = true;
  let records = [{id:'old'}], selected = '', selectedEntry, selectionVersion = 0;
  let renderedEntry = {id:'old', updated_at:'before'}, searchCaption;
  function renderTree() { tree.innerHTML = records.map(row => row.id).join(','); }
  const h = String, t = String, number = String, list = value => value, entry = value => value;
  const inventory = rows => ({byId: new Map(rows.map(row => [row.id, row]))});
  const $ = () => ({addEventListener() {}});
  ${segment('  function unavailableMessage()', '  // Body records')}
  ${segment('  async function choose(', '  async function search()')}
  return {load, choose, showUnavailable, tree, reader, count, ids: () => records.map(row => row.id)};
  `);
  const fixtureContext = () => ({desktop: {connected:true}, status:{summary:{unlocked:true, local_alive:10}}});
  await test('failed later memory catalog page preserves the reader and prior catalog', async () => {
    const ctx = fixtureContext();
    const failure = build(ctx, async (name, {limit}) => {
      assert.equal(name, 'memory_list');
      if (limit === 4) return [{id:'new1'}, {id:'new2'}];
      throw new Error('transport unavailable');
    });
    await failure.load();
    assert.deepEqual(failure.ids(), ['old']);
    assert.equal(failure.tree.innerHTML, 'old tree');
    assert.equal(failure.reader.innerHTML, 'old reader');
    assert.match(failure.count.textContent, /稍后重试/);
  });
  await test('confirmed lock invalidates an in-flight memory catalog response', async () => {
    const ctx = fixtureContext();
    let settle;
    const pending = build(ctx, () => new Promise(resolve => { settle = resolve; }));
    const loading = pending.load();
    ctx.status.summary.unlocked = false;
    assert.equal(pending.showUnavailable(), true);
    assert.deepEqual(pending.ids(), []);
    assert.match(pending.reader.innerHTML, /data-accounts/);
    ctx.status.summary.unlocked = true;
    settle([{id:'stale'}]);
    await loading;
    assert.deepEqual(pending.ids(), []);
  });
  await test('reader preserves content on transport failure but renders definite rejection', async () => {
    const ctx = fixtureContext();
    let error = new Error('transport unavailable');
    const reading = build(ctx, async () => { throw error; });
    await reading.choose('old', false);
    assert.equal(reading.reader.innerHTML, 'old reader');
    error = Object.assign(new Error('not found'), {envelope:{summary:{reason:'not_found'}}});
    await reading.choose('old', false);
    assert.match(reading.reader.innerHTML, /not found/);
  });
  console.log(`${count} focused refresh tests passed; browser/native UIA not exercised.`);
})().catch(error => { console.error(error); process.exitCode = 1; });
