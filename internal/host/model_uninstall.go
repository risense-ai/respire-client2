package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const m3UninstallStage = ".bge-m3-uninstall"

func (m *Manager) uninstallM3(ctx context.Context) (json.RawMessage, error) {
	var result json.RawMessage
	err := m.runExclusiveModelMaintenance("卸载 BGE-M3", func() error {
		var err error
		result, err = m.uninstallM3Locked(ctx)
		return err
	})
	return result, err
}

func (m *Manager) uninstallM3Locked(ctx context.Context) (json.RawMessage, error) {
	address, _, err := m.runtimeAccountEndpoint()
	if err != nil {
		return nil, err
	}
	root := windowsDOSPath(m.DataRoot)
	if err := checkModelUninstallPath(root); err != nil {
		return nil, err
	}
	takeover, err := m.lockRuntimeTakeover(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if takeover != nil {
			takeover.Close()
		}
	}()

	h, _, err := m.Health(ctx)
	running := err == nil
	if !running && !IsUnavailable(err) {
		return nil, fmt.Errorf("无法确认 Runtime 状态: %w", err)
	}
	if running && h.ManagedID != "" {
		return nil, errors.New("Runtime 正由其他宿主监督，请先用原宿主停机")
	}
	if err := checkM3UninstallAdmission(root); err != nil {
		return nil, err
	}

	clientPath := filepath.Join(root, "client.json")
	client, err := modelUninstallObject(clientPath)
	if err != nil {
		return nil, err
	}
	profile, err := modelUninstallProfile(root, client)
	if err != nil {
		return nil, err
	}
	target := windowsDOSPath(m.ModelDir())
	if err := checkM3UninstallTarget(target, profile); err != nil {
		return nil, err
	}
	inference, err := modelUninstallObject(filepath.Join(root, "inference.json"))
	if err != nil {
		return nil, err
	}
	registry, err := modelUninstallObject(filepath.Join(root, "embedding-profiles-v1.json"))
	if err != nil {
		return nil, err
	}
	recheck := func() error {
		if err := checkModelUninstallPath(profile); err != nil {
			return err
		}
		for _, input := range []struct {
			name string
			data []byte
		}{{"client.json", client}, {"inference.json", inference}, {"embedding-profiles-v1.json", registry}} {
			now, err := modelUninstallFile(filepath.Join(root, input.name))
			if err != nil {
				return err
			}
			if !bytes.Equal(now, input.data) || (now == nil) != (input.data == nil) {
				return fmt.Errorf("模型卸载期间 %s 已改变", input.name)
			}
		}
		if err := checkM3UninstallAdmission(root); err != nil {
			return err
		}
		return checkM3UninstallTarget(target, profile)
	}

	var installed InstalledRuntime
	if running {
		installed, err = m.verifyDirectM3Runtime(ctx, h, profile)
		if err != nil {
			return nil, err
		}
		if err := m.stopDetached(ctx, h); err != nil {
			return nil, fmt.Errorf("BGE-M3 卸载未开始，Runtime 停机未确认: %w", err)
		}
	}

	removed, err := removeStoppedM3(ctx, root, profile, target, address, recheck)
	if err != nil {
		if running {
			return nil, fmt.Errorf("Runtime 已停机，BGE-M3 卸载未完成: %w", err)
		}
		return nil, err
	}
	status, message := "skip", "not installed"
	if removed {
		status, message = "ok", target
	}
	result, err := json.Marshal(map[string]any{
		"command": "model", "status": status,
		"summary": map[string]any{"dir": target, "removed": removed, "model": "bge-m3"},
		"items":   []map[string]string{{"name": "model", "status": status, "value": message}},
	})
	if err != nil {
		return nil, err
	}
	if running {
		// Retain takeover through restart and check that the same account returns.
		restored, err := m.startDetachedLocked(ctx, installed)
		if err == nil && (!m.isDirectM3Runtime(restored) || !samePath(restored.DataDir, profile) || restored.AccountID != h.AccountID ||
			restored.InstanceID == h.InstanceID) {
			err = errors.New("重启后的 Runtime 身份与原实例不匹配")
		}
		if err == nil {
			err = takeover.Close()
			takeover = nil
			if err == nil {
				err = m.unlockIfNeeded(restored)
			}
		}
		if err == nil && h.StoreReady {
			current, _, healthErr := m.Health(ctx)
			err = healthErr
			if err == nil && (!current.StoreReady || current.AccountID != h.AccountID || current.InstanceID != restored.InstanceID) {
				err = errors.New("重启后的 Runtime 库未恢复就绪")
			}
		}
		if err != nil {
			return result, fmt.Errorf("BGE-M3 文件状态已确认，但 Runtime 恢复失败: %w", err)
		}
	}
	return result, nil
}

