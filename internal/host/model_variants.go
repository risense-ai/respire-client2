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

// modelDir resolves the runtime's default BGE-M3 directory
// (default_user_model_dir: <RSRS_DATA_DIR>/models/bge-m3).
func (m *Manager) modelDir(ctx context.Context) (string, error) {
	_, body, err := m.Health(ctx)
	if err != nil {
		return "", err
	}
	var health struct {
		DataDir string `json:"data_dir"`
	}
	if err = json.Unmarshal(body, &health); err != nil {
		return "", err
	}
	if health.DataDir == "" {
		return "", os.ErrInvalid
	}
	return filepath.Join(health.DataDir, "models", "bge-m3"), nil
}

// installedVariants returns the variants whose onnx file exists with the exact
// pinned size under dir.
func installedVariants(dir string) map[string]int64 {
	present := map[string]int64{}
	for _, variant := range modelVariantFiles {
		if info, err := os.Stat(filepath.Join(dir, variantFileName(variant.Name))); err == nil && info.Size() == variant.Size {
			present[variant.Name] = info.Size()
		}
	}
	return present
}

// ModelVariants reports install state and location for every BGE-M3 variant,
// powering the client's model table.
func (m *Manager) ModelVariants(ctx context.Context) (map[string]any, error) {
	dir, err := m.modelDir(ctx)
	if err != nil {
		return nil, err
	}
	present := installedVariants(dir)
	variants := make([]map[string]any, 0, len(modelVariantFiles))
	for _, variant := range modelVariantFiles {
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
		"model_dir":           dir,
		"tokenizer_installed": tokenizerErr == nil,
		"variants":            variants,
	}, nil
}
