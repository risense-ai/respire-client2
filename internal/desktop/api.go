package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"respire-client2/internal/host"
)

// actionInput is the strict shape of a POST /api/* action body: fields are
// the union of what the action branches consume. Preference keys only pass
// this shape check — patchPreferences applies them — so the struct must
// carry one JSON field per preferencePatchKeys entry.
type actionInput struct {
	Enabled        bool   `json:"enabled"`
	AutoRuntime    bool   `json:"autoRuntime"`
	AutoUpdate     bool   `json:"autoUpdate"`
	Command        string `json:"command"`
	RuntimeChannel string `json:"runtimeChannel"`
	ClientChannel  string `json:"clientChannel"`
	Layout         string `json:"layout"`
	Theme          string `json:"theme"`
	Language       string `json:"language"`
	ProxyMode      string `json:"proxyMode"`

	ClientUpdateInterval  string `json:"clientUpdateInterval"`
	RuntimeUpdateInterval string `json:"runtimeUpdateInterval"`
	ClientAutoUpdate      bool   `json:"clientAutoUpdate"`
	ClientAutoRestart     bool   `json:"clientAutoRestart"`
	RuntimeAutoUpdate     bool   `json:"runtimeAutoUpdate"`
	RuntimeAutoRestart    bool   `json:"runtimeAutoRestart"`
	SidebarWidth          int    `json:"sidebarWidth"`
	MemoryIndexWidth      int    `json:"memoryIndexWidth"`

	OnboardingVersion    int    `json:"onboardingVersion"`
	OnboardingCompleted  bool   `json:"onboardingCompleted"`
	OnboardingStep       string `json:"onboardingStep"`
	OnboardingSyncChoice string `json:"onboardingSyncChoice"`
}

// decodeActionBody strictly parses one action body: unknown fields are
// rejected and trailing JSON content is an error.
func decodeActionBody(body []byte) (actionInput, error) {
	var input actionInput
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&input)
	if err == nil {
		var extra any
		if trailing := decoder.Decode(&extra); trailing != io.EOF {
			err = errors.New("desktop action must contain one JSON object")
		}
	}
	return input, err
}

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
			latest, notes, source, clientErr, theme, updateCancel := d.clientVersion, d.clientNotes, d.clientSource, d.clientError, d.resolvedTheme, d.updateCancel
			d.mu.Unlock()
			result = map[string]any{
				"version": version, "platform": host.Platform(), "endpoint": d.m.Endpoint, "root": d.m.Root,
				"theme":       theme,
				"preferences": d.m.Preferences(), "autostart": auto,
				"proxy": d.m.ProxyInfo(),
				"task":  d.m.TaskSnapshot(), "connected": healthErr == nil,
				"managed":   healthErr == nil && d.m.Owns(h.Exe),
				"installed": info, "externalRuntime": external, "health": body,
				"clientUpdate": map[string]any{
					"state": d.app.Updater.State(), "latest": latest, "notes": notes,
					"source": source, "error": clientErr, "cancellable": updateCancel != nil,
				},
			}
			if healthErr != nil {
				result.(map[string]any)["connectionError"] = healthErr.Error()
			} else {
				// Locally readable inference selection (engine, weight file);
				// profile models override it client-side via model.catalog.
				result.(map[string]any)["engine"] = d.m.EngineSnapshot(h.InstallationRoot, h.Exe)
			}
			if autoErr != nil {
				result.(map[string]any)["autostartError"] = autoErr.Error()
			}
		} else if r.Method == "POST" && r.Header.Get("X-Respire-Client") == "1" && strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			// Preference keys are only shape-checked here; patchPreferences
			// applies them, so actionInput must carry one JSON field per
			// preferencePatchKeys entry or valid patches are rejected.
			var input actionInput
			// Buffered so /api/preferences can re-read the same request as a
			// field map and patch only the keys the sender included.
			body, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
			if readErr != nil {
				err = readErr
			} else {
				input, err = decodeActionBody(body)
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
					go d.task("安装 / 更新 Runtime", func() error { return d.m.InstallRuntime(false, d.m.Preferences().RuntimeAutoRestart) })
					result = "已请求安装 / 更新"
				case "/api/runtime-restart":
					// Restart & apply: gracefully stop the running instance and
					// start the newly installed version the pointer now names.
					go d.task("重启并应用 Runtime", func() error {
						if err := d.m.Stop(true); err != nil {
							return err
						}
						if err := d.m.Start(); err != nil {
							return err
						}
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						h, _, healthErr := d.m.Health(ctx)
						cancel()
						if healthErr != nil {
							return healthErr
						}
						if !d.m.Owns(h.Exe) {
							return errors.New("Runtime 已启动，但不是客户端管理的版本")
						}
						return nil
					})
					result = "正在重启 Runtime"
				case "/api/client-check":
					go d.checkClient(false)
					result = "已请求检查"
				case "/api/client-download":
					go d.checkClient(true)
					result = "已请求下载"
				case "/api/client-cancel":
					err = d.cancelUpdate()
					result = "已请求取消"
				case "/api/client-restart":
					if blocked := d.clientRestartBlocked(); blocked != nil {
						err = blocked
					} else {
						go func() {
							if restartErr := d.restartClient(); restartErr != nil {
								log.Printf("client update restart: %v", restartErr)
							}
						}()
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
					// Patch the stored preferences with only the keys present
					// in this request; rebuilding the struct here previously
					// dropped fields the forms never sent (runtimeAutoRestart).
					var prefs host.Preferences
					prefs, patchErr := patchPreferences(d.m.Preferences(), body)
					if patchErr != nil {
						err = patchErr
					} else {
						err = d.savePreferences(prefs)
					}
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
