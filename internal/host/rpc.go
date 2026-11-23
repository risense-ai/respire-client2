package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
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
	InstallationID   string `json:"installation_id"`
	ImagePath        string `json:"image_path"`
	ImageSHA256      string `json:"image_sha256"`
	ProcessStartID   string `json:"process_start_id"`
	RuntimeVersion   string `json:"runtime_version"`
	StoreReady       bool   `json:"store_ready"`
	StartupComplete  bool   `json:"startup_complete"`
	CredentialState  string `json:"credential_state"`
	AccountID        string `json:"account_id"`
	InstanceID       string `json:"instance_id"`
	Generation       uint64 `json:"generation"`
}

type runtimeScope struct {
	AccountID  string `json:"account_id"`
	InstanceID string `json:"instance_id"`
	Generation uint64 `json:"generation"`
}

func (m *Manager) requestScope(ctx context.Context) (*runtimeScope, error) {
	health, _, err := m.Health(ctx)
	if err != nil {
		return nil, err
	}
	if health.AccountID == "" || health.InstanceID == "" || health.Generation == 0 {
		return nil, errors.New("Runtime 未提供完整请求身份，请更新 Runtime")
	}
	return &runtimeScope{health.AccountID, health.InstanceID, health.Generation}, nil
}

// Only public, scope-exempt Runtime capabilities enter this list. A result
// lookup must keep its original scope, and account/business reads are scoped.
func runtimeScopeExempt(method string) bool {
	switch method {
	case "runtime.status", "assets.status", "model.catalog", "model.progress", "cli.progress":
		return true
	default:
		return false
	}
}

const defaultRuntimeHTTPTimeout = 30 * time.Second

func (m *Manager) runtimeHTTPTransport(method string, args []string) *http.Client {
	client := m.localClient
	if method != "cli.exec" || len(args) < 2 || args[0] != "--json" || client.Timeout != defaultRuntimeHTTPTimeout {
		return client
	}
	var timeout time.Duration
	switch {
	case len(args) >= 3 && args[1] == "model" && args[2] == "probe":
		// Preserve the existing cold-load allowance for explicit inference.
		timeout = 2 * time.Minute
	case len(args) == 2 && args[1] == "reembed":
		// Rebuilding is synchronous. Keep a finite cap inside the outer
		// 30-minute desktop job, which may already have spent time in preflight.
		// Receipt lookup still uses the original job's remaining budget.
		timeout = 29 * time.Minute
	default:
		return client
	}
	operationClient := *client
	operationClient.Timeout = timeout
	return &operationClient
}

func (m *Manager) localRequest(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var input io.Reader
	transport := m.localClient
	if method == http.MethodPost && path == "/api/rpc" {
		request, ok := body.(map[string]any)
		if !ok {
			return nil, errors.New("Runtime RPC 请求格式无效")
		}
		rpcMethod, _ := request["method"].(string)
		rpcArgs, _ := request["args"].([]string)
		transport = m.runtimeHTTPTransport(rpcMethod, rpcArgs)
		_, scoped := request["scope"]
		if !scoped && !runtimeScopeExempt(rpcMethod) {
			scope, err := m.requestScope(ctx)
			if err != nil {
				return nil, err
			}
			request["scope"] = scope
		}
		if !runtimeScopeExempt(rpcMethod) && rpcMethod != "runtime.result" {
			request["operation_id"] = request["id"]
			request["idempotency_key"] = request["id"]
			if id, ok := request["id"].(string); ok {
				if parent, _, child := strings.Cut(id, "/"); child {
					request["parent_operation_id"] = parent
				}
			}
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
	res, err := transport.Do(req)
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
