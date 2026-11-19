package host

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func hostControlBirth(ctx context.Context) (string, error) {
	birth, err := runtimeControlProcessBirth(ctx, os.Getpid())
	if err == nil && birth == "" {
		err = errors.New("macOS control parent start time is missing")
	}
	return birth, err
}

func runtimeControlProcessBirth(ctx context.Context, pid int) (string, error) {
	cmd := exec.CommandContext(ctx, "/bin/ps", "-p", strconv.Itoa(pid), "-o", "lstart=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if ctx.Err() == nil && errors.As(err, &exit) && exit.ExitCode() == 1 && len(out) == 0 && len(exit.Stderr) == 0 {
			return "", nil
		}
		return "", err
	}
	birth := strings.TrimSpace(string(out))
	if birth == "" {
		return "", errors.New("macOS control parent start time is missing")
	}
	return birth, nil
}

func verifyHostProcess(h HealthInfo) error {
	args, err := unix.SysctlRaw("kern.procargs2", int(h.PID))
	if err != nil {
		return err
	}
	if len(args) < 5 {
		return errors.New("Runtime process arguments missing")
	}
	end := bytes.IndexByte(args[4:], 0)
	if end < 0 {
		return errors.New("Runtime executable path missing")
	}
	actual := string(args[4 : 4+end])
	if !samePath(actual, h.Exe) || !acceptsRuntimeBinary(filepath.Base(actual)) {
		return errors.New("Runtime process identity mismatch")
	}
	return nil
}

func (m *Manager) stopProcess(ctx context.Context, h HealthInfo, request func() error) error {
	identity, err := unix.SysctlKinfoProc("kern.proc.pid", int(h.PID))
	if err != nil {
		return err
	}
	if err = verifyHostProcess(h); err != nil {
		return err
	}
	if err = request(); err != nil {
		return err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return errors.New("Runtime 尚未退出，未强制结束进程")
		case <-ticker.C:
			current, err := unix.SysctlKinfoProc("kern.proc.pid", int(h.PID))
			if errors.Is(err, unix.ESRCH) {
				return nil
			}
			if err != nil {
				return err
			}
			// SZOMB (5) has already exited even if the parent has not reaped it.
			if current.Proc.P_starttime != identity.Proc.P_starttime || current.Proc.P_stat == 5 {
				return nil
			}
		}
	}
}
