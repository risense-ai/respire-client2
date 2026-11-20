package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

const deviceConfigLimit = 1 << 20

type deviceConfigIntent struct {
	DataDir        *string `json:"data_dir"`
	Addr           *string `json:"addr"`
	Autosync       *bool   `json:"autosync"`
	CureAuto       *bool   `json:"cure_auto"`
	RPCParallelism *uint32 `json:"rpc_parallelism"` // Always null for the Client2 device commands.
}

type deviceConfigSnapshot struct {
	fields  map[string]json.RawMessage
	profile string
}

type deviceConfigEdit struct {
	path                string
	original, candidate []byte
	before, after       deviceConfigSnapshot
}

func (m *Manager) desktopDeviceConfig(ctx context.Context, intent deviceConfigIntent) (result json.RawMessage, err error) {
	if intent == (deviceConfigIntent{}) {
		_, snapshot, err := m.readDeviceConfig()
		if err != nil {
			return nil, err
		}
		return m.deviceConfigResult(ctx, snapshot, false)
	}
	if intent.DataDir != nil || intent.Addr != nil || intent.Autosync != nil && *intent.Autosync {
		return m.validateDeviceConfig(ctx, intent)
	}
	takeover, err := m.lockRuntimeTakeover(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, takeover.Close()) }()
	if err := m.requireNoDeviceMaintenance(); err != nil {
		return nil, err
	}
	edit, err := m.reviewDeviceConfig(intent, "")
	if err != nil {
		return nil, err
	}
	raw, err := m.deviceConfigResult(ctx, edit.after, false)
	if err != nil {
		return nil, err
	}
	if _, err := edit.publish(ctx); err != nil {
		return nil, err
	}
	return raw, nil
}

