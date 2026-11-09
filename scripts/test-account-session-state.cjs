const fs=require('node:fs');
const path=require('node:path');
const repoRoot=path.resolve(__dirname,'..');
const assert=require('node:assert/strict');
const src=fs.readFileSync(path.join(repoRoot,'internal/desktop/frontend/pages/accounts.js'),'utf8');
const core=fs.readFileSync(path.join(repoRoot,'internal/desktop/frontend/ui/core.js'),'utf8');
// Fail closed if production function boundaries change; never substitute a test copy.
function extract(source,start,end){
  const from=source.indexOf(start),to=source.indexOf(end,from+start.length);
  assert(from>=0 && to>from,`Production function boundary missing: ${start}`);
  assert.equal(source.indexOf(start,from+start.length),-1,`Ambiguous production boundary: ${start}`);
  return source.slice(from,to);
}
const action=extract(core,'export async function action','export function panel').replace('export ','');
const logic=extract(src,'  function update()','  async function load()');
const fields=Object.fromEntries(['user','token','keys','dir'].map(k=>[k,{textContent:''}]));
const buttons=Object.fromEntries(['login','dashboard','keygen','keys','logout'].map(k=>[k,{disabled:false}]));
const $=(selector)=>{const m=selector.match(/^\[data-(session-field|account)="(\w+)"\]$/);assert(m);return (m[1]==='account'?buttons:fields)[m[2]];};
const ctx={status:{summary:{session:{}}}};
const {update,accountAction}=new Function('$','ctx','sessionRoot','pendingSessionActions','notify',action+logic+';return {update,accountAction}')($,ctx,{},new WeakSet(),()=>{});
(async()=>{
  let count=0;
  for(const keys of [false,true])for(const token of [false,true]){
    ctx.status.summary.session={has_local_keys:keys,has_token:token};update();
    assert.equal(buttons.keygen.disabled,keys);
    assert.equal(buttons.keys.disabled,!keys);
    assert.equal(buttons.logout.disabled,!token);
    assert.equal(fields.token.textContent,token ? '已登录' : '未登录');
    assert.equal(fields.keys.textContent,keys ? '已保存' : '未初始化');
    count++;
  }
  ctx.status={};update();assert.equal(fields.user.textContent,'本机离线');assert.equal(fields.dir.textContent,'—');count++;
  ctx.status={summary:{session:{user:'<b>A</b>'},data_dir:'/new'}};update();assert.equal(fields.user.textContent,'<b>A</b>');assert.equal(fields.dir.textContent,'/new');count++;
  for(const kind of ['keygen','logout','login','dashboard','keys']){
    ctx.status.summary.session={has_local_keys:kind==='keys',has_token:kind==='logout'};update();
    let release;let calls=0;
    const task=accountAction(buttons[kind],async()=>{calls++;await new Promise(r=>release=r)});
    for(let i=0;i<25;i++)update();assert.equal(buttons[kind].disabled,true);
    await accountAction(buttons[kind],async()=>calls++);assert.equal(calls,1);
    ctx.status.summary.session={has_local_keys:kind==='keygen',has_token:false};
    release();await task;assert.equal(buttons[kind].disabled,['keygen','logout','keys'].includes(kind));count++;
  }
  ctx.status.summary.session={};update();await accountAction(buttons.keygen,async()=>{throw Error('expected')});assert.equal(buttons.keygen.disabled,false);count++;
  await accountAction(buttons.keygen,async()=>{});assert.equal(buttons.keygen.disabled,false);count++;
  const result={passed:count,scope:'Extracted production update/accountAction functions plus exact core.action; lightweight node stubs, not DOM/focus/browser validation'};
  console.log(JSON.stringify(result,null,2));
})().catch(e=>{console.error(e);process.exitCode=1});
