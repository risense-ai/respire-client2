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

const cliTarget = "x86_64-pc-windows-msvc"

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
}

func validVersion(v string) bool { return semver.IsValid("v"+v) && !strings.ContainsAny(v, "/\\:") }
func validHash(v string) bool    { b, e := hex.DecodeString(v); return e == nil && len(b) == sha256.Size }

func (m *Manager) fetchJSON(ctx context.Context, url string, v any) error {
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

func (m *Manager) LatestRuntime(ctx context.Context) (githubRelease, cliMetadata, error) {
	var list []githubRelease
	var meta cliMetadata
	if m.Preferences().RuntimeChannel == "stable" {
		var stable githubRelease
		if err := m.fetchJSON(ctx, "https://api.github.com/repos/risense-ai/respire-cli/releases/latest", &stable); err != nil {
			return stable, meta, err
		}
		if stable.Prerelease || semver.Prerelease(stable.Tag) != "" {
			return stable, meta, errors.New("stable channel returned a prerelease")
		}
		list = []githubRelease{stable}
	} else {
		if err := m.fetchJSON(ctx, "https://api.github.com/repos/risense-ai/respire-cli/releases?per_page=20", &list); err != nil {
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
	url, err := assetURL(latest, "cli-build-"+cliTarget+".json")
	if err != nil {
		return latest, meta, err
	}
	if err = m.fetchJSON(ctx, url, &meta); err != nil {
		return latest, meta, err
	}
	if meta.Schema != 1 || meta.Target != cliTarget || meta.Version != strings.TrimPrefix(latest.Tag, "v") ||
		meta.Binary != "rsrs-"+cliTarget+".exe" || meta.Runtime != "rsrs-"+cliTarget+"-runtime.tar.gz" || !validHash(meta.BinaryHash) || !validHash(meta.RuntimeHash) {
		return latest, meta, errors.New("CLI release metadata mismatch")
	}
	commit, err := hex.DecodeString(meta.Commit)
	if err != nil || len(commit) != 20 {
		return latest, meta, errors.New("invalid CLI source commit")
	}
	return latest, meta, nil
}

func assetURL(r githubRelease, name string) (string, error) {
	prefix := "https://github.com/risense-ai/respire-cli/releases/download/" + r.Tag + "/"
	for _, a := range r.Assets {
		if a.Name == name {
			if a.URL != prefix+name {
				return "", errors.New("unexpected CLI artifact URL")
			}
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("release is missing %s", name)
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
