package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"respire-client2/internal/host"
)

func TestModelProgressUsesOnlyLocalDownloadSlot(t *testing.T) {
	m := &host.Manager{}
	if !m.StartTaskCancellable("下载模型 baseline", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}) {
		t.Fatal("could not start isolated fixture")
	}
	defer m.CancelTask(false)
	d := &desktop{m: m}
	// No app, local HTTP client, or files: any fallback to full state/Health
	// would fail rather than replacing the download-only endpoint silently.
	if err := m.RunTask("更新 Runtime", func() error {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/models/progress", nil)
		if !d.businessRoute(recorder, request) {
			t.Fatal("endpoint is not routed")
		}
		var body struct {
			OK     bool `json:"ok"`
			Result struct {
				Task host.TaskState `json:"task"`
			} `json:"result"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || !body.OK || !body.Result.Task.Busy ||
			body.Result.Task.Stage != "下载模型 baseline" {
			t.Fatalf("wrong local progress: %s, %v", recorder.Body.String(), err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestModelDownloadBackgroundRouteKeepsItsOwnCancellation(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer origin.Close()
	m := &host.Manager{DataRoot: t.TempDir()}
	defer m.CancelTask(false)
	d := &desktop{m: m}
	post := func(ctx context.Context, path, body string) {
		t.Helper()
		request := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("X-Respire-Client", "1")
		response := httptest.NewRecorder()
		if !d.businessRoute(response, request) || response.Code != http.StatusOK {
			t.Fatalf("%s failed: %d %s", path, response.Code, response.Body.String())
		}
	}
	guard, cancelGuard := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancelGuard()
	requestCtx, finishPost := context.WithCancel(guard)
	post(requestCtx, "/api/model-download", `{"model":"m3","variant":"tokenizer","mirror":"`+origin.URL+`"}`)
	finishPost()
	select {
	case <-started:
	case <-guard.Done():
		t.Fatal("background transfer failed to outlive the POST")
	}
	if snapshot := m.ModelDownloadSnapshot(); !snapshot.Busy {
		t.Fatalf("task already stopped: %+v", snapshot)
	}
	post(guard, "/api/model-download-cancel", `{"pause":true}`)
	select {
	case <-stopped:
	case <-guard.Done():
		t.Fatal("paused download did not cancel its network request")
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for m.ModelDownloadSnapshot().Busy {
		select {
		case <-tick.C:
		case <-guard.Done():
			t.Fatal("task did not quiesce")
		}
	}
	if snapshot := m.ModelDownloadSnapshot(); snapshot.Stage != "已暂停" || snapshot.Error != "" {
		t.Fatalf("user pause lost its meaning: %+v", snapshot)
	}
}
