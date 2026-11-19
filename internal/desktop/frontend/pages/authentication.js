import {
  $,
  h,
  raw,
  input,
  panel,
  dialog,
  execute,
  api,
  waitForOperation,
  select,
  action,
  confirm,
  notify,
} from "../ui/core.js";

// Shared account operations and recovery-material handling. The accounts
// page and the first-run wizard both use these so the confirmation rules
// cannot drift apart.

// issuedRecoveryMaterial extracts recovery material a server or offline
// keygen actually issued; null means no new material exists and no
// save-confirmation step may be faked.
export function issuedRecoveryMaterial(envelope) {
  const detail = envelope?.details || {},
    s = envelope?.summary || {};
  const issued =
    detail.super ||
    detail.super_key ||
    detail.super_issued ||
    s.super ||
    s.super_key ||
    s.super_issued ||
    s.super_password ||
    s.login?.super_issued;
  if (!issued) return null;
  return { material: issued, user: detail.user || s.user || "" };
}

export function recoveryMaterialJson(issued) {
  return raw({ super: issued.material, user: issued.user });
}

const RECOVERY_NOTE =
  "恢复材料用于重新解锁本机记忆，请保存到安全位置，不要分享。登录密码不能替代恢复材料；丢失后服务端无法帮助恢复本机密钥。";

// recoveryMaterialDialog shows issued material behind a mandatory
// save-acknowledgement checkbox; the close buttons and Escape are removed so
// the receipt cannot be dismissed without the acknowledgement.
export function recoveryMaterialDialog(envelope, { note = RECOVERY_NOTE, requestId = envelope?.recoveryRequestId } = {}) {
  const issued = issuedRecoveryMaterial(envelope);
  if (!issued) return null;
  const element = dialog({
    title: "请保存恢复材料",
    wide: true,
    body:
      `<p class="warning-text">${h(note)}</p><label>恢复材料<textarea class="reading-text" readonly rows="7">${h(
        recoveryMaterialJson(issued),
      )}</textarea></label><label class="checkbox-label"><input type="checkbox" data-saved>我已将恢复材料保存到安全位置</label>`,
    submit: "我已保存恢复材料",
    onSubmit: async () => {
      if (!$("[data-saved]", element).checked) return false;
      if (requestId) await api("recovery-material-ack", { id: requestId, confirmed: true });
      notify("恢复材料已确认保存");
      document.dispatchEvent(new CustomEvent("respire-operation"));
    },
  });
  element.querySelectorAll("[data-close]").forEach((button) => button.remove());
  element.addEventListener("cancel", (event) => event.preventDefault());
  const submit = $("[type=submit]", element),
    checkbox = $("[data-saved]", element);
  submit.disabled = true;
  checkbox.addEventListener("change", () => {
    submit.disabled = !checkbox.checked;
  });
  return element;
}

export async function restorePendingRecoveryMaterials() {
  const jobs = await api("operations");
  for (const pending of jobs.filter((job) => job.materialPending)) {
    const job = await api("operation-recover", { id: pending.id });
    if (job.state !== "done") {
      notify(job.error || "恢复材料尚未确认，请在操作记录中查询原事务。", true);
      continue;
    }
    if (!job.materialPending) continue;
    const element = recoveryMaterialDialog(job.envelope, { requestId: job.id });
    if (!element) throw Error("原事务未返回恢复材料，请在操作记录中查询原事务。");
    await new Promise((resolve) => element.addEventListener("close", resolve, { once: true }));
  }
}

export function validateAuthentication(kind, values) {
  if (
    kind === "register" &&
    (values.password.length < 8 || values.password !== values.confirmation)
  )
    throw Error("密码至少 8 位，且两次输入必须一致");
}

// runAuthentication submits register/login through the CLI host RPC stdin
// transport; credentials stay out of argv and client settings.
export async function runAuthentication(kind, values) {
  const credentials = { password: values.password };
  if (values.super) credentials.super = values.super;
  if (values.totp) credentials.totp = values.totp.trim();
  return executeAuthentication(
    "auth_" + kind,
    { addr: values.addr.trim(), user: values.user.trim() },
    { secret: JSON.stringify(credentials) },
  );
}

// Keep only request IDs in memory, so cancelling an input dialog never starts
// another authentication when the original form is submitted again.
const pendingAuthentication = new Map();
export async function executeAuthentication(name, args = {}, options = {}) {
  const key = JSON.stringify([name, args]);
  const previous = pendingAuthentication.get(key);
  try {
    let result;
    if (previous) {
      result = await continueAuthentication(previous);
    } else {
      try {
        result = await execute(name, args, options);
      } catch (error) {
        if (!error.requestId || error.operation?.state === "failed") throw error;
        pendingAuthentication.set(key, error.requestId);
        result = await continueAuthentication(error.requestId);
      }
    }
    pendingAuthentication.delete(key);
    return result;
  } catch (error) {
    if (error.operation?.state === "failed") pendingAuthentication.delete(key);
    throw error;
  }
}

