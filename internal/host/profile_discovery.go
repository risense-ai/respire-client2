package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func desktopProfilePath(root string, config map[string]any) (string, error) {
	selected, present := config["data_dir"]
	if !present || selected == "" {
		return root, nil
	}
	directory, ok := selected.(string)
	if !ok {
		return "", errors.New("data_dir 必须是路径")
	}
	return expandTilde(directory)
}

func profileDirectory(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(path) || !samePath(path, resolved) {
		return fmt.Errorf("账户目录路径发生重定向: %s", path)
	}
	return nil
}

func profileFile(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("账户文件格式无效: %s", path)
	}
	return true, nil
}

func (m *Manager) desktopProfileList(spaces bool) (json.RawMessage, error) {
	if err := profileDirectory(m.DataRoot); err != nil {
		return nil, err
	}
	device, err := readAgentSettingsObject(filepath.Join(m.DataRoot, "client.json"))
	if err != nil {
		return nil, err
	}
	current, err := desktopProfilePath(m.DataRoot, device)
	if err != nil {
		return nil, err
	}
	if err := profileDirectory(current); err != nil {
		return nil, err
	}
	row := func(name, path string) (map[string]any, error) {
		sessionPath := filepath.Join(path, "session.json")
		var user string
		raw, err := readCredentialConfig(sessionPath, 1<<20)
		hasSession := err == nil
		if hasSession {
			var session *struct {
				User string `json:"user"`
			}
			parseErr := json.Unmarshal(raw, &session)
			clear(raw)
			if parseErr != nil {
				return nil, fmt.Errorf("%s: %w", sessionPath, parseErr)
			}
			if session == nil {
				return nil, fmt.Errorf("%s 必须是 JSON 对象", sessionPath)
			}
			user = session.User
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		item := map[string]any{"name": name, "dir": path, "user": user, "current": samePath(path, current)}
		if spaces {
			owner, err := profileFile(filepath.Join(path, "space_owner.json"))
			if err != nil {
				return nil, err
			}
			issuedPath := filepath.Join(path, "space_sessions.json")
			var issued []json.RawMessage
			raw, err := readCredentialConfig(issuedPath, 1<<20)
			if err == nil {
				if err = json.Unmarshal(raw, &issued); err != nil {
					return nil, fmt.Errorf("%s: %w", issuedPath, err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			item["owner"], item["members"], item["is_main"] = owner, len(issued), name == "main"
		} else if path != m.DataRoot && !hasSession {
			db, err := profileFile(filepath.Join(path, "rsrs.db"))
			if err != nil {
				return nil, err
			}
			legacyDB, err := profileFile(filepath.Join(path, "onememory.db"))
			if err != nil {
				return nil, err
			}
			if db && legacyDB {
				return nil, fmt.Errorf("%s 同时包含 rsrs.db 和 onememory.db，请明确选择数据库", path)
			}
			if !db && !legacyDB {
				return nil, nil
			}
		}
		return item, nil
	}
	main, err := row("main", m.DataRoot)
	if err != nil {
		return nil, err
	}
	rows := []map[string]any{main}
	accounts := filepath.Join(m.DataRoot, "accounts")
	if err := profileDirectory(accounts); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	entries, err := os.ReadDir(accounts)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		path := filepath.Join(accounts, entry.Name())
		if err := profileDirectory(path); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		item, err := row(entry.Name(), path)
		if err != nil {
			return nil, err
		}
		if item != nil {
			rows = append(rows, item)
		}
	}
	name := "accounts"
	if spaces {
		name = "spaces"
	}
	return json.Marshal(map[string]any{"command": name, "status": "ok", "summary": map[string]any{name: rows, "current_dir": current}})
}
