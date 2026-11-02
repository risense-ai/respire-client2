package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

// BGE-M3 onnx variants accepted by the runtime's `model engine --model` and
// `model install-m3 --model` (cli MODEL_VARIANTS). Sizes must mirror
// crates/core-sdk/src/business.rs so install state can be verified locally
// without hashing the multi-hundred-MiB files on every poll.
type modelVariantFile struct {
	Name string
	Size int64
}

var modelVariantFiles = []modelVariantFile{
	{"quantized", 569694530},
	{"fp16", 1133992936},
	{"int8", 568456694},
	{"uint8", 568456765},
	{"q4", 1248237611},
	{"q4f16", 699932840},
}

// modelVariantByName returns the pinned file name relative to the model dir.
func variantFileName(name string) string {
	return filepath.Join("onnx", "model_"+name+".onnx")
}

// modelDir resolves the runtime's BGE-M3 directory the same way the CLI's
// resolve_model_dir does: <main data dir>/models/bge-m3 first — the
// RSRS_DATA_DIR root or ~/.rsrs, reported by health as installation_root —
// then the legacy profile dirs (only for runtimes launched without
// RSRS_DATA_DIR) and the packaged copy next to the runtime executable.
// health.data_dir is the workspace profile (client.json data_dir) and must
// not be used for model files.
func (m *Manager) modelDir(ctx context.Context) (string, error) {
	_, body, err := m.Health(ctx)
	if err != nil {
		return "", err
	}
	var health struct {
		Exe              string `json:"exe"`
		InstallationRoot string `json:"installation_root"`
	}
	if err = json.Unmarshal(body, &health); err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if health.InstallationRoot == "" {
		health.InstallationRoot = filepath.Join(home, ".rsrs")
	}
	// A managed runtime is launched with RSRS_DATA_DIR=installation_root, so
	// the CLI skips the legacy dirs; external runtimes run without it.
	return resolveModelDir(health.InstallationRoot, health.Exe, m.Owns(health.Exe), home), nil
}

// resolveModelDir mirrors crates/core-sdk business::resolve_model_dir.
func resolveModelDir(installationRoot, exe string, managed bool, home string) string {
	user := filepath.Join(installationRoot, "models", "bge-m3")
	candidates := []string{user}
	if !managed {
		candidates = append(candidates,
			filepath.Join(home, ".respire", "models", "bge-m3"),
			filepath.Join(home, ".onememory", "models", "bge-m3"),
		)
	}
	if exe != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "models", "bge-m3"))
	}
	for _, candidate := range candidates {
		if modelFilesPresent(candidate) {
			return candidate
		}
	}
	return user
}

// modelFilesPresent matches the CLI's baseline: tokenizer.json plus the
// quantized onnx graph.
func modelFilesPresent(dir string) bool {
	if info, err := os.Stat(filepath.Join(dir, "tokenizer.json")); err != nil || info.IsDir() {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, variantFileName("quantized")))
	return err == nil && !info.IsDir()
}

// installedVariantsFor returns the variants of one catalog model whose onnx
// file exists with the exact pinned size under dir.
func installedVariantsFor(definition modelDefinition, dir string) map[string]int64 {
	present := map[string]int64{}
	for _, variant := range definition.Variants {
		if info, err := os.Stat(filepath.Join(dir, variantFileName(variant.Name))); err == nil && info.Size() == variant.Size {
			present[variant.Name] = info.Size()
		}
	}
	return present
}

// installedVariants returns the m3 variants whose onnx file exists with the
// exact pinned size under dir.
func installedVariants(dir string) map[string]int64 {
	return installedVariantsFor(modelDefinition{Variants: modelVariantFiles}, dir)
}

// ModelVariants reports install state and location for every variant of one
// catalog model, powering the client's model table. m3 keeps the legacy
// resolution and CLI variant table; profile models read models/<id>.
func (m *Manager) ModelVariants(ctx context.Context, model string) (map[string]any, error) {
	definition, err := modelDefinitionByID(model)
	if err != nil {
		return nil, err
	}
	var dir string
	if definition.Legacy {
		dir, err = m.modelDir(ctx)
		if err != nil {
			return nil, err
		}
	} else {
		dir = m.ProfileDir(model)
	}
	present := installedVariantsFor(definition, dir)
	variants := make([]map[string]any, 0, len(definition.Variants))
	for _, variant := range definition.Variants {
		entry := map[string]any{
			"name":      variant.Name,
			"size":      variant.Size,
			"path":      filepath.Join(dir, variantFileName(variant.Name)),
			"bytes":     0,
			"installed": false,
		}
		if size, ok := present[variant.Name]; ok {
			entry["bytes"], entry["installed"] = size, true
		}
		variants = append(variants, entry)
	}
	_, tokenizerErr := os.Stat(filepath.Join(dir, "tokenizer.json"))
	return map[string]any{
		"model":               definition.ID,
		"model_dir":           dir,
		"tokenizer_installed": tokenizerErr == nil,
		"variants":            variants,
	}, nil
}
