package desktop

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"respire-client2/internal/host"
)

var version string

//go:embed frontend/*
var frontend embed.FS

//go:embed build/icon.png
var icon []byte

//go:embed build/icon-light.png
var lightIcon []byte

type desktop struct {
	app           *application.App
	window        *application.WebviewWindow
	panel         *application.WebviewWindow
	m             *host.Manager
	mu            sync.Mutex
	updateMu      sync.Mutex
	clientVersion string
	clientNotes   string
	clientSource  string
	clientError   string
	updateCancel  context.CancelFunc
	closing       chan struct{}
	restarting    bool
	tray          *application.SystemTray
	notifier      *notifications.NotificationService
	native        nativeAppearance
	resolvedTheme string
}

func Run(m *host.Manager, buildVersion string) {
	version = buildVersion
	f, err := os.OpenFile(filepath.Join(m.Root, "client.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	log.SetOutput(f)
	d := &desktop{m: m, closing: make(chan struct{})}
	defer d.native.close()
	go ensureShellIdentity()
	files, err := fs.Sub(frontend, "frontend")
	if err != nil {
		log.Fatal(err)
	}
	static := http.FileServer(http.FS(files))
	notifier := notifications.New()
	singleInstance := &application.SingleInstanceOptions{UniqueID: "ai.risense.respire.client2", OnSecondInstanceLaunch: func(application.SecondInstanceData) {
		if d.window != nil {
			d.window.Show()
			d.window.Focus()
		}
	}}
	d.app = application.New(application.Options{
		Name: "Respire", Description: "Respire Runtime desktop client", Icon: lightIcon,
		Assets: application.AssetOptions{Handler: d.handler(static)},
		Services: []application.Service{
			application.NewService(notifier),
		},
		Windows:        application.WindowsOptions{DisableQuitOnLastWindowClosed: true, WebviewUserDataPath: filepath.Join(m.Root, "webview2")},
		Linux:          application.LinuxOptions{DisableQuitOnLastWindowClosed: true, ApplicationID: "ai.risense.respire.client2"},
		SingleInstance: singleInstance,
		OnShutdown:     func() { close(d.closing); d.app.Updater.StopPeriodicCheck() },
		ErrorHandler:   func(err error) { log.Printf("desktop: %v", err) },
	})
	if err = d.initUpdater(); err != nil {
		log.Fatal(err)
	}
	// Set the initial canvas through options; WebView2 may not exist in startup events.
	background := application.NewRGB(248, 246, 242)
	theme := m.Preferences().Theme
	if theme == "dark" || theme == "system" && d.app.Env.IsDarkMode() {
		background = application.NewRGB(36, 37, 31)
	}
	// Frameless: the webview draws its own window controls and drag region.
	d.window = d.app.Window.NewWithOptions(application.WebviewWindowOptions{Name: "main", Title: "Respire", Width: 1180, Height: 800, MinWidth: 800, MinHeight: 600, Frameless: true, URL: "/workspace.html", Hidden: hasArg("--tray"), DefaultContextMenuDisabled: true, BackgroundColour: background})
	// Tray quick panel: a frameless popup anchored to the tray icon; it hides
	// itself on focus loss so a tray click toggles it like a flyout.
	d.panel = d.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name: "quickpanel", Title: "Respire", Width: 360, Height: 540,
		URL: "/quickpanel.html", Hidden: true, Frameless: true, AlwaysOnTop: true,
		DisableResize: true, HideOnFocusLost: true, HideOnEscape: true,
		DefaultContextMenuDisabled: true,
		Windows:                    application.WindowsWindow{HiddenOnTaskbar: true},
		BackgroundColour:           background,
	})
	d.notifier = notifier
	for name, action := range map[string]func(){
		"CmdOrCtrl+Shift+R": func() { d.window.Show(); d.window.Focus() },
		"CmdOrCtrl+Shift+H": func() { d.window.Hide() },
	} {
		name, action := name, action
		if err := d.app.GlobalShortcut.Register(name, action); err != nil {
			log.Printf("global shortcut %s: %v", name, err)
		}
	}
	d.app.Event.On(updater.EventUpdateReady, func(*application.CustomEvent) {
		d.updateNotification()
	})
	d.app.Event.On(updater.EventError, func(event *application.CustomEvent) {
		if info, ok := event.Data.(updater.ErrorInfo); ok {
			d.updateFailureNotification(info.Message)
		}
	})
	d.window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) { e.Cancel(); d.window.Hide() })
	d.window.OnWindowEvent(events.Common.WindowShow, func(*application.WindowEvent) { d.applyAppearance() })
	tray := d.app.SystemTray.New()
	d.tray = tray
	tray.SetIcon(lightIcon)
	tray.SetTooltip("Respire · 点击查看状态")
	menu := d.app.NewMenu()
	menu.Add("打开 Respire").OnClick(func(*application.Context) { d.window.Show(); d.window.Focus() })
	menu.Add("启动 Runtime").OnClick(func(*application.Context) { go d.task("启动 Runtime", m.Start) })
	menu.Add("停止 Runtime").OnClick(func(*application.Context) { go d.task("停止 Runtime", func() error { return m.Stop(false) }) })
	menu.Add("检查更新").OnClick(func(*application.Context) { d.window.Show(); go d.checkClient(false) })
	menu.AddSeparator()
	menu.Add("退出客户端（保留 Runtime）").OnClick(func(*application.Context) { d.app.Quit() })
	tray.SetMenu(menu)
	// With an attached window and a menu, tray defaults apply: left click
	// toggles the quick panel next to the icon, right click opens the menu.
	tray.AttachWindow(d.panel)
	tray.WindowOffset(8)
	// Double click jumps straight to the main window, panel tucked away.
	tray.OnDoubleClick(func() {
		if d.panel != nil {
			d.panel.Hide()
		}
		d.window.Show()
		d.window.Focus()
	})
	d.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) { d.applyAppearance(); go d.background() })
	d.app.Event.OnApplicationEvent(events.Common.ThemeChanged, func(*application.ApplicationEvent) { d.applyAppearance() })
	if err = d.app.Run(); err != nil {
		log.Print(err)
	}
}

