import {
  $,
  all,
  h,
  api,
  number,
  execute,
  action,
  confirm,
  notify,
  summary,
  panel,
  metric,
  rows,
} from "../ui/core.js";
import { heading } from "./overview.js";

// Session Import: a guided, non-writing preview over the CLI's extract
// contract, then one confirmed write of the previewed result set. The CLI
// re-scans at execution time, so the page never claims a locked snapshot;
// source Harness stores stay read-only and imports are ordinary Respire
// summary memories (one per session, not full transcripts).
const SINCE_CHOICES = [
  ["", "不限时间"],
  ["today", "今天以来"],
  ["yesterday", "昨天以来"],
  ["7d", "近 7 天"],
  ["30d", "近 30 天"],
];
const LIMIT_DEFAULT = 200,
  LIMIT_MAX = 2000;

const DISCLOSURE =
  "导入会为每个会话生成一条摘要记忆（context / important），只保留首末请求与元数据，不是完整逐轮对话。源 Harness 数据只读取、不修改；凭据清洗尽力而为，不保证检出所有敏感信息；导入后的记忆按当前 Runtime 配置可能自动同步。";

function sessionImportMarkup() {
  return (
    `<p class="hint si-disclosure">${h(DISCLOSURE)}</p>` +
    panel(
      "1 · 来源",
      `<div class="panel-body" data-sources><div class="empty">等待读取</div></div>`,
    ) +
    panel(
      "2 · 过滤与预览",
      `<div class="panel-body stack"><div class="form-grid si-filters"><label>时间<select data-since>${SINCE_CHOICES.map(
        ([value, text]) => `<option value="${h(value)}">${h(text)}</option>`,
      ).join("")}</select></label><label>项目路径包含<input data-cwd maxlength="120" placeholder="如 risense" autocomplete="off"></label><label>模型 / 提供方包含<input data-model maxlength="120" placeholder="如 opus" autocomplete="off"></label><label>每源上限（1–${LIMIT_MAX}）<input type="number" data-limit min="1" max="${LIMIT_MAX}" value="${LIMIT_DEFAULT}"></label></div><div class="actions"><button class="primary" data-preview>预览会话</button><span class="hint" data-preview-note></span></div><div data-preview-result class="section-gap"></div></div>`,
    ) +
    panel(
      "3 · 确认导入与结果",
      `<div class="panel-body stack"><p class="hint">导入只处理当前预览中确认过的条件；执行时 CLI 会重新扫描，已导入的会话自动跳过。</p><div class="actions"><button class="primary" data-apply disabled>导入预览的会话</button><button data-open-memories hidden>打开记忆</button><button data-open-operations hidden>查看操作记录</button></div><div data-apply-result class="section-gap"></div></div>`,
    )
  );
}

export function sessionImport(root, ctx) {
  root.innerHTML =
    heading(
      "会话导入。",
      "发现本机其他编码 Agent 工具的会话记录，筛选并预览后导入为摘要记忆。",
    ) + sessionImportMarkup();
  return sessionImportController(root, ctx);
}

// The onboarding wizard mounts the same regions without the page heading.
export function wizardImportStep(root, ctx) {
  root.innerHTML = sessionImportMarkup();
  return sessionImportController(root, ctx);
}

