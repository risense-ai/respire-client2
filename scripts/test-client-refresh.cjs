const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const root = path.resolve(__dirname, '..');
const read = file => fs.readFileSync(path.join(root, 'internal/desktop/frontend', file), 'utf8');
const source = read('client.js');
const section = source.slice(source.indexOf('function setRefreshText('), source.indexOf('\nall("nav [data-page]")', source.indexOf('async function refresh()')));
assert.match(section, /async function refreshStatus\(initial\)/);
// Execute the production helper and refresh body, not a rewritten algorithm.
// This deliberately models text-node replacement, not a browser or Windows UIA.
class Element {
  constructor() { this.node = { nodeValue: '' }; this.writes = 0; this.classList = { toggle() {} }; }
  get textContent() { return this.node.nodeValue; }
  set textContent(value) { this.writes++; this.node = { nodeValue: value == null ? '' : `${value}` }; }
}
const elements = Object.fromEntries(['connection-dot', 'connection-label', 'client-version', 'client-platform', 'nav-memory-count', 'status-summary', 'sync-summary', 'nav-diary-count'].map(id => [`#${id}`, new Element()]));
const document = {
  documentElement: {}, body: { querySelectorAll: () => [] },
  createTreeWalker() {
    const nodes = Object.values(elements).map(e => e.node); let index = 0;
    return { nextNode() { this.currentNode = nodes[index++]; return !!this.currentNode; } };
  },
};
const i18n = new Function('document', 'NodeFilter', 'localStorage', read('ui/i18n.js').replace(/^export /gm, '') + '\nreturn {t, currentLanguage, setLanguage, applyTranslations};')(document, { SHOW_TEXT: 4 }, { setItem() {} });
const core = new Function(read('ui/core.js').replace(/^export /gm, '') + '\nreturn {number};')();
let desktop, status, failure, stateFailure, stateWait, statusWait;
const events = [], notices = [];
const ctx = { statusScope: null, statusPending: true };
let published;
const bindings = {
  ctx, $: selector => elements[selector], ...i18n, ...core,
  async api(name) {
    events.push(name);
    if (name === 'state') { if (stateFailure) throw Error('state failed'); if (stateWait) await stateWait; return structuredClone(desktop); }
    assert.equal(name, 'runtime-status');
    if (failure) throw Error('status failed');
    if (statusWait) return statusWait;
    return structuredClone(status);
  },
  appearance() { events.push('appearance'); },
  view: { update() {
    events.push('view.update'); published?.(ctx);
  } },
  refreshNavCounts() { events.push('nav'); },
  maybeShowFirstRun() {},
  notify(...args) { notices.push(args); },
};
const { refresh: startRefresh, setRefreshText } = new Function(...Object.keys(bindings),
  'let refreshPromise, flushStatus, verifiedStatus, stagedStatus; const navCounts = {at:0};\n' +
  section + '\nreturn {refresh, setRefreshText};')(...Object.values(bindings));
