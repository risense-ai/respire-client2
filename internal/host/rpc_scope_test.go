package host

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRPCBindsOperationsToRuntimeIdentity(t *testing.T) {
	for _, method := range []string{"cli.exec", "model.catalog", "runtime.result"} {
		t.Run(method, func(t *testing.T) {
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/health" {
					_, _ = w.Write([]byte(`{"server":"respire","v":1,"pid":42,"account_id":"account","instance_id":"instance","generation":7}`))
					return
				}
				called = true
				var request struct {
					Scope struct {
						AccountID  string `json:"account_id"`
						InstanceID string `json:"instance_id"`
						Generation uint64 `json:"generation"`
					} `json:"scope"`
					OperationID    string `json:"operation_id"`
					IdempotencyKey string `json:"idempotency_key"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.Scope.AccountID != "account" || request.Scope.InstanceID != "instance" || request.Scope.Generation != 7 {
					t.Error("request lost its Runtime identity")
				}
				if method == "cli.exec" {
					if request.OperationID != "original" || request.IdempotencyKey != "original" {
						t.Error("operation identity differs from the original request")
					}
				} else if request.OperationID != "" || request.IdempotencyKey != "" {
					t.Error("catalog or result query must not create an operation")
				}
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer server.Close()
			manager := &Manager{Endpoint: server.URL, localClient: server.Client()}
			_, err := manager.localRequest(context.Background(), "POST", "/api/rpc", map[string]any{"v": 1, "id": "original", "method": method, "args": []string{}})
			if err != nil || !called {
				t.Fatalf("scoped RPC failed: %v", err)
			}
		})
	}
}

func TestRuntimeStatusLockedWarning(t *testing.T) {
	const locked = `{"command":"status","status":"warn","summary":{"credential_state":"required","store_ready":false},"errors":[]}`
	for _, tc := range []struct {
		name     string
		envelope string
		exit     int
		code     string
		failure  string
		scope    *runtimeScope
		ok       bool
		noScope  bool
		wrongID  bool
		wantErr  bool
	}{
		{name: "keyless warning", envelope: locked, exit: 2},
		{name: "ready status", envelope: `{"command":"status","status":"ok","summary":{"credential_state":"available","store_ready":true},"errors":[]}`, ok: true},
		{name: "missing scope", envelope: locked, exit: 2, noScope: true, wantErr: true},
		{name: "wrong operation ID", envelope: locked, exit: 2, wrongID: true, wantErr: true},
		{name: "failed status", envelope: `{"command":"status","status":"fail","summary":{},"errors":["real failure"]}`, exit: 1, failure: "real failure", wantErr: true},
		{name: "wrong exit", envelope: locked, exit: 1, wantErr: true},
		{name: "zero exit on rejected reply", envelope: locked, exit: 0, wantErr: true},
		{name: "error code", envelope: locked, exit: 2, code: "scope_mismatch", wantErr: true},
		{name: "error message", envelope: locked, exit: 2, failure: "real failure", wantErr: true},
		{name: "wrong command", envelope: `{"command":"show","status":"warn","summary":{"credential_state":"required","store_ready":false}}`, exit: 2, wantErr: true},
		{name: "unlocked store", envelope: `{"command":"status","status":"warn","summary":{"credential_state":"required","store_ready":true}}`, exit: 2, wantErr: true},
		{name: "missing store state", envelope: `{"command":"status","status":"warn","summary":{"credential_state":"required"}}`, exit: 2, wantErr: true},
		{name: "wrong credential state", envelope: `{"command":"status","status":"warn","summary":{"credential_state":"available","store_ready":false}}`, exit: 2, wantErr: true},
		{name: "warning with errors", envelope: `{"command":"status","status":"warn","summary":{"credential_state":"required","store_ready":false},"errors":["real failure"]}`, exit: 2, wantErr: true},
		{name: "wrong account", envelope: locked, exit: 2, scope: &runtimeScope{AccountID: "other", InstanceID: "instance", Generation: 7}, wantErr: true},
		{name: "wrong instance", envelope: locked, exit: 2, scope: &runtimeScope{AccountID: "account", InstanceID: "other", Generation: 7}, wantErr: true},
		{name: "wrong generation", envelope: locked, exit: 2, scope: &runtimeScope{AccountID: "account", InstanceID: "instance", Generation: 8}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/health" {
					_, _ = w.Write([]byte(`{"server":"respire","v":1,"pid":42,"account_id":"account","instance_id":"instance","generation":7}`))
					return
				}
				var request struct {
					ID             string        `json:"id"`
					Method         string        `json:"method"`
					Args           []string      `json:"args"`
					Scope          *runtimeScope `json:"scope"`
					OperationID    string        `json:"operation_id"`
					IdempotencyKey string        `json:"idempotency_key"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				if request.Method != "cli.exec" || len(request.Args) != 2 || request.Args[0] != "--json" || request.Args[1] != "status" ||
					request.Scope == nil || *request.Scope != (runtimeScope{AccountID: "account", InstanceID: "instance", Generation: 7}) ||
					request.OperationID != request.ID || request.IdempotencyKey != request.ID {
					t.Error("status request lost its read-only command or original identity")
				}
				scope := request.Scope
				if tc.scope != nil {
					scope = tc.scope
				}
				if tc.noScope {
					scope = nil
				}
				operationID := request.ID
				if tc.wrongID {
					operationID = "other"
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{V: 1, ID: request.ID, OK: tc.ok, Exit: tc.exit,
					Scope: scope, OperationID: operationID, Envelope: json.RawMessage(tc.envelope), Code: tc.code, Error: tc.failure})
			}))
			defer server.Close()
			manager := &Manager{Endpoint: server.URL, localClient: server.Client()}
			envelope, err := manager.RuntimeStatus(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("status error = %v, want error = %v", err, tc.wantErr)
			}
			if !tc.wantErr {
				if string(envelope) != tc.envelope {
					t.Fatalf("status warning changed: %s", envelope)
				}
				if responseError(rpcResponse{Exit: 2, Envelope: envelope}) == nil {
					t.Fatal("generic response handling must still reject an unsuccessful warning")
				}
			}
		})
	}
}

func TestRPCRejectsMissingRuntimeIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			t.Error("RPC sent without a complete Runtime identity")
		}
		_, _ = w.Write([]byte(`{"server":"respire","v":1,"pid":42}`))
	}))
	defer server.Close()
	manager := &Manager{Endpoint: server.URL, localClient: server.Client()}
	if _, err := manager.localRequest(context.Background(), "POST", "/api/rpc", map[string]any{"id": "original", "method": "cli.exec"}); err == nil {
		t.Fatal("missing Runtime identity was accepted")
	}
}