function sessionImportController(root, ctx) {
  let discovery = { state: "idle", sources: [], error: "" };
  // Batch selection over the detected Harnesses (default: all). The CLI
  // takes one --id per invocation, so a subset runs one extraction per
  // selected Harness while the full selection omits --id entirely.
  let selected = new Set();
  let preview = null; // { model, envelope, error, stale }
  let previewToken = 0;
  let previewBusy = false;
  let applyBusy = false;
  let applyResult = null; // { state, results?, error?, requestId? }
  let attempted = false;

  const filterError = () => {
    const limit = Number($("[data-limit]", root).value);
    if (!Number.isInteger(limit) || limit < 1 || limit > LIMIT_MAX)
      return `每源上限必须是 1–${LIMIT_MAX} 的整数`;
    return "";
  };

  // One canonical filter model feeds preview and apply (D2): empty optional
  // strings are omitted, never forwarded. sources records the batch
  // selection; allSelected means the CLI's own all-detected pass (no --id).
  const readModel = () => {
    const installed = detected();
    return {
      sources: [...selected],
      allSelected: installed.length > 0 && selected.size === installed.length,
      since: $("[data-since]", root).value,
      cwd: $("[data-cwd]", root).value.trim(),
      model: $("[data-model]", root).value.trim(),
      limit: Number($("[data-limit]", root).value),
    };
  };

  // Filters shared by every operation of one submission.
  const baseArgs = (model) => {
    const args = {};
    if (model.since) args.since = model.since;
    if (model.cwd) args.cwd = model.cwd;
    if (model.model) args.model = model.model;
    if (Number.isInteger(model.limit) && model.limit >= 1)
      args.limit = model.limit;
    return args;
  };
  const argsFor = (model, id) => {
    const args = baseArgs(model);
    if (id) args.id = id;
    return args;
  };

  const sourceLabel = (id) =>
    discovery.sources.find((item) => item.id === id)?.name || id;

  const selectedNames = (model) =>
    model.allSelected
      ? "全部已检测 Harness"
      : model.sources.map((id) => sourceLabel(id)).join("、");

  const modelText = (model) => {
    const parts = [selectedNames(model)];
    if (model.since)
      parts.push(
        SINCE_CHOICES.find(([value]) => value === model.since)?.[1] ||
          model.since,
      );
    if (model.cwd) parts.push(`路径含「${model.cwd}」`);
    if (model.model) parts.push(`模型含「${model.model}」`);
    parts.push(`每源上限 ${model.limit}`);
    return parts.join(" · ");
  };

  const detected = () => discovery.sources.filter((item) => item.installed);

  async function discover() {
    if (!ctx.desktop?.connected) {
      discovery = {
        state: "offline",
        sources: [],
        error: "Runtime 未连接，无法读取本机 Harness；连接后可重试。",
      };
      renderSources();
      return;
    }
    discovery = { state: "running", sources: discovery.sources, error: "" };
    renderSources();
    try {
      const envelope = await execute("session_import_targets", {});
      if (!root.isConnected) return;
      discovery = {
        state: "done",
        sources: rows(envelope),
        error: "",
      };
      // Keep the selection inside the freshly detected set; an empty
      // selection falls back to all detected.
      const ids = detected().map((item) => item.id);
      selected = new Set([...selected].filter((id) => ids.includes(id)));
      if (!selected.size && ids.length) selected = new Set(ids);
    } catch (error) {
      if (!root.isConnected) return;
      discovery = { state: "error", sources: [], error: error.message };
    }
    invalidatePreview("来源已变化，请重新预览。");
    renderSources();
  }

  // Any source/filter change marks the previous preview invalid; import
  // needs a fresh, matching preview (D2/D4).
  function invalidatePreview(note = "过滤条件已变化，请重新预览。") {
    previewToken++;
    if (preview && !preview.stale)
      preview = { ...preview, stale: true, note };
    renderPreview();
    renderApply();
  }

  async function runPreview() {
    const error = filterError();
    if (error) {
      $("[data-preview-note]", root).textContent = error;
      notify(error, true);
      return;
    }
    if (!detected().length) {
      $("[data-preview-note]", root).textContent = "没有已检测到会话的 Harness 可预览";
      return;
    }
    if (!selected.size) {
      $("[data-preview-note]", root).textContent = "请先勾选至少一个已检测的 Harness";
      return;
    }
    const model = readModel();
    const token = ++previewToken;
    previewBusy = true;
    preview = { model, envelope: null, error: "", stale: false, note: "" };
    renderPreview();
    renderApply();
    try {
      let envelope;
      if (model.allSelected) {
        // The full selection is one CLI pass without --id.
        envelope = await execute("session_import_preview", argsFor(model, null));
        if (token !== previewToken || !root.isConnected) return;
      } else {
        // A subset cannot be expressed as one CLI call: dry-run each
        // selected Harness with the same filters and aggregate.
        envelope = {
          status: "ok",
          summary: { scanned: 0, pending: 0, skipped_imported: 0, deferred_by_limit: 0, by_platform: [] },
          details: { sessions: [] },
          items: [],
          errors: [],
        };
        for (const id of model.sources) {
          const part = await execute("session_import_preview", argsFor(model, id));
          if (token !== previewToken || !root.isConnected) return;
          const ps = summary(part);
          envelope.summary.scanned += ps.scanned ?? 0;
          envelope.summary.pending += ps.pending ?? 0;
          envelope.summary.skipped_imported += ps.skipped_imported ?? 0;
          envelope.summary.deferred_by_limit += ps.deferred_by_limit ?? 0;
          envelope.summary.by_platform.push(...(Array.isArray(ps.by_platform) ? ps.by_platform : []));
          (envelope.details.sessions ||= []).push(...(part.details?.sessions || []));
        }
      }
      preview = { model, envelope, error: "", stale: false, note: "" };
    } catch (caught) {
      if (token !== previewToken || !root.isConnected) return;
      preview = { model, envelope: null, error: caught.message, stale: false, note: "" };
    } finally {
      previewBusy = false;
    }
    renderPreview();
    renderApply();
  }

  async function queryOperation(requestId) {
    try {
      const job = await api("operation?id=" + encodeURIComponent(requestId));
      if (job.state === "done" && job.envelope)
        return { state: "done", envelope: job.envelope };
      if (["failed", "running", "unconfirmed"].includes(job.state))
        return { state: job.state, error: job.error || "写入结果尚未确认。" };
      return { state: "unconfirmed", error: "操作回执尚未确认。" };
    } catch (error) {
      return { state: "unconfirmed", error: error.message };
    }
  }

  // One extraction per selected Harness (or a single all-detected pass).
  // Each Harness reports its own receipt; one failure never blocks the
  // rest, and an uncertain receipt is reconciled against its original
  // operation instead of being resubmitted.
  async function runApply() {
    if (applyBusy) return;
    if (applyResult?.state === "unconfirmed") {
      const uncertain = (applyResult.results || []).filter(
        (entry) =>
          (entry.state === "unconfirmed" || entry.state === "running") &&
          entry.requestId,
      );
      if (!uncertain.length) return;
      applyBusy = true;
      applyResult.state = "running";
      renderApply();
      for (const entry of uncertain) {
        const job = await queryOperation(entry.requestId);
        if (job.state === "done" && job.envelope) {
          entry.state = "done";
          entry.envelope = job.envelope;
        } else {
          entry.state = job.state;
          entry.error = job.error || entry.error;
        }
      }
      applyResult.state = aggregateState(applyResult.results);
      applyBusy = false;
      await afterApplySettled(applyResult);
      renderApply();
      return;
    }
    if (!preview || preview.stale || preview.error || previewBusy) return;
    const selectedPreview = preview;
    const token = previewToken;
    const model = preview.model; // frozen at confirm time (D2)
    const pending = preview.envelope?.summary?.pending ?? 0;
    if (pending <= 0) return;
    if (
      !(await confirm(
        "导入预览的会话",
        `条件：${h(modelText(model))}。预览时待导入 ${number(pending)} 条；执行时 CLI 会重新扫描同条件，已导入会话自动跳过，结果以实际回执为准。确认导入？`,
        "确认导入",
      ))
    )
      return;
    if (applyBusy || preview !== selectedPreview || previewToken !== token) return;
    applyBusy = true;
    // Consume the preview before the first submission so a lost response
    // can never be followed by a second write of the same conditions.
    preview = null;
    previewToken++;
    const ids = model.allSelected
      ? [null]
      : model.sources;
    applyResult = {
      state: "running",
      results: ids.map((id) => ({
        id,
        name: id ? sourceLabel(id) : "全部已检测 Harness",
        state: "running",
      })),
    };
    renderPreview();
    renderApply();
    for (const entry of applyResult.results) {
      try {
        const envelope = await execute(
          "session_import_apply",
          argsFor(model, entry.id),
        );
        entry.state = "done";
        entry.envelope = envelope;
      } catch (error) {
        entry.state = "unconfirmed";
        entry.error = error.message;
        entry.requestId = error.requestId;
        if (error.requestId) {
          const job = await queryOperation(error.requestId);
          if (job.state === "done" && job.envelope) {
            entry.state = "done";
            entry.envelope = job.envelope;
          } else if (job.state !== "unconfirmed" && job.state !== "running") {
            entry.state = job.state;
          }
        }
      }
      applyResult.state = aggregateState(applyResult.results);
      renderApply();
    }
    applyBusy = false;
    await afterApplySettled(applyResult);
    renderSources();
    renderPreview();
    renderApply();
  }

  // Uncertain receipts keep the whole batch on the reconcile path until
  // every operation's outcome is known.
  function aggregateState(results) {
    return results.some(
      (entry) => entry.state === "unconfirmed" || entry.state === "running",
    )
      ? "unconfirmed"
      : "done";
  }

  async function afterApplySettled(result) {
    if (result.state !== "done") return;
    try {
      await ctx.refresh();
    } catch (error) {
      notify(error.message, true);
    }
  }

  function renderSources() {
    const area = $("[data-sources]", root);
    if (!area || !root.isConnected) return;
    if (discovery.state === "running") {
      area.innerHTML = '<div class="empty">正在读取本机 Harness…</div>';
      return;
    }
    if (discovery.state === "offline" || discovery.state === "error") {
      area.innerHTML = `<div class="empty">${h(
        discovery.error,
      )}</div><div class="actions"><button data-rediscover>重新检测</button></div>`;
      $("[data-rediscover]", area)?.addEventListener("click", () =>
        action($("[data-rediscover]", area), discover),
      );
      return;
    }
    const list = discovery.sources;
    if (!list.length) {
      area.innerHTML =
        '<div class="empty">没有返回 Harness 来源</div><div class="actions"><button data-rediscover>重新检测</button></div>';
      $("[data-rediscover]", area)?.addEventListener("click", () =>
        action($("[data-rediscover]", area), discover),
      );
      return;
    }
    const installedList = detected();
    const allChecked = installedList.length > 0 && selected.size === installedList.length;
    const body =
      (!installedList.length
        ? '<p class="hint">未检测到受支持的 Harness 会话存储。</p>'
        : `<label class="si-source${allChecked ? " active" : ""}"><input type="checkbox" data-source-all${
            allChecked ? " checked" : ""
          }><span><strong>全选已检测 Harness</strong><small>${h(
            `已选 ${selected.size} / ${installedList.length} 个`,
          )}</small></span></label>` +
          installedList
            .map(
              (item) =>
                `<label class="si-source${selected.has(item.id) ? " active" : ""}"><input type="checkbox" data-source="${h(
                  item.id,
                )}"${selected.has(item.id) ? " checked" : ""}><span><strong>${h(
                  item.name,
                )}</strong><small>已检测到 · ${(item.roots || []).length} 个存储位置</small></span></label>`,
            )
            .join("")) +
      list
        .filter((item) => !item.installed)
        .map(
          (item) =>
            `<label class="si-source unavailable"><input type="checkbox" disabled><span><strong>${h(
              item.name,
            )}</strong><small>未检测到本机存储</small></span></label>`,
        )
        .join("") +
      '<div class="actions"><button data-rediscover>重新检测</button></div>';
    area.innerHTML = body;
    all('input[data-source]:not([disabled])', area).forEach((box) =>
      box.addEventListener("change", () => {
        if (box.checked) selected.add(box.dataset.source);
        else selected.delete(box.dataset.source);
        invalidatePreview();
        renderSources();
      }),
    );
    all("input[data-source-all]", area).forEach((box) =>
      box.addEventListener("change", () => {
        selected = new Set(
          box.checked ? detected().map((item) => item.id) : [],
        );
        invalidatePreview();
        renderSources();
      }),
    );
    $("[data-rediscover]", area)?.addEventListener("click", () =>
      action($("[data-rediscover]", area), discover),
    );
  }

  const sessionLine = (session) => {
    const parts = [];
    if (session.updated_at || session.created_at)
      parts.push(session.updated_at || session.created_at);
    if (session.cwd) parts.push(session.cwd);
    if (session.model || session.provider)
      parts.push(session.model || session.provider);
    return parts.join(" · ");
  };

  function renderPreview() {
    const note = $("[data-preview-note]", root);
    if (note)
      note.textContent = previewBusy
        ? "正在扫描（不写入任何数据）…"
        : preview?.stale
          ? preview.note || "结果已过期，请重新预览。"
          : "";
    const area = $("[data-preview-result]", root);
    if (!area || !root.isConnected) return;
    if (!preview) {
      area.innerHTML = "";
      return;
    }
    if (preview.error) {
      area.innerHTML = `<div class="empty">${h(
        preview.error,
      )}</div><p class="hint">预览失败可能是当前 Runtime 不支持提取操作；请更新 Runtime 或检查 CLI 版本后重试。</p>`;
      return;
    }
    const s = summary(preview.envelope);
    const sessions = preview.envelope?.details?.sessions || [];
    const pending = s.pending ?? 0;
    area.innerHTML =
      `<div class="metrics">${metric("待导入", number(pending))}${metric(
        "已导入跳过",
        number(s.skipped_imported ?? 0),
      )}${metric("因上限延后", number(s.deferred_by_limit ?? 0))}${metric(
        "扫描到",
        number(s.scanned ?? 0),
      )}</div>` +
      (Array.isArray(s.by_platform) && s.by_platform.length
        ? `<p class="hint">按 Harness：${h(
            s.by_platform.map((item) => `${item.id} ${item.count}`).join(" · "),
          )}</p>`
        : "") +
      (pending === 0
        ? '<div class="empty">没有匹配的新会话；可能全部已导入或条件过严。这不是扫描失败。</div>'
        : `<div class="si-sessions">${sessions
            .map(
              (session) =>
                `<div class="si-session"><div class="row-copy"><strong>${h(
                  session.platform,
                )}</strong><small>${h(sessionLine(session))}</small><p>${h(
                  String(session.first_user_message || "").slice(0, 160),
                )}</p></div><details class="technical"><summary>会话标识</summary><pre>${h(
                  `${session.platform}:${session.id}`,
                )}</pre></details></div>`,
            )
            .join("")}</div>`);
  }

  // Per-Harness receipt line for the aggregated result list.
  const entryLine = (entry) => {
    if (entry.state === "running") return "正在导入…";
    if (entry.state === "unconfirmed") return "结果未确认，请核对操作记录";
    const s = summary(entry.envelope);
    const imported = s.imported ?? 0;
    if (entry.state === "failed") return entry.error || "导入失败";
    return imported > 0 ? `已导入 ${number(imported)} 条` : "没有待导入会话";
  };

  function renderApply() {
    const button = $("[data-apply]", root);
    if (!button || !root.isConnected) return;
    // A zero-pending preview disables import: "nothing matches" is reported
    // by the preview, never submitted as a write (spec).
    const ready = Boolean(
      preview &&
        !preview.stale &&
        !preview.error &&
        !previewBusy &&
        preview.envelope?.summary?.pending > 0,
    );
    const unresolved = applyResult?.state === "unconfirmed";
    button.disabled = applyBusy || (unresolved ? !hasUncertainRequests() : !ready);
    button.textContent = applyBusy
      ? "正在导入…"
      : unresolved
        ? "核对原操作"
        : ready
        ? `导入 ${number(preview.envelope.summary.pending)} 条预览会话`
        : "导入预览的会话";
    // The submission freezes the preview model; filter editing stays locked
    // until the apply settles.
    all("[data-since],[data-cwd],[data-model],[data-limit]", root).forEach(
      (input) => (input.disabled = applyBusy || unresolved),
    );
    const previewButton = $("[data-preview]", root);
    if (previewButton) previewButton.disabled = applyBusy || unresolved;
    const memories = $("[data-open-memories]", root);
    const operations = $("[data-open-operations]", root);
    const showNav = Boolean(applyResult);
    memories.hidden = !showNav;
    operations.hidden = !showNav;
    const area = $("[data-apply-result]", root);
    if (!area) return;
    if (!applyResult || applyResult.state === "running") {
      area.innerHTML = applyResult?.state === "running"
        ? '<div class="empty">正在导入；可以离开本页，稍后在操作记录查看进度。</div>'
        : "";
      return;
    }
    if (applyResult.state === "unconfirmed") {
      const entries = (applyResult.results || [])
        .map(
          (entry) =>
            `<div class="row"><span class="dot ${
              entry.state === "done" ? "online" : ""
            }"></span><strong>${h(entry.name)}</strong><small>${h(
              entryLine(entry),
            )}</small></div>`,
        )
        .join("");
      area.innerHTML =
        `<div class="empty">${h(
          "写入结果未确认：请到操作记录核对该请求，不要自动重试。",
        )}</div>` +
        (entries ? `<div class="stack">${entries}</div>` : "") +
        '<p class="hint">点击「核对原操作」仅重新读取原请求的回执，不会再次写入。</p>';
      return;
    }
    const results = applyResult.results || [];
    const doneEntries = results.filter((entry) => entry.state === "done");
    const failedEntries = results.filter((entry) => entry.state === "failed");
    const totals = doneEntries.reduce(
      (acc, entry) => {
        const s = summary(entry.envelope);
        acc.imported += s.imported ?? 0;
        acc.scanned += s.scanned ?? 0;
        acc.deferred += s.deferred_by_limit ?? 0;
        return acc;
      },
      { imported: 0, scanned: 0, deferred: 0 },
    );
    const rows = results
      .map(
        (entry) =>
          `<div class="row"><span class="dot ${
            entry.state === "done" ? "online" : ""
          }"></span><strong>${h(entry.name)}</strong><small>${h(
            entryLine(entry),
          )}</small></div>`,
      )
      .join("");
    area.innerHTML =
      `<div class="metrics">${metric("已导入", number(totals.imported))}${metric(
        "扫描到",
        number(totals.scanned),
      )}${metric("因上限延后", number(totals.deferred))}</div>` +
      (results.length > 1 ? `<div class="stack">${rows}</div>` : "") +
      (failedEntries.length
        ? '<p class="hint">导入可能在部分写入后失败；失败 Harness 的会话以操作记录为准，重新预览后再决定是否继续。</p>'
        : totals.imported === 0
          ? '<p class="hint">本次没有写入新记忆：匹配会话此前已导入或被安全跳过。</p>'
          : '<p class="hint">导入的记忆为普通摘要记忆；可在「整理」中进一步归档。</p>');
  }

  const hasUncertainRequests = () =>
    (applyResult?.results || []).some(
      (entry) =>
        (entry.state === "unconfirmed" || entry.state === "running") &&
        entry.requestId,
    );

  root.addEventListener("click", (event) => {
    const button = event.target.closest("button");
    if (!button) return;
    if (button.hasAttribute("data-preview")) action(button, runPreview);
    if (button.hasAttribute("data-apply")) action(button, runApply);
    if (button.hasAttribute("data-open-memories")) ctx.navigate("memories");
    if (button.hasAttribute("data-open-operations")) ctx.navigate("operations");
  });
  // Editing any filter invalidates a previous preview immediately (D2).
  root.addEventListener("input", (event) => {
    if (event.target.closest("[data-since],[data-cwd],[data-model],[data-limit]"))
      invalidatePreview();
  });

  if (ctx.desktop?.connected) {
    attempted = true;
    discover();
  } else {
    // Disconnected at mount: the honest offline state with a retry, never
    // an empty-sources result.
    discovery = {
      state: "offline",
      sources: [],
      error: "Runtime 未连接，无法读取本机 Harness；连接后可重试。",
    };
    renderSources();
  }
  return {
    refresh: () => discover(),
    update() {
      if (!attempted && ctx.desktop?.connected) {
        attempted = true;
        discover();
      }
    },
  };
}
