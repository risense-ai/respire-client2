package host

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
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

func receiveModelTestRange(t *testing.T, ctx context.Context, client *http.Client, url string, total int64, stall time.Duration) error {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "part")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reset := func() error { return file.Truncate(0) }
	return receiveModelRange(ctx, client, url, modelChunk{end: total - 1, total: total},
		0, file, make([]byte, 32), stall, reset, reset, func(int64) {})
}

func TestModelRequestBudgetCoversPreResponseConnectAndPreservesCause(t *testing.T) {
	proxyStarted := make(chan struct{})
	proxyClosed := make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CONNECT negotiates before ordinary response-header timeout starts.
		if r.Method != http.MethodConnect {
			t.Errorf("unexpected proxy request: %s", r.Method)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		close(proxyStarted)
		<-r.Context().Done()
		close(proxyClosed)
	}))
	defer proxy.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport.Proxy = http.ProxyURL(proxyURL)
	transport.ResponseHeaderTimeout = time.Second
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err = receiveModelTestRange(t, ctx, &http.Client{Transport: transport},
		"https://download.example.invalid/model", 4, 250*time.Millisecond)
	var wrapped *url.Error
	if !errors.Is(err, errModelHeaderStall) || !errors.As(err, &wrapped) {
		t.Fatalf("proxy negotiation timeout lost its origin: %v", err)
	}
	select {
	case <-proxyStarted:
	default:
		t.Fatal("fixture never reached the local proxy")
	}
	transport.CloseIdleConnections()
	select {
	case <-proxyClosed:
	case <-ctx.Done():
		t.Fatal("stalled proxy connection was not closed")
	}
}

func TestModelChunkGroupCancelsDetachedConnectOnReturn(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	guard, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("unexpected proxy request: %s", r.Method)
		}
		close(started)
		select {
		case <-r.Context().Done():
			close(stopped)
		case <-guard.Done():
		}
	}))
	defer proxy.Close()
	defer stop()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = http.ProxyURL(proxyURL)
	m := &Manager{modelHTTPClient: &http.Client{Transport: base}}
	ctx, cancel := context.WithCancel(guard)
	defer cancel()
	go func() {
		select {
		case <-started:
			cancel()
		case <-ctx.Done():
		}
	}()
	part := filepath.Join(t.TempDir(), "part")
	err = m.downloadChunks(ctx, "https://download.example.invalid/model", []string{part}, modelTransferSpec([]byte("good")), modelDownloadStall)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("group did not preserve caller cancel: %v", err)
	}
	select {
	case <-stopped:
	case <-guard.Done():
		t.Fatal("group left behind a detached proxy tunnel")
	}
}

func TestModelChunkGroupCancelsDetachedSOCKSOnReturn(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ended := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			ended <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		var greeting [3]byte
		if _, err = io.ReadFull(conn, greeting[:]); err != nil || greeting != [3]byte{5, 1, 0} {
			ended <- fmt.Errorf("unexpected SOCKS greeting %v: %v", greeting, err)
			cancel()
			return
		}
		cancel() // leave the established socket, but no SOCKS greeting reply
		var next [1]byte
		_, err = conn.Read(next[:])
		ended <- err
	}()
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = http.ProxyURL(&url.URL{Scheme: "socks5", Host: listener.Addr().String()})
	m := &Manager{modelHTTPClient: &http.Client{Transport: base}}
	guard, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	part := filepath.Join(t.TempDir(), "part")
	err = m.downloadChunks(guard, "https://download.example.invalid/model", []string{part}, modelTransferSpec([]byte("good")), modelDownloadStall)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("group did not preserve SOCKS request cancellation: %v", err)
	}
	if err := <-ended; !errors.Is(err, io.EOF) {
		t.Fatalf("abandoned SOCKS dial survived worker-group cleanup: %v", err)
	}
}

