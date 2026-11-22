package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func modelTransferSpec(data []byte) modelFileSpec {
	digest := sha256.Sum256(data)
	return modelFileSpec{rel: "model.ort", size: int64(len(data)), sha256: hex.EncodeToString(digest[:])}
}

func TestModelTransfer200DiscardsPartialBytes(t *testing.T) {
	good := []byte("complete model")
	spec := modelTransferSpec(good)
	dest := filepath.Join(t.TempDir(), spec.rel)
	if err := os.WriteFile(dest+".download.p0", []byte("OLD"), 0600); err != nil {
		t.Fatal(err)
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := fmt.Sprintf("bytes=3-%d", spec.size-1); r.Header.Get("Range") != want {
			t.Errorf("resume must request %s, received %s", want, r.Header.Get("Range"))
		}
		_, _ = w.Write(good)
	}))
	defer origin.Close()
	m := &Manager{modelHTTPClient: origin.Client()}
	if err := m.downloadModelFrom(t.Context(), origin.URL, dest, spec); err != nil {
		t.Fatal(err)
	}
	if !modelFileValid(dest, spec) {
		t.Fatal("200 fallback appended stale bytes")
	}
}

func TestModelTransferParallelFallsBackToSingleFile(t *testing.T) {
	good := bytes.Repeat([]byte{'x'}, 32<<20)
	spec := modelTransferSpec(good)
	dest := filepath.Join(t.TempDir(), spec.rel)
	var single atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-33554431" {
			single.Add(1)
			w.Header().Set("Content-Length", fmt.Sprint(spec.size))
			_, _ = w.Write(good)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer origin.Close()
	m := &Manager{modelHTTPClient: origin.Client()}
	if err := m.downloadModelFrom(t.Context(), origin.URL, dest, spec); err != nil {
		t.Fatal(err)
	}
	if single.Load() != 1 || !modelFileValid(dest, spec) {
		t.Fatalf("expected one single-file fallback; got %d", single.Load())
	}
}

func TestModelTransferRejectsMismatchedRangeAndPermanent4xx(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{{http.StatusPartialContent, "Content-Range"}, {http.StatusNotFound, "404"}} {
		t.Run(tc.want, func(t *testing.T) {
			var calls atomic.Int64
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Range", "bytes 1-3/4")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("bad"))
			}))
			defer origin.Close()
			m := &Manager{modelHTTPClient: origin.Client()}
			dest := filepath.Join(t.TempDir(), "model.ort")
			err := m.downloadModelFrom(t.Context(), origin.URL, dest, modelTransferSpec([]byte("GOOD")))
			if err == nil || !strings.Contains(err.Error(), tc.want) || calls.Load() != 1 {
				t.Fatalf("wrong failure/retry: %v, attempts=%d", err, calls.Load())
			}
			if data, err := os.ReadFile(dest + ".download.p0"); err != nil || len(data) != 0 {
				t.Fatalf("untrusted response modified partial bytes: %q %v", data, err)
			}
		})
	}
}

func TestModelRangeWatchdogCancelsBodyAndRetainsCause(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-3/4")
		w.WriteHeader(http.StatusPartialContent)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer origin.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	file, err := os.CreateTemp(t.TempDir(), "part")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	action := func() error { return nil }
	err = receiveModelRange(ctx, origin.Client(), origin.URL,
		modelChunk{end: 3, total: 4}, 0, file, make([]byte, 32), 30*time.Millisecond,
		action, action, func(int64) {})
	if !errors.Is(err, errModelReadStall) {
		t.Fatalf("no read-stall cause: %v", err)
	}
}

func TestModelChunkFailureCancelsSiblingImmediately(t *testing.T) {
	sibling := make(chan struct{})
	cancelled := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-1" {
			close(sibling)
			<-r.Context().Done()
			close(cancelled)
			return
		}
		select {
		case <-sibling:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer origin.Close()
	m := &Manager{modelHTTPClient: origin.Client()}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	root := t.TempDir()
	err := m.downloadChunks(ctx, origin.URL, []string{filepath.Join(root, "0"), filepath.Join(root, "1")}, modelTransferSpec([]byte("GOOD")))
	var status *modelHTTPError
	if !errors.As(err, &status) || status.status != 403 {
		t.Fatalf("original failure lost: %v", err)
	}
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("sibling was not cancelled")
	}
}

func TestModelTransferRejectsSymlinkedStaging(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "model.ort")
	target := filepath.Join(t.TempDir(), "unrelated")
	if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, dest+".download.p0"); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	m := &Manager{}
	err := m.downloadModelFrom(t.Context(), "http://127.0.0.1", dest, modelTransferSpec([]byte("GOOD")))
	if err == nil {
		t.Fatal("symlink must not be opened")
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "untouched" {
		t.Fatalf("symlink target changed: %q, %v", got, err)
	}
}
