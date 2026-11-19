package host

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

func hostControlBirth(context.Context) (string, error) {
	return windowsControlBirth(windows.CurrentProcess())
}

func runtimeControlProcessBirth(_ context.Context, pid int) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	status, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return "", err
	}
	if status == windows.WAIT_OBJECT_0 {
		return "", nil
	}
	if status != uint32(windows.WAIT_TIMEOUT) {
		return "", errors.New("Windows control process status is unknown")
	}
	return windowsControlBirth(handle)
}

func windowsControlBirth(process windows.Handle) (string, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return "", err
	}
	birth := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	if birth == 0 {
		return "", errors.New("Windows control parent start time is missing")
	}
	return strconv.FormatUint(birth, 10), nil
}

func configureCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

func configureRuntimeCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP}
}

func verifyHostProcess(h HealthInfo) error {
	handle, err := runtimeProcess(h)
	if err != nil {
		return err
	}
	return windows.CloseHandle(handle)
}

func IsUnavailable(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && errors.Is(err, windows.WSAECONNREFUSED)
}

func (m *Manager) stopProcess(ctx context.Context, h HealthInfo, request func() error) error {
	handle, err := runtimeProcess(h)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	if err = request(); err != nil {
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
	actualFile, actualErr := os.Stat(actual)
	reportedFile, reportedErr := os.Stat(h.Exe)
	if err := errors.Join(actualErr, reportedErr); err != nil {
		windows.CloseHandle(handle)
		return 0, err
	}
	if !os.SameFile(actualFile, reportedFile) || !acceptsRuntimeBinary(filepath.Base(actual)) {
		windows.CloseHandle(handle)
		return 0, errors.New("Runtime process identity mismatch")
	}
	return handle, nil
}