const supplementaryLabels = {
  totp: "两步验证代码（6 位数字）",
  legacy_password: "旧版登录密码",
  account_secret: "Account Secret",
  source_secret: "来源记忆库恢复材料",
  secret_key: "Secret Key",
  new_super: "新超级密码",
  super_password: "超级密码",
  password: "原登录密码",
};

function supplementalInput(job) {
  const request = job.input;
  const allowed = request.method === "auth.login.continue"
    ? ["totp", "legacy_password", "account_secret", "source_secret", "secret_key", "new_super", "super_password", "merge_local", "host_credentials"]
    : request.method === "auth.key.continue"
      ? ["password", "super_password", "account_secret", "totp"] : [];
  const required = request.required_inputs;
  if (!Array.isArray(required) || !required.length || new Set(required).size !== required.length || required.some((field) => !allowed.includes(field)))
    throw Error("CLI 返回了不支持的补充输入要求，原事务保留；请更新客户端或核对 CLI。");
  return new Promise((resolve) => {
    let accepted = false;
    const element = dialog({
      title: "继续原账户操作",
      body: `<p class="hint">补充输入只用于原操作，不会重新登录或创建另一项事务。</p><p>${h(request.user || "")}${request.addr ? " · " + h(request.addr) : ""}</p><p>原事务：<code>${h(job.transaction.operation_id)}</code></p>` + required.map((field) => {
        if (field === "host_credentials") return '<p class="hint">CLI 将从自己的保管记录恢复所需材料。</p>';
        if (field === "merge_local") return select(field, "是否合并原事务所指的本机记忆库", [["", "请选择"], ["true", "确认合并"], ["false", "不合并"]], "");
        return input(field, supplementaryLabels[field], "", { type: "password", required: true });
      }).join(""),
      submit: "提交到原事务",
      onSubmit: (values) => {
        if (required.includes("merge_local")) {
          if (!["true", "false"].includes(values.merge_local)) throw Error("请明确选择是否合并本机记忆库");
          values.merge_local = values.merge_local === "true";
        }
        accepted = true;
        resolve(values);
      },
    });
    element.addEventListener("close", () => { if (!accepted) resolve(null); }, { once: true });
  });
}

export async function continueAuthentication(id) {
  let job = await api("operation-recover", { id });
  for (;;) {
    if (job.state === "done") return job.materialPending
      ? { ...job.envelope, recoveryRequestId: id }
      : job.envelope;
    if (job.state !== "unconfirmed" || !job.input || !job.transaction) {
      const error = Error(job.error || "原事务尚未等待补充输入，请在操作记录中查询原请求。");
      error.requestId = id;
      error.operation = job;
      throw error;
    }
    const args = await supplementalInput(job);
    if (args === null) {
      const error = Error("补充输入已取消，原事务仍保留在操作记录中；未重新认证。");
      error.requestId = id;
      throw error;
    }
    const transportId = "desktop-" + crypto.randomUUID();
    let submitted;
    try {
      submitted = await api("operation-continue", { id, transportId, args, confirmed: true });
    } catch (error) {
      error.requestId = id;
      throw error; // A lost response must be reconciled, never retransmitted.
    }
    try {
      return await waitForOperation(submitted);
    } catch (error) {
      if (error.operation?.state !== "unconfirmed" || !error.operation.input) throw error;
      job = error.operation;
    }
  }
}

export function authenticationPanel(root, ctx, refresh) {
  const section = document.createElement("section");
  section.className = "section-gap";
  section.innerHTML = panel(
    "账户登录与恢复",
    '<div class="panel-body stack"><p class="hint">密码认证通过 CLI 宿主 RPC 执行，凭据不会放入进程命令行或客户端设置。需要 CLI 1.0.18 或支持账户 RPC 的兼容版本。</p><div class="actions"><button data-auth="login">密码登录</button><button data-auth="register">注册账户</button><button data-auth="recover">恢复解密材料</button><button data-auth="reset">更换超级密码</button><button data-auth="forget" class="danger">忘记本机身份</button></div></div>',
  );
  root.insertBefore(section, root.children[1]);
  const done = async (result, label) => {
    await ctx.refresh();
    await refresh();
    if (!recoveryMaterialDialog(result)) notify(label + "已完成");
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
      if (ctx.desktop?.serverAddressError) {
        notify(ctx.desktop.serverAddressError, true);
        return;
      }
      const title = kind === "login" ? "密码登录" : "注册账户";
      dialog({
        title,
        body:
          input(
            "addr",
            "服务器",
            ctx.desktop?.serverAddress || "",
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
          validateAuthentication(kind, values);
          const result = await runAuthentication(kind, values);
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
          const result = await executeAuthentication(
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
        const result = await executeAuthentication(
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