func (d *desktop) notifySystem(title, body string) {
	if d.notifier == nil {
		return
	}
	go func() {
		if err := d.notifier.SendNotification(notifications.NotificationOptions{
			ID: "respire-update-" + fmt.Sprint(time.Now().UnixNano()), Title: title, Body: body,
			ThreadID: "updates", InterruptionLevel: notifications.InterruptionLevelActive,
		}); err != nil {
			log.Printf("system notification: %v", err)
		}
	}()
}

func (d *desktop) updateNotification() {
	if d.m.Preferences().Language == "zh" {
		d.notifySystem("Respire 更新已就绪", "新版本已下载完成。请到更新页重启应用。")
		return
	}
	d.notifySystem("Respire update ready", "The new version is downloaded. Restart from Updates to apply it.")
}

func (d *desktop) updateFailureNotification(message string) {
	if d.m.Preferences().Language == "zh" {
		d.notifySystem("Respire 更新失败", message)
		return
	}
	d.notifySystem("Respire update failed", message)
}

func hasArg(arg string) bool {
	for _, a := range os.Args[1:] {
		if a == arg {
			return true
		}
	}
	return false
}

func (d *desktop) task(stage string, action func() error) {
	if err := d.m.RunTask(stage, action); err != nil {
		log.Printf("%s: %v", stage, err)
	}
}

func (d *desktop) restartClient() error {
	d.mu.Lock()
	d.restarting = true
	d.mu.Unlock()
	err := d.app.Updater.Restart(context.Background())
	if err != nil {
		d.mu.Lock()
		d.restarting = false
		d.mu.Unlock()
	}
	return err
}

// updateInterval resolves the configured check cadence; unknown values fall
// back to the historical six-hour default.
func updateInterval(value string) time.Duration {
	switch value {
	case "5m":
		return 5 * time.Minute
	case "30m":
		return 30 * time.Minute
	case "1h":
		return time.Hour
	case "24h":
		return 24 * time.Hour
	default:
		return 6 * time.Hour
	}
}

func (d *desktop) background() {
	if d.m.Preferences().AutoRuntime {
		d.task("连接 Runtime", func() error {
			err := d.m.Start()
			if err != nil {
				if _, readErr := d.m.Installed(); errors.Is(readErr, os.ErrNotExist) {
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					_, _, healthErr := d.m.Health(ctx)
					cancel()
					if host.IsUnavailable(healthErr) {
						return d.m.InstallRuntime(false, d.m.Preferences().RuntimeAutoRestart)
					}
				}
			}
			return err
		})
	}
	// Client and Runtime update on their own cadences (update page); a staged
	// client update re-checks every minute so it applies once operations end.
	var lastClient, lastRuntime time.Time
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-d.closing:
			return
		case <-timer.C:
		}
		now := time.Now()
		prefs := d.m.Preferences()
		if prefs.ClientAutoUpdate {
			if d.app.Updater.State() == updater.StateReady {
				if prefs.ClientAutoRestart && now.Sub(lastClient) >= time.Minute {
					lastClient = now
					if d.canRestartClient() {
						if err := d.restartClient(); err != nil {
							log.Printf("client update restart: %v", err)
						}
					}
				}
			} else if lastClient.IsZero() || now.Sub(lastClient) >= updateInterval(prefs.ClientUpdateInterval) {
				lastClient = now
				if err := d.checkClient(true); err != nil {
					log.Printf("client update: %v", err)
				}
			}
		}
		if prefs.RuntimeAutoUpdate &&
			(lastRuntime.IsZero() || now.Sub(lastRuntime) >= updateInterval(prefs.RuntimeUpdateInterval)) {
			lastRuntime = now
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			h, _, err := d.m.Health(ctx)
			cancel()
			if err == nil && d.m.Owns(h.Exe) {
				d.task("检查 Runtime 更新", func() error {
					// The restart switch decides whether the running instance
					// is gracefully rotated onto the new version directory.
					restart := d.m.Preferences().RuntimeAutoRestart
					if err := d.m.InstallRuntime(true, restart); err != nil {
						return d.m.InstallRuntime(false, restart)
					}
					return nil
				})
			}
		}
		delay := 30 * time.Second
		if d.app.Updater.State() == updater.StateReady {
			delay = time.Minute
		}
		timer.Reset(delay)
	}
}
