package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// parseEngineArgs reads the built argv; build() emits options before
// positional arguments, so match by prefix instead of position.
func parseEngineArgs(args []string) (engine, model string, err error) {
	for _, arg := range args {
		if value, ok := strings.CutPrefix(arg, "--model="); ok {
			model = value
		} else {
			engine = arg
		}
	}
	if engine == "" || model == "" {
		return "", "", fmt.Errorf("参数不完整")
	}
	return engine, model, nil
}

// applyEngine saves the inference engine + weight choice in the runtime
// service and immediately reloads the model through a real inference probe.
// When the selected engine fails verification, the previous selection is
// restored and the failure is reported with its cause — never silently
// downgraded to CPU (the runtime removed cross-engine fallback in
// 1.0.20-dev.15; the client must not reintroduce it).
func applyEngine(ctx context.Context, m *Manager, args []string, id string) (json.RawMessage, error) {
	engine, model, err := parseEngineArgs(args)
	if err != nil {
		return nil, err
	}
	if err := m.ensureRuntimeFeature(ctx, minVariantRuntimeVersion, "选择模型权重变体"); err != nil {
		return nil, err
	}
	cliExec := func(words ...string) (json.RawMessage, error) {
		return m.executeRPC(ctx, "cli.exec", append([]string{"--json"}, words...), id)
	}
	// Entry pre-check: applying a weight file that is not installed can only
	// end in a failed probe after a full model reload; refuse before touching
	// the runtime so the user gets an actionable message immediately.
	if dir, dirErr := m.modelDir(ctx); dirErr == nil {
		if _, ok := installedVariants(dir)[model]; !ok {
			return nil, fmt.Errorf("%s 权重文件未就绪（未下载或校验不一致），请先在列表中下载后再切换 %s", model, strings.ToUpper(engine))
		}
	}
	// Read the previous selection first: on failure this is exactly what gets
	// restored, so a fresh install without inference.json falls back to the
	// runtime defaults (CPU · quantized) it would have used anyway.
	previous := m.readInferenceSelection()
	prevEngine := previous.Engine
	if prevEngine == "" {
		prevEngine = "cpu"
	}
	prevModel := previous.Model
	if prevModel == "" {
		prevModel = defaultVariantFor(prevEngine)
	}
	// Persisting the choice clears the runtime's loaded-model cache, so the
	// probe below reloads with the selected engine and weight file.
	if _, err := cliExec("model", "engine", "--model="+model, "--", engine); err != nil {
		return nil, err
	}
	probe, probeErr := cliExec("model", "probe")
	if probeErr == nil {
		return engineEnvelope(engine, model, false, "", probe), nil
	}
	// The selected engine failed real inference. Restore the previous
	// selection, verify the restore with a probe and report both results.
	if _, err := cliExec("model", "engine", "--model="+prevModel, "--", prevEngine); err != nil {
		return nil, fmt.Errorf("切换到 %s · %s 失败（%v）；恢复原推理方式 %s · %s 也失败（%v），请重新应用原设置或执行 model reset-cpu",
			strings.ToUpper(engine), model, probeErr, strings.ToUpper(prevEngine), prevModel, err)
	}
	fbProbe, err := cliExec("model", "probe")
	if err != nil {
		return nil, fmt.Errorf("切换到 %s · %s 失败（%v）；已恢复 %s · %s 但恢复后推理验证未通过: %v",
			strings.ToUpper(engine), model, probeErr, strings.ToUpper(prevEngine), prevModel, err)
	}
	return engineEnvelope(prevEngine, prevModel, true, probeErr.Error(), fbProbe), nil
}

// engineEnvelope composes the operation receipt; probe carries the runtime's
// own probe summary (selected execution provider, dims, elapsed time) so the
// UI can show the inference method the runtime actually used. fallback means
// the requested selection failed verification and the previous selection was
// restored; warning then carries the probe failure cause.
func engineEnvelope(engine, model string, fallback bool, warning string, probe json.RawMessage) json.RawMessage {
	summary := map[string]any{"engine": engine, "model": model, "fallback": fallback}
	if warning != "" {
		summary["warning"] = warning
	}
	var probeEnvelope struct {
		Summary json.RawMessage `json:"summary"`
	}
	if json.Unmarshal(probe, &probeEnvelope) == nil && len(probeEnvelope.Summary) > 0 {
		summary["probe"] = probeEnvelope.Summary
	}
	raw, err := json.Marshal(map[string]any{
		"status": "ok", "summary": summary, "items": []any{}, "details": map[string]any{}, "errors": []any{},
	})
	if err != nil {
		raw, _ = json.Marshal(map[string]any{"status": "ok", "summary": summary})
	}
	return raw
}