func (m *Manager) isDirectM3Runtime(h HealthInfo) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	owner := m.directRuntime
	return owner.PID != 0 && owner.PID == h.PID &&
		owner.InstanceID == h.InstanceID && owner.ProcessStartID == h.ProcessStartID
}

func (m *Manager) verifyDirectM3Runtime(ctx context.Context, h HealthInfo, profile string) (InstalledRuntime, error) {
	if !m.isDirectM3Runtime(h) {
		return InstalledRuntime{}, errors.New("无法确认当前 Runtime 由本次 Client2 直接启动，请先用原宿主停机")
	}
	info, err := m.Installed()
	if err != nil {
		return info, err
	}
	if h.RuntimeVersion != info.Version || !h.StartupComplete ||
		!samePath(h.DataDir, profile) || !strings.EqualFold(h.ImageSHA256, info.SHA256) ||
		!samePath(h.Exe, h.ImagePath) {
		return info, errors.New("当前 Runtime 与已安装的映像或所选账户不匹配，请先用原宿主停机")
	}
	if err := m.verifyInstalled(info); err != nil {
		return info, err
	}
	if err := m.verifySelectedHealth(h, info); err != nil {
		return info, err
	}
	birth, err := runtimeControlProcessBirth(ctx, int(h.PID))
	if err != nil {
		return info, err
	}
	if birth == "" || h.ProcessStartID != runtimeBirthID(birth) {
		return info, errors.New("当前 Runtime 进程启动身份已改变")
	}
	return info, nil
}

func runtimeBirthID(birth string) string {
	switch runtime.GOOS {
	case "windows":
		return "windows-filetime-" + birth
	case "darwin":
		return "darwin-lstart-" + birth
	default:
		return "linux-starttime-" + birth
	}
}

func checkM3UninstallAdmission(root string) error {
	marker := filepath.Join(root, "runtime", "autostart", "maintenance.json")
	if err := checkModelUninstallPath(marker); err != nil {
		return err
	}
	if _, err := os.Lstat(marker); err == nil {
		return errors.New("存在未完成的 Runtime 维护操作")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func requireStoppedM3Runtime(ctx context.Context, root, address string) error {
	if err := requireClosedRuntimePort(ctx, address); err != nil {
		return err
	}
	// The caller holds the actual installation and library locks. A historical
	// endpoint can add proof of a live owner, but cannot establish one by itself.
	raw, err := modelUninstallFile(filepath.Join(root, "runtime", "endpoint.json"))
	var endpoint HealthInfo
	if err != nil || json.Unmarshal(raw, &endpoint) != nil ||
		endpoint.PID == 0 || endpoint.ProcessStartID == "" || endpoint.Server != "respire" ||
		!samePath(endpoint.InstallationRoot, root) || !filepath.IsAbs(endpoint.Exe) ||
		!samePath(endpoint.Exe, endpoint.ImagePath) || !validHash(endpoint.ImageSHA256) {
		return nil
	}
	birth, err := runtimeControlProcessBirth(ctx, int(endpoint.PID))
	if err != nil || birth == "" || runtimeBirthID(birth) != endpoint.ProcessStartID ||
		verifyHostProcess(endpoint) != nil {
		return ctx.Err()
	}
	hash, err := fileHash(endpoint.Exe)
	if err != nil || !strings.EqualFold(hash, endpoint.ImageSHA256) {
		return ctx.Err()
	}
	birth, err = runtimeControlProcessBirth(ctx, int(endpoint.PID))
	if err == nil && birth != "" && runtimeBirthID(birth) == endpoint.ProcessStartID {
		return errors.New("已确认记录中的 Runtime 进程仍然存活，请先用原宿主停机")
	}
	return ctx.Err()
}