func (m *Manager) requireNoDeviceMaintenance() error {
	for _, name := range []string{"maintenance.json", "account-active.json", "key-control.json", "offline-keygen.json"} {
		_, err := os.Lstat(filepath.Join(m.DataRoot, "runtime", "autostart", name))
		if err == nil {
			return fmt.Errorf("请先完成现有 Runtime 维护 (%s)", name)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (m *Manager) desktopProfileSwitch(ctx context.Context, name string, space bool) (json.RawMessage, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsFunc(name, func(ch rune) bool {
		return !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_')
	}) || (space && !desktopSpaceName.MatchString(name)) {
		return nil, errors.New("账户或空间名称无效")
	}
	profile := m.DataRoot
	if name != "main" {
		profile = filepath.Join(profile, "accounts", name)
	}
	return m.changeDeviceConfig(ctx, deviceConfigIntent{DataDir: &profile}, nil, space)
}

func (m *Manager) readDeviceConfig() ([]byte, deviceConfigSnapshot, error) {
	var snapshot deviceConfigSnapshot
	if _, err := deviceProfilePath(m.DataRoot); err != nil {
		return nil, snapshot, err
	}
	data, err := deviceConfigBytes(filepath.Join(m.DataRoot, "client.json"))
	if err != nil {
		return nil, snapshot, err
	}
	snapshot, err = m.parseDeviceConfig(data, false)
	return data, snapshot, err
}

func deviceConfigBytes(path string) ([]byte, error) {
	data, err := readCredentialConfig(path, deviceConfigLimit)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

func (m *Manager) parseDeviceConfig(data []byte, newProfile bool) (deviceConfigSnapshot, error) {
	root, err := deviceProfilePath(m.DataRoot)
	if err != nil {
		return deviceConfigSnapshot{}, err
	}
	snapshot := deviceConfigSnapshot{fields: map[string]json.RawMessage{}, profile: root}
	if data != nil && (!utf8.Valid(data) || json.Unmarshal(data, &snapshot.fields) != nil || snapshot.fields == nil) {
		return snapshot, errors.New("client.json 必须包含有效的 UTF-8 JSON 对象")
	}
	for _, key := range []string{"lang", "data_dir", "addr", "api_base", "service_mode", "engine", "model"} {
		if raw, ok := snapshot.fields[key]; ok {
			var value *string
			if json.Unmarshal(raw, &value) != nil || value == nil {
				return snapshot, fmt.Errorf("client.json 的 %s 必须是字符串", key)
			}
			switch key {
			case "service_mode":
				if *value != "normal" && *value != "readonly" && *value != "off" {
					return snapshot, errors.New("client.json 的 service_mode 无效")
				}
			case "data_dir":
				if *value == "" {
					continue
				}
				profile, err := deviceProfilePath(*value)
				if err != nil {
					return snapshot, err
				}
				rel, err := filepath.Rel(root, profile)
				if (err == nil && filepath.IsLocal(rel)) || deviceConfigDefaultProfileLayout(m.DataRoot) {
					snapshot.profile = profile
				} else if newProfile {
					return snapshot, errors.New("显式 RSRS_DATA_DIR 隔离模式下，目标数据目录必须在安装根内")
				}
			}
		}
	}
	for _, key := range []string{"autosync", "cure_auto"} {
		if raw, ok := snapshot.fields[key]; ok {
			var value *bool
			if json.Unmarshal(raw, &value) != nil || value == nil {
				return snapshot, fmt.Errorf("client.json 的 %s 必须是布尔值", key)
			}
		}
	}
	if raw, ok := snapshot.fields["rpc_parallelism"]; ok {
		var value *uint32
		if json.Unmarshal(raw, &value) != nil || value == nil {
			return snapshot, errors.New("client.json 的 rpc_parallelism 无效")
		}
	}
	return snapshot, nil
}

func deviceConfigDefaultProfileLayout(root string) bool {
	marker := os.Getenv("RSRS_DEFAULT_DATA_DIR")
	if strings.TrimSpace(marker) == "" {
		return false
	}
	defaultRoot, err := deviceConfigDefaultRoot()
	if err != nil || !samePath(defaultRoot, root) {
		return false
	}
	marked, err := deviceProfilePath(marker)
	return err == nil && samePath(marked, defaultRoot)
}

func deviceConfigHome() (string, error) {
	if home := os.Getenv("HOME"); strings.TrimSpace(home) != "" {
		return home, nil
	}
	return os.UserHomeDir()
}

func deviceConfigDefaultRoot() (string, error) {
	home, err := deviceConfigHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".rsrs"), nil
}

func deviceProfilePath(value string) (string, error) {
	if strings.HasPrefix(value, "~/") || strings.HasPrefix(value, "~\\") {
		home, err := deviceConfigHome()
		if err != nil {
			return "", err
		}
		value = home + string(filepath.Separator) + value[2:]
	}
	value = windowsDOSPath(value)
	volume := filepath.VolumeName(value)
	for _, part := range strings.Split(filepath.ToSlash(value[len(volume):]), "/") {
		if part == ".." || runtime.GOOS == "windows" && part != "." &&
			(strings.HasSuffix(part, " ") || strings.HasSuffix(part, ".") || strings.Contains(part, ":")) {
			return "", errors.New("Runtime 数据目录包含无效路径段")
		}
	}
	if !filepath.IsAbs(value) || strings.ContainsRune(value, 0) {
		return "", errors.New("Runtime 数据目录必须是绝对路径")
	}
	path := filepath.Clean(value)
	// macOS system aliases also resolve this way in Runtime's control preflight.
	if runtime.GOOS == "darwin" {
		for _, alias := range []string{"/tmp", "/var", "/etc"} {
			if strings.HasPrefix(path, alias+"/") {
				resolved, err := filepath.EvalSymlinks(alias)
				if err != nil || resolved != "/private"+alias {
					return "", errors.New("Runtime 数据目录使用了已重定向的系统路径")
				}
				path = "/private" + path
				break
			}
		}
	}
	for current := path; current != filepath.Dir(current); current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("Runtime 数据目录包含链接或非目录项 (%s)", current)
		}
	}
	return path, nil
}

func (m *Manager) reviewDeviceConfig(intent deviceConfigIntent, workspaceMode string) (deviceConfigEdit, error) {
	edit := deviceConfigEdit{path: filepath.Join(m.DataRoot, "client.json")}
	data, before, err := m.readDeviceConfig()
	if err != nil {
		return edit, err
	}
	edit.original, edit.before = data, before
	fields := make(map[string]json.RawMessage, len(before.fields))
	for key, raw := range before.fields {
		fields[key] = raw
	}
	for _, field := range []struct {
		key   string
		value *string
	}{{"data_dir", intent.DataDir}, {"addr", intent.Addr}} {
		if field.value == nil {
			continue
		}
		value := strings.TrimSpace(*field.value)
		if field.key == "addr" && !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
			return edit, errors.New("服务器地址必须以 http:// 或 https:// 开头")
		}
		if field.key == "data_dir" && value == "" {
			delete(fields, field.key)
			continue
		}
		fields[field.key], err = json.Marshal(value)
		if err != nil {
			return edit, err
		}
	}
	if intent.Autosync != nil {
		fields["autosync"] = json.RawMessage(fmt.Sprint(*intent.Autosync))
	}
	if intent.CureAuto != nil {
		fields["cure_auto"] = json.RawMessage(fmt.Sprint(*intent.CureAuto))
	}
	if workspaceMode != "" {
		fields["service_mode"], err = json.Marshal(workspaceMode)
		if err != nil {
			return edit, err
		}
	}
	edit.candidate, err = json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return edit, err
	}
	edit.after, err = m.parseDeviceConfig(edit.candidate, intent.DataDir != nil)
	return edit, err
}

