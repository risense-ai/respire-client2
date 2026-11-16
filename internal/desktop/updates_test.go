package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"testing"

	"respire-client2/internal/host"
)

func TestDirWritableAcceptsProbe(t *testing.T) {
	if !dirWritable(t.TempDir()) {
		t.Fatal("writable directory must pass the write probe")
	}
}

func TestDirWritableRejectsMissingDirectory(t *testing.T) {
	if dirWritable(filepath.Join(t.TempDir(), "missing")) {
		t.Fatal("missing directory must fail the write probe")
	}
}

type startupRuntimeFixture struct {
	selected    string
	running     string
	starts      int
	unavailable error
	healthErr   error
	installErr  error
	startErr    error
}

func (m *startupRuntimeFixture) InstallRuntime(automatic, restart bool) error {
	if automatic {
		return errors.New("startup install must not be automatic")
	}
	if m.installErr != nil {
		return m.installErr
	}
	m.selected = "new"
	if restart {
		m.running = m.selected
	}
	return nil
}

func (m *startupRuntimeFixture) Health(context.Context) (host.HealthInfo, json.RawMessage, error) {
	if m.healthErr != nil {
		return host.HealthInfo{}, nil, m.healthErr
	}
	if m.running == "" {
		return host.HealthInfo{}, nil, m.unavailable
	}
	return host.HealthInfo{}, nil, nil
}

func (m *startupRuntimeFixture) Start() error {
	m.starts++
	if m.startErr != nil {
		return m.startErr
	}
	m.running = m.selected
	return nil
}

func TestInstallRuntimeForStartup(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	conn, unavailable := net.Dial("tcp", address)
	if conn != nil {
		conn.Close()
	}
	if !host.IsUnavailable(unavailable) {
		t.Fatalf("expected a refused connection, got %v", unavailable)
	}

	for _, tc := range []struct {
		name, selected, running, wantRunning string
		restart                              bool
		wantStarts                           int
	}{
		{name: "first install starts runtime", wantRunning: "new", wantStarts: 1},
		{name: "updating stopped runtime starts new version", selected: "old", wantRunning: "new", wantStarts: 1},
		{name: "updating running runtime preserves it", selected: "old", running: "old", wantRunning: "old"},
		{name: "automatic restart applies new version", selected: "old", running: "old", restart: true, wantRunning: "new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &startupRuntimeFixture{selected: tc.selected, running: tc.running, unavailable: unavailable}
			if err := installRuntimeForStartup(m, tc.restart); err != nil {
				t.Fatal(err)
			}
			if m.selected != "new" || m.running != tc.wantRunning || m.starts != tc.wantStarts {
				t.Fatalf("selected=%q running=%q starts=%d", m.selected, m.running, m.starts)
			}
		})
	}

	for _, stage := range []string{"install", "health", "start"} {
		t.Run(stage+" failure is returned", func(t *testing.T) {
			want := errors.New(stage + " failed")
			m := &startupRuntimeFixture{unavailable: unavailable}
			switch stage {
			case "install":
				m.installErr = want
			case "health":
				m.healthErr = want
			case "start":
				m.startErr = want
			}
			if err := installRuntimeForStartup(m, false); !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			if stage != "start" && m.starts != 0 {
				t.Fatal("must not start after an earlier failure")
			}
		})
	}
}
