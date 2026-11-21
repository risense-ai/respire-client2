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
	return m.start(context.Background(), true, false)
}

func (m *Manager) Restart() error {
	return m.start(context.Background(), true, true)
}

// PrepareRuntimeForStartup reuses a running Runtime or starts the verified local selection.
func (m *Manager) PrepareRuntimeForStartup(ctx context.Context) error {
	return m.start(ctx, false, false)
}

func (m *Manager) start(ctx context.Context, switchRunning, restart bool) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	h, err := m.startWithSelection(ctx, switchRunning, restart)
	if err != nil {
		return err
	}
	return m.unlockIfNeeded(h)
}

func (m *Manager) startWithSelection(ctx context.Context, switchRunning, restart bool) (HealthInfo, error) {
	takeover, err := m.lockRuntimeTakeover(ctx)
	if err != nil {
		return HealthInfo{}, err
	}
	defer takeover.Close()
	install, err := m.lockRuntime(ctx)
	if err != nil {
		return HealthInfo{}, err
	}
	defer install.Close()

	healthCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	h, _, err := m.Health(healthCtx)
	cancel()
	if err == nil {
		if err := m.verifyRuntimeProfile(h); err != nil {
			return HealthInfo{}, err
		}
		owned := m.Owns(h.Exe)
		if restart && !owned {
			return HealthInfo{}, errors.New("Runtime 由其他宿主管理，客户端未重启它")
		}
		if !switchRunning || !owned {
			return h, nil
		}
	} else if !IsUnavailable(err) {
		return HealthInfo{}, fmt.Errorf("Runtime 状态无法确认，未启动另一个实例: %w", err)
	}
	info, err := m.Installed()
	if h.PID == 0 && errors.Is(err, os.ErrNotExist) {
		return HealthInfo{}, fmt.Errorf("%w: %w", ErrRuntimeNotInstalled, err)
	}
	if err != nil {
		return HealthInfo{}, fmt.Errorf("读取 Runtime 安装信息失败: %w", err)
	}
	if h.PID != 0 {
		if !restart && samePath(h.Exe, filepath.Join(m.runtimeDir(info.Version), runtimeBinary())) {
			return h, nil
		}
		if err := m.verifyInstalled(info); err != nil {
			return HealthInfo{}, err
		}
		if err := m.stopManagedLocked(ctx, h); err != nil {
			return HealthInfo{}, err
		}
	}
	active, err := m.startDetachedLocked(ctx, info)
	if err != nil {
		return HealthInfo{}, err
	}
	return active, m.verifyRuntimeProfile(active)
}

func (m *Manager) verifyRuntimeProfile(h HealthInfo) error {
	if !samePath(h.InstallationRoot, m.DataRoot) {
		return errors.New("RPC 端口由其他 Runtime 安装使用")
	}
	profile, err := selectedRuntimeLoginProfile(m.DataRoot)
	if err != nil {
		return err
	}
	if !samePath(h.DataDir, profile) {
		return errors.New("RPC 端口由其他 Runtime 账户使用")
	}
	return nil
}

func selectedRuntimeLoginProfile(root string) (string, error) {
	selected := root
	raw, err := readCredentialConfig(filepath.Join(root, "client.json"), 1<<20)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err == nil {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return "", errors.New("Cannot verify the selected Runtime account in client.json")
		}
		if field, present := fields["data_dir"]; present {
			var directory *string
			if json.Unmarshal(field, &directory) != nil || directory == nil ||
				(*directory != "" && !filepath.IsAbs(*directory)) {
				return "", errors.New("Invalid selected Runtime account path")
			}
			if *directory != "" {
				selected = *directory
			}
		}
	}
	chosen, err := canonicalSessionDirectory(selected)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(windowsDOSPath(root), windowsDOSPath(chosen))
	if err != nil || !filepath.IsLocal(relative) || !samePath(chosen, selected) {
		return "", errors.New("Selected Runtime account is outside its installation or was redirected")
	}
	return chosen, nil
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
