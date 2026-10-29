import {
  $,
  h,
  summary,
  panel,
  input,
  select,
  execute,
  action,
  confirm,
  resultView,
} from "../ui/core.js";

export function recallChecks(root) {
  const section = document.createElement("section");
  section.className = "section-gap";
  section.innerHTML = panel(
    "召回验证",
    `<form class="panel-body stack"><div class="form-grid">${input("query", "测试查询", "", { required: true })}${select(
      "mode",
      "验证方式",
      [
        ["fast", "快速召回"],
        ["quality", "高质量召回"],
        ["benchmark", "推理与召回 Benchmark"],
      ],
      "fast",
    )}</div><p class="hint">验证不改变默认召回方式。高质量查询与候选标题会发送给已配置的模型服务。Benchmark 只使用本机 Runtime，运行首轮及后续 8 轮，耗时包含 RPC 等待。</p><div class="actions"><button type="submit">开始验证</button><button type="button" data-stop disabled>停止后续轮次</button></div><div data-check-result></div></form>`,
  );
  root.append(section);
  let stopped = false;
  $("[data-stop]", section).onclick = () => {
    stopped = true;
  };
  $("form", section).onsubmit = (event) => {
    event.preventDefault();
    action($("[type=submit]", section), async () => {
      const { query, mode } = Object.fromEntries(new FormData(event.target));
      if (
        mode === "quality" &&
        !(await confirm(
          "测试高质量召回",
          "本次查询和候选标题将发送给配置的模型服务。",
          "开始验证",
        ))
      )
        return;
      const output = $("[data-check-result]", section);
      stopped = false;
      $("[data-stop]", section).disabled = false;
      try {
        if (mode !== "benchmark") {
          const started = performance.now();
          const result = await execute("memory_search", {
            query,
            mode,
            titles: true,
            limit: 20,
          });
          if (!section.isConnected) return;
          output.innerHTML = `<p class="hint">耗时 ${(performance.now() - started).toFixed(0)} ms · 返回 ${h(summary(result).count)} 条</p>${summary(result).selection_fallback ? `<p class="warning-text">高质量服务未完成筛选：${h(summary(result).selection_fallback)}</p>` : ""}${resultView("召回回执", result)}`;
          return;
        }
        const results = [];
        let engine = "";
        for (let i = 0; i < 9 && !stopped && section.isConnected; i++) {
          output.innerHTML = `<p class="hint">正在验证第 ${i + 1} / 9 轮，完成 ${results.length} 轮。</p>`;
          let started = performance.now();
          const probe = summary(
            await execute("model_probe", { text: `${query} [benchmark ${i}]` }),
          );
          const wall = performance.now() - started;
          if (typeof probe.elapsed_ms !== "number" || !probe.selected)
            throw new Error("Runtime 未返回推理耗时与引擎");
          if (engine && probe.selected !== engine)
            throw new Error("验证期间推理引擎已变化，请重新验证");
          engine = probe.selected;
          if (stopped || !section.isConnected) break;
          started = performance.now();
          const recalled = summary(
            await execute("memory_search", {
              query,
              mode: "fast",
              titles: true,
              limit: 20,
            }),
          );
          results.push({
            round: i,
            inference: probe.elapsed_ms,
            wall,
            recall: performance.now() - started,
            count: recalled.count,
            indexed: recalled.indexed_candidates,
            pending: recalled.index_pending,
          });
        }
        if (!section.isConnected) return;
        output.innerHTML = `<p class="hint">${stopped ? "已停止后续轮次" : "验证完成"} · 引擎 ${h(engine)} · 首轮单列</p><div class="scroll-table"><table><thead><tr><th>轮次</th><th>推理 ms</th><th>推理含 RPC ms</th><th>召回含 RPC ms</th><th>结果 / 索引 / 待索引</th></tr></thead><tbody>${results.map((r) => `<tr><td>${r.round === 0 ? "首轮" : r.round}</td><td>${r.inference.toFixed(1)}</td><td>${r.wall.toFixed(1)}</td><td>${r.recall.toFixed(1)}</td><td>${h(r.count)} / ${h(r.indexed)} / ${h(r.pending)}</td></tr>`).join("")}</tbody></table></div>`;
      } finally {
        $("[data-stop]", section).disabled = true;
      }
    });
  };
  return () => {
    stopped = true;
  };
}
