package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

type modelRangeError struct{ reason string }

func (e *modelRangeError) Error() string { return e.reason }

func receiveModelRange(ctx context.Context, client *http.Client, url string, chunk modelChunk, have int64, file *os.File, buf []byte, stall time.Duration, reset, rollback func() error, report func(int64)) error {
	requestCtx, cancel := context.WithCancelCause(ctx)
	watch := newModelRequestWatchdog(stall, cancel)
	var body io.ReadCloser
	defer func() {
		watch.stop()
		// On rejected or incomplete responses, cancel before closing.
		// Don't wait for the server to drain an untrusted error body.
		// A clean EOF has already returned a reusable connection.
		cancel(nil)
		if body != nil {
			_ = body.Close()
		}
	}()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		return &modelRangeError{err.Error()}
	}
	start := chunk.start + have
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, chunk.end))
	req.Header.Set("Accept-Encoding", "identity")
	res, err := client.Do(req)
	if err != nil {
		return errors.Join(context.Cause(requestCtx), err)
	}
	body = res.Body
	watch.bodyProgress()
	switch res.StatusCode {
	case http.StatusOK:
		if chunk.end-chunk.start+1 != chunk.total {
			return errRangeUnsupported
		}
		start = 0
	case http.StatusPartialContent:
		expected := fmt.Sprintf("bytes %d-%d/%d", start, chunk.end, chunk.total)
		if got := res.Header.Get("Content-Range"); got != expected {
			return &modelRangeError{fmt.Sprintf("模型源 Content-Range 不符: %q，预期 %q", got, expected)}
		}
	default:
		return &modelHTTPError{status: res.StatusCode, text: res.Status}
	}
	remaining := chunk.end - start + 1
	if res.ContentLength >= 0 && res.ContentLength != remaining {
		return &modelRangeError{fmt.Sprintf("模型分段 Content-Length 不符: %d，预期 %d", res.ContentLength, remaining)}
	}
	if res.StatusCode == http.StatusOK {
		if err = reset(); err != nil {
			return &modelRangeError{err.Error()}
		}
		have = 0
		rollback = reset
	}
	if _, err = file.Seek(have, io.SeekStart); err != nil {
		return &modelRangeError{err.Error()}
	}
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			watch.bodyProgress()
			if cause := context.Cause(requestCtx); cause != nil {
				return cause
			}
			if int64(n) > remaining {
				return errors.Join(&modelRangeError{"模型源发送了超出分段范围的数据"}, rollback())
			}
			if _, err = file.Write(buf[:n]); err != nil {
				return &modelRangeError{err.Error()}
			}
			remaining -= int64(n)
			report(int64(n))
		}
		if readErr == io.EOF {
			if remaining == 0 {
				return nil
			}
			return io.ErrUnexpectedEOF
		}
		if readErr != nil {
			cause := context.Cause(requestCtx)
			// If all bytes arrived but the HTTP stream failed instead of
			// ending cleanly, retry that part rather than trusting the stream.
			if remaining == 0 {
				return errors.Join(cause, readErr, rollback())
			}
			return errors.Join(cause, readErr)
		}
	}
}

func preserveModelProxyStatus(transport *http.Transport) {
	// net/http otherwise turns a refused CONNECT into a reason string,
	// losing the HTTP code needed to avoid retrying permanent 4xx.
	onProxyResponse := transport.OnProxyConnectResponse
	transport.OnProxyConnectResponse = func(ctx context.Context, proxy *url.URL, request *http.Request, response *http.Response) error {
		if onProxyResponse != nil {
			if err := onProxyResponse(ctx, proxy, request, response); err != nil {
				return err
			}
		}
		if response.StatusCode != http.StatusOK {
			return &modelHTTPError{status: response.StatusCode, text: response.Status, proxy: true}
		}
		return nil
	}

}

// One request owns its own idle clock. Before the first accepted response
// headers it also covers proxy negotiation and redirects, including paths
// where http.Transport's ResponseHeaderTimeout does not yet apply.
type modelRequestWatchdog struct {
	mu       sync.Mutex
	timer    *time.Timer
	stall    time.Duration
	deadline time.Time
	cause    error
	done     bool
}

func newModelRequestWatchdog(stall time.Duration, cancel context.CancelCauseFunc) *modelRequestWatchdog {
	w := &modelRequestWatchdog{
		stall: stall, deadline: time.Now().Add(stall), cause: errModelHeaderStall,
	}
	w.mu.Lock()
	w.timer = time.AfterFunc(stall, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.done {
			return
		}
		if remaining := time.Until(w.deadline); remaining > 0 {
			w.timer.Reset(remaining)
			return
		}
		w.done = true
		cancel(w.cause)
	})
	w.mu.Unlock()
	return w
}

func (w *modelRequestWatchdog) bodyProgress() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.done {
		// The first headers move the clock into the body phase. Subsequent
		// bodies advance it only when the transport returns actual bytes.
		w.deadline = time.Now().Add(w.stall)
		w.cause = errModelReadStall
	}
}

func (w *modelRequestWatchdog) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.done = true
	w.timer.Stop()
}

func openModelPart(path string, resume bool) (*os.File, error) {
	if err := regularModelPart(path); err != nil {
		return nil, err
	}
	flags := os.O_WRONLY | os.O_CREATE
	if !resume {
		flags |= os.O_TRUNC
	}
	return os.OpenFile(path, flags, 0600)
}

func openModelPartRead(path string) (*os.File, error) {
	if err := regularModelPart(path); err != nil {
		return nil, err
	}
	return os.Open(path)
}

func regularModelPart(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("模型下载暂存路径不是常规文件: %s", path)
	}
	return nil
}
