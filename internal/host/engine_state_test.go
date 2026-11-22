package host

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
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

func TestLegacyEngineSnapshotScansWeightsOnceInCatalogOrder(t *testing.T) {
	dir := t.TempDir()
	seedVariant(t, dir, "quantized")
	quantizedPath := filepath.Join(dir, variantFileName("quantized"))
	fp16Path := filepath.Join(dir, variantFileName("fp16"))
	wantOrder := make([]string, 0, len(modelVariantFiles))
	for _, v := range modelVariantFiles {
		wantOrder = append(wantOrder, filepath.Join(dir, variantFileName(v.Name)))
	}
	for _, test := range []struct {
		name      string
		selection inferenceSelection
		want      map[string]any
	}{
		{"implicit accelerator falls back", inferenceSelection{Engine: "gpu"}, map[string]any{
			"source": "legacy", "engine": "gpu", "variant": "quantized", "file": variantFileName("quantized"),
			"path": quantizedPath, "model_dir": dir, "variant_installed": true,
			"note": "默认 fp16 权重未安装，Runtime 实际使用 quantized；需要时请先下载对应权重。",
		}},
		{"explicit unavailable variant keeps its identity", inferenceSelection{Engine: "gpu", Model: "fp16"}, map[string]any{
			"source": "legacy", "engine": "gpu", "variant": "fp16", "file": variantFileName("fp16"),
			"path": fp16Path, "model_dir": dir, "variant_installed": false,
			"note": "所选权重文件缺失或校验不一致，切换或重启后推理可能失败。",
		}},
		{"CPU defaults unchanged", inferenceSelection{}, map[string]any{
			"source": "legacy", "engine": "cpu", "variant": "quantized", "file": variantFileName("quantized"),
			"path": quantizedPath, "model_dir": dir, "variant_installed": true, "note": "",
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var paths []string
			snapshot := legacyEngineSnapshot(dir, test.selection, func(path string) (os.FileInfo, error) {
				paths = append(paths, path)
				if path == fp16Path {
					return nil, os.ErrPermission
				}
				return os.Stat(path)
			})
			if !reflect.DeepEqual(paths, wantOrder) {
				t.Fatalf("weight probes: got %d %q, want %d %q", len(paths), paths, len(wantOrder), wantOrder)
			}
			if !reflect.DeepEqual(snapshot, test.want) {
				t.Fatalf("snapshot: got %#v, want %#v", snapshot, test.want)
			}
		})
	}
}

func TestLegacyEngineSnapshotKeepsOneObservationWithoutCachingAnotherRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	dir := t.TempDir()
	seedVariant(t, dir, "quantized")
	quantizedPath := filepath.Join(dir, variantFileName("quantized"))
	selection := inferenceSelection{Engine: "gpu"}
	observed := make(chan struct{})
	resume := make(chan struct{})
	resumed := false
	defer func() {
		if !resumed {
			close(resume)
		}
	}()
	beforeRemoval := make(chan map[string]any, 1)
	go func() {
		first := true
		beforeRemoval <- legacyEngineSnapshot(dir, selection, func(path string) (os.FileInfo, error) {
			info, err := os.Stat(path)
			if path == quantizedPath && first {
				first = false
				close(observed)
				select {
				case <-resume:
				case <-ctx.Done():
				}
			}
			return info, err
		})
	}()
	select {
	case <-observed:
	case <-ctx.Done():
		t.Fatal("first scan did not observe the original quantized file")
	}
	if err := os.Remove(quantizedPath); err != nil {
		t.Fatal(err)
	}
	// A separate overlapping request must not reuse the first request's
	// file observation after the weight has become unavailable.
	after := legacyEngineSnapshot(dir, selection, os.Stat)
	if after["variant"] != "fp16" || after["variant_installed"] != false {
		t.Fatalf("later snapshot did not see the missing weight: %#v", after)
	}
	close(resume)
	resumed = true
	var first map[string]any
	select {
	case first = <-beforeRemoval:
	case <-ctx.Done():
		t.Fatal("first scan did not return")
	}
	if first["variant"] != "quantized" || first["variant_installed"] != true ||
		first["note"] != "默认 fp16 权重未安装，Runtime 实际使用 quantized；需要时请先下载对应权重。" {
		t.Fatalf("first snapshot contradicted its sampled fallback: %#v", first)
	}
}
