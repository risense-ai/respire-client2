package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type cappedOutput struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *cappedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, err := b.Buffer.Write(p)
	return n, err
}

func (m *Manager) hostExecutable(ctx context.Context) (string, error) {
	info, err := m.Installed()
	if err == nil {
		dir := m.runtimeDir(info.Version)
		if err = verifyRuntime(dir, info.SHA256); err != nil {
			return "", err
		}
		return filepath.Join(dir, runtimeBinary()), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	h, _, err := m.Health(ctx)
	if err != nil {
		return "", err
	}
	if err = verifyHostProcess(h); err != nil {
		return "", err
	}
	if err = verifyExecutable(h.Exe); err != nil {
		return "", err
	}
	return h.Exe, nil
}

func (m *Manager) hostOperation(ctx context.Context, args []string, secret string) (json.RawMessage, error) {
	executable, err := m.hostExecutable(ctx)
	if err != nil {
		return nil, err
	}
	if secret != "" {
		if strings.ContainsAny(secret, "\r\n\x00") || len(secret) > 4096 {
			return nil, errors.New("密钥格式无效")
		}
		args = append(args, "--key-stdin")
	}
	cmd := exec.CommandContext(ctx, executable, append([]string{"--json"}, args...)...)
	configureCommand(cmd)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if strings.EqualFold(key, "RSRS_BIN_DIR") || strings.EqualFold(key, "RSRS_CLIENT_ONLY") || strings.EqualFold(key, "RSRS_NO_AUTOSTART") {
			continue
		}
		cmd.Env = append(cmd.Env, item)
	}
	cmd.Env = append(cmd.Env, "RSRS_BIN_DIR="+filepath.Dir(executable), "RSRS_CLIENT_ONLY=0", "RSRS_NO_AUTOSTART=0")
	if secret != "" {
		cmd.Stdin = strings.NewReader(secret + "\n")
	}
	out, stderr := &cappedOutput{limit: 8 << 20}, &cappedOutput{limit: 64 << 10}
	cmd.Stdout = out
	cmd.Stderr = stderr
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("无法启动宿主操作: %w", err)
	}
	runErr := cmd.Wait()
	if out.overflow {
		return nil, &unconfirmedError{cause: errors.New("宿主回执超过 8 MiB，请刷新当前状态确认结果")}
	}
	raw := bytes.TrimSpace(out.Bytes())
	// Browser authorization can print progress before its final JSON envelope.
	if !json.Valid(raw) {
		lines := bytes.Split(raw, []byte("\n"))
		raw = bytes.TrimSpace(lines[len(lines)-1])
	}
	if json.Valid(raw) {
		if err = envelopeError(raw); err != nil {
			return raw, err
		}
		return raw, nil
	}
	if runErr != nil {
		return nil, &unconfirmedError{cause: fmt.Errorf("宿主操作未返回完整回执: %w (%s)", runErr, strings.TrimSpace(stderr.String()))}
	}
	return nil, &unconfirmedError{cause: errors.New("宿主未返回有效 JSON 回执，请刷新对应状态，未自动重试")}
}
