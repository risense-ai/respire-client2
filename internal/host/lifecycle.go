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

// ErrRuntimeNotInstalled means there is no selected Runtime to start.
var ErrRuntimeNotInstalled = errors.New("尚未安装 Runtime，请先下载")

var errInvalidRuntimeSelection = errors.New("invalid installed Runtime metadata")

func (m *Manager) Installed() (InstalledRuntime, error) {
	var info InstalledRuntime
	b, err := os.ReadFile(m.runtimeManifest())
	if err != nil {
		return info, err
	}
	if err = json.Unmarshal(b, &info); err != nil {
		return info, fmt.Errorf("%w: %w", errInvalidRuntimeSelection, err)
	}
	if !validVersion(info.Version) || !validHash(info.SHA256) {
		return info, errInvalidRuntimeSelection
	}
	return info, nil
}

func (m *Manager) runtimeDir(v string) string { return filepath.Join(m.DataRoot, "bin", "runtime", v) }

func (m *Manager) Owns(exe string) bool {
	for _, root := range []string{filepath.Join(m.DataRoot, "bin", "runtime"), filepath.Join(m.DataRoot, "runtime"), filepath.Join(m.Root, "runtime")} {
		rel, err := filepath.Rel(windowsDOSPath(root), windowsDOSPath(exe))
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
		return fmt.Errorf("%w: %w", ErrRuntimeNotInstalled, err)
	}
	if err != nil {
		return fmt.Errorf("读取 Runtime 安装信息失败: %w", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h, err = m.startDetached(ctx, info)
	if err != nil {
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
	if err := m.verifySelectedHealth(h, info); err != nil {
		return err
	}
	return m.unlockIfNeeded(h)
}

func (m *Manager) verifySelectedHealth(h HealthInfo, info InstalledRuntime) error {
	if h.Version != info.Version || !samePath(h.Exe, filepath.Join(m.runtimeDir(info.Version), runtimeBinary())) ||
		!samePath(h.InstallationRoot, m.DataRoot) {
		return errors.New("启动后的 Runtime 安装目录、身份或版本不匹配")
	}
	if err := verifyHostProcess(h); err != nil {
		return fmt.Errorf("无法确认 Runtime 进程身份: %w", err)
	}
	return nil
}

func (m *Manager) unlockIfNeeded(health HealthInfo) error {
	if health.StoreReady || health.CredentialState != "required" {
		return nil
	}
	if samePath(health.InstallationRoot, m.DataRoot) && filepath.IsAbs(health.DataDir) {
		relative, err := filepath.Rel(m.DataRoot, health.DataDir)
		if err == nil && filepath.IsLocal(relative) {
			// An accountless Runtime can serve its login and status endpoints while locked.
			if _, err := os.Lstat(filepath.Join(health.DataDir, "session.json")); errors.Is(err, os.ErrNotExist) {
				return nil
			} else if err != nil {
				return err
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return m.unlockAccountRuntime(ctx, health)
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
