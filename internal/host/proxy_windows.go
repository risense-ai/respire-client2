//go:build windows

package host

import (
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
		return systemProxy{Source: proxySourceDirect}
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
