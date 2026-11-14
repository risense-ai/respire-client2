//go:build !darwin && !windows

package host

// readSystemProxy has no portable implementation on this platform: Linux
// desktops disagree on where proxy settings live (GNOME, KDE, environment),
// so the client keeps honouring HTTP_PROXY/HTTPS_PROXY/NO_PROXY only.
func readSystemProxy() systemProxy {
	return systemProxy{Source: proxySourceDirect}
}
