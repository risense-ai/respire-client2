package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func isDesktopAgentTextSetting(key string) bool {
	switch key {
	case "diary_mode", "recall_mode", "recall_api_base", "recall_model":
		return true
	default:
		return false
	}
}

// Runtime reads these settings from the selected profile for each operation.
func (m *Manager) setDesktopAgentTextSetting(ctx context.Context, id, setting string) (json.RawMessage, error) {
	key, value, hasValue := strings.Cut(setting, "=")
	key = strings.TrimSpace(key)
	if !hasValue || !isDesktopAgentTextSetting(key) {
		return nil, errors.New("无效的账户设置")
	}
	if key == "diary_mode" && value != "concise" && value != "verbose" {
		return nil, errors.New("diary_mode 必须为 concise 或 verbose")
	}
	if key == "recall_mode" && value != "fast" && value != "quality" {
		return nil, errors.New("recall_mode 必须为 fast 或 quality")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	takeover, err := m.lockRuntimeTakeover(ctx)
	if err != nil {
		return nil, err
	}
	defer takeover.Close()
	profile, err := m.coreConfigProfile()
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(m.DataRoot, "runtime", "autostart", "maintenance.json")); err == nil {
		return nil, errors.New("存在 Runtime 维护事务，未写入账户设置")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	settings, before, err := readCoreConfigFile(profile)
	if err != nil {
		return nil, err
	}
	settings[key] = encoded
	change := &coreConfigChange{Profile: profile, Before: before, Key: key}
	if err := m.publishDesktopAgentSettings(id, change, settings); err != nil {
		return nil, err
	}
	return desktopAgentSettingResult(key, encoded)
}

func desktopAgentSettingResult(key string, value json.RawMessage) (json.RawMessage, error) {
	return json.Marshal(map[string]any{"command": "agent-config", "status": "ok", "summary": map[string]any{
		"action": "updated", "key": key, "value": value,
	}})
}
