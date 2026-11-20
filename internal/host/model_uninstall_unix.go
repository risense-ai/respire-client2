//go:build linux || darwin

package host

import "os"

func modelUninstallRedirect(info os.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}
