import {
  $,
  h,
  execute,
  action,
  notify,
  panel,
  raw,
  input,
  dialog,
  showText,
  rows,
} from "../ui/core.js";
import { heading } from "../pages/overview.js";
import { entry, list } from "./model.js";

const pad = (value) => String(value).padStart(2, "0");
const dayOf = (date) =>
  `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
export function diary(root, ctx) {
  const now = new Date();
  let month = `${now.getFullYear()}-${pad(now.getMonth() + 1)}`,
    items = [],
    loading = false;
  root.innerHTML =
    heading("把日常，留在日历里。", "按日期查看活动轨迹，或检索日记片段。") +
    `<div class="memory-toolbar"><button data-prev aria-label="上个月">‹</button><input type="month" name="month" aria-label="日记月份" value="${month}"><button data-next aria-label="下个月">›</button><button data-today>本月</button><button data-search>搜索日记片段</button></div><div class="grid"><div class="panel"><div data-calendar class="calendar"></div></div><section class="panel"><div class="panel-heading"><h2 data-day-title>选择日期</h2></div><div data-entries class="empty">选择日期查看当天记录。</div></section></div>`;
  function render() {
    const [year, m] = month.split("-").map(Number),
      first = new Date(year, m - 1, 1),
      lead = (first.getDay() + 6) % 7,
      total = new Date(year, m, 0).getDate();
    const groups = new Map();
    for (const item of items) {
      const day =
        String(item.title || "").match(/\d{4}-\d{2}-\d{2}/)?.[0] ||
        String(item.created_at || "").slice(0, 10);
      if (!groups.has(day)) groups.set(day, []);
      groups.get(day).push(item);
    }
    $("[data-calendar]", root).innerHTML =
      ["一", "二", "三", "四", "五", "六", "日"]
        .map((day) => `<span class="calendar-weekday">${day}</span>`)
        .join("") +
      Array.from(
        { length: lead },
        () => '<span class="calendar-blank"></span>',
      ).join("") +
      Array.from({ length: total }, (_, i) => {
        const day = `${month}-${pad(i + 1)}`,
          count = groups.get(day)?.length || 0;
        return `<button class="calendar-day ${count ? "has-entries" : ""} ${day === dayOf(now) ? "today" : ""}" data-day="${day}"><span>${i + 1}</span>${count ? `<small>${count} 条</small>` : ""}</button>`;
      }).join("");
    function select(day) {
      $("[data-day-title]", root).textContent = day;
      const entries = groups.get(day) || [];
      $("[data-entries]", root).className = "";
      $("[data-entries]", root).innerHTML = entries.length
        ? `<ul class="clean">${entries.map((item) => `<li><button data-entry="${h(item.id)}">${h(item.title)}</button></li>`).join("")}</ul>`
        : '<div class="empty">当天没有返回日记记录</div>';
    }
    $("[data-calendar]", root).onclick = (event) => {
      const button = event.target.closest("[data-day]");
      if (button) select(button.dataset.day);
    };
    if (month === dayOf(now).slice(0, 7)) select(dayOf(now));
  }
  async function load() {
    if (loading) return;
    loading = true;
    try {
      const [year, m] = month.split("-").map(Number),
        last = new Date(year, m, 0).getDate();
      const result = await execute("diary_list", {
        from: month + "-01",
        to: `${month}-${pad(last)}`,
      });
      if (!root.isConnected) return;
      items = list(result);
      render();
    } catch (error) {
      if (root.isConnected)
        $("[data-entries]", root).innerHTML =
          `<div class="empty danger-text">${h(error.message)}</div>`;
    } finally {
      loading = false;
    }
  }
  root.addEventListener("click", (event) => {
    const button = event.target.closest("button");
    if (!button) return;
    if (
      button.hasAttribute("data-prev") ||
      button.hasAttribute("data-next") ||
      button.hasAttribute("data-today")
    ) {
      if (button.hasAttribute("data-today")) month = dayOf(now).slice(0, 7);
      else {
        const [year, m] = month.split("-").map(Number);
        const next = new Date(
          year,
          m - 1 + (button.hasAttribute("data-next") ? 1 : -1),
          1,
        );
        month = `${next.getFullYear()}-${pad(next.getMonth() + 1)}`;
      }
      $("[name=month]", root).value = month;
      load();
    }
    if (button.dataset.entry)
      action(button, async () => {
        const memory = entry(
          await execute("memory_show", { id: button.dataset.entry }),
        );
        showText(memory.title, memory.content || "");
      });
    if (button.hasAttribute("data-search"))
      dialog({
        title: "搜索日记片段",
        body:
          input("query", "查询内容", "", { required: true }) +
          "<div data-hits></div>",
        submit: "搜索",
        wide: true,
        onSubmit: async (values, element) => {
          const result = await execute("diary_search", {
            query: values.query,
            limit: 20,
            max_chars: 8000,
            mode: "hybrid",
          });
          const hits = rows(result, "fragments");
          $("[data-hits]", element).innerHTML = hits.length
            ? hits
                .map(
                  (hit) =>
                    `<div class="suggestion-card"><div class="row-copy"><strong>${h(hit.title || hit.source_id)}</strong><p>${h(hit.text || hit.content || hit.snippet || "")}</p><small>${h(hit.source_id)} / ${h(hit.fragment_id)} / ${h(hit.source_version)}</small></div><button type="button" data-source="${h(hit.source_id)}" data-fragment="${h(hit.fragment_id)}">展开</button></div>`,
                )
                .join("")
            : `<pre class="code">${h(raw(result))}</pre>`;
          $("[data-hits]", element).onclick = (event) => {
            const control = event.target.closest("[data-fragment]");
            if (control)
              action(control, async () => {
                const fragment = await execute("diary_fragment", {
                  source_id: control.dataset.source,
                  fragment_id: control.dataset.fragment,
                  neighbors: 1,
                  max_chars: 8000,
                });
                showText("日记片段", raw(fragment.details || fragment.summary));
              });
          };
          return false;
        },
      });
  });
  $("[name=month]", root).addEventListener("change", (event) => {
    if (event.target.value) {
      month = event.target.value;
      load();
    }
  });
  render();
  if (ctx.desktop?.connected) load();
  let attempted = Boolean(ctx.desktop?.connected);
  return {
    refresh: load,
    update() {
      if (!attempted && ctx.desktop?.connected) {
        attempted = true;
        load();
      }
    },
  };
}
