import {
  $,
  all,
  h,
  number,
  date,
  bytes,
  metric,
  panel,
  pairs,
  execute,
  action,
  isPending,
  notify,
} from "../ui/core.js";

const phases = {
  idle: "空闲",
  running: "正在同步",
  ok: "已完成",
  err: "同步失败",
  scheduled: "已安排",
  ready: "就绪",
  failed: "失败",
  loading: "加载中",
  unavailable: "不可用",
};
export const phase = (value) => phases[value] || value || "—";
export function heading(title, description, actions = "") {
  return `<div class="page-heading"><div><div class="eyebrow">RESPIRE / LOCAL WORKSPACE</div><h1>${h(title)}</h1><p>${h(description)}</p></div><div class="heading-actions">${actions}</div></div>`;
}
export function overview(root, ctx) {
  function update() {
    const state = ctx.desktop || {},
      s = ctx.status?.summary || {},
      health = state.health || {},
      sync = s.sync_live || {},
      index = s.retrieval_index || {},
      inference = health.inference || {},
      op = s.model_operation || {},
      session = s.session || {};
    const nowTask = state.task || {};
    root.innerHTML =
      heading(
        "记忆，保持就绪。",
        "本机数据、云端同步、模型与索引，集中查看。",
        `<button data-go="memories" class="primary">查看记忆</button>`,
      ) +
      `<div class="metrics">${metric("本机记忆", number(s.local_alive), `总计 ${number(s.local_total)} 条，包含已删除记录`)}${metric("云端记忆", number(sync.remote_alive), s.remote_configured ? "已配置云端" : "尚未连接云端")}${metric("待同步", number(sync.pending), `${number(sync.conflicts)} 项冲突`)}${metric("索引状态", phase(index.state), index.error || "")}</div>` +
      `<div class="grid">${panel(
        "同步",
        pairs([
          ["状态", phase(sync.phase)],
          [
            "自动同步",
            s.autosync === undefined ? "—" : s.autosync ? "已开启" : "已关闭",
          ],
          [
            "已拉取 / 已推送",
            `${number(sync.pulled)} / ${number(sync.pushed)}`,
          ],
          [
            "最近成功",
            s.sync_scheduler?.last_success_unix
              ? date(s.sync_scheduler.last_success_unix * 1000)
              : "尚未报告",
          ],
          ["账户", session.user || "本机离线"],
          ["服务地址", s.server_addr],
        ]) +
          `<div class="panel-body"><button data-command="sync" ${isPending("sync") ? "disabled" : ""}>${isPending("sync") ? "同步中…" : "立即同步"}</button>${sync.error ? `<p class="form-error section-gap">${h(sync.error)}</p>` : ""}</div>`,
      )}
      ${panel(
        "模型与索引",
        pairs([
          ["推理状态", phase(inference.phase)],
          ["推理队列", number(inference.queued)],
          [
            "后台索引",
            index.running
              ? "正在构建"
              : index.scheduled
                ? "等待执行"
                : phase(index.state),
          ],
          [
            "模型任务",
            op.active ? `${phase(op.phase)} · ${op.item || ""}` : "当前无任务",
          ],
        ]) +
          `<div class="panel-body">${op.active ? `<div class="progress-label"><span>${h(op.item || op.phase)}</span><span>${h(bytes(op.done))} / ${h(bytes(op.total))}</span></div><progress ${op.total ? `value="${h(op.done)}" max="${h(op.total)}"` : ""}></progress>` : ""}<button data-go="models">管理模型与索引</button></div>`,
      )}</div>` +
      panel(
        "Runtime",
        pairs([
          ["连接", state.connected ? "已连接" : "未连接"],
          ["版本", health.bin || "—"],
          [
            "管理方式",
            state.managed
              ? "客户端管理"
              : state.connected
                ? "外部 CLI 管理"
                : "未连接",
          ],
          [
            "运行中 / 排队",
            `${number(health.rpc?.executing_until_reply)} / ${number(health.rpc?.queued_or_cancelled_pending_removal)}`,
          ],
          ["客户端当前操作", nowTask.stage || "—"],
        ]) +
          `<div class="panel-body actions"><button data-host="start" ${nowTask.busy ? "disabled" : ""}>启动 Runtime</button><button data-host="stop" ${nowTask.busy || !state.connected ? "disabled" : ""}>停止 Runtime</button><button data-go="updates">安装与更新</button></div>${nowTask.error ? `<div class="panel-body form-error">${h(nowTask.error)}</div>` : ""}`,
      );
    if (!session.has_local_keys && state.connected)
      root.insertAdjacentHTML(
        "beforeend",
        `<div class="panel section-gap"><div class="panel-body row spread"><div><strong>开始使用你的本机记忆库</strong><p class="hint">登录账户，或初始化离线记忆库。</p></div><button data-go="accounts">账户与本机密钥</button></div></div>`,
      );
    if (ctx.statusError)
      root.insertAdjacentHTML(
        "beforeend",
        `<p class="form-error section-gap">状态读取失败：${h(ctx.statusError)}</p>`,
      );
  }
  root.addEventListener("click", (event) => {
    const button = event.target.closest("button");
    if (!button) return;
    if (button.dataset.go) ctx.navigate(button.dataset.go);
    if (button.dataset.command)
      action(button, async () => {
        await execute(button.dataset.command);
        notify("同步完成");
        await ctx.refresh();
      });
    if (button.dataset.host)
      action(button, async () => {
        await ctx.api(button.dataset.host, {});
        await ctx.refresh();
      });
  });
  update();
  return { update, refresh: update };
}
