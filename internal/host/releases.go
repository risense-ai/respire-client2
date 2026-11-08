package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/semver"
)

// ReleaseAsset is one downloadable release artifact; URL is GitHub's
// browser_download_url so the URL-vs-name check in ReleaseAssetURL applies to
// every consumer.
type ReleaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// ReleaseInfo is the view of a GitHub release shared by the online installer
// and the DMG build-time fetch tooling.
type ReleaseInfo struct {
	Tag        string         `json:"tag_name"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []ReleaseAsset `json:"assets"`
}

// Compat is the CLI compatibility window a Runtime release declares.
type Compat struct {
	MinCLI string `json:"min_cli"`
	MaxCLI string `json:"max_cli"`
}

// RuntimeMetadata is the normalized runtime-build-<triple>.json. Schema 2 is
// the canonical shape published by risense-ai/respire-runtime
// (target_triple / archive_file / archive_sha256); schema 1 is the legacy
// alias shape (triple / sha256) mapped onto the same fields, so the online
// installer and the DMG bootstrap staging consume one representation.
type RuntimeMetadata struct {
	Schema         int    `json:"schema_version"`
	RuntimeVersion string `json:"runtime_version"`
	Tag            string `json:"tag"`
	Target         string `json:"target_triple"`
	ArchiveFile    string `json:"archive_file"`
	ArchiveHash    string `json:"archive_sha256"`
	Binary         string `json:"binary_file"`
	BinaryHash     string `json:"binary_sha256"`
	Compat         Compat `json:"compat"`
}

// CLIMetadata is the CLI build metadata published as cli-build-<triple>.json.
// NOTE: it keeps its risense-ai/respire-cli source and is consumed by
// LatestCLIBuild; managed Runtime assets no longer come from it.
type CLIMetadata struct {
	Schema      int    `json:"schema_version"`
	Version     string `json:"version"`
	Target      string `json:"target"`
	Commit      string `json:"git_sha"`
	Binary      string `json:"binary_file"`
	Runtime     string `json:"runtime_file"`
	BinaryHash  string `json:"binary_sha256"`
	RuntimeHash string `json:"runtime_sha256"`
}

// Release repositories: Runtime assets moved to their own repository while
// CLI install metadata stays on the CLI repository (dual source). Exported
// for the build-time bootstrap-metadata helper.
const (
	RuntimeRepo = "respire-runtime"
	CLIRepo     = "respire-cli"
)

// runtimeRepo and cliRepo keep the historical lowercase names at the call
// sites inside this package.
const (
	runtimeRepo = RuntimeRepo
	cliRepo     = CLIRepo
)

// runtimeArchive is the managed Runtime payload asset published per target.
func runtimeArchive(target string) string { return "rsrs-runtime-" + target + ".tar.gz" }

func validVersion(v string) bool { return semver.IsValid("v"+v) && !strings.ContainsAny(v, "/\\:") }
func validHash(v string) bool    { b, e := hex.DecodeString(v); return e == nil && len(b) == sha256.Size }

// metadataMismatch is the single failure family for release metadata that
// parses but does not match its release, target, or declared hashes.
func metadataMismatch(why string) error {
	return fmt.Errorf("Runtime release metadata mismatch: %s", why)
}

// aliasConflict applies the contract rule for duplicated legacy/canonical
// aliases: both absent or equal passes, differing values reject the file.
func aliasConflict(alias, canonical string, name string) error {
	if alias == "" || alias == canonical {
		return nil
	}
	return metadataMismatch(fmt.Sprintf("conflicting %s", name))
}

// NormalizeRuntimeMetadata parses published runtime-build JSON bytes into the
// shared normalized form. The schema_version field decides which fields own
// the identity: unknown schemas, contradictory aliases, and missing required
// fields all reject the metadata instead of guessing.
func NormalizeRuntimeMetadata(b []byte) (RuntimeMetadata, error) {
	var raw struct {
		Schema         int    `json:"schema_version"`
		RuntimeVersion string `json:"runtime_version"`
		Tag            string `json:"tag"`
		Binary         string `json:"binary_file"`
		BinaryHash     string `json:"binary_sha256"`
		Compat         Compat `json:"compat"`
		// Canonical schema 2 fields.
		TargetTriple string `json:"target_triple"`
		ArchiveFile  string `json:"archive_file"`
		ArchiveHash  string `json:"archive_sha256"`
		// Legacy schema 1 aliases.
		Triple     string `json:"triple"`
		LegacyHash string `json:"sha256"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return RuntimeMetadata{}, err
	}
	meta := RuntimeMetadata{
		Schema: raw.Schema, RuntimeVersion: raw.RuntimeVersion, Tag: raw.Tag,
		Binary: raw.Binary, BinaryHash: raw.BinaryHash, Compat: raw.Compat,
	}
	switch raw.Schema {
	case 1:
		if raw.Triple == "" || raw.LegacyHash == "" {
			return RuntimeMetadata{}, metadataMismatch("schema 1 requires triple and sha256")
		}
		meta.Target, meta.ArchiveHash = raw.Triple, raw.LegacyHash
		meta.ArchiveFile = runtimeArchive(meta.Target)
		if err := aliasConflict(raw.TargetTriple, meta.Target, "target_triple"); err != nil {
			return RuntimeMetadata{}, err
		}
		if err := aliasConflict(raw.ArchiveHash, meta.ArchiveHash, "archive_sha256"); err != nil {
			return RuntimeMetadata{}, err
		}
		if err := aliasConflict(raw.ArchiveFile, meta.ArchiveFile, "archive_file"); err != nil {
			return RuntimeMetadata{}, err
		}
	case 2:
		if raw.TargetTriple == "" || raw.ArchiveFile == "" || raw.ArchiveHash == "" {
			return RuntimeMetadata{}, metadataMismatch("schema 2 requires target_triple, archive_file and archive_sha256")
		}
		meta.Target, meta.ArchiveFile, meta.ArchiveHash = raw.TargetTriple, raw.ArchiveFile, raw.ArchiveHash
		if err := aliasConflict(raw.Triple, meta.Target, "triple"); err != nil {
			return RuntimeMetadata{}, err
		}
		if err := aliasConflict(raw.LegacyHash, meta.ArchiveHash, "sha256"); err != nil {
			return RuntimeMetadata{}, err
		}
	default:
		return RuntimeMetadata{}, fmt.Errorf("不支持的 Runtime 元数据 schema_version %d", raw.Schema)
	}
	return meta, nil
}

