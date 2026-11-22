package host

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelSwitchUsesRuntimeProfileRulesAndReceivesTask(t *testing.T) {
	for _, tc := range []struct {
		model   string
		variant string
		want    string
	}{
		{"m3", "quantized", "quantized"},
		{"granite-311m-r2", "q4", ""},
		{"bge-base-zh-v1.5", "arbitrary formerly rejected profile label", ""},
	} {
		t.Run(tc.model, func(t *testing.T) {
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/health" {
					_, _ = w.Write([]byte(`{"server":"respire","v":1,"pid":7,"account_id":"account","instance_id":"test","generation":2}`))
					return
				}
				var request struct {
					Method string        `json:"method"`
					ID     string        `json:"id"`
					Args   []string      `json:"args"`
					Scope  *runtimeScope `json:"scope"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Method != "model.switch" || len(request.Args) != 1 || request.Scope == nil {
					t.Errorf("unexpected switch request: %+v (%v)", request, err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var choice map[string]string
				if err := json.Unmarshal([]byte(request.Args[0]), &choice); err != nil {
					t.Errorf("bad selection: %v", err)
				}
				got, present := choice["variant"]
				if got != tc.want || present != (tc.want != "") || choice["model"] != tc.model || choice["engine"] != "cpu" {
					t.Errorf("unexpected selection: %v", choice)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"v": 1, "id": request.ID, "scope": request.Scope, "operation_id": request.ID, "ok": true,
					"envelope": map[string]any{"status": "pending", "summary": map[string]string{"id": "task-from-host"}},
				})
			}))
			defer origin.Close()
			m := &Manager{Endpoint: origin.URL, localClient: origin.Client()}
			receipt, err := m.ModelSwitchWithReceipt(t.Context(), tc.model, "cpu", tc.variant)
			if err != nil || receipt.ID != "task-from-host" || receipt.Status != "pending" {
				t.Fatalf("not bound to Runtime task receipt: %+v, %v", receipt, err)
			}
		})
	}
}

func TestScopeMatchedQueuedMethodsAdmitPendingOnlyOnSuccess(t *testing.T) {
	pending := json.RawMessage(`{"status":"pending"}`)
	for _, method := range []string{"model.prepare", "index.prepare", "model.switch", "cli.exec", "auth.login"} {
		accepted := method == "model.prepare" || method == "index.prepare" || method == "model.switch"
		err := responseErrorForMethod(method, rpcResponse{OK: true, Envelope: pending})
		var unresolved *unconfirmedError
		if (accepted && err != nil) || (!accepted && !errors.As(err, &unresolved)) {
			t.Errorf("%s accepted=%v, error=%v", method, accepted, err)
		}
		err = responseErrorForMethod(method, rpcResponse{OK: false, Error: "admission rejected", Envelope: pending})
		if err == nil || !strings.Contains(err.Error(), "admission rejected") {
			t.Errorf("%s rejection lost: %v", method, err)
		}
	}
}

func TestModelRPCReportsActualSingleOrPluralError(t *testing.T) {
	for _, tc := range []struct{ reply, want string }{
		{`{"ok":false,"error":"outer single"}`, "outer single"},
		{`{"ok":false,"errors":["outer list"]}`, "outer list"},
		{`{"ok":false,"envelope":{"status":"fail","error":"nested single"}}`, "nested single"},
		{`{"ok":true,"envelope":{"status":"fail","errors":["nested list"]}}`, "nested list"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.reply))
			}))
			defer origin.Close()
			m := &Manager{Endpoint: origin.URL, localClient: origin.Client()}
			if _, err := m.ModelProgress(t.Context()); err == nil || err.Error() != tc.want {
				t.Fatalf("message %q lost: %v", tc.want, err)
			}
		})
	}
}

func TestModelSwitchRefusesUnboundTaskReceiptButPreservesLegacyNoID(t *testing.T) {
	for _, bad := range []string{"scope", "request ID", "operation ID", ""} {
		t.Run(bad, func(t *testing.T) {
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/health" {
					_, _ = w.Write([]byte(`{"server":"respire","v":1,"pid":7,"account_id":"account","instance_id":"test","generation":2}`))
					return
				}
				var request struct {
					ID    string       `json:"id"`
					Scope runtimeScope `json:"scope"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				reply := rpcResponse{V: 1, ID: request.ID, Scope: &request.Scope, OperationID: request.ID, OK: true,
					Envelope: json.RawMessage(`{"status":"pending","summary":{"id":"task-from-host"}}`)}
				switch bad {
				case "scope":
					reply.Scope.Generation++
				case "request ID":
					reply.ID = "other-request"
				case "operation ID":
					reply.OperationID = "other-operation"
				default:
					reply = rpcResponse{OK: true, Envelope: json.RawMessage(`{"status":"ok","summary":{}}`)}
				}
				_ = json.NewEncoder(w).Encode(reply)
			}))
			defer origin.Close()
			m := &Manager{Endpoint: origin.URL, localClient: origin.Client()}
			receipt, err := m.ModelSwitchWithReceipt(t.Context(), "m3", "cpu", "quantized")
			if bad == "" {
				if err != nil || receipt.ID != "" || receipt.Status != "ok" {
					t.Fatalf("compatibility result changed: %+v, %v", receipt, err)
				}
				return
			}
			var unconfirmed *unconfirmedError
			if !errors.As(err, &unconfirmed) || receipt.ID != "" {
				t.Fatalf("unbound receipt mistaken for confirmed task: %+v, %v", receipt, err)
			}
		})
	}
}
