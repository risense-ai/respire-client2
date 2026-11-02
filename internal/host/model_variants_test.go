package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseEngineArgs(t *testing.T) {
	engine, model, err := parseEngineArgs([]string{"--model=fp16", "gpu"})
	if err != nil || engine != "gpu" || model != "fp16" {
		t.Fatalf("parseEngineArgs = %q, %q, %v", engine, model, err)
	}
	engine, model, err = parseEngineArgs([]string{"cpu", "--model=quantized"})
	if err != nil || engine != "cpu" || model != "quantized" {
		t.Fatalf("parseEngineArgs = %q, %q, %v", engine, model, err)
	}
	if _, _, err = parseEngineArgs(nil); err == nil {
		t.Fatal("empty args must fail")
	}
	if _, _, err = parseEngineArgs([]string{"--model=fp16"}); err == nil {
		t.Fatal("missing engine must fail")
	}
}

func TestInstalledVariants(t *testing.T) {
	dir := t.TempDir()
	if present := installedVariants(dir); len(present) != 0 {
		t.Fatalf("empty dir must report no variants, got %v", present)
	}
	onnx := filepath.Join(dir, "onnx")
	if err := os.MkdirAll(onnx, 0o755); err != nil {
		t.Fatal(err)
	}
	full := modelVariantFiles[1]
	if err := os.WriteFile(filepath.Join(dir, variantFileName(full.Name)), make([]byte, full.Size), 0o644); err != nil {
		t.Fatal(err)
	}
	// Wrong size must not count as installed.
	truncated := modelVariantFiles[2]
	if err := os.WriteFile(filepath.Join(dir, variantFileName(truncated.Name)), make([]byte, 12), 0o644); err != nil {
		t.Fatal(err)
	}
	present := installedVariants(dir)
	if len(present) != 1 {
		t.Fatalf("expected only %q installed, got %v", full.Name, present)
	}
	if _, ok := present[full.Name]; !ok {
		t.Fatalf("%q missing from %v", full.Name, present)
	}
}

func TestEngineEnvelope(t *testing.T) {
	probe := json.RawMessage(`{"status":"ok","summary":{"engine":"gpu","dims":1024},"items":[],"details":{},"errors":[]}`)
	raw := engineEnvelope("gpu", "fp16", false, "", probe)
	var envelope struct {
		Status  string `json:"status"`
		Summary struct {
			Engine   string          `json:"engine"`
			Model    string          `json:"model"`
			Fallback bool            `json:"fallback"`
			Warning  string          `json:"warning"`
			Probe    json.RawMessage `json:"probe"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Status != "ok" || envelope.Summary.Engine != "gpu" || envelope.Summary.Model != "fp16" || envelope.Summary.Fallback {
		t.Fatalf("unexpected envelope: %s", raw)
	}
	if len(envelope.Summary.Probe) == 0 {
		t.Fatal("probe summary missing")
	}
	raw = engineEnvelope("cpu", "quantized", true, "probe failed", probe)
	envelope = struct {
		Status  string `json:"status"`
		Summary struct {
			Engine   string          `json:"engine"`
			Model    string          `json:"model"`
			Fallback bool            `json:"fallback"`
			Warning  string          `json:"warning"`
			Probe    json.RawMessage `json:"probe"`
		} `json:"summary"`
	}{}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Summary.Fallback || envelope.Summary.Warning != "probe failed" || envelope.Summary.Engine != "cpu" {
		t.Fatalf("unexpected fallback envelope: %s", raw)
	}
}

// seedBaseline writes the CLI's baseline file set (tokenizer + quantized onnx)
// so a candidate directory counts as a model home.
func seedBaseline(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "onnx"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tokenizer.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, variantFileName("quantized")), []byte("onnx"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveModelDir(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	exeDir := t.TempDir()
	user := filepath.Join(root, "models", "bge-m3")

	// Nothing installed anywhere: report the expected user location.
	if got := resolveModelDir(root, filepath.Join(exeDir, "rsrs.exe"), false, home); got != user {
		t.Fatalf("bare setup must fall back to the user dir, got %q", got)
	}
	// Files in the user dir win for both managed and external runtimes.
	seedBaseline(t, user)
	if got := resolveModelDir(root, filepath.Join(exeDir, "rsrs.exe"), true, home); got != user {
		t.Fatalf("user dir must win, got %q", got)
	}
	// External runtime without RSRS_DATA_DIR: legacy profile dirs are scanned.
	if err := os.RemoveAll(user); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(home, ".respire", "models", "bge-m3")
	seedBaseline(t, legacy)
	if got := resolveModelDir(root, filepath.Join(exeDir, "rsrs.exe"), false, home); got != legacy {
		t.Fatalf("external runtime must find the legacy dir, got %q", got)
	}
	// Managed runtime runs with RSRS_DATA_DIR set: legacy dirs are skipped and
	// the packaged copy next to the executable is the remaining fallback.
	packaged := filepath.Join(exeDir, "models", "bge-m3")
	seedBaseline(t, packaged)
	if got := resolveModelDir(root, filepath.Join(exeDir, "rsrs.exe"), true, home); got != packaged {
		t.Fatalf("managed runtime must use the packaged dir, got %q", got)
	}
}
