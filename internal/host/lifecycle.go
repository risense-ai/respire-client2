package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrRuntimeNotInstalled means neither a selected Runtime nor an external CLI exists.
var ErrRuntimeNotInstalled = errors.New("尚未安装 Runtime，请先下载")

func (m *Manager) Installed() (InstalledRuntime, error) {
	var info InstalledRuntime
	b, err := os.ReadFile(m.runtimeManifest())
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

func (m *Manager) runtimeDir(v string) string { return filepath.Join(m.DataRoot, "bin", "runtime", v) }

func (m *Manager) Owns(exe string) bool {
	for _, root := range []string{filepath.Join(m.DataRoot, "bin", "runtime"), filepath.Join(m.DataRoot, "runtime"), filepath.Join(m.Root, "runtime")} {
		rel, err := filepath.Rel(root, exe)
		if err == nil && filepath.IsLocal(rel) && acceptsRuntimeBinary(filepath.Base(rel)) {
			return true
		}
	}
	return false
}

func (m *Manager) Start() error {
	if err := m.MigrateRuntime(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	h, _, err := m.Health(ctx)
	cancel()
	if err == nil {
		if m.Owns(h.Exe) {
			info, installedErr := m.Installed()
			if installedErr != nil {
				return fmt.Errorf("读取 Runtime 安装信息失败: %w", installedErr)
			}
			if !samePath(h.Exe, filepath.Join(m.runtimeDir(info.Version), runtimeBinary())) {
				if err := m.Stop(true); err != nil {
					return err
				}
				return m.Start()
			}
		}
		return m.unlockIfNeeded(h)
	}
	if !IsUnavailable(err) {
		return fmt.Errorf("Runtime 状态无法确认，未启动另一个实例: %w", err)
	}
	info, err := m.Installed()
	if errors.Is(err, os.ErrNotExist) {
		ctx, cancel = context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		_, externalErr := m.External(ctx)
		if externalErr != nil {
			// Only a missing CLI path authorizes first-install; execution errors do not.
			pathErr, missingPath := externalErr.(*os.PathError)
			if missingPath && errors.Is(pathErr, os.ErrNotExist) &&
				pathErr.Path == filepath.Join(filepath.Dir(m.Root), "bin", cliBinary()) {
				return fmt.Errorf("%w: %w", ErrRuntimeNotInstalled, externalErr)
			}
			return fmt.Errorf("本机 CLI 无法启动 Runtime: %w", externalErr)
		}
		_, err = m.hostOperation(ctx, []string{"runtime", "start"}, "", "runtime.start", "")
		if err != nil {
			return err
		}
		return m.requireExternalActive()
	}
	if err != nil {
		return fmt.Errorf("读取 Runtime 安装信息失败: %w", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err = m.verifyInstalled(info); err != nil {
		return err
	}
	// The one-shot command shares the CLI takeover lock and native credentials.
	// Runtime itself owns the listener after the command exits.
	_, err = m.hostOperation(ctx, []string{"runtime", "start"}, "", "runtime.start", "")
	if err != nil {
		return err
	}
	return m.requireActive(info)
}

func (m *Manager) requireExternalActive() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	h, _, err := m.Health(ctx)
	if err != nil {
		return err
	}
	// The CLI and Runtime have independent images and version trains.
	if !samePath(h.InstallationRoot, m.DataRoot) {
		return errors.New("启动后的 Runtime 不属于当前安装目录")
	}
	if err = verifyHostProcess(h); err != nil {
		return err
	}
	if err = verifyExecutable(h.Exe); err != nil {
		return err
	}
	return m.unlockIfNeeded(h)
}

func (m *Manager) requireActive(info InstalledRuntime) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	h, _, err := m.Health(ctx)
	if err != nil {
		return err
	}
	if h.Version != info.Version || !samePath(h.Exe, filepath.Join(m.runtimeDir(info.Version), runtimeBinary())) ||
		!samePath(h.InstallationRoot, m.DataRoot) {
		return errors.New("启动后的 Runtime 安装目录、身份或版本不匹配")
	}
	if err := verifyHostProcess(h); err != nil {
		return fmt.Errorf("无法确认 Runtime 进程身份: %w", err)
	}
	return m.unlockIfNeeded(h)
}

// The CLI owns native credentials. Client2 asks it to unlock this existing owner;
// private keys never enter browser state, process arguments or Runtime files.
func (m *Manager) unlockIfNeeded(health HealthInfo) error {
	if health.StoreReady || health.CredentialState != "required" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := m.hostOperation(ctx, []string{"runtime", "unlock"}, "", "runtime.unlock", "")
	return err
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
	if err = m.selectRuntime(previous); err != nil {
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
	return m.stopManaged(ctx, h)
}
