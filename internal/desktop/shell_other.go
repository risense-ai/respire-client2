//go:build !windows

package desktop

// Windows-only shell registration has no cross-platform equivalent here.
func ensureShellIdentity() {}
