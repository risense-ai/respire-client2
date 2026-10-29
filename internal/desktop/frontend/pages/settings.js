import {
  $,
  all,
  h,
  raw,
  api,
  execute,
  action,
  confirm,
  notify,
  number,
  date,
  bytes,
  summary,
  panel,
  pairs,
  resultView,
  select,
} from "../ui/core.js";
import { heading } from "./overview.js";
import { typographyPanel } from "../ui/typography.js";
import { storagePanel } from "./storage.js";

function setting(label, description, control) {
  return `<div class="setting-row"><div><strong>${h(label)}</strong><p>${h(description)}</p></div>${control}</div>`;
}
export function settings(root, ctx) {
  let initialized = false,
    agentAttempted = false;
  root.innerHTML =
    heading("设置", "外观、本机启动和 Runtime 行为，分别管理。") +
    panel(
      "外观",
      setting(
        "主题",
        "图标、界面和原生窗口随主题切换。",
        `<select data-pref="theme"><option value="system">跟随系统</option><option value="light">亮色</option><option value="dark">暗色</option></select>`,
      ) +
        setting(
          "布局",
          "左右侧栏，或上下紧凑布局。",
          `<select data-pref="layout"><option value="sidebar">侧栏布局</option><option value="compact">紧凑布局</option></select>`,
        ),
    ) +
    panel(
      "启动与更新",
      setting(
        "登录系统后启动",
        "系统登录时以托盘方式启动客户端。",
        '<input data-autostart type="checkbox" aria-label="登录系统后启动">',
      ) +
        setting(
          "自动连接 Runtime",
          "没有可连接的 Runtime 时启动客户端管理的版本。",
          '<input data-pref="autoRuntime" type="checkbox" aria-label="自动连接 Runtime">',
        ) +
        setting(
          "自动检查和下载更新",
          "客户端和 CLI 分别使用下面选择的通道。",
          '<input data-pref="autoUpdate" type="checkbox" aria-label="自动更新">',
        ) +
        setting(
          "CLI 发布通道",
          "切换通道不会自动降级已经安装的版本。",
          '<select data-pref="runtimeChannel"><option value="stable">Stable</option><option value="dev">Dev</option></select>',
        ) +
        setting(
          "客户端发布通道",
          "下载完成后由你选择重启并应用。",
          '<select data-pref="clientChannel"><option value="stable">Stable</option><option value="dev">Dev</option></select>',
        ),
    ) +
    panel(
      "Runtime 工作方式",
      `<form data-runtime class="panel-body stack"><div class="form-grid">${select(
        "workspace",
        "工作区模式",
        [
          ["normal", "正常"],
          ["readonly", "只读"],
          ["off", "暂停"],
        ],
        "normal",
      )}${select(
        "diary",
        "琐事记录",
        [
          ["concise", "轨迹档 · 一日一条"],
          ["verbose", "详记档 · 逐条记录"],
        ],
        "concise",
      )}</div><div class="actions"><button type="submit">保存工作方式</button><button type="button" data-language="zh">CLI 中文</button><button type="button" data-language="en">CLI English</button></div></form>`,
    ) +
    panel(
      "数据与备份",
      `<div class="panel-body stack"><p class="hint">导出文件包含记忆明文。数据库备份与 JSON 导出分别保存。</p><div class="actions"><button data-file="export">导出记忆</button><button data-file="import">导入记忆</button><button data-file="backup">备份数据库</button><button data-file="share-import">导入分享材料</button></div><div data-paths></div></div>`,
    );
  root.addEventListener("change", (event) => {
    const control = event.target;
    if (control.dataset.pref)
      action(control, async () => {
        await ctx.savePreferences({
          [control.dataset.pref]:
            control.type === "checkbox" ? control.checked : control.value,
        });
        notify("设置已保存");
      });
    if (control.hasAttribute("data-autostart"))
      action(control, async () => {
        try {
          await api("autostart", { enabled: control.checked });
          notify("登录启动设置已保存");
        } catch (error) {
          control.checked = !control.checked;
          throw error;
        }
      });
  });
  root.addEventListener("click", (event) => {
    const button = event.target.closest("button");
    if (!button) return;
    if (button.dataset.language)
      action(button, async () => {
        await api("cli-language", { language: button.dataset.language });
        notify("CLI 语言已保存");
      });
    if (button.dataset.file)
      action(button, async () => {
        const kind = button.dataset.file,
          path = await api("file-dialog", { kind });
        if (!path) return;
        const names = {
          export: "memory_export",
          import: "memory_import",
          backup: "memory_backup",
          "share-import": "memory_share_preview",
        };
        if (
          kind === "import" &&
          !(await confirm(
            "导入记忆",
            `将导入所选文件，并按 Runtime 的导入规则写入本机记忆库。`,
            "导入",
          ))
        )
          return;
        const result = await execute(names[kind], { path });
        if (kind === "share-import") {
          const choice = await confirm(
            "确认导入分享材料",
            raw(summary(result)),
            "确认导入",
          );
          if (choice) await execute("memory_share_import", { path });
        }
        notify("文件操作已完成");
        await ctx.refresh();
      });
  });
  $("[data-runtime]", root).addEventListener("submit", (event) => {
    event.preventDefault();
    action($("[type=submit]", event.target), async () => {
      const values = Object.fromEntries(new FormData(event.target));
      await execute("agent_set", {
        setting: "workspace_mode=" + values.workspace,
      });
      await execute("agent_set", { setting: "diary_mode=" + values.diary });
      notify("工作方式已保存");
      await ctx.refresh();
    });
  });
  function update() {
    const state = ctx.desktop;
    if (!state) return;
    if (!initialized) {
      all("[data-pref]", root).forEach((node) => {
        const value = state.preferences[node.dataset.pref];
        if (node.type === "checkbox") node.checked = Boolean(value);
        else node.value = value;
      });
      $("[data-autostart]", root).checked = state.autostart;
      initialized = true;
    }
    if (!agentAttempted && state.connected) {
      agentAttempted = true;
      execute("agent_config")
        .then((envelope) => {
          if (!root.isConnected) return;
          const value = summary(envelope);
          $("[name=workspace]", root).value = value.workspace_mode || "normal";
          $("[name=diary]", root).value = value.diary_mode || "concise";
        })
        .catch((error) => notify(error.message, true));
    }
    $("[data-pref=runtimeChannel]", root).disabled = state.task?.busy;
    $("[data-paths]", root).innerHTML = pairs([
      ["客户端数据", state.root],
      ["当前记忆库", ctx.status?.summary?.data_dir],
      ["RPC 地址", state.endpoint],
    ]);
  }
  typographyPanel(root);
  storagePanel(root, ctx);
  update();
  return {
    update,
    refresh() {
      initialized = false;
      agentAttempted = false;
      update();
    },
  };
}

