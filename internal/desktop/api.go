package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"respire-client2/internal/host"
)

func (d *desktop) handler(static http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-cache")
			static.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if d.businessRoute(w, r) {
			return
		}
		var result any
		var err error
		if r.Method == "GET" && r.URL.Path == "/api/state" {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			h, body, healthErr := d.m.Health(ctx)
			cancel()
			auto, autoErr := d.app.Autostart.IsEnabled()
			info, readErr := d.m.Installed()
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				err = readErr
			}
			var external *host.ExternalRuntime
			if errors.Is(readErr, os.ErrNotExist) {
				ctx, cancel = context.WithTimeout(r.Context(), 3*time.Second)
				externalItem, externalErr := d.m.External(ctx)
				cancel()
				if externalErr == nil {
					external = &externalItem
				}
			}
			d.mu.Lock()
			latest, clientErr, theme := d.clientVersion, d.clientError, d.resolvedTheme
			d.mu.Unlock()
			result = map[string]any{
				"version": version, "platform": host.Platform(), "endpoint": d.m.Endpoint, "root": d.m.Root,
				"theme":       theme,
				"preferences": d.m.Preferences(), "autostart": auto,
				"task": d.m.TaskSnapshot(), "connected": healthErr == nil,
				"managed":   healthErr == nil && d.m.Owns(h.Exe),
				"installed": info, "externalRuntime": external, "health": body,
				"clientUpdate": map[string]any{"state": d.app.Updater.State(), "latest": latest, "error": clientErr},
			}
			if healthErr != nil {
				result.(map[string]any)["connectionError"] = healthErr.Error()
			}
			if autoErr != nil {
				result.(map[string]any)["autostartError"] = autoErr.Error()
			}
		} else if r.Method == "POST" && r.Header.Get("X-Respire-Client") == "1" && strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			var input struct {
				Enabled        bool   `json:"enabled"`
				AutoRuntime    bool   `json:"autoRuntime"`
				AutoUpdate     bool   `json:"autoUpdate"`
				Command        string `json:"command"`
				RuntimeChannel string `json:"runtimeChannel"`
				ClientChannel  string `json:"clientChannel"`
				Layout         string `json:"layout"`
				Theme          string `json:"theme"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			decoder.DisallowUnknownFields()
			err = decoder.Decode(&input)
			if err == nil {
				var extra any
				if trailing := decoder.Decode(&extra); trailing != io.EOF {
					err = errors.New("desktop action must contain one JSON object")
				}
			}
			if err == nil {
				switch r.URL.Path {
				case "/api/start":
					go d.task("启动 Runtime", d.m.Start)
					result = "已请求启动"
				case "/api/stop":
					go d.task("停止 Runtime", func() error { return d.m.Stop(false) })
					result = "已请求停止"
				case "/api/runtime-update":
					go d.task("安装 / 更新 Runtime", func() error { return d.m.InstallRuntime(false) })
					result = "已请求安装 / 更新"
				case "/api/client-check":
					go d.checkClient(false)
					result = "已请求检查"
				case "/api/client-download":
					go d.checkClient(true)
					result = "已请求下载"
				case "/api/client-restart":
					if d.m.HasActiveOperations() {
						err = errors.New("仍有业务操作在执行，请等待完成后应用客户端更新")
					} else {
						err = d.app.Updater.Restart(r.Context())
					}
					result = "正在重启"
				case "/api/autostart":
					if input.Enabled {
						err = d.app.Autostart.EnableWithOptions(application.AutostartOptions{Arguments: []string{"--tray"}})
					} else {
						err = d.app.Autostart.Disable()
					}
					result = input.Enabled
				case "/api/preferences":
					if input.Theme == "" {
						input.Theme = d.m.Preferences().Theme
					}
					err = d.savePreferences(host.Preferences{AutoRuntime: input.AutoRuntime, AutoUpdate: input.AutoUpdate, RuntimeChannel: input.RuntimeChannel, ClientChannel: input.ClientChannel, Layout: input.Layout, Theme: input.Theme})
					result = "设置已保存"
				case "/api/rpc":
					result, err = d.m.RPC(input.Command)
				default:
					w.WriteHeader(404)
					err = errors.New("unknown desktop action")
				}
			}
		} else {
			w.WriteHeader(403)
			err = errors.New("desktop API request rejected")
		}
		if err != nil {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	})
}
