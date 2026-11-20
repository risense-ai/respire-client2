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

func TestStartRuntimeForStartup(t *testing.T) {
	badSelection := errors.New("installed Runtime failed validation")
	for _, tc := range []struct {
		name                string
		prepareErr, wantErr error
	}{
		{name: "uses the selected local Runtime"},
		{name: "missing selection is left for explicit installation", prepareErr: host.ErrRuntimeNotInstalled},
		{name: "bad local selection is reported", prepareErr: badSelection, wantErr: badSelection},
	} {
		t.Run(tc.name, func(t *testing.T) {
			preparations := 0
			err := startRuntimeForStartup(context.Background(), func(context.Context) error {
				preparations++
				return tc.prepareErr
			})
			if !errors.Is(err, tc.wantErr) || preparations != 1 {
				t.Fatalf("got error %v and %d preparations, want %v and one preparation", err, preparations, tc.wantErr)
			}
		})
	}
}
