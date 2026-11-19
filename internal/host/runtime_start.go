package host

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func (m *Manager) startDetached(ctx context.Context, info InstalledRuntime) (HealthInfo, error) {
	lock, err := m.lockRuntimeTakeover(ctx)
	if err != nil {
		return HealthInfo{}, err
	}
	defer lock.Close()
	selected, err := m.Installed()
	if err != nil {
		return HealthInfo{}, err
	}
	if selected != info {
		return HealthInfo{}, errors.New("Runtime 选用版本已改变")
	}
	if err := m.verifyInstalled(info); err != nil {
		return HealthInfo{}, err
	}
	h, _, err := m.Health(ctx)
	if err == nil {
		return h, m.verifySelectedHealth(h, info)
	}
	if !IsUnavailable(err) {
		return HealthInfo{}, err
	}
	endpoint, err := url.Parse(m.Endpoint)
	if err != nil {
		return HealthInfo{}, err
	}
	if endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" || endpoint.Port() == "" {
		return HealthInfo{}, errors.New("Runtime 启动要求本机环回端口")
	}
	if err := m.archiveStoppedEndpoint(ctx, endpoint.Host); err != nil {
		return HealthInfo{}, err
	}
	executable := filepath.Join(m.runtimeDir(info.Version), runtimeBinary())
	log, err := os.CreateTemp(m.Root, "runtime-start-*.log")
	if err != nil {
		return HealthInfo{}, err
	}
	defer log.Close()
	cmd := exec.Command(executable, "serve", "--host", endpoint.Hostname(),
		"--port", endpoint.Port(), "--data-dir", m.DataRoot)
	cmd.Dir = filepath.Dir(executable)
	cmd.Stdout, cmd.Stderr = log, log
	for _, variable := range os.Environ() {
		key, _, _ := strings.Cut(variable, "=")
		switch strings.ToUpper(key) {
		case "RSRS_RUNTIME_BOOT_TOKEN", "RSRS_SUPER", "RSRS_PASS", "RSRS_SECRET", "RSRS_SECRET_KEY",
			"ONEMEMORY_SUPER", "ONEMEMORY_PASS", "ONEMEMORY_SECRET", "ONEMEMORY_SECRET_KEY",
			"RESPIRE_SUPER", "RESPIRE_PASS", "RESPIRE_SECRET", "RESPIRE_SECRET_KEY":
			continue
		}
		cmd.Env = append(cmd.Env, variable)
	}
	configureRuntimeCommand(cmd)
	if err := cmd.Start(); err != nil {
		return HealthInfo{}, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		h, _, err := m.Health(ctx)
		if err == nil {
			if h.PID != uint32(cmd.Process.Pid) {
				return HealthInfo{}, errors.New("Runtime 端口报告了其他进程")
			}
			if h.StartupComplete {
				if h.ImageSHA256 != info.SHA256 {
					return HealthInfo{}, errors.New("运行中的 Runtime 与校验后的映像不一致")
				}
				return h, m.verifySelectedHealth(h, info)
			}
		} else if !IsUnavailable(err) && ctx.Err() == nil {
			return HealthInfo{}, err
		}
		select {
		case err := <-exited:
			return HealthInfo{}, fmt.Errorf("Runtime 启动前退出 (%v)，见 %s", err, log.Name())
		case <-ctx.Done():
			return HealthInfo{}, fmt.Errorf("Runtime 启动结果未确认，见 %s: %w", log.Name(), ctx.Err())
		case <-ticker.C:
		}
	}
}
