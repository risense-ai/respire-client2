package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type releaseProof struct {
	Hash string `json:"metadata_sha256"`
	JSON string `json:"metadata_json"`
}

type sharedRuntimeReceipt struct {
	Version string        `json:"version"`
	SHA256  string        `json:"sha256"`
	Release *releaseProof `json:"release,omitempty"`
}

type releaseFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Hash string `json:"sha256"`
}

func (m *Manager) runtimeManifest() string { return filepath.Join(m.DataRoot, "bin", "runtime.json") }
func (m *Manager) runtimeReceiptPath(v string) string {
	return filepath.Join(m.DataRoot, "bin", "runtime", ".receipts", v+".json")
}

func (m *Manager) saveRuntimeReceipt(receipt sharedRuntimeReceipt) error {
	path := m.runtimeReceiptPath(receipt.Version)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return writeJSON(path, receipt)
}

func runtimeReceipt(meta RuntimeMetadata) (sharedRuntimeReceipt, error) {
	sum := sha256.Sum256([]byte(meta.ProofJSON))
	receipt := sharedRuntimeReceipt{Version: meta.RuntimeVersion, SHA256: meta.BinaryHash,
		Release: &releaseProof{Hash: hex.EncodeToString(sum[:]), JSON: meta.ProofJSON}}
	_, err := receiptFiles(receipt)
	return receipt, err
}

func receiptFiles(receipt sharedRuntimeReceipt) ([]releaseFile, error) {
	if !validVersion(receipt.Version) || !validHash(receipt.SHA256) || receipt.Release == nil || len(receipt.Release.JSON) > 2<<20 {
		return nil, errors.New("Runtime 缺少完整发布凭据，请重新安装可信发布包")
	}
	sum := sha256.Sum256([]byte(receipt.Release.JSON))
	if !strings.EqualFold(hex.EncodeToString(sum[:]), receipt.Release.Hash) {
		return nil, errors.New("Runtime 发布凭据摘要不匹配")
	}
	meta, err := NormalizeRuntimeMetadata([]byte(receipt.Release.JSON))
	if err != nil {
		return nil, err
	}
	target, err := runtimeTarget()
	if err != nil {
		return nil, err
	}
	if err = CheckRuntimeMetadata(meta, "v"+receipt.Version, target); err != nil {
		return nil, err
	}
	if meta.Schema != 2 || !strings.EqualFold(meta.BinaryHash, receipt.SHA256) {
		return nil, errors.New("Runtime 发布凭据与版本选择不匹配")
	}
	var inventory struct {
		Files    []releaseFile `json:"files"`
		Commit   string        `json:"source_commit"`
		Protocol struct {
			Minimum int `json:"minimum"`
			Maximum int `json:"maximum"`
		} `json:"protocol_range"`
		Capabilities map[string]string `json:"capabilities"`
	}
	if err = json.Unmarshal([]byte(receipt.Release.JSON), &inventory); err != nil {
		return nil, err
	}
	if len(inventory.Commit) != 40 || inventory.Protocol.Minimum != 1 || inventory.Protocol.Maximum < 1 || inventory.Capabilities["business_executor"] != "1" || inventory.Capabilities["mcp_server"] != "1" || len(inventory.Files) == 0 || len(inventory.Files) > 4096 {
		return nil, errors.New("Runtime 发布凭据缺少完整文件清单或协议能力")
	}
	if _, err = hex.DecodeString(inventory.Commit); err != nil {
		return nil, errors.New("Runtime 发布提交标识无效")
	}
	seen := map[string]bool{}
	var total int64
	for i := range inventory.Files {
		file := &inventory.Files[i]
		if !filepath.IsLocal(filepath.FromSlash(file.Path)) || strings.ContainsAny(file.Path, "\\:") || !validHash(file.Hash) || file.Size <= 0 || file.Size > 256<<20 || seen[strings.ToLower(file.Path)] {
			return nil, errors.New("Runtime 发布文件清单无效")
		}
		seen[strings.ToLower(file.Path)] = true
		total += file.Size
		if file.Path == meta.Binary {
			if !strings.EqualFold(file.Hash, receipt.SHA256) {
				return nil, errors.New("Runtime 发布文件与程序摘要不匹配")
			}
			file.Path = runtimeBinary()
		}
	}
	if total > 512<<20 || !seen[strings.ToLower(meta.Binary)] || !seen["core-runtime.json"] {
		return nil, errors.New("Runtime 发布文件清单不完整")
	}
	return inventory.Files, nil
}

