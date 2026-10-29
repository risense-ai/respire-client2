'use strict';
const $ = id => document.getElementById(id);
let state, refreshing = false;
const updateNames = {'idle':'等待检查','checking':'检查中','up-to-date':'当前已是最新版本','available':'发现更新','downloading':'下载中','verifying':'校验中','installing':'准备更新','ready':'等待重启','error':'检查 / 更新失败'};
function notice(text, error = false) { $('notice').hidden = !text; $('notice').textContent = text; $('notice').classList.toggle('error', error); }
async function api(path, body) {
  const response = await fetch('/api/' + path, body === undefined ? {} : {method:'POST', headers:{'Content-Type':'application/json','X-Respire-Client':'1'}, body:JSON.stringify(body)});
  const data = await response.json();
  if (!data.ok) throw new Error(data.error || '操作失败');
  return data.result;
}
async function refresh() {
  if (refreshing) return;
  refreshing = true;
  try {
    state = await api('state');
	const compact = state.preferences.layout === 'compact';
	document.body.dataset.layout = compact ? 'compact' : 'sidebar';
	$('layout-toggle').textContent = compact ? '侧栏布局' : '紧凑布局';
	$('layout-toggle').setAttribute('aria-pressed', String(compact));
    const h = state.health || {}, rpc = h.rpc || {}, task = state.task, update = state.clientUpdate;
    $('client-version').textContent = '客户端 ' + state.version + ' · ' + state.platform;
    $('connection-dot').classList.toggle('online', state.connected);
    $('connection-label').textContent = state.connected ? 'Runtime 已连接' : 'Runtime 未连接';
    $('runtime-title').textContent = state.connected ? '记忆服务已就绪。' : '连接你的记忆服务。';
    $('runtime-description').textContent = state.connected ? (state.managed ? 'Runtime 由此客户端管理，所有业务通过本机 RPC 执行。' : '已连接现有 Runtime，保留它当前的管理方式。') : '启动已有 Runtime，或在更新页面下载官方版本。';
    $('runtime-version').textContent = h.bin || '—';
    $('runtime-owner').textContent = state.connected ? (state.managed ? '客户端管理' : '其他 CLI 管理') : '等待连接';
    $('running-count').textContent = rpc.executing_until_reply ?? '—';
    $('queued-count').textContent = rpc.queued_or_cancelled_pending_removal ?? '—';
    $('task-state').textContent = task.stage;
    $('task-detail').textContent = task.error || (task.total > 0 ? `${(task.done / 1048576).toFixed(1)} / ${(task.total / 1048576).toFixed(1)} MiB` : task.busy ? '操作正在后台执行。' : '暂无进行中的操作。');
    $('task-detail').classList.toggle('error', Boolean(task.error));
    $('task-progress').hidden = !task.busy;
    if (task.total > 0) $('task-progress').value = Math.min(100, task.done / task.total * 100); else $('task-progress').removeAttribute('value');
    for (const [id, value] of [['autostart',state.autostart],['auto-runtime',state.preferences.autoRuntime],['auto-update',state.preferences.autoUpdate]]) if (document.activeElement !== $(id)) $(id).checked = value;
    $('endpoint').textContent = state.endpoint;
    $('data-root').textContent = state.root;
    $('health-json').textContent = state.health ? JSON.stringify(state.health, null, 2) : state.connectionError;
    $('update-state').textContent = updateNames[update.state] || update.state;
    $('client-update-description').textContent = `当前 ${state.version}` + (update.latest ? `，可用 ${update.latest}。` : '。从官方仓库的所选通道检查更新。');
    $('client-update-error').textContent = update.error || '';
    $('restart-update').hidden = update.state !== 'ready';
	$('client-channel').disabled = ['checking','downloading','verifying','installing','ready'].includes(update.state);
	$('runtime-channel').disabled = task.busy;
    $('installed-runtime').textContent = state.installed.version ? `受管理版本：${state.installed.version}` : '尚未安装受管理的 Runtime。';
    for (const [id, value] of [['runtime-channel',state.preferences.runtimeChannel],['client-channel',state.preferences.clientChannel]]) if (document.activeElement !== $(id)) $(id).value = value;
    document.querySelectorAll('[data-action="start"],[data-action="stop"],[data-action="runtime-update"]').forEach(button => button.disabled = task.busy);
    if (state.autostartError) notice(state.autostartError, true);
  } catch (error) { notice(error.message, true); }
  finally { refreshing = false; }
}
const pages = {overview:['一切尽在本机。','查看 Runtime 状态，让记忆服务保持就绪。'],settings:['按你的习惯运行。','设置启动方式与后台更新。'],updates:['保持就绪，持续更新。','客户端与 Runtime 分开更新，各自保持完整。'],diagnostics:['每个状态，都有依据。','查看 Runtime 的真实返回，便于定位问题。']};
document.querySelectorAll('[data-page]').forEach(button => button.addEventListener('click', () => {
  document.querySelectorAll('[data-page]').forEach(item => item.classList.toggle('active', item === button));
  document.querySelectorAll('[data-panel]').forEach(panel => panel.hidden = panel.dataset.panel !== button.dataset.page);
  [$('page-title').textContent, $('page-description').textContent] = pages[button.dataset.page];
}));
document.querySelectorAll('[data-action]').forEach(button => button.addEventListener('click', async () => {
  button.disabled = true;
  try { notice(await api(button.dataset.action, {})); await refresh(); }
  catch (error) { notice(error.message, true); }
  finally { button.disabled = false; }
}));
document.querySelectorAll('[data-rpc]').forEach(button => button.addEventListener('click', async () => {
  button.disabled = true;
  try { $('rpc-result').textContent = JSON.stringify(await api('rpc', {command:button.dataset.rpc}), null, 2); }
  catch (error) { $('rpc-result').textContent = error.message; notice(error.message, true); }
  finally { button.disabled = false; }
}));
$('autostart').addEventListener('change', async event => {
  try { await api('autostart', {enabled:event.target.checked}); notice('登录启动设置已保存。'); }
  catch (error) { event.target.checked = !event.target.checked; notice(error.message, true); }
});
for (const id of ['auto-runtime','auto-update','runtime-channel','client-channel']) $(id).addEventListener('change', async () => {
  try { await api('preferences', {autoRuntime:$('auto-runtime').checked, autoUpdate:$('auto-update').checked, runtimeChannel:$('runtime-channel').value, clientChannel:$('client-channel').value, layout:state.preferences.layout}); notice('设置已保存。切换通道不会自动降级已有版本。'); }
  catch (error) { notice(error.message, true); await refresh(); }
});
$('refresh').addEventListener('click', refresh);
$('layout-toggle').addEventListener('click', async () => {
	if (!state) return;
	try {
		await api('preferences', {...state.preferences, layout:state.preferences.layout === 'compact' ? 'sidebar' : 'compact'});
		await refresh();
	} catch (error) { notice(error.message, true); }
});
async function poll() { await refresh(); setTimeout(poll, document.hidden ? 30000 : 3000); }
poll();
