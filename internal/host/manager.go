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
}

func (p Preferences) validate() error {
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
	m := &Manager{Root: filepath.Join(dataRoot, "client2"), Endpoint: "http://127.0.0.1:" + port,
		localClient: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		prefs:       Preferences{AutoRuntime: true, AutoUpdate: true, RuntimeChannel: "stable", ClientChannel: "stable", Layout: "sidebar", Theme: "system"}, task: TaskState{Stage: "就绪"},
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
		err = json.Unmarshal(b, &m.prefs)
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
