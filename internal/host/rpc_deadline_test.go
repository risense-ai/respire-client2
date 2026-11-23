package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type deadlineTestTransport func(*http.Request) (*http.Response, error)

func (f deadlineTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func deadlineTestResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
}

func TestRuntimeSynchronousModelDeadlines(t *testing.T) {
	scope := runtimeScope{"account", "instance", 1}
	for _, tc := range []struct {
		name, method string
		args         []string
		delay        time.Duration
		parent       time.Duration
		wantTimeout  bool
	}{
		{"slow-reembed", "cli.exec", []string{"--json", "reembed"}, 31 * time.Second, 0, false},
		{"slow-probe", "cli.exec", []string{"--json", "model", "probe", "--text=example"}, 31 * time.Second, 0, false},
		{"probe-capped", "cli.exec", []string{"--json", "model", "probe"}, 121 * time.Second, 0, true},
		{"reembed-capped", "cli.exec", []string{"--json", "reembed"}, 30 * time.Minute, 0, true},
		{"caller-deadline", "cli.exec", []string{"--json", "reembed"}, 31 * time.Second, time.Second, true},
		{"unrelated-model-control", "cli.exec", []string{"--json", "model", "engine"}, 31 * time.Second, 0, true},
		{"receipt-lookup", "runtime.result", []string{"original"}, 31 * time.Second, 0, true},
		{"async-model-admission", "model.switch", []string{"m3"}, 31 * time.Second, 0, true},
		{"fast-probe", "cli.exec", []string{"--json", "model", "probe"}, time.Second, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := &Manager{Endpoint: "http://localhost", localClient: &http.Client{
					Timeout: defaultRuntimeHTTPTimeout,
					Transport: deadlineTestTransport(func(r *http.Request) (*http.Response, error) {
						select {
						case <-r.Context().Done():
							return nil, r.Context().Err()
						case <-time.After(tc.delay):
							return deadlineTestResponse(`{"ok":true}`), nil
						}
					}),
				}}
				ctx := t.Context()
				if tc.parent != 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, tc.parent)
					defer cancel()
				}
				_, err := m.localRequest(ctx, http.MethodPost, "/api/rpc", map[string]any{
					"method": tc.method, "id": "original", "args": tc.args, "scope": &scope,
				})
				if tc.wantTimeout {
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("wanted deadline exceeded, got %v", err)
					}
				} else if err != nil {
					t.Fatalf("received early failure: %v", err)
				}
				if m.localClient.Timeout != defaultRuntimeHTTPTimeout {
					t.Fatal("shared loopback client mutated")
				}
			})
		})
	}
}

func TestRuntimeSynchronousModelDeadlinesPreserveCustomClient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		custom := 3 * time.Second
		m := &Manager{Endpoint: "http://localhost", localClient: &http.Client{
			Timeout: custom,
			Transport: deadlineTestTransport(func(r *http.Request) (*http.Response, error) {
				<-r.Context().Done()
				return nil, r.Context().Err()
			}),
		}}
		scope := runtimeScope{"account", "instance", 1}
		for _, args := range [][]string{{"--json", "model", "probe"}, {"--json", "reembed"}} {
			start := time.Now()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			_, err := m.localRequest(ctx, http.MethodPost, "/api/rpc", map[string]any{
				"method": "cli.exec", "id": "original", "args": args, "scope": &scope,
			})
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != custom {
				t.Fatalf("%v: custom client's limit ignored: %v after %v", args, err, time.Since(start))
			}
		}
	})
}

