package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func verifyHostProcess(h HealthInfo) error {
	actual, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", h.PID))
	if err != nil {
		return err
	}
	if !samePath(actual, h.Exe) || filepath.Base(actual) != runtimeBinary() {
		return errors.New("Runtime process identity mismatch")
	}
	return nil
}

func (m *Manager) stopProcess(ctx context.Context, h HealthInfo) error {
	// A pidfd keeps the exit wait tied to this process even if its PID is reused.
	fd, err := unix.PidfdOpen(int(h.PID), 0)
	if err != nil {
		return fmt.Errorf("Runtime pidfd (requires Linux 5.3+): %w", err)
	}
	defer unix.Close(fd)
	if err = verifyHostProcess(h); err != nil {
		return err
	}
	if _, err = m.localRequest(ctx, "POST", "/api/runtime/stop", nil); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n > 0 && fds[0].Revents&unix.POLLIN != 0 {
			return nil
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	return errors.New("Runtime 尚未退出，未强制结束进程")
}