// The fixture only recognizes two local, synthetic attempts. The first
// SOCKS socket remains half-open; the second completes SOCKS and itself
// serves a tiny HTTP response, without forwarding to an external target.
func TestModelChunkCanRetryPastDetachedSOCKSHandshake(t *testing.T) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_ = listener.SetDeadline(deadline)

	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	firstWaiting := make(chan struct{})
	first := func() error {
		conn, err := listener.AcceptTCP()
		if err != nil {
			return err
		}
		defer conn.Close()
		_ = conn.SetDeadline(deadline)
		var greeting [3]byte
		if _, err := io.ReadFull(conn, greeting[:]); err != nil || greeting != [3]byte{5, 1, 0} {
			return fmt.Errorf("first SOCKS greeting %v: %v", greeting, err)
		}
		close(firstWaiting)
		var unused [1]byte
		n, err := conn.Read(unused[:])
		if n != 0 || !errors.Is(err, io.EOF) {
			return fmt.Errorf("half-open SOCKS connection did not quiesce: %d, %v", n, err)
		}
		return nil
	}
	go func() { firstDone <- first() }()

	second := func() error {
		select {
		case <-firstWaiting:
		case <-ctx.Done():
			return ctx.Err()
		}
		conn, err := listener.AcceptTCP()
		if err != nil {
			return err
		}
		defer conn.Close()
		_ = conn.SetDeadline(deadline)
		r := bufio.NewReader(conn)
		var greeting [3]byte
		if _, err := io.ReadFull(r, greeting[:]); err != nil || greeting != [3]byte{5, 1, 0} {
			return fmt.Errorf("second SOCKS greeting %v: %v", greeting, err)
		}
		if _, err := conn.Write([]byte{5, 0}); err != nil {
			return err
		}
		var connect [5]byte
		if _, err := io.ReadFull(r, connect[:]); err != nil || !bytes.Equal(connect[:4], []byte{5, 1, 0, 3}) {
			return fmt.Errorf("SOCKS connect header %v: %v", connect, err)
		}
		target := make([]byte, int(connect[4])+2)
		if _, err := io.ReadFull(r, target); err != nil || string(target[:len(target)-2]) != "download.example.invalid" {
			return fmt.Errorf("SOCKS connect target %q: %v", target, err)
		}
		if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80}); err != nil {
			return err
		}
		req, err := http.ReadRequest(r)
		if err != nil {
			return err
		}
		_ = req.Body.Close()
		if req.Method != http.MethodGet || req.URL.Path != "/model" || req.Header.Get("Range") != "bytes=0-3" {
			return fmt.Errorf("unexpected tunneled request: %s %s %q", req.Method, req.URL.Path, req.Header.Get("Range"))
		}
		select {
		case <-firstDone:
			return errors.New("fixture lost the first SOCKS connection before the retry succeeded")
		default:
		}
		_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 4\r\nConnection: close\r\n\r\ngood")
		return err
	}
	go func() { secondDone <- second() }()

	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = http.ProxyURL(&url.URL{Scheme: "socks5", Host: listener.Addr().String()})
	m := &Manager{modelHTTPClient: &http.Client{Transport: base}}
	part := filepath.Join(t.TempDir(), "part")
	if err := m.downloadChunks(ctx, "http://download.example.invalid/model", []string{part},
		modelTransferSpec([]byte("good")), 200*time.Millisecond); err != nil {
		t.Fatalf("healthy retry remained stuck behind detached SOCKS setup: %v", err)
	}
	if data, err := os.ReadFile(part); err != nil || string(data) != "good" {
		t.Fatalf("unexpected transferred part %q: %v", data, err)
	}
	for _, ended := range []<-chan error{secondDone, firstDone} {
		select {
		case err := <-ended:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("SOCKS fixture did not quiesce")
		}
	}
}

