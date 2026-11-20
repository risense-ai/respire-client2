package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type workspaceModeChange struct {
	key, raw, mode string
	value          any
}

func isWorkspaceModeSetting(key string) bool {
	return key == "workspace_mode" || key == "readonly" || key == "memory_off"
}

func (m *Manager) setDesktopWorkspaceMode(ctx context.Context, setting string) (json.RawMessage, error) {
	key, raw, ok := strings.Cut(setting, "=")
	change := workspaceModeChange{key: strings.TrimSpace(key), raw: raw}
	if !ok || !isWorkspaceModeSetting(change.key) {
		return nil, errors.New("工作区设置需要有效的 key=value")
	}
	if json.Unmarshal([]byte(raw), &change.value) != nil {
		change.value = raw
	}
	switch change.key {
	case "workspace_mode":
		change.mode, _ = change.value.(string)
	case "readonly", "memory_off":
		on, ok := change.value.(bool)
		if !ok {
			return nil, errors.New("readonly 和 memory_off 必须为 true 或 false")
		}
		switch {
		case !on:
			change.mode = "normal"
		case change.key == "readonly":
			change.mode = "readonly"
		default:
			change.mode = "off"
		}
	}
	switch change.mode {
	case "normal", "readonly", "off":
		return m.changeDeviceConfig(ctx, deviceConfigIntent{}, &change)
	default:
		return nil, errors.New("workspace_mode 必须为 normal、readonly 或 off")
	}
}

// Match the Rust host writer's recovery fences. Team access stays in agent.json.
func (c workspaceModeChange) check(root, profile string) (bool, error) {
	blocker := filepath.Join(profile, ".snapshot-maintenance.json")
	if _, err := os.Lstat(blocker); err == nil {
		return false, errors.New("请先完成当前 Runtime 快照维护")
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for _, journal := range []struct {
		path    string
		version int
	}{{filepath.Join(root, ".login-recovery.json"), 2}, {filepath.Join(profile, ".snapshot-recovery.json"), 1}} {
		info, err := os.Lstat(journal.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("恢复记录不是普通文件 (%s)", filepath.Base(journal.path))
		}
		raw, err := os.ReadFile(journal.path)
		if err != nil {
			return false, err
		}
		var record struct {
			Schema int             `json:"schema_version"`
			Active json.RawMessage `json:"active"`
			Phase  string          `json:"phase"`
		}
		err = json.Unmarshal(raw, &record)
		clear(raw)
		if err != nil || record.Schema != journal.version || journal.version == 2 && string(record.Active) != "null" ||
			journal.version == 1 && record.Phase != "verified" && record.Phase != "released" && record.Phase != "rolled_back" {
			return false, fmt.Errorf("请先完成 Runtime 恢复 (%s)", filepath.Base(journal.path))
		}
	}
	agent, err := readAgentSettingsObject(filepath.Join(profile, "agent.json"))
	if err != nil {
		return false, err
	}
	team, _ := agent["readonly_team"].(bool)
	if team && c.key == "readonly" && c.mode == "normal" {
		return false, errors.New("当前团队空间为只读，成员不能解除团队只读")
	}
	return team, nil
}

func (c workspaceModeChange) result(teamReadonly, applied, storeReady bool) (json.RawMessage, error) {
	effective := c.mode
	if teamReadonly && effective == "normal" {
		effective = "readonly"
	}
	return json.Marshal(map[string]any{"command": "agent-config", "status": "ok", "summary": map[string]any{
		"action": "updated", "key": c.key, "value": c.value, "workspace_mode": effective,
		"runtime_applied": applied, "store_ready": storeReady,
	}, "items": []map[string]string{{"name": c.key, "status": "ok", "value": c.raw}}})
}
