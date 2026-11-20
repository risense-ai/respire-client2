//go:build darwin && !cgo

package host

import (
	"context"
	"encoding/json"
	"errors"
)

func (m *Manager) desktopRuntimeKeyRecovery(context.Context, []string, string, string, string) (json.RawMessage, error) {
	return nil, errors.New("macOS Keychain support requires a native Client2 build")
}

func (m *Manager) resumeDesktopRuntimeRecovery(context.Context, Operation, json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("macOS Keychain support requires a native Client2 build")
}
