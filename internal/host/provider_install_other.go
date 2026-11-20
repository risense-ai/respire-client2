//go:build !windows

package host

import (
	"context"
	"encoding/json"
	"runtime"
)

func (m *Manager) installModelEngines(context.Context) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"command": "model", "status": "skip",
		"summary": map[string]any{"platform": runtime.GOOS, "required": false, "providers": []string{}, "npu_ready": false},
		"items":   []map[string]string{{"name": "engine", "status": "skip", "value": "此平台无需安装 Windows ML 推理引擎"}},
	})
}
