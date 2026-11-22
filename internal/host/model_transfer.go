package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const modelDownloadStall = 30 * time.Second

var (
	errRangeUnsupported = errors.New("下载源不支持分段并行")
	errModelReadStall   = errors.New("等待模型下载数据超时")
)

type modelHTTPError struct {
	status int
	text   string
}

func (e *modelHTTPError) Error() string { return "下载模型 HTTP " + e.text }

func (e *modelHTTPError) retryable() bool {
	return e.status == http.StatusRequestTimeout || e.status == http.StatusTooManyRequests || e.status >= 500
}

// downloadModelFrom retains independently resumable Range chunks or a single
// stream, never treating one layout's partial file as the other layout.
func (m *Manager) downloadModelFrom(ctx context.Context, url, dest string, spec modelFileSpec) error {
	if modelFileValid(dest, spec) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	tmp := dest + ".download"
	workers := 8
	if spec.size < 32<<20 {
		workers = 1
	}
	parts := make([]string, workers)
	for i := range parts {
		parts[i] = fmt.Sprintf("%s.p%d", tmp, i)
	}
	cleanupParts := append(append([]string{}, parts...), tmp+".single")
	err := m.downloadChunks(ctx, url, parts, spec)
	if errors.Is(err, errRangeUnsupported) && ctx.Err() == nil {
		parts = []string{tmp + ".single"}
		err = m.downloadChunks(ctx, url, parts, spec)
	}
	if err != nil {
		return err
	}

	err = m.mergeModelParts(ctx, tmp, dest, parts, spec)
	if err != nil {
		return err
	}
	for _, part := range cleanupParts {
		_ = os.Remove(part)
	}
	m.modelDownloadProgress("模型文件校验通过", 0, 0)
	return nil
}

func (m *Manager) mergeModelParts(ctx context.Context, tmp, dest string, parts []string, spec modelFileSpec) error {
	out, err := openModelPart(tmp, false)
	if err != nil {
		return err
	}
	// Remove the merge output on errors; non-corrupt the resumable parts.
	committed := false
	defer func() {
		_ = out.Close()
		if !committed {
			_ = os.Remove(tmp)
		}
	}()
	hasher := sha256.New()
	var merged int64
	writer := io.MultiWriter(out, hasher)
	base := filepath.Base(spec.rel)
	m.modelDownloadProgress("正在合并与校验 "+base, 0, spec.size)
	buf := make([]byte, 256<<10)
	for _, part := range parts {
		size, err := copyModelPart(ctx, writer, part, buf, func(n int64) {
			if n-merged >= 8<<20 || n == spec.size {
				m.modelDownloadProgress("正在合并与校验 "+base, n, spec.size)
			}
		}, merged)
		if err != nil {
			return err
		}
		merged += size
	}
	if err = out.Close(); err != nil {
		return err
	}
	if merged != spec.size || hex.EncodeToString(hasher.Sum(nil)) != spec.sha256 {
		verifyErr := fmt.Errorf("下载的 %s 大小或校验和不匹配", filepath.Base(dest))
		var cleanupErr error
		for _, part := range parts {
			cleanupErr = errors.Join(cleanupErr, os.Remove(part))
		}
		if cleanupErr != nil {
			return errors.Join(verifyErr, fmt.Errorf("%w: %w", errCorruptModelCleanup, cleanupErr))
		}
		return verifyErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err = os.Rename(tmp, dest); err != nil {
		return err
	}
	committed = true
	return nil
}

func copyModelPart(ctx context.Context, dest io.Writer, path string, buf []byte, progress func(int64), offset int64) (int64, error) {
	part, err := openModelPartRead(path)
	if err != nil {
		return 0, err
	}
	defer part.Close()
	var n int64
	last := offset
	for {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		size, readErr := part.Read(buf)
		if size != 0 {
			if _, err = dest.Write(buf[:size]); err != nil {
				return n, err
			}
			n += int64(size)
			if offset+n-last >= 8<<20 {
				last = offset + n
				progress(last)
			}
		}
		if readErr == io.EOF {
			progress(offset + n)
			return n, nil
		}
		if readErr != nil {
			return n, readErr
		}
	}
}

func (m *Manager) downloadChunks(ctx context.Context, url string, parts []string, spec modelFileSpec) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var done atomic.Int64
	client := m.modelClient()
	chunk := (spec.size + int64(len(parts)) - 1) / int64(len(parts))
	var wg sync.WaitGroup
	for i, part := range parts {
		start := int64(i) * chunk
		end := min(start+chunk, spec.size) - 1
		wg.Go(func() {
			err := m.downloadChunk(ctx, client, url, modelChunk{
				path: part, base: filepath.Base(spec.rel), start: start, end: end, total: spec.size,
			}, &done, modelDownloadStall)
			if err != nil {
				cancel(err) // first real failure wins, not faster sibling cancellation
			}
		})
	}
	wg.Wait()
	return context.Cause(ctx)
}

type modelChunk struct {
	path, base        string
	start, end, total int64
}

func (m *Manager) downloadChunk(ctx context.Context, client *http.Client, url string, chunk modelChunk, done *atomic.Int64, stall time.Duration) error {
	partTotal := chunk.end - chunk.start + 1
	file, err := openModelPart(chunk.path, true)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	have := info.Size()
	if have > partTotal {
		if err := file.Truncate(0); err != nil {
			return err
		}
		have = 0
	}
	lastReport := done.Add(have)
	defer func() {
		if now := done.Load(); now == chunk.total {
			m.modelDownloadProgress("下载 "+chunk.base, now, chunk.total)
		}
	}()
	report := func(delta int64) {
		have += delta
		now := done.Add(delta)
		if now-lastReport >= 8<<20 || now == chunk.total {
			lastReport = now
			m.modelDownloadProgress("下载 "+chunk.base, now, chunk.total)
		}
	}

	buf := make([]byte, 256<<10)
	var lastErr error
	for attempt := 0; have < partTotal && attempt < 6; attempt++ {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			case <-time.After(100 * time.Millisecond << (attempt - 1)):
			}
		}
		reset := func() error {
			if err := file.Truncate(0); err != nil {
				return err
			}
			report(-have)
			return nil
		}
		before := have
		rollback := func() error {
			if err := file.Truncate(before); err != nil {
				return err
			}
			report(before - have)
			return nil
		}
		lastErr = receiveModelRange(ctx, client, url, chunk, have, file, buf, stall, reset, rollback, report)
		if lastErr == nil {
			return nil
		}
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		var status *modelHTTPError
		var invalid *modelRangeError
		if errors.Is(lastErr, errRangeUnsupported) || errors.As(lastErr, &invalid) ||
			(errors.As(lastErr, &status) && !status.retryable()) {
			return lastErr
		}
	}
	if have >= partTotal && lastErr == nil {
		return nil
	}
	return fmt.Errorf("分段下载未完成（%d / %d）: %w", have, partTotal, lastErr)
}
