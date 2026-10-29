package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func (m *Manager) Installed() (InstalledRuntime, error) {
	var info InstalledRuntime
	b, err := os.ReadFile(filepath.Join(m.Root, "runtime.json"))
	if err != nil {
		return info, err
	}
	if err = json.Unmarshal(b, &info); err != nil {
		return info, err
	}
	if !validVersion(info.Version) || !validHash(info.SHA256) {
		return info, errors.New("invalid installed Runtime metadata")
	}
	return info, nil
}

func (m *Manager) runtimeDir(v string) string { return filepath.Join(m.Root, "runtime", v) }

func (m *Manager) Owns(exe string) bool {
	rel, err := filepath.Rel(filepath.Join(m.Root, "runtime"), exe)
	return err == nil && filepath.IsLocal(rel) && strings.EqualFold(filepath.Base(rel), "rsrs.exe")
}

func (m *Manager) hostCommand(ctx context.Context, info InstalledRuntime, args ...string) error {
	dir := m.runtimeDir(info.Version)
	if err := verifyRuntime(dir, info.SHA256); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "rsrs.exe"), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	// These host actions own lifecycle; never inherit sandbox/client flags.
	var env []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if strings.EqualFold(key, "RSRS_BIN_DIR") || strings.EqualFold(key, "RSRS_CLIENT_ONLY") || strings.EqualFold(key, "RSRS_NO_AUTOSTART") {
			continue
		}
		env = append(env, item)
	}
	cmd.Env = append(env, "RSRS_BIN_DIR="+dir, "RSRS_CLIENT_ONLY=0", "RSRS_NO_AUTOSTART=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Runtime host action failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func IsUnavailable(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && errors.Is(err, windows.WSAECONNREFUSED)
}

func (m *Manager) Start() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err := m.Health(ctx)
	cancel()
	if err == nil {
		return nil
	}
	if !IsUnavailable(err) {
		return fmt.Errorf("Runtime 状态无法确认，未启动另一个实例: %w", err)
	}
	info, err := m.Installed()
	if err != nil {
		return fmt.Errorf("尚未安装 Runtime，请先下载: %w", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err = m.hostCommand(ctx, info, "--json", "status"); err != nil {
		return err
	}
	return m.requireActive(info)
}

func (m *Manager) requireActive(info InstalledRuntime) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	h, _, err := m.Health(ctx)
	if err != nil {
		return err
	}
	if h.Version != info.Version || !strings.EqualFold(h.Exe, filepath.Join(m.runtimeDir(info.Version), "rsrs.exe")) {
		return errors.New("启动后的 Runtime 身份或版本不匹配")
	}
	return nil
}

func (m *Manager) restoreRuntime(previous, failed InstalledRuntime) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	h, _, err := m.Health(ctx)
	cancel()
	if err == nil {
		if !strings.EqualFold(h.Exe, filepath.Join(m.runtimeDir(failed.Version), "rsrs.exe")) {
			return errors.New("Runtime 管理者已改变，未执行恢复")
		}
		if err = m.Stop(true); err != nil {
			return fmt.Errorf("停止失败版本以便恢复: %w", err)
		}
	} else if !IsUnavailable(err) {
		return fmt.Errorf("无法确认失败版本是否退出: %w", err)
	}
	if err = writeJSON(filepath.Join(m.Root, "runtime.json"), previous); err != nil {
		return fmt.Errorf("恢复版本选择: %w", err)
	}
	if err = m.Start(); err != nil {
		return fmt.Errorf("重新启动旧版: %w", err)
	}
	return m.requireActive(previous)
}

func (m *Manager) Stop(ownedOnly bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	h, _, err := m.Health(ctx)
	if err != nil {
		if IsUnavailable(err) {
			return nil
		}
		return err
	}
	if ownedOnly && !m.Owns(h.Exe) {
		return errors.New("Runtime 由其他 CLI 管理，客户端未停止它")
	}
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
	if !strings.EqualFold(actual, h.Exe) || !strings.EqualFold(filepath.Base(actual), "rsrs.exe") {
		windows.CloseHandle(handle)
		return 0, errors.New("Runtime process identity mismatch")
	}
	return handle, nil
}