func (e deviceConfigEdit) publish(ctx context.Context) (bool, error) {
	return e.replace(ctx, e.original, e.candidate, false)
}

func (e deviceConfigEdit) restore(ctx context.Context) error {
	_, err := e.replace(ctx, e.candidate, e.original, true)
	return err
}

func lockDeviceConfig(ctx context.Context, path string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lockPath := path + ".lock"
	if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("client.json.lock 不是普通文件")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	// Rust's File::lock conflicts with flock on Unix and a lock at offset zero on Windows.
	if err := tryRuntimeLock(lock); err != nil {
		return nil, errors.Join(fmt.Errorf("无法取得 client.json 写入锁: %w", err), lock.Close())
	}
	return lock, nil
}

func (e deviceConfigEdit) replace(ctx context.Context, expected, next []byte, restoring bool) (attempted bool, err error) {
	lock, err := lockDeviceConfig(ctx, e.path)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	current, err := deviceConfigBytes(e.path)
	if err != nil {
		return false, err
	}
	if restoring && bytes.Equal(current, next) {
		return false, nil
	}
	if !bytes.Equal(current, expected) {
		return false, errors.New("client.json 已被其他操作修改")
	}
	if next == nil {
		return true, os.Remove(e.path)
	}
	temp, err := os.CreateTemp(filepath.Dir(e.path), ".client-json-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(temp.Name())
	_, writeErr := temp.Write(next)
	if err := errors.Join(writeErr, temp.Sync(), temp.Close()); err != nil {
		return false, err
	}
	if expected == nil {
		return true, publishFreshRuntimeFile(temp.Name(), e.path)
	}
	return true, publishSessionFile(temp.Name(), e.path)
}

func (m *Manager) deviceConfigResult(ctx context.Context, snapshot deviceConfigSnapshot, applied bool) (json.RawMessage, error) {
	const defaultServer = "https://api.rsrs.rs"
	addr, autosync, cure := defaultServer, true, true
	if value := snapshot.fields["addr"]; value != nil {
		if err := json.Unmarshal(value, &addr); err != nil {
			return nil, err
		}
		if addr == "" {
			addr = defaultServer
		}
	}
	for key, value := range map[string]*bool{"autosync": &autosync, "cure_auto": &cure} {
		if raw := snapshot.fields[key]; raw != nil {
			if err := json.Unmarshal(raw, value); err != nil {
				return nil, err
			}
		}
	}
	if value, exists := os.LookupEnv("RSRS_NO_AUTOSYNC"); exists && value != "0" {
		autosync = false
	}
	var saved *uint32
	if raw := snapshot.fields["rpc_parallelism"]; raw != nil {
		if err := json.Unmarshal(raw, &saved); err != nil {
			return nil, err
		}
		if *saved == 0 {
			saved = nil
		}
	}
	defaultRoot, err := deviceConfigDefaultRoot()
	if err != nil {
		return nil, err
	}
	var workers *uint64
	health, raw, err := m.Health(ctx)
	if err == nil {
		info, err := m.Installed()
		if err != nil {
			return nil, err
		}
		if err := m.verifyDeviceConfigOwner(ctx, health, info, snapshot.profile); err != nil {
			return nil, err
		}
		var observed struct {
			RPC struct {
				Workers uint64 `json:"effective_parallelism"`
			} `json:"rpc"`
		}
		if err := json.Unmarshal(raw, &observed); err != nil {
			return nil, err
		}
		if observed.RPC.Workers > 0 {
			workers = &observed.RPC.Workers
		}
	} else if applied || !IsUnavailable(err) {
		return nil, err
	}
	item := func(name, value string) map[string]string {
		return map[string]string{"name": name, "status": "ok", "value": value}
	}
	workerItem := map[string]string{"name": "rpc workers", "status": "skip", "value": "Runtime worker count not observed"}
	if workers != nil {
		workerItem = item("rpc workers", fmt.Sprint(*workers))
	}
	return json.Marshal(map[string]any{"command": "config", "status": "ok",
		"summary": map[string]any{"data_dir": snapshot.profile, "default_data_dir": defaultRoot,
			"addr": addr, "default_addr": defaultServer, "autosync": autosync, "cure_auto": cure,
			"rpc_parallelism": saved, "rpc_workers": workers, "runtime_applied": applied},
		"items": []map[string]string{item("data dir", snapshot.profile), item("server", addr),
			item("auto-sync", fmt.Sprint(autosync)), item("auto-cure", fmt.Sprint(cure)), workerItem},
		"details": nil, "actions": []string{}, "warnings": []string{}, "errors": []string{},
	})
}
