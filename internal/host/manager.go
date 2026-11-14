package host

import (
	"context"
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
	// ProxyMode is "system" (environment variables, then the OS proxy) or
	// "direct" (never proxy). Empty in older files and migrated to system.
	ProxyMode string `json:"proxyMode"`
	// Per-component update settings; AutoUpdate above is the legacy master
	// switch kept only to migrate older preference files.
	ClientUpdateInterval  string `json:"clientUpdateInterval"`
	RuntimeUpdateInterval string `json:"runtimeUpdateInterval"`
	ClientAutoUpdate      bool   `json:"clientAutoUpdate"`
	ClientAutoRestart     bool   `json:"clientAutoRestart"`
	RuntimeAutoUpdate     bool   `json:"runtimeAutoUpdate"`
	RuntimeAutoRestart    bool   `json:"runtimeAutoRestart"`
	SidebarWidth          int    `json:"sidebarWidth"`
	MemoryIndexWidth      int    `json:"memoryIndexWidth"`
	// Versioned first-run wizard state; steps and sync intent only — never
	// credentials or recovery material.
	OnboardingVersion    int    `json:"onboardingVersion"`
	OnboardingCompleted  bool   `json:"onboardingCompleted"`
	OnboardingStep       string `json:"onboardingStep"`
	OnboardingSyncChoice string `json:"onboardingSyncChoice"`
}

// updateIntervalValues are the cadences the updates page offers.
var updateIntervalValues = map[string]bool{
	"5m": true, "30m": true, "1h": true, "6h": true, "24h": true,
}

// onboardingSteps is the wizard's step enum; "done" is the terminal step.
var onboardingSteps = map[string]bool{
	"language": true, "sync": true, "account": true, "recovery": true,
	"model": true, "device": true, "confirm": true, "execute": true,
	"agents": true, "import": true, "done": true,
}

// onboardingSyncChoices records the cloud/offline intent picked in the
// wizard; empty means the step has not been answered yet.
var onboardingSyncChoices = map[string]bool{
	"": true, "cloud": true, "offline": true,
}

// defaultPreferences is the state of a brand-new installation: the wizard
// has not run yet, so onboardingCompleted stays false.
func defaultPreferences() Preferences {
	// RuntimeChannel starts on dev until the runtime stable train opens:
	// stable promotion is gated on G1 acceptance (respire-runtime
	// docs/version-contract.md), and /releases/latest answers 404 while the
	// repository has no full release. Flip back to "stable" once the first
	// stable runtime is published.
	return Preferences{
		AutoRuntime: true, AutoUpdate: true, RuntimeChannel: "dev", ClientChannel: "stable", Layout: "sidebar", Theme: "system", Language: "",
		ProxyMode:            ProxyModeSystem,
		ClientUpdateInterval: "6h", RuntimeUpdateInterval: "6h", ClientAutoUpdate: true, RuntimeAutoUpdate: true,
		ClientAutoRestart: true, RuntimeAutoRestart: true,
		OnboardingVersion:   1,
		OnboardingCompleted: false,
		OnboardingStep:      "language",
	}
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
	if _, ok := stored["runtimeAutoRestart"]; !ok {
		p.RuntimeAutoRestart = true
	}
	// Preference files written before the wizard existed belong to existing
	// users: they must not be forced through onboarding again. Only files
	// that already carry the wizard state keep it.
	if _, ok := stored["onboardingCompleted"]; !ok {
		p.OnboardingVersion = 1
		p.OnboardingCompleted = true
		p.OnboardingStep = "done"
	}
	if p.OnboardingVersion < 1 {
		p.OnboardingVersion = 1
	}
	if p.OnboardingCompleted {
		p.OnboardingStep = "done"
	}
	if !onboardingSteps[p.OnboardingStep] {
		p.OnboardingStep = "done"
	}
	if !onboardingSyncChoices[p.OnboardingSyncChoice] {
		p.OnboardingSyncChoice = ""
	}
	if !updateIntervalValues[p.ClientUpdateInterval] {
		p.ClientUpdateInterval = "6h"
	}
	if !updateIntervalValues[p.RuntimeUpdateInterval] {
		p.RuntimeUpdateInterval = "6h"
	}
	// Preference files written before proxy support keep the previous
	// behaviour available: an unset mode means "follow the environment and
	// the system proxy".
	if p.ProxyMode == "" {
		p.ProxyMode = ProxyModeSystem
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
	if !proxyModes[p.ProxyMode] {
		return errors.New("代理模式必须是 system 或 direct")
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
	if p.OnboardingVersion != 1 {
		return errors.New("onboarding 版本无效")
	}
	if !onboardingSteps[p.OnboardingStep] {
		return errors.New("onboarding 步骤无效")
	}
	if !onboardingSyncChoices[p.OnboardingSyncChoice] {
		return errors.New("onboarding 同步意向无效")
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
	Root            string
	DataRoot        string
	Endpoint        string
	HTTPClient      *http.Client
	modelHTTPClient *http.Client
	localClient     *http.Client
	mu              sync.Mutex
	prefs           Preferences
	task            TaskState
	taskCancel      context.CancelFunc
	taskPause       bool
	jobs            map[string]*Operation
	// proxy resolves environment and system proxy settings for outbound
	// requests. localClient deliberately bypasses it (loopback only).
	proxy *proxyResolver
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
		prefs:       defaultPreferences(), task: TaskState{Stage: "就绪"},
		proxy: newProxyResolver()}
	proxyFor := m.proxy.proxyFunc(func() string { return m.Preferences().ProxyMode })
	// Both outbound clients share the resolver: updates, release metadata and
	// model weights follow the same environment/system proxy. The defaults
	// (dial and TLS timeouts, HTTP/2) are kept from http.DefaultTransport;
	// downloads have no overall deadline (the request context governs) and
	// HTTPS-only redirects.
	outbound := http.DefaultTransport.(*http.Transport).Clone()
	outbound.Proxy = proxyFor
	m.HTTPClient = &http.Client{Timeout: 10 * time.Minute, Transport: outbound, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" {
			return errors.New("download redirects must use HTTPS")
		}
		return nil
	}}
	modelTransport := outbound.Clone()
	m.modelHTTPClient = &http.Client{Transport: modelTransport}
	if err := os.MkdirAll(m.Root, 0700); err != nil {
		return nil, err
	}
	prefs, err := loadPreferencesFile(filepath.Join(m.Root, "preferences.json"))
	if err != nil {
		return nil, err
	}
	m.prefs = prefs
	if err := m.loadOperations(); err != nil {
		return nil, err
	}
	return m, nil
}

