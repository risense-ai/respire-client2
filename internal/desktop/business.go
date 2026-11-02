package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"respire-client2/internal/host"
)

func (d *desktop) businessRoute(w http.ResponseWriter, r *http.Request) bool {
	var result any
	var err error
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/fonts":
		result, err = SystemFonts()
	case r.Method == "GET" && r.URL.Path == "/api/runtime-status":
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		result, err = d.m.RuntimeStatus(ctx)
	case r.Method == "GET" && r.URL.Path == "/api/operations":
		result = d.m.Operations()
	case r.Method == "GET" && r.URL.Path == "/api/plugins":
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		result = d.PluginStatuses(ctx)
	case r.Method == "GET" && r.URL.Path == "/api/inject-targets":
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		result, err = d.InjectTargetsView(ctx)
	case r.Method == "GET" && r.URL.Path == "/api/runtime-release":
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		_, metadata, releaseErr := d.m.LatestRuntime(ctx)
		err = releaseErr
		result = map[string]any{"version": metadata.Version, "channel": d.m.Preferences().RuntimeChannel}
	case r.Method == "GET" && r.URL.Path == "/api/model-variants":
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		result, err = d.m.ModelVariants(ctx, r.URL.Query().Get("model"))
	case r.Method == "GET" && r.URL.Path == "/api/model-profile-state":
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		defer cancel()
		catalog, catalogErr := d.m.ModelCatalogRPC(ctx)
		progress, progressErr := d.m.ModelProgress(ctx)
		result = map[string]any{"catalog": catalog, "progress": progress}
		err = catalogErr
		if err == nil {
			err = progressErr
		}
	case r.URL.Path == "/api/model-configure":
		if r.Method != "POST" || r.Header.Get("X-Respire-Client") != "1" {
			w.WriteHeader(http.StatusForbidden)
			err = errors.New("客户端请求被拒绝")
			break
		}
		var body struct {
			Model   string `json:"model"`
			Variant string `json:"variant"`
		}
		if err = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			break
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		err = d.m.ModelConfigure(ctx, body.Model, body.Variant)
		result = "模型档案已注册"
	case r.URL.Path == "/api/model-switch":
		if r.Method != "POST" || r.Header.Get("X-Respire-Client") != "1" {
			w.WriteHeader(http.StatusForbidden)
			err = errors.New("客户端请求被拒绝")
			break
		}
		var body struct {
			Model   string `json:"model"`
			Engine  string `json:"engine"`
			Variant string `json:"variant"`
		}
		if err = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			break
		}
		if body.Engine == "" {
			body.Engine = "cpu"
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		err = d.m.ModelSwitch(ctx, body.Model, body.Engine, body.Variant)
		result = "切换任务已提交"
	case r.URL.Path == "/api/model-download":
		if r.Method != "POST" || r.Header.Get("X-Respire-Client") != "1" {
			w.WriteHeader(http.StatusForbidden)
			err = errors.New("客户端请求被拒绝")
			break
		}
		var body struct {
			Model   string `json:"model"`
			Variant string `json:"variant"`
			Mirror  string `json:"mirror"`
		}
		if err = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			break
		}
		if body.Model == "" {
			body.Model = "m3"
		}
		model := body.Model
		variant := body.Variant
		go d.task("下载模型 "+variant, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
			defer cancel()
			return d.m.DownloadModel(ctx, model, variant, body.Mirror)
		})
		result = "下载已开始"
	case r.Method == "GET" && r.URL.Path == "/api/operation":
		result, err = d.m.Operation(r.URL.Query().Get("id"))
	case r.URL.Path == "/api/execute" || r.URL.Path == "/api/operation-recover" || r.URL.Path == "/api/operation-ack" || r.URL.Path == "/api/file-dialog" || r.URL.Path == "/api/open-dashboard" || r.URL.Path == "/api/open-main" || r.URL.Path == "/api/panel-size" || r.URL.Path == "/api/cli-language" || r.URL.Path == "/api/plugin-install" || r.URL.Path == "/api/inject-set" || r.URL.Path == "/api/inject-mode" || r.URL.Path == "/api/preferences-export" || r.URL.Path == "/api/preferences-import" || r.URL.Path == "/api/diagnostics-export" || r.URL.Path == "/api/open-model-dir" || r.URL.Path == "/api/window-minimise" || r.URL.Path == "/api/window-toggle-maximise" || r.URL.Path == "/api/window-close":
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
			Target    string          `json:"target"`
			Mirror    bool            `json:"mirror"`
			Path      string          `json:"path"`
			Height    float64         `json:"height"`
			Enable    bool            `json:"enable"`
			Mode      string          `json:"mode"`
			Scope     string          `json:"scope"`
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
		case "/api/open-main":
			if d.panel != nil {
				d.panel.Hide()
			}
			d.window.Show()
			d.window.Focus()
			result = "已打开主窗口"
		case "/api/window-minimise":
			d.window.Minimise()
			result = "已最小化"
		case "/api/window-toggle-maximise":
			d.window.ToggleMaximise()
			result = map[string]bool{"maximised": d.window.IsMaximised()}
		case "/api/window-close":
			d.window.Close()
			result = "已关闭"
		case "/api/panel-size":
			height := int(math.Round(input.Height))
			if height < 320 {
				height = 320
			}
			if height > 1100 {
				height = 1100
			}
			if d.panel != nil {
				d.panel.SetSize(360, height)
			}
			result = height
		case "/api/open-releases":
			url := map[string]string{
				"client":        "https://github.com/risense-ai/respire-client2/releases/latest",
				"client-mirror": "https://gh-proxy.com/https://github.com/risense-ai/respire-client2/releases/latest",
				"cli":           "https://github.com/risense-ai/respire-cli/releases/latest",
				"cli-mirror":    "https://gh-proxy.com/https://github.com/risense-ai/respire-cli/releases/latest",
			}[input.Target]
			if url == "" && strings.HasPrefix(input.Target, "plugin-") {
				repo := strings.TrimPrefix(input.Target, "plugin-")
				if strings.HasPrefix(repo, "mirror-") {
					repo = strings.TrimPrefix(repo, "mirror-")
					if _, ok := pluginByID(repo); ok {
						url = "https://gh-proxy.com/https://github.com/risense-ai/" + repo
					}
				} else {
					if _, ok := pluginByID(repo); ok {
						url = "https://github.com/risense-ai/" + repo
					}
				}
			}
			if url == "" {
				err = errors.New("未知的发布页")
				break
			}
			err = d.app.Browser.OpenURL(url)
			result = "已打开发布页"
		case "/api/cli-language":
			err = d.m.SetCLILanguage(input.Language)
			result = "CLI 语言已保存"
		case "/api/plugin-install":
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
			defer cancel()
			result, err = d.InstallPlugin(ctx, input.Target, input.Mirror)
		case "/api/open-model-dir":
			// Reveal a model file in the OS file manager; only paths inside the
			// model directory are accepted.
			dir := d.m.ModelDir()
			abs, absErr := filepath.Abs(input.Path)
			if absErr != nil {
				err = absErr
				break
			}
			rel, relErr := filepath.Rel(dir, abs)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				err = errors.New("路径不在模型目录内")
				break
			}
			var cmd *exec.Cmd
			switch runtime.GOOS {
			case "windows":
				cmd = exec.Command("explorer", "/select,"+abs)
			case "darwin":
				cmd = exec.Command("open", "-R", abs)
			default:
				cmd = exec.Command("xdg-open", filepath.Dir(abs))
			}
			if err = cmd.Start(); err == nil {
				result = "已打开文件位置"
			}
		case "/api/inject-set":
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
			defer cancel()
			result, err = d.InjectSet(ctx, input.ID, input.Enable, input.Mirror, input.Scope)
		case "/api/inject-mode":
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
			defer cancel()
			result, err = d.InjectSetMode(ctx, input.Mode)
		case "/api/preferences-export":
			err = writeSettingsExport(input.Path, d.m.Preferences())
			result = "设置已导出"
		case "/api/preferences-import":
			var p host.Preferences
			data, readErr := os.ReadFile(input.Path)
			if readErr != nil {
				err = readErr
				break
			}
			if len(data) > 64<<10 {
				err = errors.New("设置文件过大")
				break
			}
			var stored map[string]json.RawMessage
			if err = json.Unmarshal(data, &stored); err == nil {
				if err = json.Unmarshal(data, &p); err == nil {
					err = d.savePreferences(host.MigratePreferences(p, stored))
				}
			}
			result = "设置已导入"
		case "/api/diagnostics-export":
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			health, _, healthErr := d.m.Health(ctx)
			cancel()
			auto, _ := d.app.Autostart.IsEnabled()
			info, _ := d.m.Installed()
			d.mu.Lock()
			update := map[string]any{
				"state": d.app.Updater.State(), "latest": d.clientVersion, "source": d.clientSource,
				"notes": d.clientNotes, "error": d.clientError,
			}
			d.mu.Unlock()
			report := map[string]any{
				"generatedAt": time.Now().UTC(), "clientVersion": version, "platform": host.Platform(),
				"endpoint": d.m.Endpoint, "root": d.m.Root, "preferences": d.m.Preferences(),
				"autostart": auto, "task": d.m.TaskSnapshot(), "connected": healthErr == nil,
				"installed": info, "health": health, "healthError": errorText(healthErr),
				"clientUpdate": update, "operations": d.m.Operations(),
			}
			err = writeJSONFile(input.Path, report)
			result = "诊断已导出"
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

func writeSettingsExport(path string, value host.Preferences) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
