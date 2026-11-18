const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const repoRoot = path.resolve(__dirname, '..');
const src = fs.readFileSync(path.join(repoRoot, 'internal/desktop/frontend/pages/accounts.js'), 'utf8');
const core = fs.readFileSync(path.join(repoRoot, 'internal/desktop/frontend/ui/core.js'), 'utf8');

// Run the complete production Accounts module and exact core helpers. Only its
// imports are injected; DOM, backend, and dialogs below are lightweight stubs.
class Element {
  constructor(tag = 'section', attrs = '') {
    this.tag = tag;
    this.attrs = Object.fromEntries([...attrs.matchAll(/([\w-]+)(?:="([^"]*)")?/g)].map(m => [m[1], m[2] ?? '']));
    this.dataset = Object.fromEntries(Object.entries(this.attrs).filter(([key]) => key.startsWith('data-')).map(([key, value]) => [key.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase()), value]));
    this.disabled = Object.hasOwn(this.attrs, 'disabled');
    this.textContent = '';
    this.value = this.attrs.value || '';
    this.children = [];
    this.listeners = {};
    this.isConnected = true;
    this.classList = { toggle() {} };
    this.htmlWrites = 0;
  }
  set innerHTML(value) {
    this.htmlWrites++;
    this.children = [...value.matchAll(/<(button|strong|input|form|div)\b([^>]*)>/g)].map(m => new Element(m[1], m[2]));
  }
  querySelectorAll(selector) {
    const match = selector.match(/^\[([\w-]+)(?:="?([^"\]]+)"?)?\]$/);
    assert(match, `Unsupported test selector: ${selector}`);
    const all = this.children.flatMap(child => [child, ...child.descendants()]);
    return all.filter(child => Object.hasOwn(child.attrs, match[1]) && (match[2] === undefined || child.attrs[match[1]] === match[2]));
  }
  descendants() { return this.children.flatMap(child => [child, ...child.descendants()]); }
  querySelector(selector) {
    const node = this.querySelectorAll(selector)[0];
    assert(node, `Missing production DOM target: ${selector}`);
    return node;
  }
  closest(selector) { assert.equal(selector, 'button'); return this.tag === 'button' ? this : null; }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  click(button) { for (const fn of this.listeners.click || []) fn({ target: button }); }
}
const notice = new Element();
const document = { querySelector(selector) { assert.equal(selector, '#notice'); return notice; } };
const helpers = new Function('document', core.replace(/^export /gm, '') + '\nreturn {$, h, raw, summary, rows, action, panel, input, resultView};')(document);
const calls = [];
const apiCalls = [];
const handlers = new Map();
let confirmation = async () => true;
let fileDialog = async () => '/fixture/keys.json';
const dependencies = {
  ...helpers,
  execute(name, args = {}) {
    calls.push({ name, args });
    return handlers.has(name) ? handlers.get(name)(args) : Promise.resolve({ summary: {}, details: [] });
  },
  api(name, args) {
    apiCalls.push({ name, args });
    return name === 'file-dialog' ? fileDialog() : handlers.has(name) ? handlers.get(name)(args) : Promise.resolve({});
  },
  notify() {},
  confirm: (...args) => confirmation(...args),
  showText() {},
  dialog() {},
  heading: () => '',
  invitationDialog() {},
  membersDialog() {},
  authenticationPanel() {},
  executeAuthentication: (...args) => dependencies.execute(...args),
  recoveryMaterialDialog() {},
};
let imports = 0;
const moduleBody = src.replace(/^import\s[\s\S]*?from "[^"]+";\r?\n/gm, () => { imports++; return ''; });
assert.equal(imports, 4, 'Review fixture bindings when production imports change');
assert.equal((moduleBody.match(/export function accounts\(/g) || []).length, 1);
const accounts = new Function(...Object.keys(dependencies), moduleBody.replace(/^export /gm, '') + '\nreturn accounts;')(...Object.values(dependencies));
const kinds = ['login', 'dashboard', 'keygen', 'keys', 'logout'];
const commandFor = { login: 'login', dashboard: 'open-dashboard', keygen: 'keygen', keys: 'keys_export', logout: 'logout', sync: 'sync', conflicts: 'sync_conflicts', migration: 'migration_list' };
const flush = () => new Promise(resolve => setImmediate(resolve));
function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
function context(session = {}) { return { status: { summary: { session } }, async refresh() {} }; }
function mount(ctx) {
  const root = new Element();
  const view = accounts(root, ctx);
  return {
    root, view, ctx,
    button: kind => root.querySelector(`[data-account="${kind}"]`),
    field: kind => root.querySelector(`[data-session-field="${kind}"]`),
    click(kind) { root.click(this.button(kind)); },
    leave() { view.dispose?.(); root.isConnected = false; },
  };
}
function reset() {
  calls.length = 0;
  apiCalls.length = 0;
  handlers.clear();
  confirmation = async () => true;
  fileDialog = async () => '/fixture/keys.json';
  notice.textContent = '';
}
function callCount(kind) {
  return [...calls, ...apiCalls].filter(call => call.name === commandFor[kind]).length;
}
function allow(kind) { return { has_local_keys: kind === 'keys', has_token: kind === 'logout' }; }

function authenticationHarness(job, { ackError = false } = {}) {
  const requests = [], dialogs = [], events = [];
  const doc = { dispatchEvent(event) { events.push(event.type); } };
  class Event { constructor(type) { this.type = type; } }
  const testApi = async (name, args) => {
    requests.push({ name, args });
    if (name === 'operations') return job.materialSaved ? [] : [{ ...job, envelope: undefined }];
    if (name === 'operation-recover') { assert.equal(args.id, job.id); return job; }
    if (name === 'recovery-material-ack') {
      assert.deepEqual(args, { id: job.id, confirmed: true });
      if (ackError) throw Error('save receipt unavailable');
      job.materialSaved = true; job.materialPending = false;
      return 'saved';
    }
    if (name === 'operation-ack') return;
    throw Error(`Unexpected API ${name}`);
  };
  const liveCore = new Function('document', 'CustomEvent', 'testApi', core.replace(/^export /gm, '') + '\napi = testApi; return {waitForOperation};')(doc, Event, testApi);
  const bindings = {
    ...helpers, api: testApi, notify() {}, document: doc, CustomEvent: Event,
    execute: async (name) => { assert.equal(name, 'auth_reset'); return liveCore.waitForOperation(job); },
    waitForOperation: liveCore.waitForOperation,
    select() {}, confirm() {},
    dialog(options) {
      const element = new Element('dialog');
      element.children = [new Element('button', 'type="submit"'), new Element('input', 'data-saved'), new Element('button', 'data-close')];
      element.children.forEach(child => { child.remove = () => { child.removed = true; }; });
      element.close = () => { for (const fn of element.listeners.close || []) fn(); };
      dialogs.push({ element, options });
      return element;
    },
  };
  const source = fs.readFileSync(path.join(repoRoot, 'internal/desktop/frontend/pages/authentication.js'), 'utf8');
  const body = source.replace(/^import\s[\s\S]*?from "[^"]+";\r?\n/gm, '').replace(/^export /gm, '');
  const auth = new Function(...Object.keys(bindings), body + '\nreturn {recoveryMaterialDialog, restorePendingRecoveryMaterials, executeAuthentication, continueAuthentication};')(...Object.values(bindings));
  return { ...auth, requests, dialogs, events };
}

function pendingResetJob() {
  return { id: 'desktop-original-reset', name: 'auth_reset', state: 'done', materialPending: true,
    transaction: { operation_id: 'original-cli-operation', source_profile: '/original-profile', kind: 'key' },
    envelope: { status: 'ok', summary: { outcome: 'committed', super: 'synthetic-recovery-material' } } };
}
let passed = 0;
async function test(name, run) {
  reset();
  await run();
  passed++;
  console.log(`ok ${passed} - ${name}`);
}

(async () => {
  await test('reset material survives closing without save and is restored by original query on restart', async () => {
    const job = pendingResetJob(), first = authenticationHarness(job);
    const envelope = await first.executeAuthentication('auth_reset');
    assert.equal(envelope.recoveryRequestId, job.id);
    const initial = first.recoveryMaterialDialog(envelope);
    assert.equal(initial.querySelector('[type=submit]').disabled, true);
    assert.equal(await first.dialogs[0].options.onSubmit(), false);
    assert.equal(first.requests.filter(r => r.name === 'recovery-material-ack').length, 0);
    assert.equal(job.materialPending, true); // Exiting here does not produce a save receipt.
    const restarted = authenticationHarness(job), restored = restarted.restorePendingRecoveryMaterials();
    await flush();
    assert.deepEqual(restarted.requests, [{ name: 'operations', args: undefined }, { name: 'operation-recover', args: { id: job.id } }]);
    const { element, options } = restarted.dialogs[0];
    assert(options.body.includes('synthetic-recovery-material'));
    assert.equal(element.querySelector('[type=submit]').disabled, true);
    assert(element.querySelectorAll('[data-close]').every(button => button.removed));
    let prevented = false;
    element.listeners.cancel[0]({ preventDefault() { prevented = true; } });
    assert.equal(prevented, true);
    const checkbox = element.querySelector('[data-saved]'); checkbox.checked = true;
    checkbox.listeners.change[0]();
    assert.equal(element.querySelector('[type=submit]').disabled, false);
    await options.onSubmit(); element.close(); await restored;
    assert.equal(job.materialSaved, true); assert.equal(job.materialPending, false);
    assert.deepEqual(restarted.requests.at(-1), { name: 'recovery-material-ack', args: { id: job.id, confirmed: true } });
    assert(!JSON.stringify(restarted.requests).includes('synthetic-recovery-material'));
    const savedRestart = authenticationHarness(job);
    await savedRestart.restorePendingRecoveryMaterials();
    assert.equal(savedRestart.dialogs.length, 0);
    assert.deepEqual(savedRestart.requests, [{ name: 'operations', args: undefined }]);
  });
  await test('save receipt failure keeps recovery material pending and is not reported as saved', async () => {
    const job = pendingResetJob(), auth = authenticationHarness(job, { ackError: true });
    const element = auth.recoveryMaterialDialog(job.envelope, { requestId: job.id });
    element.querySelector('[data-saved]').checked = true;
    await assert.rejects(auth.dialogs[0].options.onSubmit, /save receipt unavailable/);
    assert.equal(job.materialPending, true); assert.equal(job.materialSaved, undefined);
    assert.deepEqual(auth.events, []);
  });
  await test('queried reset continuation retains save identity without submitting another reset', async () => {
    const job = pendingResetJob(), auth = authenticationHarness(job);
    const envelope = await auth.continueAuthentication(job.id);
    assert.equal(envelope.recoveryRequestId, job.id);
    auth.recoveryMaterialDialog(envelope);
    assert.deepEqual(auth.requests, [{ name: 'operation-recover', args: { id: job.id } }]);
    assert.equal(job.materialPending, true);
  });
  for (const state of ['unconfirmed', 'failed']) {
    await test(`startup ${state} reset result never confirms material or starts another reset`, async () => {
      const job = pendingResetJob(); job.state = state;
      const auth = authenticationHarness(job);
      await auth.restorePendingRecoveryMaterials();
      assert.equal(auth.dialogs.length, 0); assert.equal(job.materialSaved, undefined);
      assert.deepEqual(auth.requests.map(r => r.name), ['operations', 'operation-recover']);
    });
  }
  await test('ordinary issued material without reset identity does not create a reset save receipt', async () => {
    const auth = authenticationHarness(pendingResetJob());
    const element = auth.recoveryMaterialDialog({ summary: { super: 'other-issued-material' } });
    element.querySelector('[data-saved]').checked = true;
    await auth.dialogs[0].options.onSubmit();
    assert.deepEqual(auth.requests, []);
    assert.equal(auth.recoveryMaterialDialog({ summary: { ok: true } }), null);
  });
  // Preserve the original 13 session-state cases, now through real mounting and
  // production click dispatch instead of calling copied/extracted page logic.
  const originalCasesStart = passed;
  for (const keys of [false, true]) for (const token of [false, true]) {
    await test(`session constraints keys=${keys} token=${token}`, async () => {
      const page = mount(context({ has_local_keys: keys, has_token: token }));
      assert.equal(page.button('keygen').disabled, keys);
      assert.equal(page.button('keys').disabled, !keys);
      assert.equal(page.button('logout').disabled, !token);
      assert.equal(page.field('token').textContent, token ? '已登录' : '未登录');
      assert.equal(page.field('keys').textContent, keys ? '已保存' : '未初始化');
      page.leave();
    });
  }
  await test('missing summary uses offline defaults', async () => {
    const ctx = context(); ctx.status = {};
    const page = mount(ctx);
    assert.equal(page.field('user').textContent, '本机离线');
    assert.equal(page.field('dir').textContent, '—');
    page.leave();
  });
  await test('identity values remain literal text', async () => {
    const ctx = context({ user: '<b>A</b>' }); ctx.status.summary.data_dir = '/new';
    const page = mount(ctx);
    assert.equal(page.field('user').textContent, '<b>A</b>');
    assert.equal(page.field('dir').textContent, '/new');
    page.leave();
  });
  for (const kind of ['keygen', 'logout', 'login', 'dashboard', 'keys']) {
    await test(`${kind} stays pending across updates and restores constraints`, async () => {
      const ctx = context(allow(kind));
      const page = mount(ctx), gate = deferred();
      handlers.set(commandFor[kind], () => gate.promise);
      page.click(kind); await flush();
      for (let i = 0; i < 25; i++) page.view.update();
      assert.equal(page.button(kind).disabled, true);
      page.click(kind); await flush(); assert.equal(callCount(kind), 1);
      ctx.status.summary.session = { has_local_keys: kind === 'keygen', has_token: false };
      gate.resolve({ summary: {} }); await flush();
      assert.equal(page.button(kind).disabled, ['keygen', 'logout', 'keys'].includes(kind));
      page.leave();
    });
  }
  await test('rejection releases pending state', async () => {
    handlers.set('keygen', async () => { throw Error('expected'); });
    const page = mount(context()); page.click('keygen'); await flush();
    assert.equal(notice.textContent, 'expected');
    assert.equal(page.button('keygen').disabled, false);
    page.leave();
  });
  await test('successful action releases pending state', async () => {
    const page = mount(context()); page.click('keygen'); await flush();
    assert.equal(callCount('keygen'), 1);
    assert.equal(page.button('keygen').disabled, false);
    page.leave();
  });
  assert.equal(passed - originalCasesStart, 13, 'All original cases must run');

  for (const kind of kinds) for (const rejected of [false, true]) {
    await test(`${kind} navigation guard and ${rejected ? 'rejection' : 'completion'} refresh current controls`, async () => {
      const ctx = context(allow(kind)), gate = deferred();
      handlers.set(commandFor[kind], () => gate.promise);
      const first = mount(ctx); first.click(kind); await flush();
      assert.equal(callCount(kind), 1);
      first.leave();
      // Any access through the disposed view during completion is a failure.
      first.root.querySelectorAll = () => { throw Error('disposed view was updated'); };
      const current = mount(ctx);
      assert.equal(current.button(kind).disabled, true, 'remount must render pending without a poll');
      for (let i = 0; i < 3; i++) {
        current.button(kind).disabled = false; // Exercise admission independently of native disabled handling.
        current.click(kind); await flush();
      }
      assert.equal(callCount(kind), 1, 'new buttons must not admit a new request');
      current.view.update(); assert.equal(current.button(kind).disabled, true);
      if (rejected) gate.reject(Error('navigation failure'));
      else gate.resolve({ summary: {} });
      await flush();
      assert.equal(current.button(kind).disabled, false, 'completion must refresh the new page without a poll');
      if (rejected) assert.equal(notice.textContent, 'navigation failure');
      handlers.delete(commandFor[kind]); current.click(kind); await flush();
      assert.equal(callCount(kind), 2, 'a finished action must not leave a stale guard');
      current.leave();
    });
  }
  await test('guard starts before confirmation and cancellation releases it', async () => {
    const gate = deferred(), ctx = context();
    let confirms = 0;
    confirmation = () => { confirms++; return gate.promise; };
    const first = mount(ctx); first.click('keygen'); first.leave();
    const current = mount(ctx);
    assert.equal(current.button('keygen').disabled, true);
    current.button('keygen').disabled = false; current.click('keygen');
    assert.equal(confirms, 1); assert.equal(callCount('keygen'), 0);
    gate.resolve(false); await flush();
    assert.equal(current.button('keygen').disabled, false);
    assert.equal(callCount('keygen'), 0);
    current.leave();
  });
  await test('file-dialog cancellation releases the remounted keys action', async () => {
    const gate = deferred(), ctx = context(allow('keys'));
    fileDialog = () => gate.promise;
    const first = mount(ctx); first.click('keys'); first.leave();
    const current = mount(ctx); assert.equal(current.button('keys').disabled, true);
    current.button('keys').disabled = false; current.click('keys');
    assert.equal(apiCalls.filter(call => call.name === 'file-dialog').length, 1);
    gate.resolve(null); await flush();
    assert.equal(current.button('keys').disabled, false);
    assert.equal(callCount('keys'), 0);
    current.leave();
  });
  await test('different app contexts do not share pending state', async () => {
    const gate = deferred(); handlers.set('login', () => gate.promise);
    const first = mount(context()), other = mount(context());
    first.click('login'); await flush();
    other.view.update(); assert.equal(other.button('login').disabled, false);
    other.click('login'); await flush(); assert.equal(callCount('login'), 2);
    gate.resolve({ summary: {} }); await flush();
    assert.equal(first.button('login').disabled, false);
    assert.equal(other.button('login').disabled, false);
    first.leave(); other.leave();
  });
  await test('unrelated actions are not serialized', async () => {
    const gate = deferred(); handlers.set('login', () => gate.promise);
    const page = mount(context()); page.click('login'); await flush();
    assert.equal(page.button('dashboard').disabled, false);
    page.click('dashboard'); await flush(); assert.equal(callCount('dashboard'), 1);
    assert.equal(page.button('login').disabled, true);
    gate.resolve({ summary: {} }); await flush(); page.leave();
  });
  await test('status loss and account changes cannot reopen a pending action', async () => {
    const gate = deferred(), ctx = context(); handlers.set('login', () => gate.promise);
    const first = mount(ctx); first.click('login'); await flush(); first.leave();
    ctx.status = null;
    const current = mount(ctx); assert.equal(current.button('login').disabled, true);
    ctx.status = { summary: { session: { user: 'another fixture account' }, data_dir: '/another-fixture' } };
    current.view.update(); assert.equal(current.button('login').disabled, true);
    current.button('login').disabled = false; current.click('login'); await flush();
    assert.equal(callCount('login'), 1);
    gate.resolve({ summary: {} }); await flush();
    assert.equal(current.button('login').disabled, false); current.leave();
  });
  await test('late dispose cannot remove the new page completion callback', async () => {
    const gate = deferred(), ctx = context(); handlers.set('login', () => gate.promise);
    const first = mount(ctx); first.click('login'); await flush();
    const current = mount(ctx); first.leave();
    gate.resolve({ summary: {} }); await flush();
    assert.equal(current.button('login').disabled, false); current.leave();
  });
  await test('completion without any mounted page releases the next mount', async () => {
    const gate = deferred(), ctx = context(); handlers.set('login', () => gate.promise);
    const first = mount(ctx); first.click('login'); await flush(); first.leave();
    first.root.querySelectorAll = () => { throw Error('disposed view was updated'); };
    gate.resolve({ summary: {} }); await flush();
    const current = mount(ctx); assert.equal(current.button('login').disabled, false); current.leave();
  });
  for (const kind of ['sync', 'conflicts', 'migration']) {
    await test(`${kind} also preserves its per-action guard across remount`, async () => {
      const gate = deferred(), ctx = context(); handlers.set(commandFor[kind], () => gate.promise);
      const first = mount(ctx); first.click(kind); await flush(); first.leave();
      const current = mount(ctx); assert.equal(current.button(kind).disabled, true);
      current.button(kind).disabled = false; current.click(kind); await flush();
      assert.equal(callCount(kind), 1);
      gate.resolve({ summary: {} }); await flush();
      assert.equal(current.button(kind).disabled, false); current.leave();
    });
  }
  await test('ordinary updates retain session nodes and form drafts', async () => {
    const page = mount(context()), button = page.button('login');
    const field = page.field('user'), session = page.root.querySelector('[data-session]');
    const addr = page.root.querySelector('[name=addr]'); addr.value = 'fixture draft';
    const writes = [page.root.htmlWrites, session.htmlWrites];
    for (let i = 0; i < 25; i++) page.view.update();
    assert.equal(page.button('login'), button); assert.equal(page.field('user'), field);
    assert.equal(page.root.querySelector('[name=addr]'), addr); assert.equal(addr.value, 'fixture draft');
    assert.deepEqual([page.root.htmlWrites, session.htmlWrites], writes);
    page.leave();
  });
  console.log(JSON.stringify({ passed, originalCases: 13, scope: 'Complete production Accounts module plus exact core helpers; Node DOM/dialog/backend stubs, not real browser, focus, native dialog, or released-GUI validation' }, null, 2));
})().catch(error => { console.error(error); process.exitCode = 1; });
