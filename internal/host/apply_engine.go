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
// service, immediately reloads the model through a real inference probe and,
// when the selected engine cannot run on this machine, falls back to CPU with
// an installed weight file instead of leaving inference broken.
func applyEngine(ctx context.Context, m *Manager, args []string, id string) (json.RawMessage, error) {
	engine, model, err := parseEngineArgs(args)
	if err != nil {
		return nil, err
	}
	cliExec := func(words ...string) (json.RawMessage, error) {
		return m.executeRPC(ctx, "cli.exec", append([]string{"--json"}, words...), id)
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
	fallback := fallbackVariant(ctx, m)
	if _, err := cliExec("model", "engine", "--model="+fallback, "--", "cpu"); err != nil {
		return nil, fmt.Errorf("切换到 %s 失败（%v），且回退 CPU 也未成功: %v", engine, probeErr, err)
	}
	fbProbe, err := cliExec("model", "probe")
	if err != nil {
		return nil, fmt.Errorf("切换到 %s 失败（%v），且回退 CPU 后推理验证仍失败: %v", engine, probeErr, err)
	}
	return engineEnvelope("cpu", fallback, true, probeErr.Error(), fbProbe), nil
}

// fallbackVariant prefers the CPU default quantized graph and otherwise uses
// any installed weight file so the CPU fallback can always load a model.
func fallbackVariant(ctx context.Context, m *Manager) string {
	fallback := "quantized"
	dir, err := m.modelDir(ctx)
	if err != nil {
		return fallback
	}
	present := installedVariants(dir)
	if _, ok := present["quantized"]; ok {
		return fallback
	}
	for _, name := range []string{"int8", "uint8", "q4", "q4f16", "fp16"} {
		if _, ok := present[name]; ok {
			return name
		}
	}
	return fallback
}

// engineEnvelope composes the operation receipt; probe carries the runtime's
// own probe summary so the UI can show dims, engine and elapsed time.
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
