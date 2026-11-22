package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ncruces/go-sqlite3"
)

func checkModelUninstallPath(path string) error {
	path = windowsDOSPath(path)
	if !filepath.IsAbs(path) || !utf8.ValidString(path) || filepath.Clean(path) != path {
		return fmt.Errorf("模型维护路径必须是规范的绝对路径: %s", path)
	}
	for part := path; ; part = filepath.Dir(part) {
		parent := filepath.Dir(part)
		if part != parent {
			name := filepath.Base(part)
			if strings.ContainsAny(name, ":\\") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
				return fmt.Errorf("模型维护路径包含歧义: %s", part)
			}
		}
		info, err := os.Lstat(part)
		switch {
		case err == nil:
			if modelUninstallRedirect(info) || (part != path && !info.IsDir()) {
				return fmt.Errorf("模型维护路径包含链接、重解析点或非目录祖先: %s", part)
			}
		case !errors.Is(err, os.ErrNotExist):
			return err
		}
		if part == parent {
			return nil
		}
	}
}

func modelUninstallFile(path string) ([]byte, error) {
	if err := checkModelUninstallPath(path); err != nil {
		return nil, err
	}
	raw, err := readCredentialConfig(path, 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return raw, err
}

func modelUninstallObject(path string) ([]byte, error) {
	raw, err := modelUninstallFile(path)
	if err != nil || raw == nil {
		return raw, err
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, fmt.Errorf("无法验证模型维护配置 %s: 需要有效的 JSON 对象", path)
	}
	return raw, nil
}

func modelUninstallProfile(root string, config []byte) (string, error) {
	var device struct {
		DataDir string `json:"data_dir"`
	}
	if config != nil {
		if err := json.Unmarshal(config, &device); err != nil {
			return "", err
		}
	}
	profile := root
	if device.DataDir != "" {
		var err error
		profile, err = expandTilde(device.DataDir)
		if err != nil {
			return "", err
		}
	}
	profile = windowsDOSPath(profile)
	if err := checkModelUninstallPath(profile); err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, profile)
	if err != nil || !filepath.IsLocal(relative) {
		return "", errors.New("模型卸载账户目录不属于当前 Runtime 安装")
	}
	stat, err := os.Stat(profile)
	if err != nil {
		return "", err
	}
	if !stat.IsDir() {
		return "", errors.New("模型卸载账户路径不是目录")
	}
	return profile, nil
}

func checkM3UninstallTarget(target, profile string) error {
	for _, key := range []string{"RSRS_M3_DIR", "ONEMEMORY_M3_DIR", "RESPIRE_M3_DIR"} {
		if raw, ok := os.LookupEnv(key); ok {
			expanded, err := expandTilde(strings.TrimSpace(raw))
			if err != nil {
				return err
			}
			if err := checkModelUninstallPath(expanded); err != nil {
				return err
			}
			if !samePath(expanded, target) {
				return fmt.Errorf("%s 指向安装自有 BGE-M3 目录以外的位置，无法确认删除权属", key)
			}
			break
		}
	}
	relative, err := filepath.Rel(target, profile)
	if err == nil && filepath.IsLocal(relative) {
		return errors.New("BGE-M3 路径包含 Runtime 账户目录")
	}
	if strings.Contains(filepath.ToSlash(target), "/usr/lib/respire/models") {
		return errors.New("不能卸载系统提供的 BGE-M3 文件")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return err
	}
	if relative, err := filepath.Rel(target, windowsDOSPath(home)); err == nil && filepath.IsLocal(relative) {
		return errors.New("BGE-M3 路径包含用户主目录")
	}
	return checkModelUninstallPath(target)
}

