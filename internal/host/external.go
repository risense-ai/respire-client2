package host

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type ExternalRuntime struct {
	Version string `json:"version"`
	Path    string `json:"path"`
}

// npm and manual installations share the data-root bin directory but are not client-managed.
func (m *Manager) External(ctx context.Context) (ExternalRuntime, error) {
	path := filepath.Join(filepath.Dir(m.Root), "bin", runtimeBinary())
	if _, err := os.Stat(path); err != nil {
		return ExternalRuntime{}, err
	}
	if err := verifyExecutable(path); err != nil {
		return ExternalRuntime{}, err
	}
	cmd := exec.CommandContext(ctx, path, "--version")
	configureCommand(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ExternalRuntime{}, fmt.Errorf("读取本机 CLI 版本失败: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[0] != "rsrs" || !validVersion(fields[1]) {
		return ExternalRuntime{}, fmt.Errorf("本机 CLI 版本无效: %s", strings.TrimSpace(string(out)))
	}
	return ExternalRuntime{Version: fields[1], Path: path}, nil
}
