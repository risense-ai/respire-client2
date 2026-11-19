//go:build darwin && !cgo

package host

import (
	"context"
	"errors"
)

func (m *Manager) unlockAccountRuntime(context.Context, HealthInfo) error {
	return errors.New("macOS Keychain support requires a native Client2 build")
}
