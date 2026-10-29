package host

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

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
	if !samePath(actual, h.Exe) || filepath.Base(actual) != runtimeBinary() {
		return errors.New("Runtime process identity mismatch")
	}
	return nil
}

func (m *Manager) stopProcess(ctx context.Context, h HealthInfo) error {
	identity, err := unix.SysctlKinfoProc("kern.proc.pid", int(h.PID))
	if err != nil {
		return err
	}
	if err = verifyHostProcess(h); err != nil {
		return err
	}
	if _, err = m.localRequest(ctx, "POST", "/api/runtime/stop", nil); err != nil {
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
