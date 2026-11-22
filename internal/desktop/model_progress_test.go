package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