// CheckRuntimeMetadata verifies a normalized metadata against the release it
// was fetched from and the local build target: identity, asset names, hashes,
// and the declared CLI compatibility window must all be intact.
func CheckRuntimeMetadata(meta RuntimeMetadata, releaseTag, target string) error {
	if meta.RuntimeVersion == "" || meta.RuntimeVersion != strings.TrimPrefix(meta.Tag, "v") {
		return metadataMismatch("runtime_version does not match tag")
	}
	if meta.Tag != releaseTag {
		return metadataMismatch("tag does not match release")
	}
	if meta.Target != target {
		return metadataMismatch(fmt.Sprintf("target %q is not %q", meta.Target, target))
	}
	if meta.Binary != "rsrs-runtime-"+target+binaryExtension() {
		return metadataMismatch(fmt.Sprintf("binary_file %q", meta.Binary))
	}
	if meta.ArchiveFile != runtimeArchive(target) {
		return metadataMismatch(fmt.Sprintf("archive_file %q", meta.ArchiveFile))
	}
	if !validHash(meta.BinaryHash) || !validHash(meta.ArchiveHash) {
		return metadataMismatch("invalid SHA-256")
	}
	if !validVersion(meta.Compat.MinCLI) {
		return metadataMismatch("compat.min_cli")
	}
	if meta.Compat.MaxCLI != "*" && !validVersion(meta.Compat.MaxCLI) {
		return metadataMismatch("compat.max_cli")
	}
	return nil
}

// CheckCLICompat reports whether cliVersion sits inside the compatibility
// window a Runtime release declares; "*" as max_cli means no upper bound.
func CheckCLICompat(cliVersion string, compat Compat) error {
	if !validVersion(cliVersion) {
		return fmt.Errorf("CLI 版本无效: %s", cliVersion)
	}
	if semver.Compare("v"+cliVersion, "v"+compat.MinCLI) < 0 {
		return fmt.Errorf("CLI %s 低于 Runtime 要求的最低版本 %s", cliVersion, compat.MinCLI)
	}
	if compat.MaxCLI != "*" && semver.Compare("v"+cliVersion, "v"+compat.MaxCLI) > 0 {
		return fmt.Errorf("CLI %s 超出 Runtime 兼容上限 %s", cliVersion, compat.MaxCLI)
	}
	return nil
}

// SelectRelease picks the release a channel resolves to from a release list:
// stable only accepts full non-prerelease versions, dev accepts the highest
// published prerelease as well; drafts never win either channel.
func SelectRelease(list []ReleaseInfo, channel string) (ReleaseInfo, error) {
	var latest ReleaseInfo
	for _, r := range list {
		v := strings.TrimPrefix(r.Tag, "v")
		if r.Draft || !validVersion(v) {
			continue
		}
		if channel == "stable" && (r.Prerelease || semver.Prerelease(r.Tag) != "") {
			continue
		}
		if latest.Tag == "" || semver.Compare("v"+v, latest.Tag) > 0 {
			latest = r
		}
	}
	if latest.Tag == "" {
		return ReleaseInfo{}, errors.New("no published release")
	}
	return latest, nil
}

