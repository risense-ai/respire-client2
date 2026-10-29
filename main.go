package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"respire-client2/internal/desktop"
	"respire-client2/internal/host"
)

var version = "0.2.0-dev.0"

func main() {
	updater.HandleHelperMode()
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		json.NewEncoder(os.Stdout).Encode(map[string]string{"version": version, "platform": host.Platform()})
		return
	}
	m, err := host.New()
	if err != nil {
		log.Fatal(err)
	}
	if len(os.Args) > 1 && os.Args[1] == "--headless" {
		headless(m)
		return
	}
	desktop.Run(m, version)
}

func headless(m *host.Manager) {
	var result any
	var err error
	action := "status"
	if len(os.Args) > 2 {
		action = os.Args[2]
	}
	switch action {
	case "fonts":
		result, err = desktop.SystemFonts()
	case "status":
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, result, err = m.Health(ctx)
	case "start":
		err = m.RunTask("启动 Runtime", m.Start)
	case "stop":
		err = m.RunTask("停止 Runtime", func() error { return m.Stop(false) })
	case "install-runtime":
		err = m.RunTask("安装 Runtime", func() error { return m.InstallRuntime(false) })
	case "runtime-release":
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, result, err = m.LatestRuntime(ctx)
	case "rpc-status":
		result, err = m.RPC("status")
	default:
		err = errors.New("supported actions: status, start, stop, install-runtime, runtime-release, rpc-status, fonts")
	}
	output := map[string]any{"ok": err == nil, "result": result, "task": m.TaskSnapshot()}
	if err != nil {
		output["error"] = err.Error()
	}
	if encodeErr := json.NewEncoder(os.Stdout).Encode(output); encodeErr != nil {
		os.Exit(1)
	}
	if err != nil {
		os.Exit(1)
	}
}
