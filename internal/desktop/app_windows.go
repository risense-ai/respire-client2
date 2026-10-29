package desktop

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/risense-ai/respire-client2/internal/host"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/updater"
)

var version string

//go:embed frontend/*
var frontend embed.FS

//go:embed build/icon.png
var icon []byte

type desktop struct {
	app           *application.App
	window        *application.WebviewWindow
	m             *host.Manager
	mu            sync.Mutex
	updateMu      sync.Mutex
	clientVersion string
	clientError   string
	closing       chan struct{}
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
	files, err := fs.Sub(frontend, "frontend")
	if err != nil {
		log.Fatal(err)
	}
	static := http.FileServer(http.FS(files))
	d.app = application.New(application.Options{
		Name: "Respire", Description: "Respire Runtime desktop client", Icon: icon,
		Assets:  application.AssetOptions{Handler: d.handler(static)},
		Windows: application.WindowsOptions{DisableQuitOnLastWindowClosed: true, WebviewUserDataPath: filepath.Join(m.Root, "webview2")},
		SingleInstance: &application.SingleInstanceOptions{UniqueID: "ai.risense.respire.client2", OnSecondInstanceLaunch: func(application.SecondInstanceData) {
			if d.window != nil {
				d.window.Show()
				d.window.Focus()
			}
		}},
		OnShutdown:   func() { close(d.closing); d.app.Updater.StopPeriodicCheck() },
		ErrorHandler: func(err error) { log.Printf("desktop: %v", err) },
	})
	if err = d.initUpdater(); err != nil {
		log.Fatal(err)
	}
	d.window = d.app.Window.NewWithOptions(application.WebviewWindowOptions{Name: "main", Title: "Respire", Width: 1060, Height: 760, MinWidth: 800, MinHeight: 600, URL: "/", Hidden: hasArg("--tray")})
	d.window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) { e.Cancel(); d.window.Hide() })
	tray := d.app.SystemTray.New()
	tray.SetIcon(icon)
	tray.SetTooltip("Respire · Runtime 管理")
	menu := d.app.NewMenu()
	menu.Add("打开 Respire").OnClick(func(*application.Context) { d.window.Show(); d.window.Focus() })
	menu.Add("启动 Runtime").OnClick(func(*application.Context) { go d.task("启动 Runtime", m.Start) })
	menu.Add("停止 Runtime").OnClick(func(*application.Context) { go d.task("停止 Runtime", func() error { return m.Stop(false) }) })
	menu.Add("检查更新").OnClick(func(*application.Context) { d.window.Show(); go d.checkClient(false) })
	menu.AddSeparator()
	menu.Add("退出客户端（保留 Runtime）").OnClick(func(*application.Context) { d.app.Quit() })
	tray.SetMenu(menu)
	tray.OnClick(func() { d.window.Show(); d.window.Focus() })
	d.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) { go d.background() })
	if err = d.app.Run(); err != nil {
		log.Print(err)
	}
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
						return d.m.InstallRuntime(false)
					}
				}
			}
			return err
		})
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-d.closing:
			return
		case <-timer.C:
		}
		if d.m.Preferences().AutoUpdate {
			if d.app.Updater.State() != updater.StateReady {
				if err := d.checkClient(true); err != nil {
					log.Printf("client update: %v", err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			h, _, err := d.m.Health(ctx)
			cancel()
			if err == nil && d.m.Owns(h.Exe) {
				d.task("检查 Runtime 更新", func() error { return d.m.InstallRuntime(true) })
			}
		}
		timer.Reset(6 * time.Hour)
	}
}
