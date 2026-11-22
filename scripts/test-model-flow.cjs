const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
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
    const {applyEngineChoice, cancelModelWork, detectBestEngine, engineAvailable, localModelDownload, modelActivity, profileStateSettled, submitProfileSwitch, switchFailure, switchProgressLabel, variantFromItem, fetchModelDownloadTask, MODEL_MODES} = await import(pathToFileURL(path.join(tree, "pages/model-kit.js")));
    const S = (sw={}, active=null, operation={}) => ({progress:{details:{switch:sw,operation}},catalog:{details:{active}}});
    const exact={expectedId:"new",priorId:"old"};
    assert.equal(profileStateSettled(S({id:"old",state:"ready"}),exact).settled,false);
    assert.equal(profileStateSettled(S({id:"new",state:"pending"}),exact).settled,false);
    assert.deepEqual(profileStateSettled(S({id:"new",state:"failed"}),exact),{settled:true,failed:true});
    const legacy={expectedId:null,priorId:"old",priorKnown:true,model:"m3",engine:"cpu",sawPending:false};
    assert.equal(profileStateSettled(S({id:"old",state:"ready"}),legacy).settled,false);
    assert.equal(profileStateSettled(S({id:"other",state:"pending",target:"granite"}),legacy).settled,false);
    assert.equal(legacy.expectedId,null);
    assert.equal(profileStateSettled(S({id:"new",state:"pending",target:"m3"}),legacy).settled,false);
    assert.equal(profileStateSettled(S({id:"new",state:"ready"}),legacy).settled,true);
    assert.equal(profileStateSettled(S({id:"old",state:"ready"},{model:"m3",engine:"cpu"}),{model:"m3",engine:"cpu",priorKnown:false}).settled,false);
    assert.match(switchFailure({switchState:{error:"real failure",semantic:"unchanged"}}),/real failure.*原模型未被更改/);
    assert.match(switchFailure({switchState:{rollback:{state:"unverified"}}}),/尚未确认/);
    assert.match(switchFailure({switchState:{recovery:"previous_ready"}}),/重新验证可用/);
    assert.equal(switchProgressLabel(S({},null,{phase:"switch-index",done:1,total:2})),"重建记忆索引 · 50%");
    assert.equal(detectBestEngine("windows-amd64",{npu:{available:true}}),"cpu");
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
     return {ok:true,json:async()=>value};
    };
    const ok=(result)=>({ok:true,result});
    responses=[ok(S()),ok({id:"abc",status:"pending"}),ok(S({id:"abc",state:"ready"},{model:"m3",engine:"cpu",variant:"quantized"}))];
    assert.equal((await applyEngineChoice({model:"m3",engine:"cpu",variant:"quantized"})).failed,false);
    assert.deepEqual(calls.slice(0,3),[["/api/model-profile-state",undefined],["/api/model-switch",{model:"m3",engine:"cpu",variant:"quantized",receipt:true}],["/api/model-profile-state",undefined]]);
    responses=[ok(S()),ok("configured"),ok({id:"def",status:"pending"})];
    assert.equal((await submitProfileSwitch("granite-97m-r2","cpu","standard")).expectedId,"def");
    assert.equal(calls.at(-2)[0],"/api/model-configure");
    responses=[ok(S()),new Error("connection lost")];
    await assert.rejects(applyEngineChoice({model:"m3",engine:"cpu",variant:"quantized"}),/connection lost/);
    assert.equal(calls.at(-1)[0],"/api/model-switch");
    responses=[new Error("404"),ok({task:{busy:true,stage:"下载模型 quantized"}})];
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