// ReleaseAssetURL resolves the browser_download_url of an asset, rejecting
// assets whose URL does not match the canonical download prefix for repo/tag.
func ReleaseAssetURL(r ReleaseInfo, repo, name string) (string, error) {
	prefix := "https://github.com/risense-ai/" + repo + "/releases/download/" + r.Tag + "/"
	for _, a := range r.Assets {
		if a.Name == name {
			if a.URL != prefix+name {
				return "", errors.New("unexpected release artifact URL")
			}
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("该 %s 发布未提供 %s 所需的 %s；仍可连接已有 Runtime", repo, Platform(), name)
}

func (m *Manager) fetchJSON(ctx context.Context, url string, v any) error {
	err := m.fetchJSONOnce(ctx, url, v)
	if err == nil {
		return nil
	}
	// Blocked or flaky GitHub: retry once through the gh-proxy mirror.
	if mirrored := mirrorURL(url); mirrored != url {
		if retryErr := m.fetchJSONOnce(ctx, mirrored, v); retryErr == nil {
			return nil
		}
	}
	return err
}

// httpStatusError reports a non-200 response from a release source so
// callers can react to specific codes (e.g. a stable channel whose
// repository has no full release yet).
type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string { return fmt.Sprintf("release source HTTP %d", e.code) }

// stableChannelMissing reports a stable-channel lookup that GitHub answered
// with 404: /releases/latest returns that when the repository has no
// published non-prerelease release, which is the current gated state of the
// runtime train (respire-runtime docs/version-contract.md: stable promotion
// stays closed until G1 evidence is verified and separately authorized).
func stableChannelMissing(err error) bool {
	var status *httpStatusError
	return errors.As(err, &status) && status.code == http.StatusNotFound
}

func (m *Manager) fetchJSONOnce(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Respire-Client2")
	res, err := m.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return &httpStatusError{code: res.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 2<<20+1))
	if err != nil {
		return err
	}
	if len(b) > 2<<20 {
		return errors.New("release metadata too large")
	}
	return json.Unmarshal(b, v)
}

// mirrorURL routes a github.com URL through the gh-proxy download mirror.
func mirrorURL(url string) string {
	if strings.HasPrefix(url, "https://github.com/") || strings.HasPrefix(url, "https://api.github.com/") {
		return "https://gh-proxy.com/" + url
	}
	return url
}

// fetchReleaseList downloads the release listing a channel resolves against.
// Stable reads /releases/latest so a repository without any full release
// surfaces as 404; dev scans the recent list for the highest prerelease.
func (m *Manager) fetchReleaseList(ctx context.Context, repo, channel string) ([]ReleaseInfo, error) {
	if channel == "stable" {
		var stable ReleaseInfo
		if err := m.fetchJSON(ctx, "https://api.github.com/repos/risense-ai/"+repo+"/releases/latest", &stable); err != nil {
			if stableChannelMissing(err) && repo == runtimeRepo {
				return nil, errors.New("Runtime stable 通道暂无正式发布；可在「设置 → 更新」把 Runtime 通道切换为 dev")
			}
			return nil, err
		}
		return []ReleaseInfo{stable}, nil
	}
	var list []ReleaseInfo
	if err := m.fetchJSON(ctx, "https://api.github.com/repos/risense-ai/"+repo+"/releases?per_page=20", &list); err != nil {
		return nil, err
	}
	return list, nil
}

// LatestRuntime resolves the newest managed Runtime release from
// risense-ai/respire-runtime together with its runtime-build-<triple>.json
// metadata. The reported version is the runtime version (runtime_version),
// independent of the CLI release train.
func (m *Manager) LatestRuntime(ctx context.Context) (ReleaseInfo, RuntimeMetadata, error) {
	channel := m.Preferences().RuntimeChannel
	target, err := runtimeTarget()
	if err != nil {
		return ReleaseInfo{}, RuntimeMetadata{}, err
	}
	list, err := m.fetchReleaseList(ctx, runtimeRepo, channel)
	if err != nil {
		return ReleaseInfo{}, RuntimeMetadata{}, err
	}
	if channel == "stable" && (list[0].Prerelease || semver.Prerelease(list[0].Tag) != "") {
		return ReleaseInfo{}, RuntimeMetadata{}, errors.New("stable channel returned a prerelease")
	}
	latest, err := SelectRelease(list, channel)
	if err != nil {
		return ReleaseInfo{}, RuntimeMetadata{}, errors.New("no published Runtime release")
	}
	var raw json.RawMessage
	url, err := ReleaseAssetURL(latest, runtimeRepo, "runtime-build-"+target+".json")
	if err != nil {
		return latest, RuntimeMetadata{}, err
	}
	if err = m.fetchJSON(ctx, url, &raw); err != nil {
		return latest, RuntimeMetadata{}, err
	}
	meta, err := NormalizeRuntimeMetadata(raw)
	if err != nil {
		return latest, RuntimeMetadata{}, err
	}
	if err = CheckRuntimeMetadata(meta, latest.Tag, target); err != nil {
		return latest, meta, err
	}
	return latest, meta, nil
}

// LatestCLIBuild reads CLI install metadata (cli-build-<triple>.json) from
// risense-ai/respire-cli. CLI release metadata keeps its original repository
// source; only managed Runtime assets moved to risense-ai/respire-runtime.
func (m *Manager) LatestCLIBuild(ctx context.Context) (ReleaseInfo, CLIMetadata, error) {
	channel := m.Preferences().RuntimeChannel
	cliTarget, err := runtimeTarget()
	if err != nil {
		return ReleaseInfo{}, CLIMetadata{}, err
	}
	list, err := m.fetchReleaseList(ctx, cliRepo, channel)
	if err != nil {
		return ReleaseInfo{}, CLIMetadata{}, err
	}
	if channel == "stable" && (list[0].Prerelease || semver.Prerelease(list[0].Tag) != "") {
		return ReleaseInfo{}, CLIMetadata{}, errors.New("stable channel returned a prerelease")
	}
	latest, err := SelectRelease(list, channel)
	if err != nil {
		return ReleaseInfo{}, CLIMetadata{}, errors.New("no published CLI release")
	}
	var raw json.RawMessage
	url, err := ReleaseAssetURL(latest, cliRepo, "cli-build-"+cliTarget+".json")
	if err != nil {
		return latest, CLIMetadata{}, err
	}
	if err = m.fetchJSON(ctx, url, &raw); err != nil {
		return latest, CLIMetadata{}, err
	}
	meta, err := NormalizeCLIMetadata(raw, latest.Tag, cliTarget)
	if err != nil {
		return latest, meta, err
	}
	return latest, meta, nil
}

// NormalizeCLIMetadata parses cli-build-<triple>.json bytes and verifies the
// metadata against its release and target, including the pinned source commit.
func NormalizeCLIMetadata(b []byte, releaseTag, target string) (CLIMetadata, error) {
	var meta CLIMetadata
	if err := json.Unmarshal(b, &meta); err != nil {
		return meta, err
	}
	if meta.Schema != 1 || meta.Target != target || meta.Version != strings.TrimPrefix(releaseTag, "v") ||
		meta.Binary != "rsrs-"+target+binaryExtension() || meta.Runtime != "rsrs-"+target+"-runtime.tar.gz" ||
		!validHash(meta.BinaryHash) || !validHash(meta.RuntimeHash) {
		return meta, errors.New("CLI release metadata mismatch")
	}
	commit, err := hex.DecodeString(meta.Commit)
	if err != nil || len(commit) != 20 {
		return meta, errors.New("invalid CLI source commit")
	}
	return meta, nil
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (m *Manager) download(ctx context.Context, url, dest, hash string, max int64) error {
	err := m.downloadOnce(ctx, url, dest, hash, max)
	if err == nil {
		return nil
	}
	// Blocked or flaky GitHub: retry once through the gh-proxy mirror.
	if mirrored := mirrorURL(url); mirrored != url {
		_ = os.Remove(dest)
		if retryErr := m.downloadOnce(ctx, mirrored, dest, hash, max); retryErr == nil {
			return nil
		}
	}
	return err
}

func (m *Manager) downloadOnce(ctx context.Context, url, dest, hash string, max int64) error {
	if !validHash(hash) {
		return errors.New("missing artifact SHA256")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	res, err := m.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("artifact HTTP %d", res.StatusCode)
	}
	if res.ContentLength > max {
		return errors.New("artifact too large")
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	h := sha256.New()
	reader := io.LimitReader(res.Body, max+1)
	buf := make([]byte, 128<<10)
	var done int64
	for {
		n, re := reader.Read(buf)
		if n > 0 {
			if _, err = io.MultiWriter(f, h).Write(buf[:n]); err != nil {
				break
			}
			done += int64(n)
			m.progress("下载 "+filepath.Base(dest), done, res.ContentLength)
		}
		if re != nil {
			if re != io.EOF {
				err = re
			}
			break
		}
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if done > max {
		return errors.New("artifact exceeds size limit")
	}
	if hex.EncodeToString(h.Sum(nil)) != hash {
		return errors.New("artifact SHA256 mismatch")
	}
	return nil
}
