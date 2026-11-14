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
	"runtime"
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
		return errors.New("installed Runtime hash mismatch")
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
		path := filepath.Join(dir, filepath.FromSlash(file.Path))
		if strings.HasPrefix(file.Path, "core-notices/") {
			if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return err
			}
		}
		hash, err := fileHash(path)
		if err != nil {
			return err
		}
		if hash != file.Hash {
			return fmt.Errorf("runtime checksum mismatch: %s", file.Path)
		}
	}
	return nil
}

// publishRuntime promotes a verified Runtime tree into the immutable version
// directory. The fill callback materializes the candidate version inside a
// staging directory on the same filesystem as the runtime parent so the final
// rename is atomic; any interruption or failed verification leaves the
// existing installation untouched. An existing directory for the same version
// is only verified and reused, never merged.
func (m *Manager) publishRuntime(meta RuntimeMetadata, fill func(stage string) error) error {
	receipt, err := runtimeReceipt(meta)
	if err != nil {
		return err
	}
	dest := m.runtimeDir(meta.RuntimeVersion)
	if _, err := os.Stat(dest); err == nil {
		if err := verifyReleaseTree(dest, receipt); err != nil {
			return err
		}
		return m.saveRuntimeReceipt(receipt)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	versions := filepath.Dir(dest)
	if err := os.MkdirAll(versions, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(versions, ".stage-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err = fill(stage); err != nil {
		return err
	}
	m.progress("校验并准备 Runtime", 0, 0)
	if err = verifyRuntime(stage, meta.BinaryHash); err != nil {
		return err
	}
	clean, err := os.MkdirTemp(versions, ".verified-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(clean)
	if err = copyReleaseTree(stage, clean, receipt); err != nil {
		return err
	}
	if err = os.Rename(clean, dest); err != nil {
		return err
	}
	return m.saveRuntimeReceipt(receipt)
}

// stageDownloadRuntime materializes the online release into the staging
// directory: the raw binary plus the verified archive expansion.
func (m *Manager) stageDownloadRuntime(ctx context.Context, release ReleaseInfo, meta RuntimeMetadata) func(string) error {
	return func(stage string) error {
		binURL, err := ReleaseAssetURL(release, runtimeRepo, meta.Binary)
		if err != nil {
			return err
		}
		archiveURL, err := ReleaseAssetURL(release, runtimeRepo, runtimeArchive(meta.Target))
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
		if err = m.download(ctx, archiveURL, archive, meta.ArchiveHash, 128<<20); err != nil {
			return err
		}
		if err = extractRuntime(archive, stage); err != nil {
			return err
		}
		return os.Remove(archive)
	}
}

// installOnlineCLI resolves the same channel without coupling CLI/Runtime versions.
func (m *Manager) installOnlineCLI(ctx context.Context, compat Compat) error {
	release, meta, err := m.LatestCLIBuild(ctx)
	if err != nil {
		return err
	}
	url, err := ReleaseAssetURL(release, cliRepo, meta.Binary)
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp(m.Root, ".cli-download-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	source := filepath.Join(stage, cliBinary())
	if err = m.download(ctx, url, source, meta.BinaryHash, 128<<20); err != nil {
		return err
	}
	if meta.Schema == 2 {
		info, err := os.Stat(source)
		if err != nil {
			return err
		}
		if info.Size() != meta.BinarySize {
			return errors.New("CLI release binary size mismatch")
		}
	}
	return m.installFrontendCLI(ctx, source, meta.Version, meta.BinaryHash, compat)
}

func (m *Manager) InstallRuntime(automatic bool, restart bool) error {
	if err := m.MigrateRuntime(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	lock, err := m.lockRuntime(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	m.progress("检查 Runtime 发布", 0, 0)
	previous, prevErr := m.Installed()
	if prevErr != nil && !errors.Is(prevErr, os.ErrNotExist) {
		return prevErr
	}
	var release ReleaseInfo
	var meta RuntimeMetadata
	var payload *bootstrapPayload
	if embedded, payloadErr := readBootstrapPayload(); errors.Is(prevErr, os.ErrNotExist) && payloadErr == nil && embedded.Manifest.Channel == m.Preferences().RuntimeChannel {
		// Only bootstrap an absent installation from a matching channel.
		// Installed clients use release discovery so the bundle cannot pin updates.
		payload = &embedded
		meta = embedded.runtimeMetadata()
		if meta, err = m.provenRuntimeMetadata(ctx, meta); err != nil {
			return err
		}
	} else {
		// No valid payload (absent, foreign platform, or failed verification):
		// keep the documented online path so non-DMG builds behave unchanged.
		var err error
		release, meta, err = m.LatestRuntime(ctx)
		if err != nil {
			return err
		}
	}
	reuseRuntime := false
	if prevErr == nil && semver.Compare("v"+previous.Version, "v"+meta.RuntimeVersion) >= 0 {
		// Verify the selected version against its own receipt. A newer
		// version cannot share the incoming release's binary hash.
		if verifyErr := m.verifyInstalled(previous); verifyErr == nil {
			if previous.Version != meta.RuntimeVersion {
				// Bind CLI compatibility to the retained Runtime's published metadata.
				tag := "v" + previous.Version
				var retainedRelease ReleaseInfo
				if err := m.fetchJSON(ctx, "https://api.github.com/repos/risense-ai/"+runtimeRepo+"/releases/tags/"+tag, &retainedRelease); err != nil {
					return err
				}
				if retainedRelease.Tag != tag || retainedRelease.Draft {
					return errors.New("installed Runtime release identity mismatch")
				}
				url, err := ReleaseAssetURL(retainedRelease, runtimeRepo, "runtime-build-"+meta.Target+".json")
				if err != nil {
					return err
				}
				var raw json.RawMessage
				if err = m.fetchJSON(ctx, url, &raw); err != nil {
					return err
				}
				retainedMeta, err := NormalizeRuntimeMetadata(raw)
				if err != nil {
					return err
				}
				if err = CheckRuntimeMetadata(retainedMeta, tag, meta.Target); err != nil {
					return err
				}
				if retainedMeta.BinaryHash != previous.SHA256 {
					return errors.New("installed Runtime metadata does not match its receipt")
				}
				meta = retainedMeta
			}
			reuseRuntime = true
		} else if semver.Compare("v"+previous.Version, "v"+meta.RuntimeVersion) > 0 {
			return fmt.Errorf("installed newer Runtime verification failed: %w", verifyErr)
		}
	}
	h, _, err := m.Health(ctx)
	if err == nil && !m.Owns(h.Exe) {
		return errors.New("已连接其他 CLI 管理的 Runtime；未接管或更新它。请先停止现有 Runtime，再安装")
	}
	if err == nil && h.ManagedID != "" {
		return errors.New("Runtime 已由 CLI 的系统监督器管理，请通过 CLI 更新；客户端未停止或替换该实例")
	}
	if err != nil && !IsUnavailable(err) {
		return err
	}
	if !reuseRuntime {
		if payload != nil {
			err = m.publishRuntime(meta, stageBootstrapRuntime(payload.Dir))
		} else {
			err = m.publishRuntime(meta, m.stageDownloadRuntime(ctx, release, meta))
		}
		if err != nil {
			return err
		}
	}
	// Check activity after staging; work may have arrived during the copy.
	h, body, err := m.Health(ctx)
	if err != nil && !IsUnavailable(err) {
		return err
	}
	if err == nil && !m.Owns(h.Exe) {
		return errors.New("Runtime 管理者已改变，未执行版本切换")
	}
	if err == nil && h.ManagedID != "" {
		return errors.New("Runtime 已改由 CLI 的系统监督器管理，未执行版本切换")
	}
	if m.HasActiveOperations() {
		return errors.New("客户端业务操作尚未完成，Runtime 更新已延后；已下载版本会保留")
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
	if payload != nil {
		if err := m.installBootstrapCLI(ctx, payload); err != nil {
			return err
		}
	} else if err := m.installOnlineCLI(ctx, meta.Compat); err != nil {
		return err
	}
	if reuseRuntime {
		if err := m.Start(); err != nil {
			return err
		}
		return m.finishBootstrapPath(payload)
	}
	// Without restart the new files publish under their own version dir and
	// the running instance stays on the old one until the next start.
	if !restart {
		next := InstalledRuntime{Version: meta.RuntimeVersion, SHA256: meta.BinaryHash}
		if err = m.selectRuntime(next); err != nil {
			return err
		}
		return m.finishBootstrapPath(payload)
	}
	m.progress("等待 Runtime 停止", 0, 0)
	if err = m.Stop(true); err != nil {
		return err
	}
	next := InstalledRuntime{Version: meta.RuntimeVersion, SHA256: meta.BinaryHash}
	if err = m.selectRuntime(next); err != nil {
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
		return m.finishBootstrapPath(payload)
	}
	if prevErr != nil {
		return err
	}
	return errors.Join(err, m.restoreRuntime(previous, next))
}

// finishBootstrapPath configures the shell PATH once every component is
// verified and published; a failure surfaces as manual guidance without ever
// rolling back the verified components.
func (m *Manager) finishBootstrapPath(payload *bootstrapPayload) error {
	if payload == nil || runtime.GOOS == "windows" {
		return nil
	}
	m.progress("配置终端 PATH", 0, 0)
	return configureUserShellPath()
}
