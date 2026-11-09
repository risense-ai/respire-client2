package host

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStartTaskCancellablePublishesBusyBeforeReturning(t *testing.T) {
	m := &Manager{task: TaskState{Stage: "就绪"}}
	release := make(chan struct{})
	started := make(chan struct{})
	if !m.StartTaskCancellable("下载模型 quantized", func(ctx context.Context) error {
		close(started)
		<-release
		return nil
	}) {
		t.Fatal("first start must succeed")
	}
	// The busy state must be visible synchronously after Start returns: this
	// is what lets a poller trust the snapshot instead of a stale pre-start
	// terminal state from a previous task.
	if snap := m.TaskSnapshot(); !snap.Busy || snap.Stage != "下载模型 quantized" {
		t.Fatalf("snapshot after start = %+v, want busy with stage set", snap)
	}
	if m.StartTaskCancellable("另一个任务", func(ctx context.Context) error { return nil }) {
		t.Fatal("second start while busy must be rejected")
	}
	// A rejected start must not clobber the running task's progress state.
	if snap := m.TaskSnapshot(); !snap.Busy || snap.Stage != "下载模型 quantized" {
		t.Fatalf("rejected start must leave the running task untouched: %+v", snap)
	}
	close(release)
	<-started
	waitTaskIdle(t, m)
	snap := m.TaskSnapshot()
	if snap.Stage != "完成" || snap.Error != "" {
		t.Fatalf("finished task = %+v, want 完成 without error", snap)
	}
}

func TestStartTaskCancellableReportsFailure(t *testing.T) {
	m := &Manager{task: TaskState{Stage: "就绪"}}
	if !m.StartTaskCancellable("失败任务", func(ctx context.Context) error {
		return errors.New("下载源不可达")
	}) {
		t.Fatal("start must succeed")
	}
	waitTaskIdle(t, m)
	snap := m.TaskSnapshot()
	if snap.Stage != "操作失败" || snap.Error != "下载源不可达" {
		t.Fatalf("failed task = %+v, want 操作失败 with error kept", snap)
	}
}

func waitTaskIdle(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for m.TaskSnapshot().Busy {
		if time.Now().After(deadline) {
			t.Fatal("task did not finish in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
