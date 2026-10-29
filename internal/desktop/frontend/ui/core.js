export const $ = (selector, root = document) => root.querySelector(selector);
export const all = (selector, root = document) => [
  ...root.querySelectorAll(selector),
];
export const h = (value) =>
  String(value ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
export const number = (value) =>
  typeof value === "number" ? value.toLocaleString() : "—";
export const date = (value) => (value ? new Date(value).toLocaleString() : "—");
export const bytes = (value) =>
  typeof value === "number"
    ? value < 1048576
      ? `${(value / 1024).toFixed(1)} KiB`
      : `${(value / 1048576).toFixed(1)} MiB`
    : "—";
export const summary = (envelope) => envelope?.summary || {};
export function rows(envelope, key) {
  if (Array.isArray(envelope?.details)) return envelope.details;
  if (key && Array.isArray(envelope?.details?.[key]))
    return envelope.details[key];
  if (key && Array.isArray(envelope?.summary?.[key]))
    return envelope.summary[key];
  return [];
}
export const raw = (value) => JSON.stringify(value, null, 2);
export function notify(message, error = false) {
  const node = $("#notice");
  node.textContent = message;
  node.hidden = !message;
  node.classList.toggle("error", error);
}
export async function api(path, body) {
  const response = await fetch(
    "/api/" + path,
    body === undefined
      ? {}
      : {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            "X-Respire-Client": "1",
          },
          body: JSON.stringify(body),
        },
  );
  const data = await response.json();
  if (!response.ok || !data.ok)
    throw new Error(data.error || `请求失败 (${response.status})`);
  return data.result;
}
const pause = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const pending = new Map();
export const isPending = (name) => [...pending.values()].includes(name);
export async function execute(
  name,
  args = {},
  { secret = "", onProgress } = {},
) {
  const id = "desktop-" + crypto.randomUUID();
  let job = await api("execute", { name, id, args, confirmed: true, secret });
  pending.set(id, name);
  try {
    let rounds = 0;
    while (job.state === "running") {
      onProgress?.(job);
      await pause(rounds++ < 5 ? 120 : 500);
      job = await api("operation?id=" + encodeURIComponent(id));
    }
    if (job.state !== "done") {
      const error = new Error(
        job.error ||
          `请求 ${id} 的结果未确认。请在操作记录中查询，不要重复提交。`,
      );
      error.requestId = id;
      error.envelope = job.envelope;
      throw error;
    }
    return job.envelope;
  } catch (error) {
    if (job.state === "running") {
      error.message = `请求 ${id} 的回执读取失败，结果尚未确认。请到操作记录查询原请求，不要重复提交。${error.message}`;
      error.requestId = id;
    }
    throw error;
  } finally {
    pending.delete(id);
    if (job.state === "done" || job.state === "failed")
      api("operation-ack", { id }).catch((error) =>
        notify(error.message, true),
      );
    document.dispatchEvent(
      new CustomEvent("respire-operation", {
        detail: { name, id, state: job.state },
      }),
    );
  }
}
export async function action(button, fn) {
  if (button.disabled) return;
  button.disabled = true;
  try {
    return await fn();
  } catch (error) {
    notify(error.message, true);
  } finally {
    button.disabled = false;
  }
}
export function panel(title, body, extra = "") {
  return `<section class="panel"><div class="panel-heading"><h2>${h(title)}</h2>${extra}</div>${body}</section>`;
}
export function pairs(items) {
  return `<dl class="facts">${items.map(([label, value]) => `<div><dt>${h(label)}</dt><dd>${h(value ?? "—")}</dd></div>`).join("")}</dl>`;
}
export function metric(label, value, note = "") {
  return `<div class="metric"><span>${h(label)}</span><strong>${h(value)}</strong><small>${h(note)}</small></div>`;
}
export function input(name, label, value = "", options = {}) {
  return `<label>${h(label)}<input name="${h(name)}" value="${h(value)}" type="${h(options.type || "text")}" ${options.required ? "required" : ""} ${options.placeholder ? `placeholder="${h(options.placeholder)}"` : ""} autocomplete="${options.type === "password" ? "new-password" : "off"}"></label>`;
}
export function select(name, label, items, value) {
  return `<label>${h(label)}<select name="${h(name)}">${items
    .map((item) => {
      const [key, text] = Array.isArray(item) ? item : [item, item];
      return `<option value="${h(key)}" ${key === value ? "selected" : ""}>${h(text)}</option>`;
    })
    .join("")}</select></label>`;
}
export function dialog({
  title,
  body,
  submit = "保存",
  onSubmit,
  wide = false,
}) {
  const element = document.createElement("dialog");
  element.className = wide ? "dialog wide" : "dialog";
  element.innerHTML = `<form><div class="dialog-heading"><h2>${h(title)}</h2><button type="button" class="icon-button" data-close aria-label="关闭">×</button></div><div class="dialog-body">${body}<p class="form-error" role="alert" hidden></p></div><footer><button type="button" data-close>取消</button><button class="primary" type="submit">${h(submit)}</button></footer></form>`;
  document.body.append(element);
  let busy = false;
  all("[data-close]", element).forEach((button) =>
    button.addEventListener("click", () => {
      if (!busy) element.close();
    }),
  );
  element.addEventListener("cancel", (event) => {
    if (busy) event.preventDefault();
  });
  element.addEventListener("close", () => element.remove(), { once: true });
  $("form", element).addEventListener("submit", async (event) => {
    event.preventDefault();
    if (busy) return;
    busy = true;
    const button = $("[type=submit]", element);
    button.disabled = true;
    const errorNode = $(".form-error", element);
    errorNode.hidden = true;
    try {
      const value = await onSubmit(
        Object.fromEntries(new FormData(event.target)),
        element,
      );
      if (value !== false) element.close();
    } catch (error) {
      errorNode.textContent = error.message;
      errorNode.hidden = false;
    } finally {
      busy = false;
      button.disabled = false;
    }
  });
  element.showModal();
  return element;
}
export async function confirm(title, message, label = "确认执行") {
  return new Promise((resolve) => {
    let accepted = false;
    const element = dialog({
      title,
      body: `<p>${h(message)}</p>`,
      submit: label,
      onSubmit: () => {
        accepted = true;
        resolve(true);
      },
    });
    element.addEventListener(
      "close",
      () => {
        if (!accepted) resolve(false);
      },
      { once: true },
    );
  });
}
export function showText(title, text) {
  const element = dialog({
    title,
    body: `<textarea class="reading-text" aria-label="${h(title)}" readonly rows="16">${h(text)}</textarea>`,
    submit: "复制",
    onSubmit: async () => {
      await navigator.clipboard.writeText(text);
      notify("已复制");
      return false;
    },
    wide: true,
  });
  return element;
}
export function resultView(title, envelope) {
  const details = envelope?.details;
  const body =
    Array.isArray(envelope?.items) && envelope.items.length
      ? `<div class="result-lines">${envelope.items.map((item) => `<div><span>${h(item.name)}</span><strong class="${item.status === "fail" ? "danger-text" : ""}">${h(typeof item.value === "object" ? raw(item.value) : item.value)}</strong></div>`).join("")}</div>`
      : pairs(
          Object.entries(summary(envelope)).filter(
            ([, value]) => value === null || typeof value !== "object",
          ),
        );
  return panel(
    title,
    (Array.isArray(envelope?.details?.warnings) &&
    envelope.details.warnings.length
      ? `<div class="panel-body warning-text">${envelope.details.warnings.map(h).join("<br>")}</div>`
      : "") +
      (Array.isArray(envelope?.errors) && envelope.errors.length
        ? `<div class="panel-body form-error" role="alert">${envelope.errors.map(h).join("<br>")}</div>`
        : "") +
      body +
      (details && typeof details === "object"
        ? `<details class="technical"><summary>详细回执</summary><pre>${h(raw(details))}</pre></details>`
        : ""),
  );
}
