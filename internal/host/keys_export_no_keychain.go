//go:build darwin && !cgo

package host

import (
	"context"
	"encoding/json"
	"errors"
)

func (m *Manager) desktopKeysExport(context.Context, []string) (json.RawMessage, error) {
	return nil, errors.New("macOS Keychain support requires a native Client2 build")
}