// loadPreferencesFile reads one preferences file: a missing file means a
// brand-new installation that keeps the first-run wizard state, while an
// existing file without wizard fields belongs to an upgraded user who is
// migrated to completed.
func loadPreferencesFile(path string) (Preferences, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		prefs := defaultPreferences()
		if err := prefs.validate(); err != nil {
			return prefs, err
		}
		return prefs, nil
	}
	if err != nil {
		return Preferences{}, fmt.Errorf("read preferences: %w", err)
	}
	return loadPreferences(b)
}

func loadPreferences(b []byte) (Preferences, error) {
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(b, &stored); err != nil {
		return Preferences{}, fmt.Errorf("read preferences: %w", err)
	}
	prefs := defaultPreferences()
	if err := json.Unmarshal(b, &prefs); err != nil {
		return Preferences{}, fmt.Errorf("read preferences: %w", err)
	}
	prefs = migratePreferences(prefs, stored)
	if err := prefs.validate(); err != nil {
		return Preferences{}, err
	}
	return prefs, nil
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

// StartTaskCancellable runs action with a cancellable context in the
// background so a download can be paused (canceled while keeping partial
// chunks) or aborted. The busy state is published synchronously before it
// returns: the HTTP handler can answer immediately and clients polling the
// task snapshot never observe a stale pre-start terminal state. It reports
// false when another task is already running, without touching that task.
func (m *Manager) StartTaskCancellable(stage string, action func(ctx context.Context) error) bool {
	m.mu.Lock()
	if m.task.Busy {
		m.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.taskCancel = cancel
	m.taskPause = false
	m.task = TaskState{Busy: true, Stage: stage}
	m.mu.Unlock()
	go func() {
		err := action(ctx)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.taskCancel = nil
		m.task.Busy = false
		paused := m.taskPause && errors.Is(err, context.Canceled)
		m.taskPause = false
		switch {
		case err == nil:
			m.task.Stage = "完成"
			m.task.Error = ""
		case paused:
			m.task.Stage = "已暂停"
			m.task.Error = ""
		case errors.Is(err, context.Canceled):
			m.task.Stage = "已取消"
			m.task.Error = ""
		default:
			m.task.Stage = "操作失败"
			m.task.Error = err.Error()
		}
	}()
	return true
}

// CancelTask aborts the running task; pause keeps partial chunks so the
// next run resumes from where it stopped.
func (m *Manager) CancelTask(pause bool) {
	m.mu.Lock()
	m.taskPause = pause
	cancel := m.taskCancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *Manager) TaskSnapshot() TaskState { m.mu.Lock(); defer m.mu.Unlock(); return m.task }
