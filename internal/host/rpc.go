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
	Server           string `json:"server"`
	Version          string `json:"bin"`
	Protocol         int    `json:"v"`
	PID              uint32 `json:"pid"`
	Exe              string `json:"exe"`
	DataDir          string `json:"data_dir"`
	ManagedID        string `json:"managed_id"`
	InstallationRoot string `json:"installation_root"`
	StoreReady       bool   `json:"store_ready"`
	CredentialState  string `json:"credential_state"`
	AccountID        string `json:"account_id"`
	InstanceID       string `json:"instance_id"`
	Generation       uint64 `json:"generation"`
}

func (m *Manager) localRequest(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var input io.Reader
	if method == http.MethodPost && path == "/api/rpc" {
		request, ok := body.(map[string]any)
		if !ok {
			return nil, errors.New("Runtime RPC 请求格式无效")
		}
		health, _, err := m.Health(ctx)
		if err != nil {
			return nil, err
		}
		if health.AccountID == "" || health.InstanceID == "" || health.Generation == 0 {
			return nil, errors.New("Runtime 未提供完整请求身份，请更新 Runtime")
		}
		request["scope"] = map[string]any{"account_id": health.AccountID, "instance_id": health.InstanceID, "generation": health.Generation}
		switch request["method"] {
		case "runtime.status", "assets.status", "model.catalog", "model.progress", "cli.progress":
		case "runtime.result":
		default:
			request["operation_id"] = request["id"]
			request["idempotency_key"] = request["id"]
		}
	}
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
	if method == http.MethodGet && path == "/api/health" {
		req.Header.Set("X-Respire-Health", "runtime-v1")
	}
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
