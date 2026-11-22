package host

import (
	"context"
	"errors"
)

type modelDownloadTask struct {
	state  TaskState
	cancel context.CancelFunc
	pause  bool
}

func (m *Manager) ModelDownloadSnapshot() TaskState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.modelDownload.state
}

func (m *Manager) modelDownloadProgress(stage string, done, total int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.modelDownload.state.Stage, m.modelDownload.state.Done, m.modelDownload.state.Total = stage, done, total
}

// StartTaskCancellable publishes the model download in its own task slot.
// Direct model maintenance retains an exclusive fence against that writer.
func (m *Manager) StartTaskCancellable(stage string, action func(ctx context.Context) error) bool {
	m.mu.Lock()
	if m.modelDownload.state.Busy || m.modelExclusive {
		m.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.modelDownload = modelDownloadTask{state: TaskState{Busy: true, Stage: stage}, cancel: cancel}
	m.lastModelTask = true
	m.mu.Unlock()

	go func() {
		defer cancel()
		err := action(ctx)
		m.mu.Lock()
		defer m.mu.Unlock()
		task := &m.modelDownload
		task.cancel = nil
		task.state.Busy = false
		switch {
		case err == nil:
			task.state.Stage = "完成"
		case task.pause && errors.Is(err, context.Canceled):
			task.state.Stage = "已暂停"
		case errors.Is(err, context.Canceled):
			task.state.Stage = "已取消"
		default:
			task.state.Stage = "操作失败"
			task.state.Error = err.Error()
		}
		task.pause = false
	}()
	return true
}

// CancelTask affects only the model download, never updates or other tasks.
func (m *Manager) CancelTask(pause bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.modelDownload.cancel != nil {
		m.modelDownload.pause = pause
		m.modelDownload.cancel()
	}
}

func (m *Manager) runExclusiveModelMaintenance(stage string, action func() error) error {
	m.mu.Lock()
	if m.modelDownload.state.Busy || m.modelExclusive {
		m.mu.Unlock()
		return errors.New("已有模型操作正在执行")
	}
	m.modelExclusive = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.modelExclusive = false
		m.mu.Unlock()
	}()
	return m.RunTask(stage, action)
}