func TestReembedDisconnectUsesOnlyOriginalScopeAndRemainingJobBudget(t *testing.T) {
	const id = "desktop-12345678-1234-1234-1234-123456789abc"
	const health = `{"server":"respire","v":1,"pid":42,"account_id":"account","instance_id":"instance","generation":1}`
	scope := runtimeScope{"account", "instance", 1}
	for _, expire := range []bool{false, true} {
		name := "receipt-within-budget"
		if expire {
			name = "parent-budget-exhausted"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				healthReads, writes, lookups := 0, 0, 0
				m := &Manager{Root: root, Endpoint: "http://localhost", jobs: map[string]*Operation{
					id: {ID: id, Name: "index_rebuild", State: "running", write: true},
				}, localClient: &http.Client{
					Timeout: defaultRuntimeHTTPTimeout,
					Transport: deadlineTestTransport(func(r *http.Request) (*http.Response, error) {
						if r.URL.Path == "/api/health" {
							healthReads++
							time.Sleep(25 * time.Second)
							return deadlineTestResponse(health), nil
						}
						var req struct {
							ID          string       `json:"id"`
							Method      string       `json:"method"`
							Args        []string     `json:"args"`
							OperationID string       `json:"operation_id"`
							Scope       runtimeScope `json:"scope"`
						}
						if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
							return nil, err
						}
						if req.Scope != scope {
							t.Fatalf("scope changed: %+v", req)
						}
						switch req.Method {
						case "cli.exec":
							writes++
							if req.ID != id || req.OperationID != id || strings.Join(req.Args, " ") != "--json reembed" {
								t.Fatalf("mutation not bound to the original request: %+v", req)
							}
							<-r.Context().Done()
							if got := time.Since(start); got != 29*time.Minute+25*time.Second {
								t.Fatalf("write deadline omitted preflight or wrong operation limit: %v", got)
							}
							return nil, r.Context().Err()
						case "runtime.result":
							lookups++
							if req.ID != "inspect-"+id || req.OperationID != "" || len(req.Args) != 1 || req.Args[0] != id {
								t.Fatalf("lookup not bound to the original request: %+v", req)
							}
							if expire {
								if lookups == 1 {
									time.Sleep(29 * time.Second)
									return deadlineTestResponse(`{"v":1,"id":"inspect-` + id + `","ok":true,"envelope":{"status":"ok","summary":{"state":"pending"}}}`), nil
								}
								<-r.Context().Done()
								return nil, r.Context().Err()
							}
							return deadlineTestResponse(`{"v":1,"id":"inspect-` + id + `","ok":true,"envelope":{"status":"ok","summary":{"state":"completed","response":{"v":1,"id":"` + id + `","operation_id":"` + id + `","ok":true,"scope":{"account_id":"account","instance_id":"instance","generation":1},"envelope":{"status":"ok"}}}}}`), nil
						default:
							t.Fatalf("unexpected method: %s", req.Method)
							return nil, errors.New("unexpected method")
						}
					}),
				}}
				m.runOperation(runtimeCommands["index_rebuild"], []string{"reembed"}, "", id)
				job := m.jobs[id]
				if healthReads != 1 || writes != 1 || job.RuntimeID != id || job.Scope == nil || *job.Scope != scope {
					t.Fatalf("request counts/identity health=%d writes=%d job=%+v", healthReads, writes, job)
				}
				if expire {
					if lookups != 2 || job.State != "unconfirmed" || !strings.Contains(job.Error, id) || len(job.Envelope) != 0 ||
						time.Since(start) != 30*time.Minute {
						t.Fatalf("parent deadline lost or false outcome: lookups=%d elapsed=%v job=%+v", lookups, time.Since(start), job)
					}
				} else if lookups != 1 || job.State != "done" || job.Error != "" || string(job.Envelope) != `{"status":"ok"}` {
					t.Fatalf("no completed scoped receipt: lookups=%d job=%+v", lookups, job)
				}
				reopened := &Manager{Root: root}
				if err := reopened.loadOperations(); err != nil {
					t.Fatal(err)
				}
				saved := reopened.jobs[id]
				if saved == nil || saved.State != job.State || saved.RuntimeID != id || saved.Scope == nil || *saved.Scope != scope {
					t.Fatalf("original identity was not persisted: %+v", saved)
				}
			})
		})
	}
}
