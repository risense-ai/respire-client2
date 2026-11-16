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
