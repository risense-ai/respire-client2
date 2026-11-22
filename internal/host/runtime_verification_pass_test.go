package host

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeReleaseHashesInventoryOnceAndRejectsCoreTampering(t *testing.T) {
	buildPayload(t, "1.2.3")
	payload, err := readBootstrapPayload()
	if err != nil {
		t.Fatal(err)
	}
	m := newInstallTestManager(t)
	if err := m.publishRuntime(payload.Metadata, stageBootstrapRuntime(payload.Dir), false); err != nil {
		t.Fatal(err)
	}
	receipt, err := runtimeReceipt(payload.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	dir := m.runtimeDir(receipt.Version)
	counts := map[string]int{}
	hasher := func(path string) (string, error) {
		counts[path]++
		return fileHash(path)
	}
	if err := verifyReleaseTreeWithHasher(dir, dir, receipt, hasher); err != nil {
		t.Fatal(err)
	}
	files, _, err := receiptFiles(receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		path := filepath.Join(dir, filepath.FromSlash(file.Path))
		if _, err := os.Stat(path); err == nil && counts[path] != 1 {
			t.Errorf("expected a single digest pass for %s, got %d", file.Path, counts[path])
		}
	}
	executable := filepath.Join(dir, runtimeBinary())
	data, err := os.ReadFile(executable)
	if err != nil || len(data) == 0 {
		t.Fatalf("fixture executable missing: %v", err)
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(executable, data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseTree(dir, receipt); err == nil {
		t.Fatal("a later verification trusted a prior digest despite same-size tampering")
	}
}
