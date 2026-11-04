import { $, h, number, panel, metric, execute } from "../ui/core.js";
import { heading } from "./overview.js";

function rate(hits, recalls) {
  if (!recalls) return "今日还没有召回";
  return `${Math.round((hits / recalls) * 100)}% 有结果`;
}

function localDay() {
  const now = new Date();
  const month = String(now.getMonth() + 1).padStart(2, "0");
  const day = String(now.getDate()).padStart(2, "0");
  return `${now.getFullYear()}-${month}-${day}`;
}

export function stats(root, ctx) {
  root.innerHTML =
    heading(
      "本地召回统计",
      "只统计这台机器上的召回。成功命中是一次召回至少返回一条候选，不上传。",
      `<button data-reload>刷新</button>`,
    ) +
    `<div data-body><div class="panel empty">正在读取本机召回日志。</div></div>`;
  async function load() {
    const body = $("[data-body]", root);
    if (!ctx.desktop?.connected) {
      body.innerHTML = `<div class="panel empty">Runtime 未连接，暂时读不到本机召回日志。</div>`;
      return;
    }
    try {
      const envelope = await execute("query_log", { stats: true, limit: 1 });
      if (!root.isConnected) return;
      const details = envelope?.details || {};
      const daily = Array.isArray(details.daily) ? details.daily : [];
      const today = daily.find((row) => row.day === localDay()) || {
        day: localDay(),
        recalls: 0,
        hits: 0,
        adopted: 0,
        good: 0,
      };
      const rows = [...daily].reverse();
      body.innerHTML =
        `<div class="metrics">${metric("今日召回", number(today.recalls), today.day)}${metric(
          "今日成功命中",
          number(today.hits),
          rate(today.hits, today.recalls),
        )}${metric("累计召回", number(details.total), `空结果 ${number(details.empty_candidates)}`)}${metric(
          "累计采纳",
          number(details.with_adopted),
          `自评有用 ${number(details.good_total)}`,
        )}</div>` +
        panel(
          "最近 30 天",
          rows.length
            ? `<div class="scroll-table"><table><thead><tr><th>日期</th><th>召回</th><th>成功命中</th><th>采纳</th><th>自评有用</th></tr></thead><tbody>${rows
                .map(
                  (row) =>
                    `<tr><td>${h(row.day)}</td><td>${h(number(row.recalls))}</td><td>${h(number(row.hits))}</td><td>${h(number(row.adopted))}</td><td>${h(number(row.good))}</td></tr>`,
                )
                .join("")}</tbody></table></div>`
            : `<div class="panel-body"><p class="hint">还没有召回记录。正常使用后，这里会按本机日期累计。</p></div>`,
        );
      const badge = document.querySelector("#nav-stats-count");
      if (badge) badge.textContent = today.recalls ? rate(today.hits, today.recalls) : "";
    } catch (error) {
      if (!root.isConnected) return;
      body.innerHTML = `<div class="panel empty">${h(error.message || "读取失败")}</div>`;
    }
  }
  $("[data-reload]", root).onclick = () => load();
  load();
  return { dispose() {} };
}
