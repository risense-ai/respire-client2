import {
  $,
  h,
  rows,
  raw,
  input,
  dialog,
  execute,
  action,
  confirm,
  notify,
  showText,
} from "../ui/core.js";

export function invitationDialog() {
  dialog({
    title: "创建空间邀请",
    body:
      input("note", "成员或设备备注") +
      '<label class="checkbox-label"><input type="checkbox" name="readonly">只读邀请（不能修改记忆）</label><p class="warning-text">邀请材料包含该空间的访问和解密能力，请仅交给可信成员。</p>',
    submit: "生成邀请",
    onSubmit: async (value) => {
      const result = await execute("space_invite", {
        note: value.note,
        readonly: Boolean(value.readonly),
      });
      showText("保存空间邀请材料", raw(result.details || result.summary));
    },
  });
}
export async function membersDialog() {
  const element = dialog({
    title: "空间成员",
    body: "<div data-members>正在读取…</div>",
    submit: "关闭",
    onSubmit: () => {},
  });
  async function load() {
    const result = await execute("space_members");
    if (!element.isConnected) return;
    const members = rows(result, "members");
    $("[data-members]", element).innerHTML = members.length
      ? `<ul class="clean">${members.map((member) => `<li class="row"><div class="row-copy"><strong>${h(member.device_name || member.session_id)}</strong><small>${member.readonly ? "只读" : "读写"} · ${h(member.issued_at || "")}</small></div><button class="danger" data-kick="${h(member.session_id)}">撤销成员访问</button></li>`).join("")}</ul>`
      : '<p class="hint">当前空间尚未签发成员会话。</p>';
  }
  element.addEventListener("click", (event) => {
    const button = event.target.closest("[data-kick]");
    if (button)
      action(button, async () => {
        if (
          !(await confirm(
            "撤销成员访问",
            "该成员的会话将被撤销。对方已经读取或导出的内容无法追回。",
            "撤销访问",
          ))
        )
          return;
        await execute("space_kick", { session: button.dataset.kick });
        notify("成员访问已撤销");
        await load();
      });
  });
  try {
    await load();
  } catch (e) {
    $("[data-members]", element).textContent = e.message;
  }
}