func readM3SavedSelection(ctx context.Context, profile string) (database, selection string, err error) {
	for _, name := range []string{"rsrs.db", "onememory.db"} {
		path := filepath.Join(profile, name)
		if err := checkModelUninstallPath(path); err != nil {
			return "", "", err
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		if database != "" || !info.Mode().IsRegular() {
			return "", "", errors.New("无法唯一确认 Runtime 模型选择数据库")
		}
		database = path
	}
	if database == "" {
		return "", "", nil
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		path := database + suffix
		if err := checkModelUninstallPath(path); err != nil {
			return "", "", err
		}
		info, err := os.Lstat(path)
		if (err == nil && !info.Mode().IsRegular()) || (err != nil && !errors.Is(err, os.ErrNotExist)) {
			return "", "", fmt.Errorf("无法验证模型选择数据库附件 %s: %v", path, err)
		}
	}
	db, err := sqlite3.OpenFlags(database, sqlite3.OPEN_READONLY|sqlite3.OPEN_NOFOLLOW)
	if err != nil {
		return "", "", err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	db.SetInterrupt(ctx)
	stmt, _, err := db.Prepare("SELECT value FROM meta WHERE key='embedding.active.selection.v1'")
	if err != nil {
		return "", "", fmt.Errorf("无法读取已存模型选择: %w", err)
	}
	defer func() { err = errors.Join(err, stmt.Close()) }()
	if !stmt.Step() {
		return database, "", stmt.Err()
	}
	selection = stmt.ColumnText(0)
	if !json.Valid([]byte(selection)) {
		return "", "", errors.New("数据库中的模型选择不是有效 JSON")
	}
	if stmt.Step() {
		return "", "", errors.New("数据库中存在重复的模型选择")
	}
	return database, selection, stmt.Err()
}

func removeStoppedM3(ctx context.Context, root, profile, target, address string, recheck func() error) (removed bool, err error) {
	for _, directory := range []string{filepath.Join(root, "runtime"), profile} {
		if err := checkModelUninstallPath(filepath.Join(directory, "lock.db")); err != nil {
			return false, err
		}
		lock, err := lockRuntimeDatabase(ctx, directory, 0)
		if err != nil {
			return false, fmt.Errorf("Runtime 安装或账户库仍被占用，无法卸载: %w", err)
		}
		defer func() { err = errors.Join(err, lock.Close()) }()
	}
	if err := requireStoppedM3Runtime(ctx, root, address); err != nil {
		return false, err
	}
	lockPath := filepath.Join(root, "inference.json.lock")
	if err := checkModelUninstallPath(lockPath); err != nil {
		return false, err
	}
	if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() {
		return false, errors.New("模型配置锁不是普通文件")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	lock, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	if err := tryRuntimeLock(lock); err != nil {
		return false, fmt.Errorf("无法取得模型配置锁: %w", err)
	}
	database, selection, err := readM3SavedSelection(ctx, profile)
	if err != nil {
		return false, err
	}
	verify := func() error {
		if err := recheck(); err != nil {
			return err
		}
		path, current, err := readM3SavedSelection(ctx, profile)
		if err != nil {
			return err
		}
		if path != database || current != selection {
			return errors.New("账户库中的模型选择已改变")
		}
		return ctx.Err()
	}
	if err := verify(); err != nil {
		return false, err
	}
	return stageAndRemoveM3(target, verify)
}

func stageAndRemoveM3(target string, verify func() error) (bool, error) {
	parent := filepath.Dir(target)
	originalParent, err := os.Lstat(parent)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !originalParent.IsDir() || modelUninstallRedirect(originalParent) {
		return false, errors.New("BGE-M3 模型父目录已重定向")
	}
	models, err := os.OpenRoot(parent)
	if err != nil {
		return false, err
	}
	defer models.Close()
	opened, err := models.Stat(".")
	if err != nil || !os.SameFile(opened, originalParent) {
		return false, errors.Join(errors.New("BGE-M3 模型父目录已改变"), err)
	}
	verifyParent := func() error {
		if err := verify(); err != nil {
			return err
		}
		current, err := os.Lstat(parent)
		if err != nil || !os.SameFile(current, originalParent) || modelUninstallRedirect(current) {
			return errors.Join(errors.New("BGE-M3 模型父目录已改变"), err)
		}
		return nil
	}
	if err := recoverEmptyM3Stage(models, verifyParent); err != nil {
		return false, fmt.Errorf("已有未完成的 BGE-M3 卸载暂存目录，已保留内容: %s: %w", filepath.Join(parent, m3UninstallStage), err)
	}
	name := filepath.Base(target)
	original, err := models.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !original.IsDir() || modelUninstallRedirect(original) {
		return false, errors.New("BGE-M3 卸载目标不是普通目录")
	}
	if err := checkM3UninstallTree(models, name); err != nil {
		return false, err
	}
	if err := verifyParent(); err != nil {
		return false, err
	}
	if err := models.Mkdir(m3UninstallStage, 0700); err != nil {
		return false, err
	}
	if err := removeStagedM3(models, name, original, verifyParent); err != nil {
		return false, fmt.Errorf("BGE-M3 卸载未完成，请检查暂存目录 %s: %w", filepath.Join(parent, m3UninstallStage), err)
	}
	return true, nil
}

func checkM3UninstallTree(root *os.Root, name string) error {
	return fs.WalkDir(root.FS(), name, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if modelUninstallRedirect(info) || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("BGE-M3 目录含有链接、重解析点或特殊文件: %s", path)
		}
		return nil
	})
}

func removeStagedM3(models *os.Root, name string, original os.FileInfo, verify func() error) (err error) {
	originalStage, err := models.Lstat(m3UninstallStage)
	if err != nil {
		return err
	}
	if !originalStage.IsDir() || modelUninstallRedirect(originalStage) {
		return errors.New("BGE-M3 暂存目录已重定向")
	}
	stage, err := models.OpenRoot(m3UninstallStage)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, stage.Close())
		if err == nil {
			err = models.Remove(m3UninstallStage)
		}
	}()
	opened, err := stage.Stat(".")
	if err != nil || !os.SameFile(opened, originalStage) {
		return errors.Join(errors.New("BGE-M3 暂存目录已改变"), err)
	}
	if err := models.Rename(name, filepath.Join(m3UninstallStage, name)); err != nil {
		return err
	}
	actual, err := stage.Lstat(name)
	if err != nil {
		return err
	}
	if !os.SameFile(actual, original) || modelUninstallRedirect(actual) {
		return errors.New("BGE-M3 暂存后目录身份已改变")
	}
	if err := checkM3UninstallTree(stage, name); err != nil {
		return err
	}
	if err := verify(); err != nil {
		return err
	}
	if err := stage.RemoveAll(name); err != nil {
		return err
	}
	if _, err := stage.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		return errors.Join(errors.New("BGE-M3 暂存模型仍存在或无法检查"), err)
	}
	if _, err := models.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		return errors.Join(errors.New("BGE-M3 原目录仍存在或无法检查"), err)
	}
	return verify()
}
