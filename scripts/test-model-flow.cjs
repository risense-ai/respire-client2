const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const vm = require("node:vm");
const { pathToFileURL } = require("node:url");

async function main() {
  const tree = await fs.mkdtemp(path.join(os.tmpdir(), "respire-model-contract-"));
  const source = path.join(__dirname, "../internal/desktop/frontend");
  try {
    await fs.writeFile(path.join(tree, "package.json"), '{"type":"module"}');
    for (const file of ["pages/model-kit.js", "ui/core.js"]) {
      await fs.mkdir(path.dirname(path.join(tree, file)), { recursive: true });
      await fs.copyFile(path.join(source, file), path.join(tree, file));
    }
    const {applyEngineChoice, cancelModelWork, detectBestEngine, engineAvailable, localModelDownload, modelActivity, profileStateSettled, submitProfileSwitch, switchFailure, switchProgressLabel, variantFromItem, fetchModelDownloadTask, runtimeModelScope, MODEL_MODES} = await import(pathToFileURL(path.join(tree, "pages/model-kit.js")));
    const S = (sw={}, active=null, operation={}) => ({
      progress:{command:"model.progress",status:"ok",summary:{switch:sw,operation}},
      catalog:{command:"model.catalog",status:"ok",summary:{active}},
    });
    const exact={expectedId:"new",priorId:"old"};
    assert.equal(profileStateSettled(S({id:"old",state:"ready"}),exact).settled,false);
    assert.equal(profileStateSettled(S({id:"new",state:"pending"}),exact).settled,false);
    assert.deepEqual(profileStateSettled(S({id:"new",state:"failed"}),exact),{settled:true,failed:true});
    const unbound = {expectedId:null};
    for (const phase of ["pending","ready","failed"])
      assert.deepEqual(profileStateSettled(S({id:"other",state:phase,target:"m3"},{model:"m3",engine:"cpu"}),unbound),
        {settled:false,failed:false});
    assert.equal(unbound.expectedId,null,"no progress observation can retroactively bind an unconfirmed receipt");
    const invalidProgress = S({id:"new",state:"ready"});
    invalidProgress.progress.status = "fail";
    assert.equal(profileStateSettled(invalidProgress, exact).settled,false);
    assert.match(switchFailure({switchState:{error:"real failure",semantic:"unchanged"}}),/real failure.*原模型未被更改/);
    assert.match(switchFailure({switchState:{rollback:{state:"unverified"}}}),/尚未确认/);
    assert.match(switchFailure({switchState:{recovery:"previous_ready"}}),/重新验证可用/);
    assert.equal(switchProgressLabel(S({id:"own",state:"pending"},null,{id:"own",phase:"switch-index",done:1,total:2})),"重建记忆索引 · 50%");
    assert.equal(switchProgressLabel(S({id:"own",state:"pending"},null,{id:"other",phase:"switch-index",done:1,total:2})),"等待 Runtime 完成切换");
    assert.equal(detectBestEngine("windows-amd64",{gpu:false,npu:{available:true}}),"cpu");
    assert.equal(detectBestEngine("windows-amd64",{gpu:{available:true},npu:{available:true}}),"gpu");
    assert.equal(engineAvailable("windows-amd64","npu"),false);
    assert.equal(modelActivity({index:{running:true},operation:{active:true,phase:"verify"}}),"indexing");
    assert.equal(modelActivity({operation:{active:true,phase:"switch-download"}}),"switching");
    assert.equal(modelActivity({task:{busy:true,stage:"下载 model_quantized.cpu-v1.ort"}}),"downloading");
    assert.equal(modelActivity({task:{busy:true,stage:"更新 Runtime"}}),"idle");
    assert.equal(localModelDownload({busy:true,stage:"下载 Granite_97m.onnx"}),true);
    assert.equal(variantFromItem("下载 model_quantized.cpu-v1.ort"),"quantized");
    assert.equal(variantFromItem("下载 model.onnx",[{name:"standard",path:"C:/some/model.onnx"}]),"standard");
    assert.equal(MODEL_MODES.filter(x=>x.engine==="cpu").length,1);
    let responses=[]; const calls=[];
    globalThis.fetch=async (url,opts)=>{
     calls.push([url,opts?.body ? JSON.parse(opts.body) : undefined]);
     const value=responses.shift();
     assert.ok(value!==undefined,"unexpected "+url);
     if(value instanceof Error) throw value;
     if(typeof value === "function") return value(url,opts);
     return {ok:true,status:200,json:async()=>value};
    };
    const ok=(result)=>({ok:true,result});
    responses=[ok(S()),ok({id:"abc",status:"pending"}),ok(S({id:"abc",state:"ready"},{model:"m3",engine:"cpu",variant:"quantized"}))];
    assert.equal((await applyEngineChoice({model:"m3",engine:"cpu",variant:"quantized"})).failed,false);
    assert.deepEqual(calls.slice(0,3),[["/api/model-profile-state",undefined],["/api/model-switch",{model:"m3",engine:"cpu",variant:"quantized",receipt:true}],["/api/model-profile-state",undefined]]);
    responses=[ok(S()),ok("configured"),ok({id:"def",status:"pending"})];
    assert.equal((await submitProfileSwitch("granite-97m-r2","cpu","standard")).expectedId,"def");
    assert.equal(calls.at(-2)[0],"/api/model-configure");
    const pick = {model:"m3",engine:"cpu",variant:"quantized"};
    let from = calls.length;
    for (const receipt of ["切换任务已提交",{status:"ok"},{id:" ",status:"pending"}]) {
      responses = [ok(S({id:"previous",state:"ready"})),ok(receipt),
        ok(S({id:"newer-but-other",state:"ready",target:"m3"},{model:"m3",engine:"cpu"}))];
      const outcome = await applyEngineChoice(pick);
      assert.equal(outcome.unconfirmed,true);
      assert.equal(outcome.failed,false);
      assert.match(outcome.warning,/结果尚未确认.*不要重复提交/);
      assert.deepEqual(calls.slice(from).map(x=>x[0]),["/api/model-profile-state","/api/model-switch"],
        "an unbound write is neither polled as a current task nor replayed");
      assert.equal(responses.length,1);
      from = calls.length;
    }
    responses=[ok(S()),new Error("connection lost after dispatch")];
    assert.equal((await applyEngineChoice(pick)).unconfirmed,true);
    assert.deepEqual(calls.slice(from).map(x=>x[0]),["/api/model-profile-state","/api/model-switch"]);
    from = calls.length;
    responses=[ok(S()),() => ({ok:false,status:500,json:async()=>({ok:false,error:"method not found"})})];
    assert.equal((await applyEngineChoice(pick)).unconfirmed,true);
    assert.deepEqual(calls.slice(from).map(x=>x[0]),["/api/model-profile-state","/api/model-switch"]);
    from = calls.length;
    responses=[ok(S()),() => ({ok:false,status:403,json:async()=>({ok:false,error:"method not found"})})];
    await assert.rejects(applyEngineChoice(pick),/method not found/);
    assert.deepEqual(calls.slice(from).map(x=>x[0]),["/api/model-profile-state","/api/model-switch"]);

    let scopeIsCurrent = true;
    from = calls.length;
    responses = [ok(S()),ok({id:"same-task",status:"pending"}),() => {
      scopeIsCurrent = false;
      return {ok:true,status:200,json:async()=>ok(S({id:"same-task",state:"ready"}))};
    }];
    let observed = 0;
    assert.equal((await applyEngineChoice({...pick,isCurrent:()=>scopeIsCurrent,onTick:()=>observed++})).unconfirmed,true);
    assert.equal(observed,0,"a matching ID must never be rendered in another observed account scope");
    assert.deepEqual(calls.slice(from).map(x=>x[0]),["/api/model-profile-state","/api/model-switch","/api/model-profile-state"]);
    scopeIsCurrent = true;
    from = calls.length;
    responses = [ok(S()),()=>{scopeIsCurrent = false;return {ok:true,status:200,json:async()=>ok("configured")};}];
    await assert.rejects(submitProfileSwitch("granite-97m-r2","cpu","standard",()=>scopeIsCurrent),/账户或模型选择已变化/);
    assert.deepEqual(calls.slice(from).map(x=>x[0]),["/api/model-profile-state","/api/model-configure"]);

    const delay = globalThis.setTimeout;
    try {
      globalThis.setTimeout = fn => { fn(); return 1; };
      const effects = [];
      from = calls.length;
      responses = [ok(S({id:"previous",state:"ready"})),ok({id:"own-task",status:"pending"}),
        ok(S({id:"previous",state:"failed"})),ok(S({id:"peer",state:"ready",target:"m3"},{model:"m3",engine:"cpu"})),
        ok(S({id:"own-task",state:"failed",error:"original failure",semantic:"unchanged"}))];
      const settled = await applyEngineChoice({...pick,onTick:state=>effects.push(state.progress.summary.switch.id)});
      assert.equal(settled.failed,true);
      assert.match(settled.warning,/original failure.*原模型未被更改/);
      assert.deepEqual(effects,["own-task"],"ignore other task progress as well as terminal state");
      assert.equal(calls.slice(from).filter(row=>row[0]==="/api/model-switch").length,1);
    } finally {
      globalThis.setTimeout = delay;
    }

    globalThis.document = {dispatchEvent(){}};
    from = calls.length;
    responses=[ok(S()),() => ({ok:false,status:404,json:async()=>({ok:false,error:"unknown desktop action"})}),
      (_url,opts) => ({ok:true,status:200,json:async()=>ok({
        id:JSON.parse(opts.body).id,name:"model_engine_apply",state:"done",
        envelope:{summary:{engine:"cpu",model:"quantized"}},
      })}),ok(null)];
    const realLegacy = await applyEngineChoice(pick);
    assert.equal(realLegacy.failed,false);
    assert.equal(realLegacy.engine,"cpu");
    assert.deepEqual(calls.slice(from).map(x=>x[0]),["/api/model-profile-state","/api/model-switch","/api/execute","/api/operation-ack"],
      "preserve the old handler only when the outer router proves the new request was never dispatched");
    const modelsSource = await fs.readFile(path.join(source,"pages/models.js"),"utf8");
    const wizardSource = await fs.readFile(path.join(source,"onboarding/wizard.js"),"utf8");
    const desktop = () => ({platform:"windows-amd64",connected:true,health:{account_id:"a",
      instance_id:"rt",generation:1,data_dir:"/fixture",credential_state:"available"}});
    const modulePart = (source, first, next) => {
      const start=source.indexOf(first), end=source.indexOf(next,start);
      assert.ok(start >= 0 && end > start);
      return source.slice(start,end);
    };
    const models = {
      ctx:{desktop:desktop(),refresh:async()=>{}}, root:{isConnected:true}, modelID:"m3",
      switchInFlight:false, switchingVariant:"", stateOverride:null,lastVerifiedAt:null,
      PAGE_STATES:{ERROR:"error",FALLBACK:"fallback",UNCONFIRMED:"unconfirmed"},runtimeModelScope,applyEngineChoice,
      adoptProfileState(){ assert.fail("unbound model action observed peer progress"); },
      renderDynamic(){}, renderVariants(){},
      notify:(message,isError)=>models.notices.push([message,isError]),notices:[],
    };
    let modelsAction = modulePart(modelsSource,"  async function applyEngineAfterDownload(","  // Host configuration");
    responses = [ok(S()),ok("切换任务已提交")];
    await vm.runInNewContext(modelsAction+'\napplyEngineAfterDownload({title:"均衡",variant:"quantized"},"cpu")',models);
    assert.equal(models.lastVerifiedAt,null);
    assert.equal(models.stateOverride,"unconfirmed","unconfirmed stays visible without looking like failure");
    assert.match(models.notices[0][0],/尚未确认.*不要重复提交/);
    const oldDownloadScope = {
      ctx:{desktop:desktop()}, root:{isConnected:true}, switchInFlight:false,stateOverride:null,modelID:"m3",
      runtimeModelScope,isProfileModel:id=>id!=="m3",currentMode:()=>MODEL_MODES[0],
      inventory:{tokenizer_installed:true,cpu_container_ready:false},modeVariant:()=>({installed:true}),
      currentMirror:()=>"auto",renderDynamic(){},
      openDownloadModal:()=>({done:Promise.resolve().then(()=>{
        oldDownloadScope.ctx.desktop.health.generation++;
        return true;
      })}),
      fetchInventory(){assert.fail("the detached account's download must not continue");},
      applyEngineAfterDownload(){assert.fail("the detached account's download must not start a switch");},
    };
    const primaryAction = modulePart(modelsSource,"  async function handlePrimaryAction(","  async function applyEngineAfterDownload(");
    await vm.runInNewContext(primaryAction+'\nhandlePrimaryAction()',oldDownloadScope);

    const wizard = {
      ctx:{desktop:desktop()}, root:{isConnected:true}, state:{step:"execute",model:"m3",device:"cpu",variant:"quantized"},
      active:()=>true,runtimeModelScope,engineAvailable,engineCapabilities:()=>({}),applyEngineChoice,
      setStage(){},executeStatus:(message,isError)=>wizard.notices.push([message,isError]),
      executeActions:choices=>{wizard.actions=choices;},notices:[], actions:[],
      renderExecuteSuccess(){assert.fail("unconfirmed wizard claims success");},
      runDownloadStage(){assert.fail("unexpected new download");},
      runIndexStage(){assert.fail("unconfirmed wizard must not start a follow-up write");},
      failure(){assert.fail("unconfirmed is not marked failed or offered retry");},
    };
    const executePart = modulePart(wizardSource,"  async function runExecute(","  async function runDownloadStage");
    const enablePart = modulePart(wizardSource,"  async function runEnableStage(","  async function runIndexStage");
    from = calls.length;
    responses = [ok(S()),ok({status:"ok"})];
    await vm.runInNewContext(executePart+enablePart+'\nrunExecute("enable")',wizard);
    assert.match(wizard.notices.at(-1)[0],/尚未确认/);
    assert.deepEqual(Array.from(wizard.actions,x=>x[1]),["exit-models"]);
    assert.deepEqual(calls.slice(from).map(x=>x[0]),["/api/model-profile-state","/api/model-switch"]);

    responses=[new Error("desktop API request rejected"),ok({task:{busy:true,stage:"下载模型 quantized"}})];
    assert.equal((await fetchModelDownloadTask()).busy,true);
    assert.equal(calls.at(-1)[0],"/api/state");
    responses=[ok("cancelled")];
    await cancelModelWork({busy:true,stage:"下载模型 quantized"},{active:true,id:"unrelated"});
    assert.deepEqual(calls.at(-1),["/api/model-download-cancel",{pause:false}]);
    console.log("PASS targeted model states, task ID/correlation, pending receipt, Windows capability, progress, cancellation and safe fallback");
  } finally {
    await fs.rm(tree, { recursive: true, force: true });
  }
}
main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
