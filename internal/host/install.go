package host

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

func extractRuntime(archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	var total int64
	for {
		entry, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(entry.Name, "./")
		name = strings.TrimSuffix(name, "/")
		if name == "" || name == "." {
			if entry.Typeflag == tar.TypeDir {
				continue
			}
			return errors.New("invalid archive entry")
		}
		if strings.ContainsAny(name, "\\:") || !filepath.IsLocal(filepath.FromSlash(name)) {
			return errors.New("unsafe archive path")
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		switch entry.Typeflag {
		case tar.TypeDir:
			if err = os.MkdirAll(target, 0700); err != nil {
				return err
			}
		case tar.TypeReg:
			if entry.Size < 0 || entry.Size > 256<<20 || total+entry.Size > 512<<20 {
				return errors.New("Runtime archive exceeds extraction limit")
			}
			total += entry.Size
			if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			mode := os.FileMode(0600)
			if entry.Mode&0111 != 0 {
				mode = 0700
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			_, err = io.CopyN(out, reader, entry.Size)
			closeErr := out.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return errors.New("Runtime archive contains unsupported links or special files")
		}
	}
}

func verifyRuntime(dir, binaryHash string) error {
	hash, err := fileHash(filepath.Join(dir, runtimeBinary()))
	if err != nil {
		return err
	}
	if hash != binaryHash {
		return errors.New("installed CLI hash mismatch")
	}
	if err := verifyExecutable(filepath.Join(dir, runtimeBinary())); err != nil {
		return err
	}
	cliTarget, err := runtimeTarget()
	if err != nil {
		return err
	}
	var manifest struct {
		Schema int    `json:"schema_version"`
		Target string `json:"target"`
		Files  []struct {
			Path string `json:"path"`
			Hash string `json:"sha256"`
		} `json:"files"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "core-runtime.json"))
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, &manifest); err != nil {
		return err
	}
	if manifest.Schema != 1 || manifest.Target != cliTarget || len(manifest.Files) == 0 {
		return errors.New("invalid core-runtime manifest")
	}
	for _, file := range manifest.Files {
		if strings.ContainsAny(file.Path, "\\:") || !filepath.IsLocal(filepath.FromSlash(file.Path)) || !validHash(file.Hash) {
			return errors.New("unsafe runtime manifest file")
		}
		hash, err := fileHash(filepath.Join(dir, filepath.FromSlash(file.Path)))
		if err != nil {
			return err
		}
		if hash != file.Hash {
			return fmt.Errorf("runtime checksum mismatch: %s", file.Path)
		}
	}
	return nil
}

// stageRuntime publishes a complete immutable version before lifecycle changes begin.
func (m *Manager) stageRuntime(ctx context.Context, release githubRelease, meta cliMetadata) error {
	dest := m.runtimeDir(meta.Version)
	if _, err := os.Stat(dest); err == nil {
		return verifyRuntime(dest, meta.BinaryHash)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	versions := filepath.Join(m.Root, "runtime")
	if err := os.MkdirAll(versions, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(versions, ".download-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	binURL, err := assetURL(release, meta.Binary)
	if err != nil {
		return err
	}
	archiveURL, err := assetURL(release, meta.Runtime)
	if err != nil {
		return err
	}
	if err = m.download(ctx, binURL, filepath.Join(stage, runtimeBinary()), meta.BinaryHash, 128<<20); err != nil {
		return err
	}
	if err = os.Chmod(filepath.Join(stage, runtimeBinary()), 0700); err != nil {
		return err
	}
	archive := filepath.Join(stage, "runtime.tar.gz")
	if err = m.download(ctx, archiveURL, archive, meta.RuntimeHash, 128<<20); err != nil {
		return err
	}
	m.progress("校验并准备 Runtime", 0, 0)
	if err = extractRuntime(archive, stage); err != nil {
		return err
	}
	if err = verifyRuntime(stage, meta.BinaryHash); err != nil {
		return err
	}
	if err = os.Remove(archive); err != nil {
		return err
	}
	return os.Rename(stage, dest)
}

func (m *Manager) InstallRuntime(automatic bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	m.progress("检查 Runtime 发布", 0, 0)
	release, meta, err := m.LatestRuntime(ctx)
	if err != nil {
		return err
	}
	previous, prevErr := m.Installed()
	if prevErr != nil && !errors.Is(prevErr, os.ErrNotExist) {
		return prevErr
	}
	if prevErr == nil && semver.Compare("v"+previous.Version, "v"+meta.Version) >= 0 {
		return m.Start()
	}
	h, _, err := m.Health(ctx)
	if err == nil && !m.Owns(h.Exe) {
		return errors.New("已连接其他 CLI 管理的 Runtime；未接管或更新它。请先停止现有 Runtime，再安装")
	}
	if err != nil && !IsUnavailable(err) {
		return err
	}
	if err = m.stageRuntime(ctx, release, meta); err != nil {
		return err
	}
	// Check activity after downloading; work may have arrived during the download.
	h, body, err := m.Health(ctx)
	if err != nil && !IsUnavailable(err) {
		return err
	}
	if err == nil && !m.Owns(h.Exe) {
		return errors.New("Runtime 管理者已改变，未执行版本切换")
	}
	if automatic && err == nil {
		var load struct {
			RPC struct {
				Running *int `json:"executing_until_reply"`
				Queued  *int `json:"queued_or_cancelled_pending_removal"`
			} `json:"rpc"`
		}
		if err = json.Unmarshal(body, &load); err != nil {
			return err
		}
		if load.RPC.Running == nil || load.RPC.Queued == nil {
			return errors.New("Runtime 未报告队列状态，无法确认空闲")
		}
		if *load.RPC.Running > 0 || *load.RPC.Queued > 0 {
			return errors.New("Runtime 正忙，本次自动更新已延后；已下载版本会保留")
		}
	}
	m.progress("等待 Runtime 停止", 0, 0)
	if err = m.Stop(true); err != nil {
		return err
	}
	next := InstalledRuntime{Version: meta.Version, SHA256: meta.BinaryHash}
	if err = writeJSON(filepath.Join(m.Root, "runtime.json"), next); err != nil {
		if prevErr == nil {
			err = errors.Join(err, m.Start())
		}
		return err
	}
	err = m.Start()
	if err == nil {
		err = m.requireActive(next)
	}
	if err == nil {
		return nil
	}
	if prevErr != nil {
		return err
	}
	return errors.Join(err, m.restoreRuntime(previous, next))
}
