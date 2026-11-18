//go:build windows

package host

import (
	"errors"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// readSystemProxy reads the per-user Internet Settings proxy configuration.
// That is the same key the Settings app and WinINET use, so a proxy set in
// Windows applies to the client without extra configuration.
func readSystemProxy() systemProxy {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return systemProxy{Source: proxySourceDirect}
		}
		return systemProxy{Source: proxySourceUnavailable, Note: "读取 Windows 系统代理失败"}
	}
	defer key.Close()
	enabled := false
	if value, _, err := key.GetIntegerValue("ProxyEnable"); err == nil {
		enabled = value == 1
	}
	server, _, _ := key.GetStringValue("ProxyServer")
	override, _, _ := key.GetStringValue("ProxyOverride")
	autoConfigURL, _, _ := key.GetStringValue("AutoConfigURL")
	return windowsSystemProxy(enabled, server, override, strings.TrimSpace(autoConfigURL))
}
