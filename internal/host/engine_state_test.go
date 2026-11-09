package host

import (
	"os"
	"path/filepath"
	"testing"
)

// seedVariant writes a size-exact sparse file so installedVariants counts it.
func seedVariant(t *testing.T, dir, variant string) {
	t.Helper()
	path := filepath.Join(dir, variantFileName(variant))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	size := int64(4)
	for _, candidate := range modelVariantFiles {
		if candidate.Name == variant {
			size = candidate.Size
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeInferenceJSON(t *testing.T, root, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "inference.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadInferenceSelection(t *testing.T) {
	m := &Manager{DataRoot: t.TempDir()}
	if selection := m.readInferenceSelection(); selection.Engine != "" || selection.Model != "" {
		t.Fatalf("missing file must degrade to empty, got %+v", selection)
	}
	writeInferenceJSON(t, m.DataRoot, `{"engine":"gpu","model":"fp16"}`)
	selection := m.readInferenceSelection()
	if selection.Engine != "gpu" || selection.Model != "fp16" {
		t.Fatalf("unexpected selection: %+v", selection)
	}
	// Legacy auto and force_cpu both pin the engine to CPU.
	writeInferenceJSON(t, m.DataRoot, `{"engine":"auto"}`)
	if selection = m.readInferenceSelection(); selection.Engine != "cpu" {
		t.Fatalf("auto must read as cpu, got %+v", selection)
	}
	writeInferenceJSON(t, m.DataRoot, `{"engine":"gpu","force_cpu":true}`)
	if selection = m.readInferenceSelection(); selection.Engine != "cpu" {
		t.Fatalf("force_cpu must read as cpu, got %+v", selection)
	}
	writeInferenceJSON(t, m.DataRoot, `not json`)
	if selection = m.readInferenceSelection(); selection.Engine != "" {
		t.Fatalf("broken json must degrade to empty, got %+v", selection)
	}
}

func TestEngineSnapshot(t *testing.T) {
	root := t.TempDir()
	install := filepath.Join(root, "rsrs")
	m := &Manager{DataRoot: root}
	dir := filepath.Join(install, "models", "bge-m3")

	// Bare install: CPU defaults, nothing installed, path still reported.
	snapshot := m.EngineSnapshot(install, "")
	if snapshot["engine"] != "cpu" || snapshot["variant"] != "quantized" {
		t.Fatalf("bare snapshot = %v", snapshot)
	}
	if snapshot["variant_installed"] != false || snapshot["file"] != variantFileName("quantized") {
		t.Fatalf("bare snapshot = %v", snapshot)
	}
	if filepath.Dir(filepath.Dir(snapshot["path"].(string))) != dir {
		t.Fatalf("path must live under %s, got %v", dir, snapshot["path"])
	}

	// GPU without an explicit variant: fp16 missing, quantized present — the
	// runtime downgrades to quantized and the snapshot must say so.
	seedVariant(t, dir, "quantized")
	writeInferenceJSON(t, root, `{"engine":"gpu"}`)
	snapshot = m.EngineSnapshot(install, "")
	if snapshot["engine"] != "gpu" || snapshot["variant"] != "quantized" || snapshot["variant_installed"] != true {
		t.Fatalf("downgrade snapshot = %v", snapshot)
	}
	if snapshot["note"] == "" {
		t.Fatal("downgrade must carry a note")
	}

	// Explicit gpu+fp16 with the file installed: reported as-is.
	seedVariant(t, dir, "fp16")
	writeInferenceJSON(t, root, `{"engine":"gpu","model":"fp16"}`)
	snapshot = m.EngineSnapshot(install, "")
	if snapshot["engine"] != "gpu" || snapshot["variant"] != "fp16" || snapshot["variant_installed"] != true || snapshot["note"] != "" {
		t.Fatalf("gpu snapshot = %v", snapshot)
	}

	// Explicit variant whose file went missing: flag it, keep the name.
	if err := os.Remove(filepath.Join(dir, variantFileName("fp16"))); err != nil {
		t.Fatal(err)
	}
	snapshot = m.EngineSnapshot(install, "")
	if snapshot["variant"] != "fp16" || snapshot["variant_installed"] != false || snapshot["note"] == "" {
		t.Fatalf("missing-file snapshot = %v", snapshot)
	}
}
