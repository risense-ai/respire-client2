import {
  $,
  h,
  raw,
  input,
  panel,
  dialog,
  execute,
  action,
  confirm,
  notify,
  summary,
} from "../ui/core.js";

function recoveryReceipt(result, label) {
  const detail = result.details || {},
    s = summary(result);
  const issued =
    detail.super ||
    detail.super_key ||
    detail.super_issued ||
    s.super ||
    s.super_key ||
    s.super_issued;
  if (!issued) {
    notify(label + "已完成");
    return;
  }
  const receipt = dialog({
    title: "请保存恢复材料",
    body: `<p class="warning-text">${h(label)}已完成。恢复材料用于重新解锁本机记忆，请保存到安全位置，不要分享。</p><label>恢复材料<textarea class="reading-text" readonly rows="7">${h(raw({ super: issued, user: detail.user || s.user }))}</textarea></label>`,
    submit: "我已保存恢复材料",
    onSubmit: () => {
      notify("恢复材料已确认保存");
    },
  });
  receipt.querySelectorAll("[data-close]").forEach((button) => button.remove());
  receipt.addEventListener("cancel", (event) => event.preventDefault());
}

export function authenticationPanel(root, ctx, refresh) {
  const section = document.createElement("section");
  section.className = "section-gap";
  section.innerHTML = panel(
    "账户登录与恢复",
    '<div class="panel-body stack"><p class="hint">密码认证通过 CLI 宿主 RPC 执行，凭据不会放入进程命令行或客户端设置。需要支持账户 RPC 的 CLI Dev 版本。</p><div class="actions"><button data-auth="login">密码登录</button><button data-auth="register">注册账户</button><button data-auth="recover">恢复解密材料</button><button data-auth="reset">更换超级密码</button><button data-auth="forget" class="danger">忘记本机身份</button></div></div>',
  );
  root.insertBefore(section, root.children[1]);
  const done = async (result, label) => {
    await ctx.refresh();
    await refresh();
    recoveryReceipt(result, label);
  };
  section.onclick = (event) => {
    const button = event.target.closest("[data-auth]");
    if (!button) return;
    const kind = button.dataset.auth;
    if (kind === "forget") {
      action(button, async () => {
        if (
          !(await confirm(
            "忘记本机身份",
            "将移除本机会话和解密材料。记忆密文保留，但再次解锁需要你保存的恢复材料。请先导出密钥。",
            "我已有恢复材料，继续",
          ))
        )
          return;
        await execute("logout_full");
        await ctx.refresh();
        await refresh();
        notify("本机身份已清除");
      });
      return;
    }
    if (kind === "login" || kind === "register") {
      const title = kind === "login" ? "密码登录" : "注册账户";
      dialog({
        title,
        body:
          input(
            "addr",
            "服务器",
            ctx.status?.summary?.server_addr || "https://api.rsrs.rs",
            { required: true },
          ) +
          input("user", "账户名称", "", { required: true }) +
          input("password", "登录密码", "", {
            type: "password",
            required: true,
          }) +
          (kind === "register"
            ? input("confirmation", "再次输入密码", "", {
                type: "password",
                required: true,
              })
            : input("super", "超级密码（已有本机密钥时可留空）", "", {
                type: "password",
              }) + input("totp","两步验证代码（启用时填写 6 位代码）","",{type:"password"})),
        submit: title,
        onSubmit: async (values) => {
          if (
            kind === "register" &&
            (values.password.length < 8 ||
              values.password !== values.confirmation)
          )
            throw Error("密码至少 8 位，且两次输入必须一致");
          const credentials = { password: values.password };
          if (values.super) credentials.super = values.super;
          if (values.totp) credentials.totp=values.totp.trim();
          const result = await execute(
            "auth_" + kind,
            { addr: values.addr.trim(), user: values.user.trim() },
            { secret: JSON.stringify(credentials) },
          );
          await done(result, title);
        },
      });
      return;
    }
    if (kind === "reset") {
      dialog({
        title: "更换超级密码",
        body:
          '<p class="hint">由 CLI 重新封装现有解密密钥，不会生成新的记忆库。请保存操作完成后返回的新恢复材料。</p>' +
          input("super", "当前超级密码（可使用本机已保存密钥）", "", {
            type: "password",
          }),
        submit: "更换并显示新材料",
        onSubmit: async (values) => {
          if (
            !(await confirm(
              "更换超级密码",
              "旧恢复码将不再适用于新的密钥封装。记忆内容保持不变。",
              "更换",
            ))
          )
            return false;
          const result = await execute(
            "auth_reset",
            {},
            {
              secret: JSON.stringify(
                values.super ? { super: values.super } : {},
              ),
            },
          );
          await done(result, "超级密码更换");
        },
      });
      return;
    }
    dialog({
      title: "恢复解密材料",
      wide: true,
      body:
        '<p class="warning-text">请使用你此前导出的完整材料。不会通过服务器重置密钥，也不会替换为新的记忆库。留空服务器与账户可先在离线状态恢复。</p>' +
        input("addr", "服务器（可选）") +
        input("user", "账户（可选）") +
        input("password", "登录密码（连接云端时需要）", "", {
          type: "password",
        }) +
        input("super", "超级密码", "", { type: "password" }) +
        input("secret", "旧版 Account Secret（按恢复材料填写）", "", {
          type: "password",
        }) +
        input("kdf_salt", "kdf_salt", "", { required: true }) +
        input("wrapped_urk", "wrapped_urk", "", { required: true }) +
        input("urk_nonce", "urk_nonce", "", { required: true }),
      submit: "验证并恢复",
      onSubmit: async (values) => {
        const { addr, user, ...credentials } = values;
        if (!credentials.super && !(credentials.password && credentials.secret))
          throw Error("请填写超级密码，或旧版登录密码与 Account Secret");
        if (
          !(await confirm(
            "恢复所选密钥材料",
            "CLI 将校验并提交当前本机身份。请确认恢复材料属于需要打开的记忆库。",
            "恢复",
          ))
        )
          return false;
        const result = await execute(
          "auth_recover",
          {
            ...(addr.trim() ? { addr: addr.trim() } : {}),
            ...(user.trim() ? { user: user.trim() } : {}),
          },
          { secret: JSON.stringify(credentials) },
        );
        await done(result, "密钥恢复");
      },
    });
  };
}
