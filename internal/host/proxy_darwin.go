//go:build darwin

package host

import (
	"context"
	"os/exec"
	"time"
)

// readSystemProxy asks SystemConfiguration for the active proxy settings.
// scutil ships with macOS and prints the same dictionary the network
// preference pane edits. Failures fall back to a direct connection rather
// than blocking outbound traffic.
func readSystemProxy() systemProxy {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--proxy").Output()
	if err != nil {
		return systemProxy{Source: proxySourceDirect}
	}
	return parseScutilProxy(string(out))
}
