package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
			issued, err := readSpaceMembers(path)
			if err != nil {
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

type issuedSpaceMember struct {
	SessionID  string `json:"session_id"`
	DeviceName string `json:"device_name"`
	ReadOnly   bool   `json:"readonly"`
	IssuedAt   string `json:"issued_at"`
}

func readSpaceMembers(profile string) ([]issuedSpaceMember, error) {
	path := filepath.Join(profile, "space_sessions.json")
	raw, err := readCredentialConfig(path, 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return []issuedSpaceMember{}, nil
	}
	if err != nil {
		return nil, err
	}
	var members []issuedSpaceMember
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if members == nil {
		return nil, fmt.Errorf("%s 必须是 JSON 数组", path)
	}
	return members, nil
}

var desktopSpaceName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

func (m *Manager) desktopSpaceMembers(args []string) (json.RawMessage, error) {
	name := ""
	if len(args) > 0 {
		name = strings.TrimSpace(args[0])
	}
	if err := profileDirectory(m.DataRoot); err != nil {
		return nil, err
	}
	profile := m.DataRoot
	if name == "" {
		device, err := readAgentSettingsObject(filepath.Join(m.DataRoot, "client.json"))
		if err != nil {
			return nil, err
		}
		profile, err = desktopProfilePath(m.DataRoot, device)
		if err != nil {
			return nil, err
		}
	} else if name != "main" {
		if !desktopSpaceName.MatchString(name) {
			return nil, errors.New("空间名称须为 1 到 40 个英文字母、数字、连字符或下划线")
		}
		profile = filepath.Join(m.DataRoot, "accounts", name)
	}
	if err := profileDirectory(profile); err != nil {
		return nil, err
	}
	members, err := readSpaceMembers(profile)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = "main"
		if relative, err := filepath.Rel(filepath.Join(m.DataRoot, "accounts"), profile); err == nil && desktopSpaceName.MatchString(relative) {
			name = relative
		}
	}
	status := "ok"
	if len(members) == 0 {
		status = "skip"
	}
	return json.Marshal(map[string]any{"command": "space", "status": status, "details": members,
		"summary": map[string]any{"action": "members", "space": name, "count": len(members)}})
}