const refresh = (options = {immediate: false}) => startRefresh(options);
const fields = Object.values(elements).slice(1, -1);
const nodes = () => fields.map(e => e.node);
const writes = () => fields.map(e => e.writes);
let count = 0;
async function test(name, fn) { await fn(); console.log(`PASS ${++count}: ${name}`); }
(async () => {
  desktop = { connected: true, version: '2.0.0-dev.test', platform: 'windows/amd64',
    health: { bin: 'fixture', account_id:'account-a', instance_id:'runtime-a', generation:1,
      data_dir:'/fixture/library', credential_state:'available', store_ready:true } };
  status = { command:'status', status:'ok', summary: { unlocked:true, data_dir:desktop.health.data_dir,
    local_alive: 12, workspace: 'normal', sync_live: { phase: 'idle', pending: 0, conflicts: 0 } } };
  await test('initial English values and existing call order', async () => {
    await refresh();
    assert.deepEqual(events, ['state', 'appearance', 'view.update', 'runtime-status', 'state', 'appearance', 'view.update', 'nav']);
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
    assert.equal(events.filter(e => e === 'state').length, 100);
    assert.equal(events.filter(e => e === 'runtime-status').length, 100);
  });
  await test('real status count version platform and sync changes propagate', async () => {
    desktop.version = '2.0.0-dev.next'; desktop.platform = 'linux/amd64';
    status.summary.local_alive = 13; status.summary.workspace = 'readonly'; status.summary.sync_live.pending = 2;
    await refresh();
    assert.equal(elements['#nav-memory-count'].textContent, '12'); // first unbracketed sample is not displayed
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
  await test('status error preserves verified display; verified retry restores current status', async () => {
    failure = true; await refresh();
    assert.equal(ctx.statusError, 'status failed');
    assert.equal(ctx.status, null);
    assert.equal(ctx.displayStatus.summary.local_alive, 13);
    assert.equal(elements['#nav-memory-count'].textContent, '13');
    assert.match(elements['#sync-summary'].textContent, /last verified/);
    failure = false; await refresh(); assert.equal(ctx.statusError, '');
    assert.equal(ctx.status.summary.local_alive, 13);
  });
  await test('disconnect skips status and clears private data; reconnect brackets before restoring it', async () => {
    desktop.connected = false; events.length = 0; await refresh();
    assert.deepEqual(events, ['state', 'appearance', 'view.update', 'nav']);
    assert.equal(ctx.displayStatus, null);
    assert.equal(ctx.statusScope, null);
    assert.equal(elements['#connection-label'].textContent, 'Runtime disconnected');
    assert.equal(elements['#status-summary'].textContent, 'Runtime disconnected');
    assert.equal(elements['#nav-memory-count'].textContent, '');
    desktop.connected = true; await refresh(); assert.equal(elements['#connection-label'].textContent, 'Runtime connected');
  });
  await test('state failure clears the last private sample and offers a retry (not fake zero or key error)', async () => {
    stateFailure = true; await refresh();
    assert.equal(ctx.statusError, 'state failed');
    assert.equal(ctx.displayStatus, null);
    assert.equal(elements['#nav-memory-count'].textContent, '');
    assert.deepEqual(notices, []);
    stateFailure = false; events.length = 0; await refresh(); assert.ok(events.includes('view.update'));
  });
  await test('overlapping same-scope refresh shares one request; real manual retry upgrades the pending read', async () => {
    let release;
    statusWait = new Promise(resolve => { release = resolve; }); events.length = 0;
    const pending = refresh();
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(refresh(), pending);
    assert.deepEqual(events, ['state', 'appearance', 'view.update', 'runtime-status']);
    assert.equal(refresh({immediate: true}), pending);
    assert.equal(ctx.status, null);
    assert.equal(ctx.displayStatus.summary.local_alive, 13);
    release(structuredClone(status)); await pending; statusWait = null;
    assert.equal(events.filter(e => e === 'state').length, 2);
    assert.equal(events.filter(e => e === 'runtime-status').length, 1);
    assert.equal(ctx.status.summary.local_alive, 13);
  });
  await test('same directory with a new account/generation clears A before delayed status and rejects older payload', async () => {
    // Poll under A: its fresh sample expects the next Health observation.
    status.summary.local_alive = 3341; await refresh();
    const seen = [];
    published = (context) => seen.push({
      scope:context.statusScope, count:context.displayStatus?.summary.local_alive, ready:Boolean(context.status),
    });
    desktop.health.account_id = 'account-b';
    desktop.health.generation++;
    let release;
    statusWait = new Promise(resolve => { release = resolve; });
    const reading = refresh();
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(ctx.status, null);
    assert.equal(ctx.displayStatus, null);
    assert.equal(elements['#nav-memory-count'].textContent, '');
    assert.ok(seen.every(x => x.count !== 3341));
    // Scenario: generation B changes to a subsequent same-path A before the bracket closes.
    desktop.health.account_id = 'account-a';
    desktop.health.generation++;
    release({ ...status, summary:{ ...status.summary, local_alive:22 } });
    await reading; statusWait = null; published = null;
    assert.equal(ctx.status, null);
    assert.equal(ctx.displayStatus, null);
    assert.equal(ctx.statusScope, null);
    status.summary.local_alive = 37;
    await refresh();
    assert.equal(ctx.status.summary.local_alive, 37);
  });
  await test('changed directory rejects a status body for another profile without exposing counts', async () => {
    status.summary.data_dir = '/fixture/unrelated';
    await refresh();
    assert.equal(ctx.status, null);
    assert.equal(ctx.displayStatus, null);
    assert.equal(ctx.statusScope, null);
    assert.match(elements['#status-summary'].textContent, /Click Refresh/);
    status.summary.data_dir = desktop.health.data_dir;
    await refresh();
    assert.equal(ctx.status.summary.local_alive, 37);
  });
  await test('first-run wizard still supports a clean offline install but does not interpret temporary status loss as new account', () => {
    const from = source.indexOf('function maybeShowFirstRun()');
    const countShows = new Function('ctx', 'wizard', 'let checkFirstRun = true;\n' +
      source.slice(from, source.indexOf('async function poll(', from)) + '\nreturn maybeShowFirstRun;');
    let shown = 0;
    const wizard = {show: () => shown++};
    const existing = {desktop:{connected:true, preferences:{}}, status:null};
    const waiting = countShows(existing, wizard);
    waiting();
    assert.equal(shown, 0);
    existing.status = {summary:{session:{has_token:true}}};
    waiting();
    assert.equal(shown, 0);
    countShows({desktop:{connected:false, localSessionHasToken:true}}, wizard)();
    assert.equal(shown, 0);
    const offline = countShows({desktop:{connected:false}}, wizard);
    offline();
    offline();
    assert.equal(shown, 1);
  });
  await test('first status loss stays actionable without lock and verified lock never shows an old count', async () => {
    desktop.health.instance_id = 'next-instance';
    failure = true; await refresh();
    assert.equal(ctx.status, null);
    assert.equal(ctx.displayStatus, null);
    assert.match(elements['#status-summary'].textContent, /Click Refresh/);
    failure = false;
    desktop.health.credential_state = 'required';
    desktop.health.store_ready = false;
    status.summary.unlocked = false;
    status.summary.local_alive = null;
    await refresh({immediate:true});
    assert.equal(ctx.status.summary.unlocked, false);
    assert.equal(elements['#nav-memory-count'].textContent, '');
    desktop.health.credential_state = 'available';
    desktop.health.store_ready = true;
    status.summary.unlocked = true;
    status.summary.local_alive = 13;
    await refresh();
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
  await test('memory workspace has actionable retry text separate from a confirmed lock', () => {
    const workspace = read('memory/workspace.js');
    const start = workspace.indexOf('  function confirmedLock()');
    const end = workspace.indexOf('  let catalogEpoch', start);
    const ctx = {desktop:{connected:false}, status:null, statusError:''};
    const message = new Function('ctx','readableLibrary', workspace.slice(start,end) +
      '\nreturn unavailableMessage;')(ctx, () => ctx.status?.summary?.unlocked === true);
    assert.equal(message(), '等待 Runtime 连接');
    ctx.statusError = 'host failed';
    assert.match(message(), /重连.*刷新/);
    ctx.desktop.connected = true;
    assert.match(message(), /无法读取.*刷新/);
    ctx.status = {summary:{unlocked:false}};
    assert.equal(message(), '需要解锁记忆库');
    ctx.status = {summary:{unlocked:true}};
    assert.equal(message(), '');
  });
  // Reuse the real workspace module for memory readback and refresh rules.
  const workspace = read('memory/workspace.js');
  const segment = (from, to) => workspace.slice(workspace.indexOf(from), workspace.indexOf(to));
  const build = new Function('ctx', 'execute', `
  const root = { isConnected: true }, checked = new Set(), expanded = new Set();
  const reader = { innerHTML: 'old reader', querySelector: () => true };
  const tree = { innerHTML: 'old tree' }, count = { textContent: '' };
  let loaded = true, revision = 'loaded', busy = false, limit = 4, metadataCap = true,
    catalogContext = ctx.statusScope, selectionAnchor = null, lastFoldClick, filter = '', actionEpoch = 0, statusUnavailable = false;
  let records = [{id:'old'}], selected = 'old', selectionVersion = 0;
  let renderedEntry = {id:'old', updated_at:'before', content:'prior body'}, selectedEntry = renderedEntry, searchCaption;
  let renderedContext = readableLibrary(), readerToken = {};
  function renderTree() { tree.innerHTML = records.map(row => row.id).join(','); }
  const h = String, t = String, number = String, list = value => value, entry = value => value;
  const inventory = rows => ({byId: new Map(rows.map(row => [row.id, row]))});
  const $ = () => ({addEventListener() {}, insertAdjacentHTML() {}});
  const all = () => [];
  function closeMemoryDialogs() {} function update() {} function keepDialog() {}
  const renderReader = (memory) => { reader.innerHTML = memory.content; };
  const memoryTools = () => {};
  ${segment('  function readableLibrary()', '  // Body records')}
  ${segment('  async function choose(', '  async function search()')}
  return {load, choose, showUnavailable, activeReaderEntry, requireCurrentReader, tree, reader, count, ids: () => records.map(row => row.id)};
  `);
  const fixtureContext = () => ({desktop: {connected:true, health:{account_id:'account-a', instance_id:'fixture-runtime', generation:1}}, statusScope:'verified-a', status:{summary:{unlocked:true, local_alive:10, data_dir:'fixture-library'}}});
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
    const before = reading.requireCurrentReader(reading.activeReaderEntry());
    await reading.choose('old', false);
    assert.equal(reading.reader.innerHTML, 'old reader');
    assert.equal(reading.activeReaderEntry(), null);
    assert.throws(before, /重新选择/);
    error = Object.assign(new Error('not found'), {envelope:{summary:{reason:'not_found'}}});
    await reading.choose('old', false);
    assert.match(reading.reader.innerHTML, /not found/);
    assert.equal(reading.activeReaderEntry(), null);
    assert.throws(before, /重新选择/);
  });
  await test('reader refuses in-flight old-account result even at the same library path', async () => {
    const ctx = fixtureContext();
    let finish;
    const reading = build(ctx, () => new Promise(resolve => { finish = resolve; }));
    const before = reading.requireCurrentReader(reading.activeReaderEntry());
    const pending = reading.choose('old', false);
    ctx.desktop.health.account_id = 'account-b';
    ctx.statusScope = 'verified-b'; ctx.status = null;
    assert.equal(reading.activeReaderEntry(), null);
    assert.throws(before, /刷新后重试/);
    finish({id:'old', updated_at:'new', content:'wrong account'});
    await pending;
    assert.match(reading.reader.innerHTML, /正在读取/);
    assert.doesNotMatch(reading.reader.innerHTML, /wrong account|old reader/);
    assert.equal(reading.activeReaderEntry(), null);
  });
  await test('reader actions cannot revive after a confirmed lock or selecting away and back', async () => {
    const ctx = fixtureContext();
    const reading = build(ctx, async (_name, {id}) => ({id, updated_at:'fresh', content:'fresh body'}));
    const before = reading.requireCurrentReader(reading.activeReaderEntry());
    await reading.choose('second');
    await reading.choose('old');
    assert.equal(reading.activeReaderEntry().id, 'old');
    assert.throws(before, /重新选择/);
    const after = reading.requireCurrentReader(reading.activeReaderEntry());
    ctx.status.summary.unlocked = false;
    assert.equal(reading.activeReaderEntry(), null);
    assert.throws(after, /刷新后重试/);
    assert.equal(reading.showUnavailable(), true);
    ctx.status.summary.unlocked = true;
    await reading.choose('old');
    assert.equal(reading.activeReaderEntry().id, 'old');
    assert.throws(after, /刷新后重试/);
  });
  await test('reader blocks before slow status completes when Health sees a new library or confirmed lock', () => {
    const ctx = fixtureContext();
    ctx.desktop.health.data_dir = 'fixture-library';
    ctx.desktop.health.credential_state = 'available';
    const reading = build(ctx, () => {});
    const before = reading.requireCurrentReader(reading.activeReaderEntry());
    ctx.desktop.health.data_dir = 'new-library';
    ctx.statusScope = 'changed'; ctx.status = null;
    assert.equal(reading.activeReaderEntry(), null);
    assert.throws(before, /刷新后重试/);
    ctx.desktop.health.data_dir = 'fixture-library';
    ctx.desktop.health.credential_state = 'required';
    assert.equal(reading.activeReaderEntry(), null);
    assert.equal(reading.showUnavailable(), true);
    ctx.desktop.health.credential_state = 'available';
    assert.throws(before, /刷新后重试/);
  });
  const loadModelPageTools = (api, execute) => new Function('api', 'execute',
    read('pages/model-kit.js').replace(/^import \{[\s\S]+?;\n/, '').replace(/^export /gm, '') +
    '\nreturn {engineAvailable, detectBestEngine, fetchModelDownloadTask, watchModelDownloadTask, cancelDisplayedModelWork, localModelDownload};'
  )(api, execute);
  await test('Windows GPU probe stays selectable without capabilities but explicit denies and NPU are respected', () => {
    const {engineAvailable, detectBestEngine} = loadModelPageTools();
    assert.equal(engineAvailable('windows/amd64', 'gpu'), true);
    assert.equal(engineAvailable('windows/amd64', 'gpu', {gpu:false}), false);
    assert.equal(engineAvailable('windows/amd64', 'gpu', {gpu:{available:false}}), false);
    assert.equal(engineAvailable('windows/amd64', 'gpu', {gpu:{available:true}}), true);
    assert.equal(engineAvailable('windows/amd64', 'npu'), false);
    assert.equal(engineAvailable('windows/amd64', 'npu', {npu:{available:true}}), true);
    assert.equal(detectBestEngine('windows/amd64', {gpu:false, npu:true}), 'cpu');
    assert.equal(detectBestEngine('windows/amd64'), 'gpu');
  });
  await test('model page watches isolated download during unrelated update and never falls back on transport failure', async () => {
    const downloading = {busy:true, stage:'下载模型 baseline', done:20, total:100};
    const calls = [], samples = [], scheduled = [];
    let error, task = downloading;
    const tools = loadModelPageTools(async (route) => {
      calls.push(route);
      if (error) throw error;
      return {task};
    });
    const flush = () => new Promise(resolve => setImmediate(resolve));
    const stop = tools.watchModelDownloadTask((sample) => samples.push(sample), {
      read: () => tools.fetchModelDownloadTask(() => ({busy:true, stage:'更新 Runtime'})),
      schedule(callback, delay) { scheduled.push({callback, delay}); return callback; },
      cancel(callback) { scheduled.splice(scheduled.findIndex(item => item.callback === callback), 1); },
    });
    await flush();
    assert.deepEqual(samples, [downloading]);
    assert.equal(scheduled[0].delay, 700);
    error = new Error('network failure');
    await scheduled.shift().callback();
    assert.deepEqual(samples, [downloading]);
    assert.deepEqual(calls, ['models/progress','models/progress']);
    error = null;
    task = {busy:false, stage:'完成'};
    await scheduled.shift().callback();
    assert.equal(samples.at(-1), task);
    assert.equal(scheduled[0].delay, 3000);
    stop();
    assert.deepEqual(scheduled, []);
    error = new Error('desktop API request rejected');
    const old = {busy:true, stage:'下载模型 baseline'};
    assert.equal(await tools.fetchModelDownloadTask(() => old), old);
  });
  await test('model page displays progress and enables cancel outside modal even without an inventory', () => {
    const page = read('pages/models.js');
    const section = (from, to) => page.slice(page.indexOf(from), page.indexOf(to, page.indexOf(from)));
    const {localModelDownload} = loadModelPageTools();
    const progress = {}, cancel = {disabled:true};
    const find = (selector) => ({
      '[data-mode-progress]': progress,
      '[data-model=cancel]': cancel,
    })[selector];
    const ctx = {desktop:{task:{busy:true, stage:'更新 Runtime'}}};
    let isolated = {busy:true, stage:'下载模型 baseline', done:25, total:100};
    const drive = new Function('ctx', '$', 'currentModelTask', 'localModelDownload', 'h',
      'const root = {}, inventory = null, switchInFlight = false;\n' +
      section('  function updateModeProgress()', '  function renderAdvancedVisibility()') +
      section('  function updateVariantsProgress()', '  function update()') +
      '\nreturn () => { updateModeProgress(); updateVariantsProgress(); };'
    )(ctx, find, () => isolated, localModelDownload, String);
    drive();
    assert.equal(progress.hidden, false);
    assert.match(progress.innerHTML, /25%/);
    assert.doesNotMatch(progress.innerHTML, /更新 Runtime/);
    assert.equal(cancel.disabled, false);
    isolated = {busy:false, stage:'完成'};
    drive();
    assert.equal(progress.hidden, true);
    assert.equal(cancel.disabled, true);
  });
  await test('model page cancels the shown local download without redirecting to a Runtime task', async () => {
    const calls = [];
    const tools = loadModelPageTools(async (route, body) => { calls.push([route, body]); }, async (...request) => { calls.push(request); });
    const local = {busy:true, stage:'下载模型 baseline'};
    const runtime = {active:true, id:'runtime-task'};
    await tools.cancelDisplayedModelWork(local, runtime, {read:async () => local, currentOperation:() => runtime});
    assert.deepEqual(calls, [['model-download-cancel', {pause:false}]]);
    await assert.rejects(tools.cancelDisplayedModelWork(local, runtime, {read:async () => ({busy:false}), currentOperation:() => runtime}), /已变化/);
    await assert.rejects(tools.cancelDisplayedModelWork(null, runtime, {read:async () => local, currentOperation:() => runtime}), /已变化/);
    await assert.rejects(tools.cancelDisplayedModelWork(null, runtime, {read:async () => ({}), currentOperation:() => ({active:true,id:'replacement'})}), /已变化/);
    assert.equal(calls.length, 1);
  });
  function organizeFixture(onSuggestions = async () => ({suggests: []}), onModel) {
    const bySelector = new Map();
    const $ = (selector) => {
      if (!bySelector.has(selector))
        bySelector.set(selector, {innerHTML:'', textContent:'', value:'',
          disabled: selector === '[data-plan-apply]',
          addEventListener(name, callback) { this[name] = callback; },
          dataset: {}, hasAttribute(name) { return selector === `[${name}]`; },
        });
      return bySelector.get(selector);
    };
    const listeners = {};
    const root = {isConnected:true, addEventListener(name, callback) { listeners[name] = callback; }};
    const ctx = {desktop:{connected:true,health:{account_id:'acct-a', instance_id:'runtime-a', generation:1,
      data_dir:'library-a', inference:{worker_generation:4}}},status:{summary:{unlocked:true,data_dir:'library-a'}},refresh:async () => {}};
    const calls = [], reads = [];
    const model = {generation:'initial'};
    const fixture = {desktop:null, revision:"current"};
    const errors = [];
    const source = read('pages/organize.js');
    const organizers = new Function('$', 'h', 'heading','panel','select','input','api','report','execute','summary',
      'action','raw','resultView','confirm','notify','FormData',
      source.slice(source.indexOf('export function organize')).replace(/^export /,'') + '\nreturn organize;'
    )($, String, () => '', (_name, html) => html, () => '', () => '',
      async (path) => {
        reads.push(path);
        if (path === "state") return fixture.desktop || ctx.desktop;
        await onModel?.();
        const active = model.legacy ? null : {model:'m3',engine:'cpu',variant:'quantized',generation:model.generation};
        const task = model.switch ?? null;
        return {
          catalog:{summary:{registry:{},active,switch:task}},
          progress:{summary:{switch:task,operation:{},pending_selection:model.pending ?? null,pending_task:null}},
        };
      },
      (value) => ({...value?.summary, ...value?.details}),
      async (name, args) => {
        calls.push([name,args]);
        if (name === 'organize_suggestions') return {summary:await onSuggestions()};
        if (name === 'memory_revision') return fixture.onRevision?.() ??
          {summary:{profile:'library-a', revision:fixture.revision}};
        if (name === 'organize_resort_preview') return fixture.onPreview?.() ?? {};
        if (name === 'organize_resort_apply') return fixture.onApply?.() ?? {};
        if (name === 'organize_plan' || name === 'organize_classify') return {summary:{ops:[]}};
        return {};
      },
      (x) => x.summary || {},
      async (button, handler) => {
        if (button.disabled) return;
        button.disabled = true;
        try { await handler(); } catch (error) { errors.push(error.message); }
        finally { button.disabled = false; }
      },
      JSON.stringify,
      (heading) => heading,
      async () => fixture.onConfirm ? await fixture.onConfirm() : true,
      () => {},
      function FormData() { return Object.entries({backend:'ds',limit:'50',root:'',all:false}); },
    );
    const flush = () => new Promise(resolve => setImmediate(resolve));
    return {ctx, root, calls, reads, model, fixture, flush, listeners, errors, report: $('[data-report]'),
      field: $('[name=spec]'), apply: $('[data-plan-apply]'),
      click(name) { listeners.click({target:{closest:() => $(`[${name}]`)}}); },
      analyze(kind) { listeners.click({target:{closest:() => ({dataset:{analyze:kind},hasAttribute:()=>false})}}); },
      aiPreview() { $('[data-ai]').submit({target:$('[data-ai]'),preventDefault(){}}); },
      mount: () => organizers(root,ctx), suggestions: () => calls.filter(([name]) => name === 'organize_suggestions')};
  }

  await test('Organize mount starts one suggestion read, simultaneous refresh joins, and explicit refresh still works', async () => {
    let complete;
    const f = organizeFixture(() => new Promise(resolve => {complete = resolve;}));
    const view = f.mount();
    const refreshing = view.refresh();
    assert.equal(view.refresh(), refreshing);
    await f.flush();
    assert.deepEqual(f.suggestions(), [['organize_suggestions',{top:50}]]);
    complete({suggests: []});
    await refreshing;
    assert.match(f.report.innerHTML, /当前没有归纲建议/);
    assert.deepEqual(f.reads, ['model-profile-state','model-profile-state','state']);
    for (let i = 0; i < 100; ++i) view.update();
    assert.equal(f.suggestions().length, 1);
    const again = view.refresh();
    await f.flush();
    assert.equal(f.suggestions().length, 2);
    complete({suggests: []});
    await again;
    view.dispose();
  });
  await test('loading Organize before account status waits once; regular app refresh never repeats suggestions', async () => {
    const f = organizeFixture();
    f.ctx.status = null;
    const view = f.mount();
    await f.flush();
    assert.equal(f.suggestions().length, 0);
    f.ctx.status = {summary:{unlocked:true, data_dir:'library-a'}};
    view.update();
    view.update();
    await f.flush();
    assert.equal(f.suggestions().length,1);
    view.update();
    await f.flush();
    assert.equal(f.suggestions().length,1);
  });
  await test('Organize drops stale late results after account, Runtime generation, model worker, lock, or disposal', async () => {
    for (const change of [
      (f) => {f.ctx.desktop.health.account_id = 'acct-b';},
      (f) => {f.ctx.desktop.health.generation += 1;},
      (f) => {f.ctx.desktop.health.inference.worker_generation += 1;},
      (f) => {f.ctx.status.summary.unlocked = false;},
      (_f, view) => view.dispose(),
    ]) {
      let complete;
      const f = organizeFixture(() => new Promise(resolve => {complete = resolve;}));
      const view = f.mount();
      const pending = view.refresh();
      await f.flush();
      change(f, view);
      view.update();
      complete({suggests:[{orphan:{id:'old',title:'STALE'},target:{id:'target'}}]});
      await pending;
      assert.doesNotMatch(f.report.innerHTML, /STALE/);
      assert.equal(f.suggestions().length, 1);
    }
  });
  await test('Organize does not start analysis if scope changes during the first model read', async () => {
    let finish;
    const f = organizeFixture(undefined, () => new Promise(resolve => {finish = resolve;}));
    const view = f.mount();
    const pending = view.refresh();
    f.ctx.desktop.health.account_id = 'acct-b';
    finish();
    await pending;
    assert.equal(f.suggestions().length, 0);
    view.update();
    await f.flush();
    assert.equal(f.suggestions().length, 0);
  });
  await test('a requested audit wins over the initial suggestions, and a later refresh does not overlap it', async () => {
    const responses = [];
    const f = organizeFixture(() => new Promise(resolve => {responses.push(resolve);}));
    const view = f.mount();
    const first = view.refresh();
    await f.flush();
    const audit = {dataset:{analyze:'audit'},hasAttribute:()=>false};
    f.listeners.click({target:{closest:()=>audit}});
    await f.flush();
    assert.equal(f.report.innerHTML, '结构检查');
    const queued = view.refresh();
    await f.flush();
    assert.equal(f.suggestions().length,1);
    responses[0]({suggests:[{orphan:{id:'old',title:'STALE'},target:{id:'target'}}]});
    await first;
    await f.flush();
    assert.doesNotMatch(f.report.innerHTML,/STALE/);
    assert.equal(f.suggestions().length,2);
    responses[1]({suggests: []});
    await queued;
    assert.match(f.report.innerHTML,/当前没有归纲建议/);
  });
  await test('Organize rejects changes to catalog generation even when global Health has not refreshed', async () => {
    let complete;
    const f = organizeFixture(() => new Promise(resolve => {complete = resolve;}));
    const view = f.mount();
    const pending = view.refresh();
    await f.flush();
    f.model.generation = 'different-profile-generation';
    complete({suggests:[{orphan:{id:'old',title:'STALE'},target:{id:'target'}}]});
    await pending;
    assert.match(f.report.innerHTML,/模型已变化/);
    assert.doesNotMatch(f.report.innerHTML,/STALE/);
    assert.equal(f.suggestions().length,1);
  });
  await test('Organize rechecks the real current account before showing results, even before app refresh', async () => {
    let finish;
    const f = organizeFixture(() => new Promise(resolve => {finish = resolve;}));
    const view = f.mount();
    const pending = view.refresh();
    await f.flush();
    f.fixture.desktop = {connected:true, health:{...f.ctx.desktop.health, account_id:'acct-b'}};
    finish({suggests:[{orphan:{id:'old',title:'STALE'},target:{id:'target'}}]});
    await pending;
    assert.doesNotMatch(f.report.innerHTML,/STALE/);
    assert.match(f.report.innerHTML,/运行环境已变化/);
    assert.equal(f.suggestions().length,1);
  });
  await test('Organize defers suggestion requests while Runtime reports an active model switch', async () => {
    const f = organizeFixture();
    f.model.switch = {state:'pending',id:'switch-task'};
    const view = f.mount();
    await view.refresh();
    assert.match(f.report.innerHTML, /模型正在切换/);
    assert.equal(f.suggestions().length, 0);
    f.model.switch = {state:'ready',id:'switch-task'};
    view.update();
    await f.flush();
    assert.equal(f.suggestions().length,0);
    await view.refresh();
    assert.equal(f.suggestions().length,1);
  });
  async function shownSuggestion() {
    const f = organizeFixture(async () => ({suggests:[{orphan:{id:'child'},target:{id:'parent'}}]}));
    const view = f.mount();
    await view.refresh();
    assert.match(f.report.innerHTML,/预览全部建议/);
    return {f, view};
  }
  await test('Organize refuses suggestions when memory changes during analysis', async () => {
    let finish;
    const f = organizeFixture(() => new Promise(resolve => {finish = resolve;}));
    const view = f.mount();
    const pending = view.refresh();
    await f.flush();
    f.fixture.revision = 'new-memory';
    finish({suggests:[{orphan:{id:'old',title:'STALE'},target:{id:'parent'}}]});
    await pending;
    assert.match(f.report.innerHTML,/记忆库已变化/);
    assert.doesNotMatch(f.report.innerHTML,/STALE/);
  });
  await test('suggestion preview checks the original revision before submitting even after suggestion display', async () => {
    const {f} = await shownSuggestion();
    f.fixture.revision = 'new-memory';
    f.click('data-suggestions-all');
    await f.flush();
    assert.equal(f.calls.filter(([name]) => name === 'organize_resort_preview').length,0);
    assert.equal(f.field.value,'');
    assert.equal(f.apply.disabled,true);
    assert.match(f.errors.at(-1),/记忆库已变化/);
  });
  await test('late suggestion preview cannot retain a plan after revision, model, remote account or observed account ABA', async () => {
    for (const drift of [
      (f) => {f.fixture.revision = 'new-memory';},
      (f) => {f.model.generation = 'new-model';},
      (f) => {f.fixture.desktop = {connected:true,health:{...f.ctx.desktop.health,account_id:'acct-b'}};},
      (f, view) => {
        f.ctx.desktop.health.account_id = 'acct-b'; view.update();
        f.ctx.desktop.health.account_id = 'acct-a'; view.update();
      },
    ]) {
      const {f,view} = await shownSuggestion();
      let finish;
      f.fixture.onPreview = () => new Promise(resolve => {finish = resolve;});
      f.click('data-suggestions-all');
      await f.flush();
      assert.equal(f.apply.disabled,true);
      assert.equal(typeof finish,'function');
      drift(f,view);
      finish({});
      await f.flush();
      assert.equal(f.apply.disabled,true);
      assert.equal(f.field.value,'');
      assert.doesNotMatch(f.report.innerHTML,/^方案预览$/);
      f.click('data-plan-apply');
      await f.flush();
      assert.equal(f.calls.filter(([name]) => name === 'organize_resort_apply').length,0);
    }
  });
  await test('suggestion apply rechecks after confirmation for revision, model, fresh account and edited-and-reverted text', async () => {
    for (const drift of [
      (f) => {f.fixture.revision = 'new-memory';},
      (f) => {f.model.generation = 'new-model';},
      (f) => {f.fixture.desktop = {connected:true,health:{...f.ctx.desktop.health,account_id:'acct-b'}};},
      (f) => {f.field.input();},
    ]) {
      const {f} = await shownSuggestion();
      f.click('data-suggestions-all');
      await f.flush();
      assert.equal(f.report.innerHTML,'方案预览');
      assert.equal(f.apply.disabled,false);
      let finish;
      f.fixture.onConfirm = () => new Promise(resolve => {finish = resolve;});
      f.click('data-plan-apply');
      await f.flush();
      assert.equal(typeof finish,'function');
      drift(f);
      finish(true);
      await f.flush();
      assert.equal(f.calls.filter(([name]) => name === 'organize_resort_apply').length,0);
      assert.equal(f.apply.disabled,true);
    }
  });
  await test('unchanged suggestion confirmation submits the existing spec once and consumes the preview', async () => {
    const {f} = await shownSuggestion();
    f.click('data-suggestions-all');
    await f.flush();
    assert.equal(f.apply.disabled,false);
    f.click('data-plan-apply');
    f.click('data-plan-apply');
    await f.flush();
    f.click('data-plan-apply');
    await f.flush();
    assert.deepEqual(f.calls.filter(([name]) => name === 'organize_resort_apply'),[
      ['organize_resort_apply',{spec:'{"ops":[{"id":"child","parent":"parent"}]}'}],
    ]);
    assert.equal(f.report.innerHTML,'整理结果');
    assert.equal(f.apply.disabled,true);
  });
  await test('manual JSON preview/apply still uses the existing revision and wire with no model dependency', async () => {
    const {f} = await shownSuggestion();
    f.fixture.desktop = {connected:false};
    f.model.switch = {state:'running'};
    const reads = f.reads.length;
    f.field.value = '{"ops":[{"id":"manual","parent":"any"}]}';
    f.field.input();
    f.click('data-plan-preview');
    await f.flush();
    assert.equal(f.apply.disabled,false);
    f.click('data-plan-apply');
    await f.flush();
    assert.deepEqual(f.calls.filter(([name]) => name === 'organize_resort_apply'),[
      ['organize_resort_apply',{spec:f.field.value}],
    ]);
    assert.equal(f.reads.length,reads);
    assert.equal(f.apply.disabled,true);
  });
  await test('published dev.28 and dev.31 admit legacy null active and configured model catalog/Health', async () => {
    for (const legacy of [false, true]) {
      const f = organizeFixture(async () => ({suggests:[{orphan:{id:'child'},target:{id:'parent'}}]}));
      f.model.legacy = legacy;
      const {health} = f.ctx.desktop;
      health.generation = 1;
      health.credential_state = 'available';
      // The published Health inference error/legacy status can omit the
      // optional worker generation; account and Runtime generation still bind.
      health.inference = {state:'ready'};
      const view = f.mount();
      await view.refresh();
      assert.equal(f.suggestions().length,1);
      f.click('data-suggestions-all');
      await f.flush();
      assert.equal(f.apply.disabled,false);
    }
  });
  await test('suggestion is not applied across an account move while waiting on final revision read', async () => {
    const {f} = await shownSuggestion();
    f.click('data-suggestions-all');
    await f.flush();
    let finishConfirmation;
    f.fixture.onConfirm = () => new Promise(resolve => {finishConfirmation = resolve;});
    f.click('data-plan-apply');
    await f.flush();
    let finishRevision;
    f.fixture.onRevision = () => new Promise(resolve => {finishRevision = resolve;});
    finishConfirmation(true);
    await f.flush();
    f.fixture.desktop = {connected:true,health:{...f.ctx.desktop.health,account_id:'acct-b'}};
    finishRevision({summary:{profile:'library-a',revision:'current'}});
    await f.flush();
    assert.equal(f.calls.filter(([name]) => name === 'organize_resort_apply').length,0);
    assert.equal(f.apply.disabled,true);
  });
  await test('independent Organize actions use their own Runtime calls with no unused memory revision probe', async () => {
    const {f} = await shownSuggestion();
    f.calls.length = 0;
    f.fixture.onRevision = () => { throw new Error('unexpected revision probe'); };
    for (const kind of ['duplicates', 'audit', 'plan']) {
      f.analyze(kind);
      await f.flush();
    }
    f.aiPreview();
    await f.flush();
    let finish;
    f.fixture.onConfirm = () => new Promise(resolve => {finish = resolve;});
    f.click('data-ai-apply');
    await f.flush();
    assert.equal(f.calls.filter(([name]) => name === 'organize_classify').length,1);
    finish(true);
    await f.flush();
    assert.deepEqual(f.calls, [
      ['organize_duplicates',{min:0.8,top:30}],
      ['organize_audit',{}],
      ['organize_plan',{}],
      ['organize_classify',{backend:'ds',limit:50,causal:true}],
      ['organize_classify',{backend:'ds',limit:50,causal:true,auto:true}],
    ]);
    assert.deepEqual(f.errors,[]);
  });
  await test('manual preview keeps the report and matching Apply when late mount suggestions succeed or fail', async () => {
    for (const fail of [false, true]) {
      let complete;
      const f = organizeFixture(() => new Promise((resolve, reject) => {
        complete = () => fail ? reject(new Error('stale failure')) :
          resolve({suggests:[{orphan:{id:'stale',title:'STALE'},target:{id:'other'}}]});
      }));
      const view = f.mount();
      const initial = view.refresh();
      await f.flush();
      const spec = '{"ops":[{"id":"manual","parent":"target"}]}';
      f.field.value = spec;
      f.field.input();
      f.click('data-plan-preview');
      await f.flush();
      assert.equal(f.report.innerHTML,'方案预览');
      assert.equal(f.apply.disabled,false);
      complete();
      await initial;
      assert.equal(f.report.innerHTML,'方案预览');
      assert.equal(f.apply.disabled,false);
      assert.equal(f.suggestions().length,1);
      f.click('data-plan-apply');
      await f.flush();
      assert.deepEqual(f.calls.filter(([name]) => name === 'organize_resort_preview' || name === 'organize_resort_apply'),[
        ['organize_resort_preview',{spec}],
        ['organize_resort_apply',{spec}],
      ]);
    }
  });
  await test('a newer audit owns the report and ignores a manual preview that resolves later', async () => {
    const {f} = await shownSuggestion();
    let finish;
    f.fixture.onPreview = () => new Promise(resolve => {finish = resolve;});
    f.field.value = '{"ops":[{"id":"manual","parent":"target"}]}';
    f.field.input();
    f.click('data-plan-preview');
    await f.flush();
    assert.equal(typeof finish,'function');
    f.analyze('audit');
    await f.flush();
    assert.equal(f.report.innerHTML,'结构检查');
    finish({});
    await f.flush();
    assert.equal(f.report.innerHTML,'结构检查');
    assert.equal(f.apply.disabled,true);
  });
  await test('manual Apply confirmation cannot submit when explicit suggestions replace its visible preview', async () => {
    const {f,view} = await shownSuggestion();
    f.field.value = '{"ops":[{"id":"manual","parent":"target"}]}';
    f.field.input();
    f.click('data-plan-preview');
    await f.flush();
    let finish;
    f.fixture.onConfirm = () => new Promise(resolve => {finish = resolve;});
    f.click('data-plan-apply');
    await f.flush();
    assert.equal(typeof finish,'function');
    await view.refresh();
    assert.match(f.report.innerHTML,/预览全部建议/);
    assert.equal(f.apply.disabled,true);
    finish(true);
    await f.flush();
    assert.equal(f.calls.filter(([name]) => name === 'organize_resort_apply').length,0);
    assert.equal(f.apply.disabled,true);
  });
  await test('production client and memory page handle DOM-fixture clicks and delayed synthetic status', () => {
    const output = require('node:child_process').execFileSync(process.execPath, [
      path.join(__dirname, 'fixtures/client-refresh-dom.cjs'),
    ], {encoding:'utf8'});
    assert.match(output, /initial retry, same-scope display only, delayed same-root ABA, fresh reader, real lock passed/);
  });
  console.log(`${count} focused refresh tests passed; browser/native UIA not exercised.`);
})().catch(error => { console.error(error); process.exitCode = 1; });