func TestModelTransferDoesNotRetryPermanentProxyConnectStatus(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("unexpected proxy request: %s", r.Method)
		}
		calls.Add(1)
		w.WriteHeader(http.StatusProxyAuthRequired)
	}))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := (&Manager{}).modelClient()
	defer client.CloseIdleConnections()
	client.Transport.(*http.Transport).Proxy = http.ProxyURL(proxyURL)
	m := &Manager{modelHTTPClient: client}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	dest := filepath.Join(t.TempDir(), "model.ort")
	err = m.downloadModelFrom(ctx, "https://download.example.invalid/model", dest, modelTransferSpec([]byte("good")))
	var status *modelHTTPError
	if !errors.As(err, &status) || !status.proxy || status.status != http.StatusProxyAuthRequired || calls.Load() != 1 {
		t.Fatalf("permanent proxy HTTP status was lost or retried: %v, attempts=%d", err, calls.Load())
	}
}

func TestModelResponseProgressMayOutliveIdleBudgetAndKeepsConnectionReusable(t *testing.T) {
	const size = 24
	payload := []byte(strings.Repeat("x", size))
	var calls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		if calls.Add(1) != 1 {
			_, _ = w.Write(payload)
			return
		}
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for _, ch := range payload {
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
			}
			if _, err := w.Write([]byte{ch}); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	defer origin.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	const stall = 250 * time.Millisecond
	client := (&Manager{}).modelClient()
	defer client.CloseIdleConnections()
	start := time.Now()
	if err := receiveModelTestRange(t, ctx, client, origin.URL, size, stall); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < stall {
		t.Fatalf("test must actually run beyond the idle budget: %s", elapsed)
	}
	var reused atomic.Bool
	traced := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) },
	})
	if err := receiveModelTestRange(t, traced, client, origin.URL, size, stall); err != nil || !reused.Load() {
		t.Fatalf("successful response must keep HTTP connection reusable: reused=%v err=%v", reused.Load(), err)
	}
}

func TestModelStallOnlyCancelsItsOwnHTTP2Stream(t *testing.T) {
	stalledStarted := make(chan struct{})
	stalledClosed := make(chan struct{})
	const size = 24
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("fixture requires HTTP/2, got %s", r.Proto)
		}
		if r.URL.Path == "/stalled" {
			w.Header().Set("Content-Length", "4")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			close(stalledStarted)
			<-r.Context().Done()
			close(stalledClosed)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(size))
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for range size {
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
			}
			if _, err := w.Write([]byte("x")); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	origin.EnableHTTP2 = true
	origin.StartTLS()
	defer origin.Close()
	transport := origin.Client().Transport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	const stall = 250 * time.Millisecond
	stalled := make(chan error, 1)
	file, err := os.CreateTemp(t.TempDir(), "stalled")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reset := func() error { return file.Truncate(0) }
	go func() {
		stalled <- receiveModelRange(ctx, client, origin.URL+"/stalled", modelChunk{end: 3, total: 4},
			0, file, make([]byte, 32), stall, reset, reset, func(int64) {})
	}()
	select {
	case <-stalledStarted:
	case <-ctx.Done():
		t.Fatal("stalled stream did not reach the fixture")
	}
	var reused atomic.Bool
	traced := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) },
	})
	if err := receiveModelTestRange(t, traced, client, origin.URL+"/progress", size, stall); err != nil || !reused.Load() {
		t.Errorf("progressing peer lost its shared TLS connection: reused=%v err=%v", reused.Load(), err)
	}
	select {
	case err := <-stalled:
		if !errors.Is(err, errModelReadStall) {
			t.Errorf("peer activity hid the read stall: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("stalled stream did not return")
	}
	select {
	case <-stalledClosed:
	case <-ctx.Done():
		t.Fatal("stalled stream remained open")
	}
}

func TestModelGroupCleanupDoesNotTouchPeerColdHTTP2Connection(t *testing.T) {
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("fixture requires HTTP/2, got %s", r.Proto)
		}
		if r.URL.Path == "/denied" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("good"))
	}))
	origin.EnableHTTP2 = true
	origin.StartTLS()
	defer origin.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	m := &Manager{modelHTTPClient: origin.Client()}
	peer := m.modelClient()
	defer peer.CloseIdleConnections()
	arrived := make(chan struct{})
	resume := make(chan struct{})
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	traced := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			if info.Reused {
				t.Error("fixture must reserve a new HTTP/2 connection")
			}
			select {
			case <-arrived:
				t.Error("cold peer unexpectedly retried its connection")
				return
			default:
				close(arrived)
			}
			select {
			case <-resume:
			case <-ctx.Done():
			}
		},
	})
	done := make(chan error, 1)
	go func() { done <- receiveModelTestRange(t, traced, peer, origin.URL+"/good", 4, 3*time.Second) }()
	select {
	case <-arrived:
	case <-ctx.Done():
		t.Fatal("peer did not reserve a connection")
	}
	part := filepath.Join(t.TempDir(), "part")
	err := m.downloadChunks(ctx, origin.URL+"/denied", []string{part}, modelTransferSpec([]byte("good")), modelDownloadStall)
	var status *modelHTTPError
	if !errors.As(err, &status) || status.status != http.StatusForbidden {
		t.Fatalf("unexpected failing group result: %v", err)
	}
	close(resume)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a different group closed the cold HTTP/2 peer: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("healthy peer did not finish")
	}
}

