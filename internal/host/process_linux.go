package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func hostControlBirth(context.Context) (string, error) {
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return "", err
	}
	end := strings.LastIndexByte(string(stat), ')')
	fields := strings.Fields(string(stat[end+1:]))
	if end < 0 || len(fields) <= 19 {
		return "", errors.New("Linux control parent start time is missing")
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || ticks == 0 {
		return "", errors.New("Linux control parent start time is invalid")
	}
	return strconv.FormatUint(ticks, 10), nil
}

func verifyHostProcess(h HealthInfo) error {
	actual, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", h.PID))
	if err != nil {
		return err
	}
	if !samePath(actual, h.Exe) || !acceptsRuntimeBinary(filepath.Base(actual)) {
		return errors.New("Runtime process identity mismatch")
	}
	return nil
}

func (m *Manager) stopProcess(ctx context.Context, h HealthInfo, request func() error) error {
	// A pidfd keeps the exit wait tied to this process even if its PID is reused.
	fd, err := unix.PidfdOpen(int(h.PID), 0)
	if err != nil {
		return fmt.Errorf("Runtime pidfd (requires Linux 5.3+): %w", err)
	}
	defer unix.Close(fd)
	if err = verifyHostProcess(h); err != nil {
		return err
	}
	if err = request(); err != nil {
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
