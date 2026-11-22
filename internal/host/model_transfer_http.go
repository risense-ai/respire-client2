package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type modelRangeError struct{ reason string }

func (e *modelRangeError) Error() string { return e.reason }

func receiveModelRange(ctx context.Context, client *http.Client, url string, chunk modelChunk, have int64, file *os.File, buf []byte, stall time.Duration, reset, rollback func() error, report func(int64)) error {
	requestCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		return &modelRangeError{err.Error()}
	}
	start := chunk.start + have
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, chunk.end))
	req.Header.Set("Accept-Encoding", "identity")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
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
	// Cancel transport reads even if the remote has only sent headers. Only
	// actual bytes reset the idle clock; this is not a whole-download timeout.
	touch := watchModelReads(requestCtx, cancel, stall)
	for {
		n, readErr := res.Body.Read(buf)
		if n > 0 {
			touch()
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

func watchModelReads(ctx context.Context, cancel context.CancelCauseFunc, stall time.Duration) func() {
	activity := make(chan struct{}, 1)
	go func() {
		timer := time.NewTimer(stall)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-activity:
				timer.Reset(stall)
			case <-timer.C:
				select {
				case <-activity:
					timer.Reset(stall)
				default:
					cancel(errModelReadStall)
					return
				}
			}
		}
	}()
	return func() {
		select {
		case activity <- struct{}{}:
		default:
		}
	}
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