func TestModelRequestHonorsCallerDeadlineEvenWithProgressBudget(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4")
		_, _ = w.Write([]byte("g"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer origin.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Millisecond)
	defer cancel()
	err := receiveModelTestRange(t, ctx, origin.Client(), origin.URL, 4, time.Second)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errModelReadStall) {
		t.Fatalf("caller deadline lost: %v", err)
	}
}

func TestModelTransferDoesNotDrainPermanentErrorBody(t *testing.T) {
	cancelled := make(chan struct{})
	var calls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
	}))
	defer origin.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	m := &Manager{modelHTTPClient: origin.Client()}
	dest := filepath.Join(t.TempDir(), "model.ort")
	err := m.downloadModelFrom(ctx, origin.URL, dest, modelTransferSpec([]byte("good")))
	var status *modelHTTPError
	if !errors.As(err, &status) || status.status != http.StatusForbidden || calls.Load() != 1 {
		t.Fatalf("permanent HTTP failure was lost or retried: %v, attempts=%d", err, calls.Load())
	}
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("rejected response was left open")
	}
}

func TestModelTransferPreservesBackoffAndRetriesTransientStatuses(t *testing.T) {
	var calls atomic.Int32
	started := make(chan time.Time, 3)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- time.Now()
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			_, _ = w.Write([]byte("good"))
		}
	}))
	defer origin.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	m := &Manager{modelHTTPClient: origin.Client()}
	dest := filepath.Join(t.TempDir(), "model.ort")
	if err := m.downloadModelFrom(ctx, origin.URL, dest, modelTransferSpec([]byte("good"))); err != nil || calls.Load() != 3 {
		t.Fatalf("transient retry failed: %v, attempts=%d", err, calls.Load())
	}
	first, second, third := <-started, <-started, <-started
	if second.Sub(first) < 95*time.Millisecond || third.Sub(second) < 195*time.Millisecond {
		t.Fatalf("missing exponential pacing: first %s, second %s", second.Sub(first), third.Sub(second))
	}
}

func TestModelChunkFailureCancelsSiblingImmediately(t *testing.T) {
	sibling := make(chan struct{})
	cancelled := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-1" {
			w.Header().Set("Content-Range", "bytes 0-1/4")
			w.WriteHeader(http.StatusPartialContent)
			w.(http.Flusher).Flush()
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
	err := m.downloadChunks(ctx, origin.URL, []string{filepath.Join(root, "0"), filepath.Join(root, "1")}, modelTransferSpec([]byte("GOOD")), modelDownloadStall)
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
