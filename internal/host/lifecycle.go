package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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
	return err == nil && filepath.IsLocal(rel) && samePath(filepath.Base(rel), runtimeBinary())
}

func (m *Manager) hostCommand(ctx context.Context, info InstalledRuntime, args ...string) error {
	dir := m.runtimeDir(info.Version)
	if err := verifyRuntime(dir, info.SHA256); err != nil {
		return err
	}
	return runRuntimeCommand(ctx, filepath.Join(dir, runtimeBinary()), args...)
}

func runtimeEnv(dir string) []string {
	// These host actions own lifecycle; never inherit sandbox/client flags.
	var env []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if strings.EqualFold(key, "RSRS_BIN_DIR") || strings.EqualFold(key, "RSRS_CLIENT_ONLY") || strings.EqualFold(key, "RSRS_NO_AUTOSTART") {
			continue
		}
		env = append(env, item)
	}
	return append(env, "RSRS_BIN_DIR="+dir, "RSRS_CLIENT_ONLY=0", "RSRS_NO_AUTOSTART=0")
}

func runRuntimeCommand(ctx context.Context, executable string, args ...string) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	configureCommand(cmd)
	cmd.Env = runtimeEnv(filepath.Dir(executable))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Runtime host action failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
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
	if errors.Is(err, os.ErrNotExist) {
		ctx, cancel = context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		external, externalErr := m.External(ctx)
		if externalErr != nil {
			return fmt.Errorf("尚未安装 Runtime，请先下载: %w", externalErr)
		}
		if err = runRuntimeCommand(ctx, external.Path, "--json", "runtime", "start"); err != nil {
			return err
		}
		return m.requireExternalActive(external)
	}
	if err != nil {
		return fmt.Errorf("尚未安装 Runtime，请先下载: %w", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	probe, probeErr := m.hostOperation(ctx, []string{"runtime", "status"}, "", "", "")
	args := []string{"--json", "runtime", "start"}
	if probeErr != nil {
		var legacy struct {
			Summary struct {
				Reason string `json:"reason"`
			} `json:"summary"`
		}
		if json.Unmarshal(probe, &legacy) != nil || legacy.Summary.Reason != "invalid_input" {
			return probeErr
		}
		args = []string{"--json", "status"}
	}
	if err = m.hostCommand(ctx, info, args...); err != nil {
		return err
	}
	return m.requireActive(info)
}

func (m *Manager) requireExternalActive(external ExternalRuntime) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	h, _, err := m.Health(ctx)
	if err != nil {
		return err
	}
	if h.Version != external.Version || !samePath(h.Exe, external.Path) {
		return errors.New("启动后的本机 Runtime 身份或版本不匹配")
	}
	return nil
}

func (m *Manager) requireActive(info InstalledRuntime) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	h, _, err := m.Health(ctx)
	if err != nil {
		return err
	}
	if h.Version != info.Version || !samePath(h.Exe, filepath.Join(m.runtimeDir(info.Version), runtimeBinary())) {
		return errors.New("启动后的 Runtime 身份或版本不匹配")
	}
	return nil
}

func (m *Manager) restoreRuntime(previous, failed InstalledRuntime) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	h, _, err := m.Health(ctx)
	cancel()
	if err == nil {
		if !samePath(h.Exe, filepath.Join(m.runtimeDir(failed.Version), runtimeBinary())) {
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
	if h.ManagedID != "" {
		return m.stopManaged(ctx, h)
	}
	return m.stopProcess(ctx, h)
}