func regularRuntimeFile(root, name string) (string, error) {
	path := root
	for _, part := range append([]string{"."}, strings.Split(filepath.FromSlash(name), string(filepath.Separator))...) {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("Runtime 文件不能包含符号链接")
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("Runtime 发布项不是普通文件")
	}
	return path, nil
}

func copyReleaseTree(source, dest string, receipt sharedRuntimeReceipt) error {
	files, err := receiptFiles(receipt)
	if err != nil {
		return err
	}
	target, err := runtimeTarget()
	if err != nil {
		return err
	}
	for _, file := range files {
		name := file.Path
		if name == runtimeBinary() {
			if _, err := os.Stat(filepath.Join(source, name)); errors.Is(err, os.ErrNotExist) {
				name = "rsrs-runtime-" + target + binaryExtension()
			}
		}
		if strings.HasPrefix(name, "core-notices/") {
			continue
		}
		input, err := regularRuntimeFile(source, name)
		if err != nil {
			return err
		}
		info, err := os.Stat(input)
		if err != nil {
			return err
		}
		hash, err := fileHash(input)
		if err != nil {
			return err
		}
		if info.Size() != file.Size || !strings.EqualFold(hash, file.Hash) {
			return fmt.Errorf("Runtime 发布文件校验失败: %s", file.Path)
		}
		output := filepath.Join(dest, filepath.FromSlash(file.Path))
		if err = os.MkdirAll(filepath.Dir(output), 0700); err != nil {
			return err
		}
		in, err := os.Open(input)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		err = errors.Join(copyErr, out.Sync(), out.Close(), in.Close())
		if err != nil {
			return err
		}
	}
	return verifyReleaseTree(dest, receipt)
}

func verifyReleaseTree(dir string, receipt sharedRuntimeReceipt) error {
	files, err := receiptFiles(receipt)
	if err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, file := range files {
		if strings.HasPrefix(file.Path, "core-notices/") {
			if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(file.Path))); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return err
			}
		}
		path, err := regularRuntimeFile(dir, file.Path)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		hash, err := fileHash(path)
		if err != nil {
			return err
		}
		if info.Size() != file.Size || !strings.EqualFold(hash, file.Hash) {
			return fmt.Errorf("Runtime 发布文件校验失败: %s", file.Path)
		}
		expected[path] = true
	}
	if err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && !expected[path] {
			return fmt.Errorf("Runtime 存在未认证文件: %s", path)
		}
		return nil
	}); err != nil {
		return err
	}
	return verifyRuntime(dir, receipt.SHA256)
}

func (m *Manager) selectRuntime(info InstalledRuntime) error {
	b, err := os.ReadFile(m.runtimeReceiptPath(info.Version))
	if err != nil {
		return err
	}
	var receipt sharedRuntimeReceipt
	if err = json.Unmarshal(b, &receipt); err != nil {
		return err
	}
	if receipt.Version != info.Version || receipt.SHA256 != info.SHA256 {
		return errors.New("Runtime 安装凭据不匹配")
	}
	if err = verifyReleaseTree(m.runtimeDir(info.Version), receipt); err != nil {
		return err
	}
	return writeJSON(m.runtimeManifest(), receipt)
}

func (m *Manager) verifyInstalled(info InstalledRuntime) error {
	b, err := os.ReadFile(m.runtimeReceiptPath(info.Version))
	if err != nil {
		return err
	}
	var receipt sharedRuntimeReceipt
	if err = json.Unmarshal(b, &receipt); err != nil {
		return err
	}
	if receipt.Version != info.Version || receipt.SHA256 != info.SHA256 {
		return errors.New("Runtime 安装凭据不匹配")
	}
	return verifyReleaseTree(m.runtimeDir(info.Version), receipt)
}

