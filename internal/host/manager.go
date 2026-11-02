package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type Preferences struct {
	AutoRuntime    bool   `json:"autoRuntime"`
	AutoUpdate     bool   `json:"autoUpdate"`
	RuntimeChannel string `json:"runtimeChannel"`
	ClientChannel  string `json:"clientChannel"`
	Layout         string `json:"layout"`
	Theme          string `json:"theme"`
	Language       string `json:"language"`
	// Per-component update settings; AutoUpdate above is the legacy master
	// switch kept only to migrate older preference files.
	ClientUpdateInterval  string `json:"clientUpdateInterval"`
	RuntimeUpdateInterval string `json:"runtimeUpdateInterval"`
	ClientAutoUpdate      bool   `json:"clientAutoUpdate"`
	ClientAutoRestart     bool   `json:"clientAutoRestart"`
	RuntimeAutoUpdate     bool   `json:"runtimeAutoUpdate"`
	SidebarWidth          int    `json:"sidebarWidth"`
	MemoryIndexWidth      int    `json:"memoryIndexWidth"`
}

// updateIntervalValues are the cadences the updates page offers.
var updateIntervalValues = map[string]bool{
	"5m": true, "30m": true, "1h": true, "6h": true, "24h": true,
}

// migratePreferences fills settings that older preference files predate: the
// per-component auto-update switches inherit the legacy master switch, and
// unset intervals take the historical six-hour cadence.
func migratePreferences(p Preferences, stored map[string]json.RawMessage) Preferences {
	return MigratePreferences(p, stored)
}

// MigratePreferences is the exported form used when importing preference
// backups produced by older builds.
func MigratePreferences(p Preferences, stored map[string]json.RawMessage) Preferences {
	if _, ok := stored["clientAutoUpdate"]; !ok {
		p.ClientAutoUpdate = p.AutoUpdate
	}
	if _, ok := stored["clientAutoRestart"]; !ok {
		p.ClientAutoRestart = true
	}
	if _, ok := stored["runtimeAutoUpdate"]; !ok {
		p.RuntimeAutoUpdate = p.AutoUpdate
	}
	if !updateIntervalValues[p.ClientUpdateInterval] {
		p.ClientUpdateInterval = "6h"
	}
	if !updateIntervalValues[p.RuntimeUpdateInterval] {
		p.RuntimeUpdateInterval = "6h"
	}
	if p.SidebarWidth < 0 {
		p.SidebarWidth = 0
	}
	if p.MemoryIndexWidth < 0 {
		p.MemoryIndexWidth = 0
	}
	return p
}

func (p Preferences) validate() error {
	if p.Language != "" && p.Language != "en" && p.Language != "zh" {
		return errors.New("language must be en or zh")
	}
	if p.Theme != "system" && p.Theme != "light" && p.Theme != "dark" {
		return errors.New("主题必须是 system、light 或 dark")
	}
	if p.Layout != "sidebar" && p.Layout != "compact" {
		return errors.New("布局必须是 sidebar 或 compact")
	}
	for _, channel := range []string{p.RuntimeChannel, p.ClientChannel} {
		if channel != "stable" && channel != "dev" {
			return errors.New("更新通道必须是 stable 或 dev")
		}
	}
	if !updateIntervalValues[p.ClientUpdateInterval] || !updateIntervalValues[p.RuntimeUpdateInterval] {
		return errors.New("更新频率必须是 5m、30m、1h、6h 或 24h")
	}
	if p.SidebarWidth < 0 {
		return errors.New("侧栏宽度无效")
	}
	if p.MemoryIndexWidth < 0 {
		return errors.New("记忆树宽度无效")
	}
	return nil
}

type TaskState struct {
	Busy  bool   `json:"busy"`
	Stage string `json:"stage"`
	Error string `json:"error,omitempty"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
}

type InstalledRuntime struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

type Manager struct {
	Root        string
	DataRoot    string
	Endpoint    string
	HTTPClient  *http.Client
	localClient *http.Client
	mu          sync.Mutex
	prefs       Preferences
	task        TaskState
	jobs        map[string]*Operation
}

func New() (*Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dataRoot := os.Getenv("RSRS_DATA_DIR")
	if dataRoot == "" {
		dataRoot = filepath.Join(home, ".rsrs")
	}
	dataRoot, err = filepath.Abs(dataRoot)
	if err != nil {
		return nil, err
	}
	port := os.Getenv("RSRS_RPC_PORT")
	if port == "" {
		port = "15169"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, errors.New("RSRS_RPC_PORT must be a valid TCP port")
	}
	m := &Manager{Root: filepath.Join(dataRoot, "client2"), DataRoot: dataRoot, Endpoint: "http://127.0.0.1:" + port,
		localClient: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		prefs: Preferences{AutoRuntime: true, AutoUpdate: true, RuntimeChannel: "stable", ClientChannel: "stable", Layout: "sidebar", Theme: "system", Language: "",
			ClientUpdateInterval: "6h", RuntimeUpdateInterval: "6h", ClientAutoUpdate: true, RuntimeAutoUpdate: true,
			ClientAutoRestart: true}, task: TaskState{Stage: "就绪"},
		HTTPClient: &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return errors.New("download redirects must use HTTPS")
			}
			return nil
		}}}
	if err := os.MkdirAll(m.Root, 0700); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(m.Root, "preferences.json"))
	if err == nil {
		var stored map[string]json.RawMessage
		if err = json.Unmarshal(b, &stored); err == nil {
			err = json.Unmarshal(b, &m.prefs)
			if err == nil {
				m.prefs = migratePreferences(m.prefs, stored)
			}
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read preferences: %w", err)
	}
	if err := m.prefs.validate(); err != nil {
		return nil, err
	}
	if err := m.loadOperations(); err != nil {
		return nil, err
	}
	return m, nil
}

func writeJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (m *Manager) Preferences() Preferences { m.mu.Lock(); defer m.mu.Unlock(); return m.prefs }

func (m *Manager) SavePreferences(p Preferences) error {
	if err := p.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := writeJSON(filepath.Join(m.Root, "preferences.json"), p); err != nil {
		return err
	}
	m.prefs = p
	return nil
}

func (m *Manager) progress(stage string, done, total int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.task.Stage, m.task.Done, m.task.Total = stage, done, total
}

func (m *Manager) RunTask(stage string, action func() error) error {
	m.mu.Lock()
	if m.task.Busy {
		m.mu.Unlock()
		return errors.New("已有操作正在执行")
	}
	m.task = TaskState{Busy: true, Stage: stage}
	m.mu.Unlock()
	err := action()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.task.Busy = false
	if err != nil {
		m.task.Stage = "操作失败"
		m.task.Error = err.Error()
	} else {
		m.task.Stage = "完成"
	}
	return err
}

func (m *Manager) TaskSnapshot() TaskState { m.mu.Lock(); defer m.mu.Unlock(); return m.task }
