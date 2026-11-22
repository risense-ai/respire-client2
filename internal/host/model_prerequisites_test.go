package host

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestM3DownloadPlansDoNotMixCPUAndGPURequirements(t *testing.T) {
	for _, tc := range []struct {
		variant, engine string
		owned           bool
		want            []string
	}{
		{"quantized", "cpu", true, []string{"tokenizer.json", "onnx/model_quantized.onnx", m3CPUContainer.rel}},
		{"baseline", "", true, []string{"tokenizer.json", "onnx/model_quantized.onnx", m3CPUContainer.rel}},
		{"quantized", "cpu", false, []string{"tokenizer.json", "onnx/model_quantized.onnx"}},
		{"quantized", "gpu", true, []string{"tokenizer.json", "onnx/model_quantized.onnx"}},
		{"fp16", "gpu", true, []string{"tokenizer.json", "onnx/model_fp16.onnx"}},
		{"tokenizer", "cpu", true, []string{"tokenizer.json"}},
	} {
		files, err := m3DownloadPlan(tc.variant, tc.engine, tc.owned)
		if err != nil {
			t.Fatal(err)
		}
		actual := make([]string, 0, len(files))
		for _, file := range files {
			actual = append(actual, file.spec.rel)
			if file.ort != (file.spec.rel == m3CPUContainer.rel) {
				t.Fatalf("wrong repository for %s", file.spec.rel)
			}
		}
		if !reflect.DeepEqual(actual, tc.want) {
			t.Errorf("%+v: selected %v", tc, actual)
		}
	}
}

func TestRecoverModelStageOnlyRemovesEmptyOwnedShell(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	stage := filepath.Join(dir, m3UninstallStage)
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	verifyCalls := 0
	verify := func() error { verifyCalls++; return nil }
	if err := recoverEmptyM3Stage(root, verify); err != nil || verifyCalls != 1 {
		t.Fatalf("empty safely locked stage should be reusable: %v, %d", err, verifyCalls)
	}
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(stage, "unverified")
	if err := os.WriteFile(retained, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := recoverEmptyM3Stage(root, verify); err == nil {
		t.Fatal("unowned staged model bytes must never be removed")
	}
	if actual, err := os.ReadFile(retained); err != nil || string(actual) != "preserve" {
		t.Fatalf("unverified bytes changed: %q %v", actual, err)
	}
}

func TestLegacyProbeAloneGetsColdStartBudget(t *testing.T) {
	original := &http.Client{Timeout: defaultRuntimeHTTPTimeout}
	m := &Manager{localClient: original}
	for _, tc := range []struct {
		method string
		args   []string
		want   time.Duration
	}{
		{"cli.exec", []string{"--json", "model", "probe"}, 2 * time.Minute},
		{"cli.exec", []string{"--json", "model", "engine"}, defaultRuntimeHTTPTimeout},
		{"cli.exec", []string{"--json", "keygen"}, defaultRuntimeHTTPTimeout},
		{"model.switch", []string{"--json", "model", "probe"}, defaultRuntimeHTTPTimeout},
	} {
		if got := m.runtimeHTTPTransport(tc.method, tc.args).Timeout; got != tc.want {
			t.Errorf("wrong deadline for %s %v: %s", tc.method, tc.args, got)
		}
	}
	if original.Timeout != defaultRuntimeHTTPTimeout {
		t.Fatal("probe modified common HTTP timeout")
	}
}
