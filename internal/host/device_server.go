package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DeviceServerAddress can be read while the library is locked or Runtime is offline.
func (m *Manager) DeviceServerAddress() (string, error) {
	const defaultServer = "https://api.rsrs.rs"
	raw, err := readCredentialConfig(filepath.Join(m.DataRoot, "client.json"), 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return defaultServer, nil
	}
	if err != nil {
		return "", fmt.Errorf("读取设备服务器设置: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return "", errors.New("client.json 必须包含 JSON 对象")
	}
	field, present := fields["addr"]
	if !present {
		return defaultServer, nil
	}
	var address *string
	if err := json.Unmarshal(field, &address); err != nil || address == nil {
		return "", errors.New("client.json 的 addr 必须是字符串")
	}
	value := strings.TrimSpace(*address)
	if value == "" {
		return defaultServer, nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New("client.json 的 addr 不是有效的 HTTP 或 HTTPS 服务器地址")
	}
	return value, nil
}
