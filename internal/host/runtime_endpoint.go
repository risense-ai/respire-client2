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
			if (err != nil || !filepath.IsLocal(relative)) && !deviceConfigDefaultProfileLayout(m.DataRoot) {
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
	return archiveRuntimeRecord(path, original)
}

// Call with takeover and the affected library locks held.
func archiveRuntimeRecord(path string, original os.FileInfo) error {
	if original.IsDir() {
		return fmt.Errorf("Runtime 记录路径是目录 (%s)", filepath.Base(path))
	}
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(current, original) || !current.ModTime().Equal(original.ModTime()) || current.Size() != original.Size() {
		return fmt.Errorf("Runtime 记录在归档前已改变 (%s)", filepath.Base(path))
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	ext := filepath.Ext(path)
	archive := path[:len(path)-len(ext)] + ".stopped-" + hex.EncodeToString(nonce[:]) + ext
	if err := os.Rename(path, archive); err != nil {
		return fmt.Errorf("归档历史 Runtime 记录 (%s): %w", filepath.Base(path), err)
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