const updateNames = {
  idle: "等待检查",
  checking: "正在检查",
  "up-to-date": "已是最新版本",
  available: "发现更新",
  downloading: "下载中",
  verifying: "校验中",
  installing: "准备中",
  ready: "等待重启",
  error: "更新失败",
};
export function updates(root, ctx) {
  let latest = null;
  function update() {
    const state = ctx.desktop || {},
      u = state.clientUpdate || {},
      task = state.task || {},
      p = state.preferences || {};
    root.innerHTML =
      heading(
        "保持更新",
        "客户端与 CLI 独立更新。更新包会按当前系统和架构匹配。",
      ) +
      `<div class="grid">${panel(
        "桌面客户端",
        pairs([
          ["当前版本", state.version],
          ["发布通道", p.clientChannel],
          ["状态", updateNames[u.state] || u.state || "—"],
          ["可用版本", u.latest || "—"],
        ]) +
          `<div class="panel-body actions"><button data-update="client-check">检查更新</button><button data-update="client-download" ${u.state === "ready" ? "disabled" : ""}>下载更新</button><button class="primary" data-update="client-restart" ${u.state !== "ready" ? "disabled" : ""}>重启并应用</button></div>${u.error ? `<div class="panel-body form-error">${h(u.error)}</div>` : ""}`,
      )}
      ${panel(
        "CLI / Runtime",
        pairs([
          ["当前运行", state.health?.bin || "未连接"],
          ["已安装", state.installed?.version || "未安装"],
          ["发布通道", p.runtimeChannel],
          ["可用版本", latest?.version || "尚未检查"],
        ]) +
          `<div class="panel-body actions"><button data-cli-check>检查 CLI 版本</button><button class="primary" data-update="runtime-update" ${task.busy ? "disabled" : ""}>安装 / 更新 CLI</button></div>`,
      )}</div>` +
      panel(
        "当前操作",
        `<div class="panel-body"><p>${h(task.stage || "就绪")}</p>${task.total ? `<div class="progress-label"><span>${h(bytes(task.done))} / ${h(bytes(task.total))}</span></div><progress value="${h(task.done)}" max="${h(task.total)}"></progress>` : ""}${task.error ? `<p class="form-error section-gap">${h(task.error)}</p>` : ""}</div>`,
      ) +
      `<p class="hint section-gap">更换通道请到设置。客户端下载后需要重启；业务操作执行期间不会应用客户端更新。</p>`;
  }
  root.addEventListener("click", (event) => {
    const button = event.target.closest("button");
    if (!button) return;
    action(button, async () => {
      if (button.hasAttribute("data-cli-check")) {
        latest = await api("runtime-release");
        update();
      } else if (button.dataset.update) {
        await api(button.dataset.update, {});
        notify("更新请求已提交");
        await ctx.refresh();
      }
    });
  });
  update();
  return { update, refresh: update };
}

