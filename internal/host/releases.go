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

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}
type githubRelease struct {
	Tag        string         `json:"tag_name"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}
type cliMetadata struct {
	Schema      int    `json:"schema_version"`
	Version     string `json:"version"`
	Target      string `json:"target"`
	Commit      string `json:"git_sha"`
	Binary      string `json:"binary_file"`
	Runtime     string `json:"runtime_file"`
	BinaryHash  string `json:"binary_sha256"`
	RuntimeHash string `json:"runtime_sha256"`

	// NOTE: cli-build metadata keeps its risense-ai/respire-cli source and is
	// consumed by LatestCLIBuild; managed Runtime assets no longer come from it.
}

// runtimeMetadata is the build metadata published by risense-ai/respire-runtime
// as runtime-build-<triple>.json. sha256 covers the runtimeArchive payload.
type runtimeMetadata struct {
	RuntimeVersion string `json:"runtime_version"`
	Tag            string `json:"tag"`
	Target         string `json:"triple"`
	ArchiveHash    string `json:"sha256"`
	Binary         string `json:"binary_file"`
	BinaryHash     string `json:"binary_sha256"`
	Compat         struct {
		MinCLI string `json:"min_cli"`
		MaxCLI string `json:"max_cli"`
	} `json:"compat"`
}

// Release repositories: Runtime assets moved to their own repository while
// CLI install metadata stays on the CLI repository (dual source).
const (
	runtimeRepo = "respire-runtime"
	cliRepo     = "respire-cli"
)

// runtimeArchive is the managed Runtime payload asset published per target.
func runtimeArchive(target string) string { return "rsrs-runtime-" + target + ".tar.gz" }

func validVersion(v string) bool { return semver.IsValid("v"+v) && !strings.ContainsAny(v, "/\\:") }
func validHash(v string) bool    { b, e := hex.DecodeString(v); return e == nil && len(b) == sha256.Size }

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
		return fmt.Errorf("release source HTTP %d", res.StatusCode)
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

// LatestRuntime resolves the newest managed Runtime release from
// risense-ai/respire-runtime together with its runtime-build-<triple>.json
// metadata. The reported version is the runtime version (runtime_version),
// independent of the CLI release train.
func (m *Manager) LatestRuntime(ctx context.Context) (githubRelease, runtimeMetadata, error) {
	target, err := runtimeTarget()
	if err != nil {
		return githubRelease{}, runtimeMetadata{}, err
	}
	var list []githubRelease
	if m.Preferences().RuntimeChannel == "stable" {
		var stable githubRelease
		if err := m.fetchJSON(ctx, "https://api.github.com/repos/risense-ai/"+runtimeRepo+"/releases/latest", &stable); err != nil {
			return stable, runtimeMetadata{}, err
		}
		if stable.Prerelease || semver.Prerelease(stable.Tag) != "" {
			return stable, runtimeMetadata{}, errors.New("stable channel returned a prerelease")
		}
		list = []githubRelease{stable}
	} else {
		if err := m.fetchJSON(ctx, "https://api.github.com/repos/risense-ai/"+runtimeRepo+"/releases?per_page=20", &list); err != nil {
			return githubRelease{}, runtimeMetadata{}, err
		}
	}
	var latest githubRelease
	for _, r := range list {
		v := strings.TrimPrefix(r.Tag, "v")
		if r.Draft || !validVersion(v) {
			continue
		}
		if latest.Tag == "" || semver.Compare("v"+v, latest.Tag) > 0 {
			latest = r
		}
	}
	if latest.Tag == "" {
		return latest, runtimeMetadata{}, errors.New("no published Runtime release")
	}
	var meta runtimeMetadata
	url, err := assetURL(latest, runtimeRepo, "runtime-build-"+target+".json")
	if err != nil {
		return latest, meta, err
	}
	if err = m.fetchJSON(ctx, url, &meta); err != nil {
		return latest, meta, err
	}
	if meta.RuntimeVersion != strings.TrimPrefix(meta.Tag, "v") || meta.Tag != latest.Tag ||
		meta.Target != target || meta.Binary != "rsrs-runtime-"+target+binaryExtension() ||
		!validHash(meta.BinaryHash) || !validHash(meta.ArchiveHash) {
		return latest, meta, errors.New("Runtime release metadata mismatch")
	}
	return latest, meta, nil
}

// LatestCLIBuild reads CLI install metadata (cli-build-<triple>.json) from
// risense-ai/respire-cli. CLI release metadata keeps its original repository
// source; only managed Runtime assets moved to risense-ai/respire-runtime.
func (m *Manager) LatestCLIBuild(ctx context.Context) (githubRelease, cliMetadata, error) {
	cliTarget, err := runtimeTarget()
	if err != nil {
		return githubRelease{}, cliMetadata{}, err
	}
	var list []githubRelease
	var meta cliMetadata
	if m.Preferences().RuntimeChannel == "stable" {
		var stable githubRelease
		if err := m.fetchJSON(ctx, "https://api.github.com/repos/risense-ai/"+cliRepo+"/releases/latest", &stable); err != nil {
			return stable, meta, err
		}
		if stable.Prerelease || semver.Prerelease(stable.Tag) != "" {
			return stable, meta, errors.New("stable channel returned a prerelease")
		}
		list = []githubRelease{stable}
	} else {
		if err := m.fetchJSON(ctx, "https://api.github.com/repos/risense-ai/"+cliRepo+"/releases?per_page=20", &list); err != nil {
			return githubRelease{}, meta, err
		}
	}
	var latest githubRelease
	for _, r := range list {
		v := strings.TrimPrefix(r.Tag, "v")
		if r.Draft || !validVersion(v) {
			continue
		}
		if latest.Tag == "" || semver.Compare("v"+v, latest.Tag) > 0 {
			latest = r
		}
	}
	if latest.Tag == "" {
		return latest, meta, errors.New("no published CLI release")
	}
	url, err := assetURL(latest, cliRepo, "cli-build-"+cliTarget+".json")
	if err != nil {
		return latest, meta, err
	}
	if err = m.fetchJSON(ctx, url, &meta); err != nil {
		return latest, meta, err
	}
	if meta.Schema != 1 || meta.Target != cliTarget || meta.Version != strings.TrimPrefix(latest.Tag, "v") ||
		meta.Binary != "rsrs-"+cliTarget+binaryExtension() || meta.Runtime != "rsrs-"+cliTarget+"-runtime.tar.gz" || !validHash(meta.BinaryHash) || !validHash(meta.RuntimeHash) {
		return latest, meta, errors.New("CLI release metadata mismatch")
	}
	commit, err := hex.DecodeString(meta.Commit)
	if err != nil || len(commit) != 20 {
		return latest, meta, errors.New("invalid CLI source commit")
	}
	return latest, meta, nil
}

func assetURL(r githubRelease, repo, name string) (string, error) {
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
