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

func (m *Manager) desktopKeysExport(context.Context, []string) (json.RawMessage, error) {
	return nil, errors.New("macOS Keychain support requires a native Client2 build")
}

func (m *Manager) desktopRuntimeAccountLogin(context.Context, []string, string, string, bool) (json.RawMessage, error) {
	return nil, errors.New("macOS Keychain support requires a native Client2 build")
}

func (m *Manager) recoverDesktopRuntimeLogin(context.Context, Operation) (json.RawMessage, error) {
	return nil, errors.New("macOS Keychain support requires a native Client2 build")
}

func (m *Manager) continueDesktopRuntimeLogin(context.Context, Operation, json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("macOS Keychain support requires a native Client2 build")
}
