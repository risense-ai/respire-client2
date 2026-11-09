package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// The legacy inference selection lives in inference.json under the runtime's
// settings dir (RSRS_DATA_DIR or ~/.rsrs). It is the source of truth for the
// m3 legacy path; profile models keep their immutable selection in the store
// and are reported through model.catalog instead.
type inferenceSelection struct {
	Engine string
	Model  string
}

// defaultVariantFor mirrors the runtime's default_variant: CPU stays on the
// quantized graph, accelerators prefer fp16.
func defaultVariantFor(engine string) string {
	if engine == "gpu" || engine == "npu" {
		return "fp16"
	}
	return "quantized"
}

// readInferenceSelection parses inference.json; missing, legacy ("auto") or
// unreadable entries degrade to the runtime's own defaults (CPU). force_cpu
// pins the engine to CPU while keeping the stored variant.
func (m *Manager) readInferenceSelection() inferenceSelection {
	data, err := os.ReadFile(filepath.Join(m.DataRoot, "inference.json"))
	if err != nil {
		return inferenceSelection{}
	}
	var parsed struct {
		Engine   string `json:"engine"`
		Model    string `json:"model"`
		ForceCPU bool   `json:"force_cpu"`
	}
	if json.Unmarshal(data, &parsed) != nil {
		return inferenceSelection{}
	}
	engine := strings.ToLower(strings.TrimSpace(parsed.Engine))
	if engine == "" || engine == "auto" {
		engine = "cpu"
	}
	if parsed.ForceCPU {
		engine = "cpu"
	}
	return inferenceSelection{Engine: engine, Model: strings.TrimSpace(parsed.Model)}
}

// EngineSnapshot composes the locally readable inference state for the
// dashboard: configured engine, effective weight file and its on-disk path.
// It reads only files, so /api/state can embed it on every poll. When a
// profile model is active the runtime ignores these settings; the frontend
// overrides this snapshot with model.catalog's active selection.
func (m *Manager) EngineSnapshot(installationRoot, exe string) map[string]any {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	if installationRoot == "" && home != "" {
		installationRoot = filepath.Join(home, ".rsrs")
	}
	dir := resolveModelDir(installationRoot, exe, m.Owns(exe), home)
	selection := m.readInferenceSelection()
	engine := selection.Engine
	if engine == "" {
		engine = "cpu"
	}
	variant := selection.Model
	// The runtime silently downgrades to quantized only when the accelerator
	// default was never explicitly selected and its file is missing; mirror
	// that so the snapshot names the weight file inference actually uses.
	note := ""
	if engine != "cpu" && variant == "" {
		variant = defaultVariantFor(engine)
		installed := installedVariants(dir)
		if _, ok := installed[variant]; !ok {
			if _, ok := installed["quantized"]; ok {
				variant = "quantized"
				note = "默认 " + defaultVariantFor(engine) + " 权重未安装，Runtime 实际使用 quantized；需要时请先下载对应权重。"
			}
		}
	}
	if variant == "" {
		variant = defaultVariantFor(engine)
	}
	variantInstalled := false
	if _, ok := installedVariants(dir)[variant]; ok {
		variantInstalled = true
	} else if note == "" {
		note = "所选权重文件缺失或校验不一致，切换或重启后推理可能失败。"
	}
	return map[string]any{
		"source":            "legacy",
		"engine":            engine,
		"variant":           variant,
		"file":              variantFileName(variant),
		"path":              filepath.Join(dir, variantFileName(variant)),
		"model_dir":         dir,
		"variant_installed": variantInstalled,
		"note":              note,
	}
}