func (m *Manager) provenRuntimeMetadata(ctx context.Context, meta RuntimeMetadata) (RuntimeMetadata, error) {
	if meta.ProofJSON != "" {
		return meta, nil
	}
	target, err := runtimeTarget()
	if err != nil {
		return meta, err
	}
	name := "runtime-build-" + target + ".json"
	var raw json.RawMessage
	if payload, err := bootstrapDir(); err == nil {
		if bytes, readErr := os.ReadFile(filepath.Join(payload, name)); readErr == nil {
			raw = bytes
		}
	}
	if len(raw) == 0 {
		if err = m.fetchJSON(ctx, "https://github.com/risense-ai/respire-runtime/releases/download/v"+meta.RuntimeVersion+"/"+name, &raw); err != nil {
			return meta, err
		}
	}
	proven, err := NormalizeRuntimeMetadata(raw)
	if err != nil {
		return meta, err
	}
	if proven.RuntimeVersion != meta.RuntimeVersion || proven.BinaryHash != meta.BinaryHash {
		return meta, errors.New("Runtime 旧安装与可信发布不匹配")
	}
	_, err = runtimeReceipt(proven)
	return proven, err
}

// MigrateRuntime verifies legacy installations before shared startup version checks.
func (m *Manager) MigrateRuntime() error {
	if _, err := os.Stat(m.runtimeManifest()); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var selected sharedRuntimeReceipt
	var source string
	for _, base := range []string{m.DataRoot, m.Root} {
		b, err := os.ReadFile(filepath.Join(base, "runtime.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var candidate sharedRuntimeReceipt
		if err = json.Unmarshal(b, &candidate); err != nil {
			return err
		}
		if !validVersion(candidate.Version) || !validHash(candidate.SHA256) {
			return errors.New("旧 Runtime 安装凭据无效")
		}
		if selected.Version == candidate.Version && selected.SHA256 != candidate.SHA256 {
			return errors.New("旧 Runtime 安装凭据冲突")
		}
		if selected.Version != "" && semver.Compare("v"+selected.Version, "v"+candidate.Version) >= 0 {
			continue
		}
		selected, source = candidate, filepath.Join(base, "runtime", candidate.Version)
	}
	if source == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	meta := RuntimeMetadata{RuntimeVersion: selected.Version, BinaryHash: selected.SHA256}
	if selected.Release != nil {
		meta.ProofJSON = selected.Release.JSON
	}
	meta, err := m.provenRuntimeMetadata(ctx, meta)
	if err != nil {
		return err
	}
	if selected.Release != nil {
		if _, err = receiptFiles(selected); err != nil {
			return err
		}
		meta, err = NormalizeRuntimeMetadata([]byte(selected.Release.JSON))
		if err != nil {
			return err
		}
	}
	lock, err := m.lockRuntime(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err = os.Stat(m.runtimeManifest()); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = m.publishRuntime(meta, func(stage string) error {
		receipt, err := runtimeReceipt(meta)
		if err != nil {
			return err
		}
		return copyReleaseTree(source, stage, receipt)
	}); err != nil {
		return err
	}
	return m.selectRuntime(InstalledRuntime{Version: meta.RuntimeVersion, SHA256: meta.BinaryHash})
}

func (m *Manager) lockRuntime(ctx context.Context) (*os.File, error) {
	root := filepath.Join(m.DataRoot, "bin", "runtime")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(root, ".install.lock"), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		if err = tryRuntimeLock(file); err == nil {
			return file, nil
		} else if !errors.Is(err, errRuntimeLocked) {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-deadline.C:
			file.Close()
			return nil, errors.New("另一个宿主正在安装 Runtime，请稍后重试")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

var errRuntimeLocked = errors.New("Runtime installation locked")
