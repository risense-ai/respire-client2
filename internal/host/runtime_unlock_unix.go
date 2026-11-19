//go:build darwin && !cgo

package host

import (
	"context"
	"encoding/json"
	"errors"
)

func (m *Manager) unlockAccountRuntime(context.Context, HealthInfo) error {
	return errors.New("macOS Keychain support requires a native Client2 build")
}

func (m *Manager) desktopRuntimePasswordLogin(context.Context, []string, string, string) (json.RawMessage, error) {
	return nil, errors.New("macOS Keychain support requires a native Client2 build")
}
