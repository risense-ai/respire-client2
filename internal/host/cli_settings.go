package host

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// CLI language is a host preference; the CLI exposes no language RPC command.
func (m *Manager) SetCLILanguage(language string) error {
	if language != "zh" && language != "en" {
		return errors.New("CLI 语言必须是 zh 或 en")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	path := filepath.Join(filepath.Dir(m.Root), "client.json")
	settings := map[string]json.RawMessage{}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		if err = json.Unmarshal(b, &settings); err != nil {
			return err
		}
		if settings == nil {
			return errors.New("CLI 配置必须是 JSON 对象")
		}
	}
	settings["lang"], err = json.Marshal(language)
	if err != nil {
		return err
	}
	return writeJSON(path, settings)
}
