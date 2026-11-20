package desktop

import (
	"context"
	"errors"
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
	selected   string
	installs   int
	installErr error
	startErr   error
}

func (m *startupRuntimeFixture) InstallRuntime(automatic, restart bool) error {
	if automatic || restart {
		return errors.New("first installation must only select the Runtime")
	}
	m.installs++
	if m.installErr != nil {
		return m.installErr
	}
	m.selected = "new"
	return nil
}

func (m *startupRuntimeFixture) PrepareRuntimeForStartup(context.Context) error {
	if m.selected == "" {
		return host.ErrRuntimeNotInstalled
	}
	return m.startErr
}

func TestInstallRuntimeForStartup(t *testing.T) {
	badSelection := errors.New("installed Runtime failed validation")
	installFailure := errors.New("release query failed")
	for _, tc := range []struct {
		name, selected, wantSelection string
		startErr, installErr, wantErr error
		wantInstalls                  int
	}{
		{name: "first start installs the missing Runtime", wantSelection: "new", wantInstalls: 1},
		{name: "cold startup uses the selected local Runtime", selected: "local", wantSelection: "local"},
		{name: "bad selection does not trigger an installation", selected: "bad", startErr: badSelection, wantErr: badSelection, wantSelection: "bad"},
		{name: "failed first installation is reported", installErr: installFailure, wantErr: installFailure, wantInstalls: 1},
		{name: "failed start after first installation is reported", startErr: badSelection, wantErr: badSelection, wantSelection: "new", wantInstalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &startupRuntimeFixture{selected: tc.selected, startErr: tc.startErr, installErr: tc.installErr}
			if err := startRuntimeForStartup(context.Background(), m); !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
			if m.selected != tc.wantSelection || m.installs != tc.wantInstalls {
				t.Fatalf("selected=%q installs=%d", m.selected, m.installs)
			}
		})
	}
}
