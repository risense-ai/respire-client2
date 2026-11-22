// Minimal DOM/event fixture, deliberately not a browser or native WebView.
// The page and all application modules are loaded unmodified from the tree.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { pathToFileURL } = require("node:url");

const baseline = Boolean(process.argv[2]);
const frontend = process.argv[2] || path.resolve(__dirname, "../../internal/desktop/frontend");
const camel = (key) => key.replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
class Node {
  constructor(tag = "", attributes = {}, text = "") {
    this.tagName = tag.toUpperCase();
    this.attributes = attributes;
    this.dataset = {};
    this.children = [];
    this.style = { setProperty() {}, removeProperty() {} };
    this.listeners = {};
    this.value = "";
    this.disabled = "disabled" in attributes;
    this.text = text;
    for (const [key, value] of Object.entries(attributes))
      if (key.startsWith("data-")) this.dataset[camel(key.slice(5))] = value;
    this.classList = {
      add: (key) => this.classList.toggle(key, true),
      remove: (key) => this.classList.toggle(key, false),
      contains: (key) => (this.attributes.class || "").split(/\s+/).includes(key),
      toggle: (key, on = !this.classList.contains(key)) => {
        const values = new Set((this.attributes.class || "").split(/\s+/).filter(Boolean));
        if (on) values.add(key); else values.delete(key);
        this.attributes.class = [...values].join(" ");
        return on;
      },
    };
  }
  get isConnected() { return this === document || Boolean(this.parent?.isConnected); }
  get className() { return this.attributes.class || ""; }
  set className(value) { this.attributes.class = value; }
  get textContent() { return this.text + this.children.map(node => node.textContent).join(""); }
  set textContent(value) { this.replaceChildren(); this.text = String(value); }
  set innerHTML(html) { this.replaceChildren(); this.insertAdjacentHTML("beforeend", html); }
  insertAdjacentHTML(position, html) {
    assert.equal(position, "beforeend");
    const stack = [this];
    const voids = new Set(["meta", "link", "img", "input", "br", "hr"]);
    for (const item of html.replace(/<!--[\s\S]*?-->|<script\b[^>]*>[\s\S]*?<\/script>/gi, "").matchAll(/<\/?[a-z][^>]*>|[^<]+/gi)) {
      const raw = item[0], parent = stack.at(-1);
      if (!raw.startsWith("<")) { parent.append(new Node("", {}, raw)); continue; }
      if (raw.startsWith("</")) { if (stack.length > 1) stack.pop(); continue; }
      const [,tag, attrs] = raw.match(/^<([\w-]+)(.*)>$/s);
      const attributes = {};
      for (const [,key,dq,sq,bare] of attrs.matchAll(/([-\w:]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s/>]+)))?/g))
        attributes[key] = dq ?? sq ?? bare ?? "";
      const node = new Node(tag, attributes);
      parent.append(node);
      if (!raw.endsWith("/>") && !voids.has(tag)) stack.push(node);
    }
  }
  append(...values) { for (const node of values) { this.children.push(node); node.parent = this; } }
  replaceChildren(...values) { for (const node of this.children) node.parent = null; this.children = []; this.text = ""; this.append(...values); }
  matches(selector) {
    return selector.split(",").some(s => {
      const pieces = s.trim().split(/\s+/), last = pieces.pop();
      const tag = last.match(/^[a-z][\w-]*/i);
      const attr = [...last.matchAll(/\[([-\w]+)(?:=["']?([^\]"']+)["']?)?\]/g)];
      const names = [...last.replace(/\[[^\]]+\]/g, "").matchAll(/([.#])([\w-]+)/g)];
      return (!tag || tag[0].toUpperCase() === this.tagName) &&
        attr.every(([,key,val]) => key in this.attributes && (val === undefined || this.attributes[key] === val)) &&
        names.every(([,op,name]) => op === "." ? this.classList.contains(name) : this.attributes.id === name) &&
        (!pieces.length || Boolean(this.parent?.closest(pieces.join(" "))));
    });
  }
  contains(node) { return this === node || !!node?.parent && this.contains(node.parent); }
  matchesAttributes(selector) { return !!selector && this.matches(selector); }
  closest(selector) { return this.matchesAttributes(selector) ? this : this.parent?.closest(selector) || null; }
  querySelectorAll(selector) { return this.children.flatMap(child => [...(child.matches(selector) ? [child] : []), ...child.querySelectorAll(selector)]); }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  getAttribute(key) { return this.attributes[key] ?? null; }
  setAttribute(key, value) { this.attributes[key] = String(value); }
  hasAttribute(key) { return key in this.attributes; }
  removeAttribute(key) { delete this.attributes[key]; }
  addEventListener(key, callback) { (this.listeners[key] ||= []).push(callback); }
  removeEventListener(key, callback) { this.listeners[key] = (this.listeners[key] || []).filter(fn => fn !== callback); }
  dispatchEvent(event) { for (const fn of this.listeners[event.type] || []) fn(event); }
  async click() {
    if (this.disabled) return;
    const event = { type:"click", detail:1, target:this, preventDefault() {} };
    const promises = [];
    for (let node = this; node; node = node.parent) {
      event.currentTarget = node;
      for (const fn of node.listeners.click || []) promises.push(fn(event));
    }
    await Promise.all(promises);
  }
}

const document = new Node("document");
globalThis.document = document;
document.innerHTML = fs.readFileSync(path.join(frontend, "workspace.html"), "utf8");
document.documentElement = document.querySelector("html");
document.body = document.querySelector("body");
document.createElement = (tag) => new Node(tag);
document.getElementById = (id) => document.querySelector("#" + id);
document.createTreeWalker = () => ({ nextNode: () => false });
globalThis.NodeFilter = { SHOW_TEXT: 4 };
globalThis.window = new Node("window");
globalThis.location = { hash: "#memories", href:"http://fixture.invalid/#memories" };
globalThis.localStorage = { getItem: () => "zh", setItem() {} };
globalThis.matchMedia = () => ({ matches:false, addEventListener() {} });
globalThis.MutationObserver = class { observe() {} };
globalThis.requestAnimationFrame = () => 1;
globalThis.cancelAnimationFrame = () => {};
const polls = [];
globalThis.setTimeout = (callback) => { polls.push(callback); return polls.length; };

const fixture = { account:"A", generation:1, lock:false, statusError:true, held:null, calls:[] };
const snapshot = (result) => ({ ok:true, json:async () => ({ok:true, result}) });
const memory = () => ({
  id:"same-id", title:"Fixture title " + fixture.account, content:"Fixture body " + fixture.account,
  kind:"context", updated_at:"2026-01-01T00:00:00Z",
});
const status = (count = fixture.account === "A" ? 3341 : 1) => ({
  command:"status", status:fixture.lock ? "warn" : "ok",
  summary:{ data_dir:"/shared/synthetic", unlocked:!fixture.lock, local_alive:fixture.lock ? null : count,
    local_total:count, max_updated_at:String(fixture.generation), session:{has_token:true},
    sync_live:{phase:"idle", pending:0, conflicts:0} },
});
globalThis.fetch = async (url, options) => {
  fixture.calls.push(url);
  if (url === "/api/state") return snapshot({
    connected:true, version:"fixture", platform:"fixture", preferences:{language:"zh", onboardingCompleted:true},
    health:{ data_dir:"/shared/synthetic", account_id:fixture.account, instance_id:"synthetic-instance",
      generation:fixture.generation, bin:"fixture", credential_state:fixture.lock ? "required" : "available",
      store_ready:!fixture.lock },
  });
  if (url === "/api/runtime-status") {
    if (fixture.held) { const deferred = fixture.held; fixture.held = null; deferred.started = true; return deferred.promise; }
    if (fixture.statusError) throw new Error("simulated dropped status response");
    return snapshot(status());
  }
  if (url === "/api/operations") return snapshot([]);
  if (url === "/api/operation-ack") return snapshot({});
  if (url === "/api/execute") {
    const {id,name} = JSON.parse(options.body);
    fixture.calls.push(name);
    const envelope = name === "capabilities" ? { summary:{capabilities:{memory_list_metadata:true}} }
      : name === "memory_list" ? { details:[memory()] }
      : name === "memory_show" ? { details:{entry:memory()} }
      : name === "diary_list" ? { details:[] }
      : assert.fail("unexpected memory action " + name);
    return snapshot({id, name, state:"done", envelope});
  }
  assert.fail("unexpected endpoint " + url);
};
const $ = (selector) => document.querySelector(selector);
const reader = () => $(".memory-reader").textContent;
const catalog = () => $(".memory-index").textContent;
const top = () => $("#nav-memory-count").textContent;
const tick = () => new Promise(resolve => setImmediate(resolve));
async function until(predicate, reason) {
  for (let n = 0; n < 20; n++) { if (predicate()) return; await tick(); }
  assert.fail(reason);
}
const pendingResponse = () => {
  const held = {};
  held.promise = new Promise(resolve => { held.resolve = (value) => resolve(snapshot(value)); });
  fixture.held = held;
  return held;
};
(async () => {
  if (baseline) fixture.statusError = false;
  await import(pathToFileURL(path.join(frontend, "client.js")));
  if (!baseline) {
    await until(() => $("[data-retry-status]"), "first dropped connection should offer a reader retry");
    assert.match(reader(), /刷新重试/);
    assert.doesNotMatch(reader(), /尚未解锁/);
    assert.equal(top(), "");
    fixture.statusError = false;
    await $("[data-retry-status]").click();
  }
  await until(() => catalog().includes("Fixture title A"), "retry loads A catalog");
  await $('[data-select="same-id"].memory-title').click();
  await until(() => reader().includes("Fixture body A"), "click loads A body");
  assert.equal(top(), "3,341");
  assert.equal($("[data-edit]").disabled, false);

  fixture.statusError = true;
  await $("#refresh").click();
  assert.match(reader(), /Fixture body A/);
  if (!baseline) {
    assert.equal($("[data-edit]").disabled, true);
    assert.match($("#sync-summary").textContent, /上次状态/);
    assert.equal(top(), "3,341");
  } else {
    assert.match($("#sync-summary").textContent, /状态读取失败/);
    assert.equal(top(), "—");
    assert.equal($("[data-edit]").disabled, false);
    console.log("Baseline reproduces dropped status: recovery label is Status read failed, 3,341 becomes —, and stale Edit is still visible/enabled.");
    await $("#refresh").click();
  }
  fixture.statusError = false;

  fixture.account = "B";
  fixture.generation = 2;
  const held = pendingResponse();
  const refresh = $("#refresh").click();
  await until(() => held.started, "B Health must be consumed before delaying status");
  if (baseline) {
    assert.match(catalog(), /Fixture title A/);
    assert.match(reader(), /Fixture body A/);
    console.log("Baseline reproduces same-root privacy window: after B Health while status was suspended, A catalog/body were still displayed.");
    held.resolve(status());
    await refresh;
    return;
  }
  assert.equal(top(), "");
  assert.doesNotMatch(catalog(), /Fixture title A/);
  assert.doesNotMatch(reader(), /Fixture body A|尚未解锁/);
  assert.equal($("[data-edit]"), null);
  // A delayed response cannot publish if even the same account/path comes
  // back at a different Runtime generation before its closing observation.
  fixture.account = "A";
  fixture.generation = 3;
  held.resolve(status(79));
  await refresh;
  assert.equal(top(), "");
  assert.doesNotMatch(reader(), /Fixture body A/);

  await $("#refresh").click();
  await until(() => catalog().includes("Fixture title A"), "fresh generation can recover");
  await $('[data-select="same-id"].memory-title').click();
  await until(() => reader().includes("Fixture body A"), "fresh generation opens selection");
  fixture.lock = true;
  await $("#refresh").click();
  assert.match(reader(), /尚未解锁/);
  assert.doesNotMatch(reader(), /Fixture body A/);
  assert.equal(top(), "");
  assert.ok(polls.length > 0);
  console.log("Production workspace.html + client.js + memoryWorkspace DOM: initial retry, same-scope display only, delayed same-root ABA, fresh reader, real lock passed");
})().catch(error => { console.error(error); process.exitCode = 1; });
