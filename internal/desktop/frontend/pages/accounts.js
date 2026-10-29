import {
  $,
  h,
  raw,
  summary,
  rows,
  api,
  execute,
  action,
  notify,
  panel,
  pairs,
  input,
  dialog,
  confirm,
  showText,
  resultView,
} from "../ui/core.js";
import { heading } from "./overview.js";

export function accounts(root, ctx) {
  let loading = false,
    attempted = false;
  root.innerHTML =
    heading(
      "账户与工作区",
      "查看身份、切换已保存账户，并管理本机与共享空间。",
    ) +
    "<div data-session></div>" +
    `<div class="grid section-gap">${panel("已保存账户", '<div data-accounts class="empty">等待读取</div>')}${panel("空间", '<div class="panel-body actions"><button data-space="create">创建空间</button><button data-space="join">加入空间</button><button data-space="invite">生成邀请</button><button data-space="members">成员</button></div><div data-spaces></div>')}</div>` +
    panel(
      "同步与服务器",
      `<form data-server class="panel-body stack">${input("addr", "服务器地址", "https://api.rsrs.rs", { required: true })}<label class="checkbox-label"><input name="autosync" type="checkbox">自动同步</label><div class="actions"><button type="submit">保存</button><button type="button" data-account="sync">立即同步</button><button type="button" data-account="conflicts">查看同步冲突</button></div></form>`,
    ) +
    panel(
      "旧账户迁移",
      '<div class="panel-body stack"><p class="hint">先检查候选旧库，再明确选择来源 ID 和新账户名称。</p><div class="actions"><button data-account="migration">检查旧账户</button><button data-account="migrate">迁移所选旧账户</button></div><div data-migration></div></div>',
    ) +
    '<div data-result class="section-gap"></div>';
  function update() {
    const s = ctx.status?.summary || {},
      session = s.session || {};
    $("[data-session]", root).innerHTML = panel(
      "当前身份",
      pairs([
        ["账户", session.user || "本机离线"],
        ["云端会话", session.has_token ? "已登录" : "未登录"],
        ["本机密钥", session.has_local_keys ? "已保存" : "未初始化"],
        ["记忆库", s.data_dir],
      ]) +
        `<div class="panel-body actions"><button data-account="login" class="primary">浏览器登录</button><button data-account="dashboard">打开 dashboard</button><button data-account="keygen" ${session.has_local_keys ? "disabled" : ""}>初始化离线记忆库</button><button data-account="keys" ${!session.has_local_keys ? "disabled" : ""}>导出密钥</button><button data-account="logout" ${!session.has_token ? "disabled" : ""}>退出云端会话</button></div>`,
    );
  }
  async function load() {
    if (loading) return;
    loading = true;
    attempted = true;
    const results = await Promise.allSettled([
      execute("account_list"),
      execute("space_list"),
      execute("config"),
    ]);
    if (!root.isConnected) {
      loading = false;
      return;
    }
    const [profileResult, spaceResult, configResult] = results;
    if (profileResult.status === "fulfilled") {
      const profiles = rows(profileResult.value, "accounts");
      $("[data-accounts]", root).className = "";
      $("[data-accounts]", root).innerHTML = profiles.length
        ? `<ul class="clean">${profiles.map((item) => `<li class="row"><div class="row-copy"><strong>${h(item.name)}</strong><small>${h(item.user || item.dir || "")}</small></div>${item.current ? '<span class="badge good">当前</span>' : `<button data-use="${h(item.name)}">切换</button>`}</li>`).join("")}</ul>`
        : '<div class="empty">没有返回已保存账户</div>';
    } else
      $("[data-accounts]", root).textContent = profileResult.reason.message;
    if (spaceResult.status === "fulfilled") {
      const spaces = rows(spaceResult.value, "spaces");
      $("[data-spaces]", root).innerHTML = spaces.length
        ? `<ul class="clean">${spaces.map((item) => `<li class="row"><div class="row-copy"><strong>${h(item.name)}</strong><small>${h(item.role || item.kind || "")}</small></div>${item.current ? '<span class="badge">当前</span>' : `<button data-space-use="${h(item.name)}">使用</button>`}<button class="danger" data-space-remove="${h(item.name)}">删除</button></li>`).join("")}</ul>`
        : '<div class="empty">没有返回空间</div>';
    } else $("[data-spaces]", root).textContent = spaceResult.reason.message;
    if (configResult.status === "fulfilled") {
      $("[name=addr]", root).value =
        summary(configResult.value).addr ||
        ctx.status?.summary?.server_addr ||
        "https://api.rsrs.rs";
      $("[name=autosync]", root).checked =
        ctx.status?.summary?.autosync === true;
    } else notify(configResult.reason.message, true);
    loading = false;
  }
  const commands = {
    sync: "sync",
    conflicts: "sync_conflicts",
    migration: "migration_list",
  };
  root.addEventListener("click", (event) => {
    const button = event.target.closest("button");
    if (!button) return;
    if (button.dataset.use)
      action(button, async () => {
        if (
          !(await confirm(
            "切换账户",
            `将切换到「${button.dataset.use}」的记忆库与会话。`,
            "切换",
          ))
        )
          return;
        await execute("account_use", { name: button.dataset.use });
        await ctx.refresh();
        await load();
      });
    if (button.dataset.spaceUse)
      action(button, async () => {
        await execute("space_use", { name: button.dataset.spaceUse });
        await ctx.refresh();
        await load();
      });
    if (button.dataset.spaceRemove)
      action(button, async () => {
        if (
          !(await confirm(
            "删除空间",
            `删除「${button.dataset.spaceRemove}」会移除对应本机空间及其数据。请确认已有备份。`,
            "删除空间",
          ))
        )
          return;
        await execute("space_remove", { name: button.dataset.spaceRemove });
        await load();
      });
    if (button.dataset.space) {
      const kind = button.dataset.space;
      if (kind === "create" || kind === "join")
        dialog({
          title: kind === "create" ? "创建空间" : "加入空间",
          body: input("value", kind === "create" ? "空间名称" : "邀请码", "", {
            required: true,
          }),
          onSubmit: async (value) => {
            await execute(
              kind === "create" ? "space_create" : "space_join",
              kind === "create" ? { name: value.value } : { code: value.value },
            );
            await load();
            await ctx.refresh();
          },
        });
      else
        action(button, async () => {
          const result = await execute(
            kind === "members" ? "space_members" : "space_invite",
          );
          showText(
            kind === "members" ? "空间成员" : "空间邀请",
            raw(result.details || result.summary),
          );
        });
    }
    if (button.dataset.account)
      action(button, async () => {
        const kind = button.dataset.account;
        if (commands[kind]) {
          const result = await execute(commands[kind]);
          const target =
            kind === "migration" ? "[data-migration]" : "[data-result]";
          $(target, root).innerHTML = resultView(button.textContent, result);
          if (kind === "sync") await ctx.refresh();
          return;
        }
        if (kind === "dashboard") {
          await api("open-dashboard", {});
          return;
        }
        if (kind === "login") {
          notify("请在浏览器完成账户授权。");
          await execute("login");
          notify("登录流程已完成");
          await ctx.refresh();
          await load();
          return;
        }
        if (kind === "keygen") {
          if (
            !(await confirm(
              "初始化离线记忆库",
              "将为当前未初始化的本机记忆库创建密钥。CLI 会拒绝覆盖已有记忆或云端密钥。请保存随后显示的恢复材料。",
              "创建本机密钥",
            ))
          )
            return;
          const result = await execute("keygen");
          showText("请保存本机恢复材料", raw(result.details || result.summary));
          await ctx.refresh();
          await load();
          return;
        }
        if (kind === "keys") {
          const out = await api("file-dialog", { kind: "keys" });
          if (!out) return;
          if (
            !(await confirm(
              "导出密钥",
              "文件包含解密材料。请保存在你控制的安全位置，不要分享。",
              "导出",
            ))
          )
            return;
          await execute("keys_export", { out });
          notify("密钥已导出");
          return;
        }
        if (kind === "logout") {
          if (
            !(await confirm(
              "退出云端会话",
              "退出后不再使用当前云端会话。本机记忆保留。",
              "退出",
            ))
          )
            return;
          await execute("logout");
          await ctx.refresh();
          await load();
          return;
        }
        if (kind === "migrate")
          dialog({
            title: "迁移所选旧账户",
            body:
              input("source", "来源 ID", "", { required: true }) +
              input("account", "新账户名称", "", { required: true }),
            submit: "迁移",
            onSubmit: async (values) => {
              const result = await execute("migration_apply", values);
              $("[data-migration]", root).innerHTML = resultView(
                "迁移结果",
                result,
              );
              await load();
            },
          });
      });
  });
  $("[data-server]", root).addEventListener("submit", (event) => {
    event.preventDefault();
    action($("[type=submit]", event.target), async () => {
      const value = Object.fromEntries(new FormData(event.target));
      await execute("server_set", { addr: value.addr.trim() });
      await execute("autosync_set", { enabled: Boolean(value.autosync) });
      notify("服务器与同步配置已保存");
      await ctx.refresh();
    });
  });
  update();
  if (ctx.desktop?.connected) load();
  return {
    update() {
      update();
      if (!attempted && ctx.desktop?.connected) load();
    },
    refresh: load,
  };
}
