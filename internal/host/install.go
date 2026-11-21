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

func verifyRuntime(dir, binaryHash, target string) error {
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
	if manifest.Schema != 1 || manifest.Target != target || len(manifest.Files) == 0 {
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
func (m *Manager) publishRuntime(meta RuntimeMetadata, fill func(stage string) error, reportProgress bool) error {
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
	if reportProgress {
		m.progress("校验并准备 Runtime", 0, 0)
	}
	if err = verifyRuntime(stage, meta.BinaryHash, meta.Target); err != nil {
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
func (m *Manager) stageDownloadRuntime(ctx context.Context, release ReleaseInfo, meta RuntimeMetadata, reportProgress bool) func(string) error {
	return func(stage string) error {
		binURL, err := ReleaseAssetURL(release, runtimeRepo, meta.Binary)
		if err != nil {
			return err
		}
		archiveURL, err := ReleaseAssetURL(release, runtimeRepo, runtimeArchive(meta.Target))
		if err != nil {
			return err
		}
		if err = m.download(ctx, binURL, filepath.Join(stage, runtimeBinary()), meta.BinaryHash, 128<<20, reportProgress); err != nil {
			return err
		}
		if err = os.Chmod(filepath.Join(stage, runtimeBinary()), 0700); err != nil {
			return err
		}
		archive := filepath.Join(stage, "runtime.tar.gz")
		if err = m.download(ctx, archiveURL, archive, meta.ArchiveHash, 128<<20, reportProgress); err != nil {
			return err
		}
		if err = extractRuntime(archive, stage); err != nil {
			return err
		}
		return os.Remove(archive)
	}
}

// installOnlineCLI resolves the same channel without coupling CLI/Runtime versions.
func (m *Manager) installOnlineCLI(ctx context.Context) error {
	release, meta, err := m.LatestCLIBuild(ctx)
	if err != nil {
		return err
	}
	url, err := ReleaseAssetURL(release, cliRepo, meta.Binary)
	if err != nil {
		return err
	}
	reuse, err := m.canReuseFrontendCLI(ctx, meta.Version, meta.BinaryHash)
	if err != nil || reuse {
		return err
	}
	stage, err := os.MkdirTemp(m.Root, ".cli-download-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	source := filepath.Join(stage, cliBinary())
	if err = m.download(ctx, url, source, meta.BinaryHash, 128<<20, true); err != nil {
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
	return m.installFrontendCLI(ctx, source, meta.Version, meta.BinaryHash)
}

// InstallCLI installs or updates the optional CLI for external integrations.
func (m *Manager) InstallCLI(ctx context.Context) error {
	m.cliInstallMu.Lock()
	defer m.cliInstallMu.Unlock()
	return m.installOnlineCLI(ctx)
}

func (m *Manager) InstallRuntime(automatic bool, restart bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	lock, err := m.lockRuntime(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()

	release, meta, err := m.LatestRuntime(ctx)
	if err != nil {
		return err
	}

	// Publishing a verified version directory does not require the lifecycle task.
	if err := m.publishRuntime(meta, m.stageDownloadRuntime(ctx, release, meta, !automatic), !automatic); err != nil {
		return err
	}
	previous, previousErr := m.Installed()
	selected := InstalledRuntime{Version: meta.RuntimeVersion, SHA256: meta.BinaryHash}
	if automatic && previousErr == nil && previous == selected && !restart {
		return nil
	}
	apply := func() error {
		if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) &&
			(automatic || !errors.Is(previousErr, errInvalidRuntimeSelection)) {
			return fmt.Errorf("已暂存 Runtime，但无法读取当前选择: %w", previousErr)
		}
		if automatic && errors.Is(previousErr, os.ErrNotExist) {
			return errRuntimeUpdateDeferred
		}
		if previousErr == nil {
			newerSelection := semver.Compare("v"+previous.Version, "v"+selected.Version) > 0
			if newerSelection || (automatic && previous != selected) {
				if err := m.verifyInstalled(previous); err != nil {
					return fmt.Errorf("已暂存 Runtime，但无法验证当前选择: %w", err)
				}
			}
			if newerSelection {
				if automatic {
					return errRuntimeUpdateDeferred
				}
				selected = previous
			}
		}
		return m.activateRuntimeRelease(ctx, selected, automatic, restart, previousErr == nil && previous == selected)
	}
	if automatic {
		return m.RunTask("更新 Runtime", apply)
	}
	return apply()
}

var errRuntimeUpdateDeferred = errors.New("Runtime update deferred while requests are active")

func (m *Manager) runtimeMatchesRelease(h HealthInfo, selected InstalledRuntime) bool {
	return h.Version == selected.Version && h.RuntimeVersion == selected.Version && h.ImageSHA256 == selected.SHA256 &&
		samePath(h.InstallationRoot, m.DataRoot) &&
		samePath(h.Exe, filepath.Join(m.runtimeDir(selected.Version), runtimeBinary()))
}

func (m *Manager) activateRuntimeRelease(ctx context.Context, selected InstalledRuntime, automatic, restart, alreadySelected bool) error {
	if !automatic && !restart {
		return m.selectRuntime(selected)
	}

	healthCtx, cancelHealth := context.WithTimeout(ctx, 3*time.Second)
	h, body, err := m.Health(healthCtx)
	cancelHealth()
	if err == nil {
		if automatic && alreadySelected && m.runtimeMatchesRelease(h, selected) {
			return errRuntimeUpdateDeferred
		}
		if !m.Owns(h.Exe) || !samePath(h.InstallationRoot, m.DataRoot) {
			return errors.New("RPC 端口由其他 Runtime 实例使用，已保留暂存包")
		}
		if h.ManagedID != "" {
			return errors.New("Runtime 由 CLI 的系统监督器管理，请通过 CLI 切换版本")
		}
		if err := verifyHostProcess(h); err != nil {
			return fmt.Errorf("无法确认当前 Runtime 进程，已保留暂存包: %w", err)
		}
	} else if !IsUnavailable(err) {
		return err
	} else if automatic {
		return errRuntimeUpdateDeferred
	}
	if m.HasActiveOperations() {
		if automatic {
			return errRuntimeUpdateDeferred
		}
		return errors.New("客户端业务操作尚未完成，已保留暂存包")
	}
	if !restart {
		return m.selectRuntime(selected)
	}
	if automatic {
		var load struct {
			RPC struct {
				Running *int `json:"executing_until_reply"`
				Queued  *int `json:"queued_or_cancelled_pending_removal"`
			} `json:"rpc"`
		}
		if err := json.Unmarshal(body, &load); err != nil {
			return err
		}
		if load.RPC.Running == nil || load.RPC.Queued == nil {
			return errors.New("Runtime 未报告队列状态，无法确认空闲")
		}
		if *load.RPC.Running > 0 || *load.RPC.Queued > 0 {
			return errRuntimeUpdateDeferred
		}
	}
	if h.PID != 0 && !m.runtimeMatchesRelease(h, selected) {
		m.progress("等待 Runtime 停止", 0, 0)
		if err := m.stopManaged(ctx, h); err != nil {
			return err
		}
	}
	if err := m.selectRuntime(selected); err != nil {
		return err
	}
	startCtx, cancelStart := context.WithTimeout(ctx, 90*time.Second)
	defer cancelStart()
	active, err := m.startDetached(startCtx, selected)
	if err != nil {
		return fmt.Errorf("新版 Runtime 启动失败，已保留新版选择和原始错误: %w", err)
	}
	if !m.runtimeMatchesRelease(active, selected) {
		return errors.New("新版 Runtime 未通过启动验证，已保留新版选择")
	}
	return m.unlockIfNeeded(active)
}
