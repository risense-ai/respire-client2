package host

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

func IsUnavailable(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && errors.Is(err, windows.WSAECONNREFUSED)
}

func (m *Manager) stopProcess(ctx context.Context, h HealthInfo) error {
	handle, err := runtimeProcess(h)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	if _, err = m.localRequest(ctx, "POST", "/api/runtime/stop", nil); err != nil {
		return err
	}
	result, err := windows.WaitForSingleObject(handle, 30000)
	if err != nil {
		return err
	}
	if result != windows.WAIT_OBJECT_0 {
		return errors.New("Runtime 尚未退出，未强制结束进程")
	}
	return nil
}

func runtimeProcess(h HealthInfo) (windows.Handle, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, h.PID)
	if err != nil {
		return 0, err
	}
	var buf [32768]uint16
	size := uint32(len(buf))
	if err = windows.QueryFullProcessImageName(handle, 0, &buf[0], &size); err != nil {
		windows.CloseHandle(handle)
		return 0, err
	}
	actual := windows.UTF16ToString(buf[:size])
	if !samePath(actual, h.Exe) || !samePath(filepath.Base(actual), runtimeBinary()) {
		windows.CloseHandle(handle)
		return 0, errors.New("Runtime process identity mismatch")
	}
	return handle, nil
}
