package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (m *Manager) desktopSpaceCreate(ctx context.Context, name string) (json.RawMessage, error) {
	name = strings.TrimSpace(name)
	if !desktopSpaceName.MatchString(name) || name == "main" {
		return nil, errors.New("新空间名称无效或为保留名称 main")
	}
	profile := filepath.Join(m.DataRoot, "accounts", name)
	return m.changeDeviceConfig(ctx, deviceConfigIntent{DataDir: &profile}, nil, configTargetNewSpace)
}

type freshSpaceDirectory struct {
	path     string
	original os.FileInfo
}

// The caller holds takeover. Mkdir is the only admission for a new space.
func createFreshSpaceDirectory(path string) (*freshSpaceDirectory, error) {
	parent := filepath.Dir(path)
	if err := os.Mkdir(parent, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if err := checkRuntimeAccountDirectory(parent); err != nil {
		return nil, err
	}
	if err := os.Mkdir(path, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("空间 %q 已存在，请使用其他名称或切换到现有空间", filepath.Base(path))
		}
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	space := &freshSpaceDirectory{path, info}
	if err := space.verifyEmpty(); err != nil {
		return nil, err
	}
	return space, nil
}

func (s freshSpaceDirectory) verifyIdentity() error {
	if err := checkRuntimeAccountDirectory(s.path); err != nil {
		return err
	}
	info, err := os.Lstat(s.path)
	if err != nil || !os.SameFile(s.original, info) {
		return errors.Join(errors.New("新空间目录身份已改变"), err)
	}
	return nil
}

func (s freshSpaceDirectory) verifyEmpty() error {
	if err := s.verifyIdentity(); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "lock.db" {
			return errors.New("新空间目录出现额外文件，保留目录以便核对")
		}
		if _, err := profileFile(filepath.Join(s.path, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (s freshSpaceDirectory) removeEmpty() error {
	if err := s.verifyEmpty(); err != nil {
		return err
	}
	// Remove refuses a nonempty directory, including one with a Runtime lock file.
	return os.Remove(s.path)
}

func (s freshSpaceDirectory) publishOwner() error {
	if err := s.verifyEmpty(); err != nil {
		return err
	}
	data, err := json.Marshal(map[string]string{"name": filepath.Base(s.path)})
	if err != nil {
		return err
	}
	return writeRuntimeAccountRecord(filepath.Join(s.path, "space_owner.json"), data, nil)
}

func (s freshSpaceDirectory) result() (json.RawMessage, error) {
	name := filepath.Base(s.path)
	const hint = "空间已创建并切换，Runtime 保持停止。请先注册该空间的账户，再创建邀请码。"
	return json.Marshal(map[string]any{
		"command": "space", "status": "ok",
		"summary": map[string]string{"action": "create", "name": name},
		"items":   []map[string]string{{"name": "space", "status": "ok", "value": name}},
		"details": map[string]string{"name": name, "dir": s.path, "hint": hint},
		"actions": []string{hint},
	})
}
