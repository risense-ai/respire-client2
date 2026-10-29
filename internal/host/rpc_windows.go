package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type HealthInfo struct {
	Server   string `json:"server"`
	Version  string `json:"bin"`
	Protocol int    `json:"v"`
	PID      uint32 `json:"pid"`
	Exe      string `json:"exe"`
	DataDir  string `json:"data_dir"`
}

func (m *Manager) localRequest(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var input io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		input = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.Endpoint+path, input)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := m.localClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 8<<20 {
		return nil, errors.New("Runtime response exceeds 8 MiB")
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("Runtime HTTP %d", res.StatusCode)
	}
	if !json.Valid(b) {
		return nil, errors.New("Runtime response is not JSON")
	}
	return b, nil
}

func (m *Manager) Health(ctx context.Context) (HealthInfo, json.RawMessage, error) {
	b, err := m.localRequest(ctx, "GET", "/api/health", nil)
	var h HealthInfo
	if err != nil {
		return h, nil, err
	}
	if err = json.Unmarshal(b, &h); err != nil {
		return h, nil, err
	}
	if h.Server != "respire" || h.Protocol != 1 || h.PID == 0 {
		return h, nil, errors.New("端口不是兼容的 Respire Runtime")
	}
	return h, b, nil
}

func (m *Manager) RPC(command string) (json.RawMessage, error) {
	if command != "status" && command != "doctor" {
		return nil, errors.New("unsupported diagnostic command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, _, err := m.Health(ctx); err != nil {
		return nil, err
	}
	return m.localRequest(ctx, "POST", "/api/rpc", map[string]any{"v": 1, "id": fmt.Sprintf("client2-%d", time.Now().UnixNano()), "method": "cli.exec", "args": []string{"--json", command}})
}
