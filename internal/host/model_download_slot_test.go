package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestModelTaskDoesNotBlockDesktopTaskButExcludesUninstall(t *testing.T) {
	m := &Manager{}
	if !m.StartTaskCancellable("下载模型 quantized", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}) {
		t.Fatal("model download must start")
	}
	defer m.CancelTask(false)
	if err := m.runExclusiveModelMaintenance("卸载 BGE-M3", func() error {
		t.Fatal("model uninstall ran during download")
		return nil
	}); err == nil {
		t.Fatal("model uninstall must be excluded during download")
	}
	if err := m.RunTask("更新 Runtime", func() error {
		m.progress("校验 Runtime", 1, 2)
		if task := m.ModelDownloadSnapshot(); !task.Busy || task.Stage != "下载模型 quantized" {
			t.Fatalf("download progress overwritten: %+v", task)
		}
		if task := m.TaskSnapshot(); !task.Busy || task.Stage != "校验 Runtime" {
			t.Fatalf("host progress overwritten: %+v", task)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m.CancelTask(true)
	waitTaskIdle(t, m)
	if task := m.ModelDownloadSnapshot(); task.Busy || task.Stage != "已暂停" {
		t.Fatalf("model pause failed: %+v", task)
	}
	if err := m.runExclusiveModelMaintenance("卸载 BGE-M3", func() error {
		if m.StartTaskCancellable("下载模型 quantized", func(ctx context.Context) error { return nil }) {
			t.Fatal("model download ran during uninstall")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckModelRevealPathAcceptsProfilesAndRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	m := &Manager{DataRoot: root}
	models := filepath.Join(root, "models")
	profile := filepath.Join(models, "granite-311m-r2", "onnx")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(profile, "model.onnx")
	if err := os.WriteFile(model, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{model, profile} {
		if accepted, err := m.CheckModelRevealPath(path); err != nil || accepted != path {
			t.Fatalf("existing profile path rejected: %s, %v", accepted, err)
		}
	}
	unrelated := filepath.Join(root, "secret")
	if err := os.WriteFile(unrelated, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	pointsOutside := filepath.Join(models, "points-outside")
	linkErr := os.Symlink(unrelated, pointsOutside)
	badPaths := []string{unrelated, "", filepath.Join(profile, "missing")}
	if linkErr == nil {
		badPaths = append(badPaths, pointsOutside)
	}
	for _, path := range badPaths {
		if _, err := m.CheckModelRevealPath(path); err == nil {
			t.Fatalf("unsafe/missing reveal accepted: %q", path)
		}
	}
}

func TestModelCancelIsAlreadyAddressedToSeparateSlot(t *testing.T) {
	m := &Manager{}
	done := make(chan struct{})
	if !m.StartTaskCancellable("下载模型 file", func(ctx context.Context) error {
		defer close(done)
		<-ctx.Done()
		return ctx.Err()
	}) {
		t.Fatal("start failed")
	}
	if err := m.RunTask("宿主任务", func() error {
		m.CancelTask(false)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("download was not signalled")
		}
		if got := m.TaskSnapshot(); got.Stage != "宿主任务" || !got.Busy {
			t.Fatalf("host task was cancelled by model cancel: %+v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