export function diagnostics(root, ctx) {
  root.innerHTML =
    heading(
      "诊断",
      "查看 Runtime 的实际检查结果和连接状态。",
      `<button data-doctor>运行诊断</button>`,
    ) +
    `<div data-result></div>` +
    panel(
      "连接信息",
      `<div data-connection></div><details class="technical"><summary>Runtime 健康详情</summary><pre data-health></pre></details>`,
    );
  function update() {
    if (!ctx.desktop) return;
    $("[data-connection]", root).innerHTML = pairs([
      ["RPC", ctx.desktop.endpoint],
      ["客户端目录", ctx.desktop.root],
      ["连接", ctx.desktop.connected ? "已连接" : "未连接"],
      ["错误", ctx.statusError || "无"],
    ]);
    $("[data-health]", root).textContent = raw(ctx.desktop.health || {});
  }
  $("[data-doctor]", root).addEventListener("click", (event) =>
    action(event.currentTarget, async () => {
      let value;
      try {
        value = await execute("doctor");
      } catch (error) {
        if (!error.envelope) throw error;
        value = error.envelope;
      }
      if (root.isConnected)
        $("[data-result]", root).innerHTML = resultView("检查结果", value);
    }),
  );
  update();
  return { update, refresh: update };
}

export function operations(root) {
  root.innerHTML =
    heading(
      "操作记录",
      "保留请求 ID 与执行状态。结果未确认时仅查询原请求，不重新提交写操作。",
    ) +
    `<p class="hint section-gap">本次会话可查看写操作的完整回执；客户端重启后只保留请求 ID 和状态。</p><div data-jobs></div><div data-receipt class="section-gap"></div>`;
  async function refresh() {
    try {
      const jobs = await api("operations");
      if (!root.isConnected) return;
      $("[data-jobs]", root).innerHTML = panel(
        "最近操作",
        jobs.length
          ? `<div class="scroll-table"><table><thead><tr><th>操作</th><th>状态</th><th>时间</th><th>请求 ID</th><th></th></tr></thead><tbody>${jobs.map((job) => `<tr><td>${h(job.name)}</td><td>${h({ running: "执行中", done: "已完成", failed: "失败", unconfirmed: "结果未确认" }[job.state] || job.state)}${job.error ? `<p class="form-error">${h(job.error)}</p>` : ""}</td><td>${h(date(job.started))}</td><td><code>${h(job.id)}</code></td><td>${job.state === "unconfirmed" ? `<button data-recover="${h(job.id)}">查询原请求</button>` : ""}</td></tr>`).join("")}</tbody></table></div>`
          : '<div class="empty">暂无操作记录</div>',
      );
      all("tbody tr", root).forEach((row, index) => {
        const job = jobs[index];
        if (job.state !== "done" && job.state !== "failed") return;
        const control = document.createElement("button");
        control.textContent = "查看回执";
        control.dataset.receipt = job.id;
        row.lastElementChild.append(control);
      });
    } catch (error) {
      notify(error.message, true);
    }
  }
  root.addEventListener("click", (event) => {
    const receipt = event.target.closest("[data-receipt]");
    if (receipt?.dataset.receipt)
      action(receipt, async () => {
        const job = await api(
          "operation?id=" + encodeURIComponent(receipt.dataset.receipt),
        );
        $("div[data-receipt]", root).innerHTML = job.envelope
          ? resultView("操作回执", job.envelope)
          : panel(
              "操作回执",
              '<p class="panel-body hint">完整回执已不在本次会话中。请刷新对应页面确认当前数据。</p>',
            );
      });
    const button = event.target.closest("[data-recover]");
    if (button)
      action(button, async () => {
        const job = await api("operation-recover", {
          id: button.dataset.recover,
        });
        notify(
          job.state === "done" ? "原请求已完成" : job.error || "结果仍未确认",
          job.state !== "done",
        );
        await refresh();
      });
  });
  refresh();
  return { refresh, update: refresh };
}
