package host

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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
	if _, err := m.hostOperation(ctx, []string{"runtime", "stop"}, "", "runtime.stop", ""); err != nil {
		return fmt.Errorf("通过 CLI 生命周期协调器停止 Runtime: %w", err)
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
