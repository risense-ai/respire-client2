package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ncruces/go-sqlite3"
)

// The CLI holds a SQLite exclusive transaction on this same file.
func (m *Manager) lockRuntimeTakeover(ctx context.Context) (*sqlite3.Conn, error) {
	directory, err := filepath.EvalSymlinks(m.DataRoot)
	if err != nil {
		return nil, err
	}
	if !samePath(directory, m.DataRoot) {
		return nil, errors.New("Runtime 安装根目录已重定向")
	}
	for _, component := range []string{"runtime", "takeover"} {
		directory = filepath.Join(directory, component)
		if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, err := os.Lstat(directory)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, errors.New("Runtime 切换锁目录无效")
		}
	}
	wait := time.Duration(0)
	if deadline, present := ctx.Deadline(); present {
		wait = time.Until(deadline)
	}
	return lockRuntimeDatabase(ctx, directory, wait)
}

func lockRuntimeDatabase(ctx context.Context, directory string, wait time.Duration) (*sqlite3.Conn, error) {
	path := filepath.Join(directory, "lock.db")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("Runtime 切换锁文件无效")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	db, err := sqlite3.OpenFlags(path, sqlite3.OPEN_READWRITE|sqlite3.OPEN_CREATE)
	if err != nil {
		return nil, err
	}
	db.SetInterrupt(ctx)
	err = db.BusyTimeout(wait)
	if err == nil {
		err = db.Exec("BEGIN EXCLUSIVE")
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("无法取得 Runtime 切换锁: %w", err), db.Close())
	}
	return db, nil
}
