//go:build !linux && !windows && (!darwin || !cgo)

package host

import (
	"context"
	"encoding/json"
	"errors"
)

func (m *Manager) desktopLegacyMigration(context.Context, string, []string, bool) (json.RawMessage, error) {
	return nil, errors.New("当前 Client2 构建无法使用系统钥匙串执行旧库迁移")
}
