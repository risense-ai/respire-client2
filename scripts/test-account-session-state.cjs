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
let passed = 0;
async function test(name, run) {
  reset();
  await run();
  passed++;
  console.log(`ok ${passed} - ${name}`);
}

(async () => {
  // Preserve the original 13 session-state cases, now through real mounting and
  // production click dispatch instead of calling copied/extracted page logic.
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
  assert.equal(passed, 13, 'All original cases must run');

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
