package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// The caller holds takeover. Only the current listener and library locks matter.
func (m *Manager) archiveStoppedEndpoint(ctx context.Context, address string) error {
	path := filepath.Join(m.DataRoot, "runtime", "endpoint.json")
	original, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !original.Mode().IsRegular() {
		return errors.New("旧 Runtime 端点文件无效")
	}
	installation, err := lockRuntimeDatabase(ctx, m.DataRoot, 0)
	if err != nil {
		return fmt.Errorf("旧 Runtime 安装仍占用或无法验证数据库锁: %w", err)
	}
	defer installation.Close()

	profile := m.DataRoot
	config, err := os.ReadFile(filepath.Join(m.DataRoot, "client.json"))
	if err == nil {
		var selected struct {
			Directory string `json:"data_dir"`
		}
		if err := json.Unmarshal(config, &selected); err != nil {
			return errors.New("无法解析当前 Runtime 账户目录")
		}
		if selected.Directory != "" {
			if !filepath.IsAbs(selected.Directory) {
				return errors.New("Runtime 账户目录必须是绝对路径")
			}
			profile, err = filepath.EvalSymlinks(selected.Directory)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(m.DataRoot, profile)
			if err != nil || !filepath.IsLocal(relative) {
				return errors.New("Runtime 账户目录不属于当前安装")
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !samePath(profile, m.DataRoot) {
		account, err := lockRuntimeDatabase(ctx, profile, 0)
		if err != nil {
			return fmt.Errorf("旧 Runtime 账户仍占用或无法验证数据库锁: %w", err)
		}
		defer account.Close()
	}
	if err := requireClosedRuntimePort(ctx, address); err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(current, original) || !current.ModTime().Equal(original.ModTime()) || current.Size() != original.Size() {
		return errors.New("旧 Runtime 端点在归档前已改变")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	archive := filepath.Join(filepath.Dir(path), "endpoint.stopped-"+hex.EncodeToString(nonce[:])+".json")
	if err := os.Rename(path, archive); err != nil {
		return fmt.Errorf("归档已停止 Runtime 的端点: %w", err)
	}
	return nil
}

func requireClosedRuntimePort(ctx context.Context, address string) error {
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if IsUnavailable(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("无法确认旧 Runtime 监听已关闭 (%s): %w", address, err)
	}
	err = connection.Close()
	return errors.Join(fmt.Errorf("旧 Runtime 仍在监听 %s", address), err)
}
