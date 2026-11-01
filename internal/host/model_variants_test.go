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
