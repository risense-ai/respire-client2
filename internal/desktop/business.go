package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

func (d *desktop) businessRoute(w http.ResponseWriter, r *http.Request) bool {
	var result any
	var err error
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/runtime-status":
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		result, err = d.m.RuntimeStatus(ctx)
	case r.Method == "GET" && r.URL.Path == "/api/operations":
		result = d.m.Operations()
	case r.Method == "GET" && r.URL.Path == "/api/runtime-release":
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		_, metadata, releaseErr := d.m.LatestRuntime(ctx)
		err = releaseErr
		result = map[string]any{"version": metadata.Version, "channel": d.m.Preferences().RuntimeChannel}
	case r.Method == "GET" && r.URL.Path == "/api/operation":
		result, err = d.m.Operation(r.URL.Query().Get("id"))
	case r.URL.Path == "/api/execute" || r.URL.Path == "/api/operation-recover" || r.URL.Path == "/api/operation-ack" || r.URL.Path == "/api/file-dialog" || r.URL.Path == "/api/open-dashboard" || r.URL.Path == "/api/cli-language":
		if r.Method != "POST" || r.Header.Get("X-Respire-Client") != "1" || r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusForbidden)
			err = errors.New("客户端请求被拒绝")
			break
		}
		var input struct {
			Name      string          `json:"name"`
			ID        string          `json:"id"`
			Args      json.RawMessage `json:"args"`
			Confirmed bool            `json:"confirmed"`
			Secret    string          `json:"secret"`
			Kind      string          `json:"kind"`
			Language  string          `json:"language"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&input); err != nil {
			break
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			err = errors.New("请求必须是单个 JSON 对象")
			break
		}
		switch r.URL.Path {
		case "/api/execute":
			result, err = d.m.Submit(input.Name, input.ID, input.Args, input.Confirmed, input.Secret)
		case "/api/operation-recover":
			result, err = d.m.Recover(input.ID)
		case "/api/operation-ack":
			err = d.m.Acknowledge(input.ID)
			result = "已处理回执"
		case "/api/file-dialog":
			result, err = d.chooseFile(input.Kind)
		case "/api/open-dashboard":
			err = d.app.Browser.OpenURL("https://dash.rsrs.rs")
			result = "已打开 dashboard"
		case "/api/cli-language":
			err = d.m.SetCLILanguage(input.Language)
			result = "CLI 语言已保存"
		}
	default:
		return false
	}
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
	} else {
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}
	return true
}
