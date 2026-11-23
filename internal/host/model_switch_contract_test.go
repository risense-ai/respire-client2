package host

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
					"envelope": map[string]any{"status": "pending", "summary": map[string]string{"id": request.ID}},
				})
			}))
			defer origin.Close()
			m := &Manager{Endpoint: origin.URL, localClient: origin.Client()}
			receipt, err := m.ModelSwitchWithReceipt(t.Context(), tc.model, "cpu", tc.variant)
			if err != nil || !strings.HasPrefix(receipt.ID, "client2-model-") || receipt.Status != "pending" {
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
	for _, bad := range []string{"scope", "request ID", "operation ID", "different task ID", ""} {
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
					Envelope: json.RawMessage(`{"command":"model.switch","status":"pending","summary":{"id":"` + request.ID + `"}}`)}
				switch bad {
				case "scope":
					reply.Scope.Generation++
				case "request ID":
					reply.ID = "other-request"
				case "operation ID":
					reply.OperationID = "other-operation"
				case "different task ID":
					reply.Envelope = json.RawMessage(`{"command":"model.switch","status":"pending","summary":{"id":"other-task"}}`)
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

func TestModelSwitchSeparatesPreDispatchRejectionFromAmbiguousRuntimeRoundTrip(t *testing.T) {
	for _, name := range []string{"health rejects", "dropped response", "HTTP failure", "malformed response",
		"bound scope rejection", "unbound scope rejection", "bound possibly-written failure", "bound in progress"} {
		t.Run(name, func(t *testing.T) {
			var health, post atomic.Int32
			runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/health" {
					health.Add(1)
					if name == "health rejects" {
						http.Error(w, "no usable scope", http.StatusServiceUnavailable)
						return
					}
					_, _ = w.Write([]byte(`{"server":"respire","v":1,"pid":7,"account_id":"account","instance_id":"instance","generation":2}`))
					return
				}
				post.Add(1)
				var input struct {
					ID          string       `json:"id"`
					Scope       runtimeScope `json:"scope"`
					OperationID string       `json:"operation_id"`
					Method      string       `json:"method"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Method != "model.switch" ||
					input.Scope.AccountID != "account" || input.OperationID != input.ID {
					t.Errorf("unexpected dispatch: %+v %v", input, err)
				}
				switch name {
				case "dropped response":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
				case "HTTP failure":
					http.Error(w, "unknown HTTP proxy outcome", http.StatusBadGateway)
				case "malformed response":
					_, _ = w.Write([]byte(`{"unclosed":`))
				default:
					reply := rpcResponse{V: 1, ID: input.ID, Scope: &input.Scope, OperationID: input.ID,
						OK: false, Error: "Runtime reply detail"}
					switch name {
					case "bound scope rejection":
						reply.Code = "scope_mismatch"
					case "unbound scope rejection":
						reply.Code, reply.OperationID = "scope_mismatch", "other"
					case "bound possibly-written failure":
						reply.Code = "model_profile_failed"
					case "bound in progress":
						reply.Code = "request_in_progress"
					}
					_ = json.NewEncoder(w).Encode(reply)
				}
			}))
			defer runtime.Close()
			m := &Manager{Endpoint: runtime.URL, localClient: runtime.Client()}
			receipt, err := m.ModelSwitchWithReceipt(t.Context(), "m3", "cpu", "quantized")
			wantPosts, wantUnconfirmed := int32(1), true
			if name == "health rejects" {
				wantPosts, wantUnconfirmed = 0, false
			}
			if name == "bound scope rejection" {
				wantUnconfirmed = false
			}
			var report *unconfirmedError
			if err == nil || errors.As(err, &report) != wantUnconfirmed || receipt.ID != "" ||
				health.Load() != 1 || post.Load() != wantPosts {
				t.Fatalf("receipt=%+v error=%v Health=%d POST=%d", receipt, err, health.Load(), post.Load())
			}
			if name == "bound scope rejection" && !strings.Contains(err.Error(), "Runtime reply detail") {
				t.Fatalf("definite rejection detail lost: %v", err)
			}
		})
	}
}
