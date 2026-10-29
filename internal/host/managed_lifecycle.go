package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The CLI lifecycle owner must pause supervision before stopping its actor.
func (m *Manager) stopManaged(ctx context.Context, h HealthInfo) error {
	if h.InstallationRoot == "" || !filepath.IsAbs(h.InstallationRoot) {
		return errors.New("受监督 Runtime 未报告有效安装根目录")
	}
	if err := verifyHostProcess(h); err != nil {
		return err
	}
	if err := verifyExecutable(h.Exe); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, h.Exe, "--json", "runtime", "stop")
	configureCommand(cmd)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if strings.EqualFold(key, "RSRS_DATA_DIR") || strings.EqualFold(key, "RSRS_BIN_DIR") || strings.EqualFold(key, "RSRS_CLIENT_ONLY") || strings.EqualFold(key, "RSRS_NO_AUTOSTART") {
			continue
		}
		cmd.Env = append(cmd.Env, item)
	}
	cmd.Env = append(cmd.Env, "RSRS_DATA_DIR="+h.InstallationRoot, "RSRS_BIN_DIR="+filepath.Dir(h.Exe), "RSRS_CLIENT_ONLY=0", "RSRS_NO_AUTOSTART=0")
	output := &cappedOutput{limit: 1 << 20}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("通过 CLI 生命周期协调器停止 Runtime: %w (%s)", err, strings.TrimSpace(output.String()))
	}
	if output.overflow {
		return errors.New("Runtime 停止回执超出限制，请检查诊断状态")
	}
	_, _, err := m.Health(ctx)
	if IsUnavailable(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("CLI 停止后 RPC 端口仍在服务，未继续更新")
}
