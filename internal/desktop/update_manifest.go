package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"golang.org/x/mod/semver"
	"respire-client2/internal/host"
)

const updateManifestAsset = "client-updates.json"

type manifestArtifact struct {
	Filename string `json:"filename"`
	URL      string `json:"url"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

type manifestRelease struct {
	Version     string             `json:"version"`
	Notes       string             `json:"notes"`
	PublishedAt string             `json:"publishedAt"`
	Artifacts   []manifestArtifact `json:"artifacts"`
}

type updateManifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	Stable        manifestRelease `json:"stable"`
	Dev           manifestRelease `json:"dev"`
}

type releaseProvider struct {
	client      *http.Client
	preferences func() host.Preferences
	stable      updater.Provider
	dev         updater.Provider
}

func newReleaseProvider(client *http.Client, preferences func() host.Preferences, stable, dev updater.Provider) releaseProvider {
	return releaseProvider{client: client, preferences: preferences, stable: stable, dev: dev}
}

func (p releaseProvider) Name() string { return "respire" }

func (p releaseProvider) Check(ctx context.Context, req updater.CheckRequest) (*updater.Release, error) {
	var m updateManifest
	if err := p.fetchJSON(ctx, manifestURL(true), &m); err != nil {
		if mirrorErr := p.fetchJSON(ctx, manifestURL(false), &m); mirrorErr != nil {
			source := p.stable
			channel := "stable"
			if p.preferences().ClientChannel == "dev" {
				source, channel = p.dev, "dev"
			}
			fallback, fallbackErr := source.Check(ctx, req)
			if fallbackErr != nil {
				return nil, errors.Join(
					fmt.Errorf("镜像更新清单不可用: %w", mirrorErr),
					fmt.Errorf("GitHub 更新清单不可用: %w", err),
					fmt.Errorf("GitHub API 兜底不可用: %w", fallbackErr),
				)
			}
			if fallback == nil {
				return nil, nil
			}
			if fallback.Verification == nil || fallback.Verification.DigestAlgo != "sha256" || len(fallback.Verification.Digest) != 32 {
				return nil, errors.New("GitHub 兜底发布缺少有效 SHA256SUMS")
			}
			fallback.Channel = channel
			fallback.Metadata["respire.fallback"] = channel
			return fallback, nil
		}
	}
	if m.SchemaVersion != 1 {
		return nil, fmt.Errorf("不支持的更新清单版本 %d", m.SchemaVersion)
	}
	candidates := []manifestRelease{m.Stable}
	if p.preferences().ClientChannel == "dev" {
		candidates = append(candidates, m.Dev)
	}
	current := "v" + req.CurrentVersion
	var picked *manifestRelease
	for i := range candidates {
		item := &candidates[i]
		if !semver.IsValid("v"+item.Version) || (picked != nil && semver.Compare("v"+picked.Version, "v"+item.Version) >= 0) {
			continue
		}
		if p.preferences().ClientChannel != "dev" && semver.Prerelease("v"+item.Version) != "" {
			continue
		}
		if semver.Compare("v"+item.Version, current) <= 0 {
			continue
		}
		picked = item
	}
	if picked == nil {
		return nil, nil
	}
	filename := host.ClientAsset(req.Platform, req.Arch)
	var artifact *manifestArtifact
	for i := range picked.Artifacts {
		if picked.Artifacts[i].Filename == filename {
			artifact = &picked.Artifacts[i]
			break
		}
	}
	if artifact == nil {
		return nil, fmt.Errorf("更新清单缺少 %s 的安装包", host.Platform())
	}
	urls, err := artifactURLs(*artifact, picked.Version, filename)
	if err != nil {
		return nil, err
	}
	digest, err := base64.StdEncoding.DecodeString(artifact.SHA256)
	if err != nil || len(digest) != 32 {
		return nil, errors.New("更新清单缺少有效 SHA256")
	}
	return &updater.Release{
		Version: picked.Version, Channel: p.preferences().ClientChannel, Name: "Respire " + picked.Version,
		Notes: picked.Notes, Artifact: updater.Artifact{Filename: filename, Size: artifact.Size, Platform: req.Platform, Arch: req.Arch},
		Verification: &updater.Verification{DigestAlgo: "sha256", Digest: digest},
		Metadata:     map[string]any{"respire.urls": urls},
	}, nil
}

func (p releaseProvider) Download(ctx context.Context, r *updater.Release, dst io.Writer, progress func(int64, int64)) error {
	if channel, ok := r.Metadata["respire.fallback"].(string); ok {
		source := p.stable
		if channel == "dev" {
			source = p.dev
		}
		return source.Download(ctx, r, dst, progress)
	}
	raw, ok := r.Metadata["respire.urls"].([]string)
	if !ok || len(raw) == 0 {
		return errors.New("更新清单缺少下载地址")
	}
	var failures []error
	for _, source := range raw {
		limit := r.Artifact.Size
		if limit == 0 {
			limit = 256 << 20
		}
		err := p.download(ctx, source, limit, dst, progress)
		if err == nil {
			return nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", sourceLabel(source), err))
	}
	return errors.Join(failures...)
}

func (p releaseProvider) download(ctx context.Context, source string, limit int64, dst io.Writer, progress func(int64, int64)) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/octet-stream")
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		return errors.New("安装包超过大小限制")
	}
	reader := io.LimitReader(response.Body, limit+1)
	buffer := make([]byte, 128<<10)
	var written int64
	for {
		n, readErr := reader.Read(buffer)
		if n > 0 {
			if _, writeErr := dst.Write(buffer[:n]); writeErr != nil {
				return writeErr
			}
			written += int64(n)
			progress(written, response.ContentLength)
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}

func (p releaseProvider) fetchJSON(ctx context.Context, source string, value any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20+1))
	if err != nil {
		return err
	}
	if len(data) > 2<<20 {
		return errors.New("更新清单过大")
	}
	if err = json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("更新清单不是有效 JSON: %w", err)
	}
	return nil
}

func manifestURL(mirror bool) string {
	source := "https://github.com/risense-ai/respire-client2/releases/latest/download/" + updateManifestAsset
	if mirror {
		return "https://gh-proxy.com/" + source
	}
	return source
}

func artifactURLs(item manifestArtifact, releaseVersion, filename string) ([]string, error) {
	if item.Filename != filename || item.SHA256 == "" || item.Size < 0 {
		return nil, errors.New("更新清单安装包字段无效")
	}
	base := "https://github.com/risense-ai/respire-client2/releases/download/v" + releaseVersion + "/" + filename
	direct, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	if item.URL != "" {
		parsed, err := url.Parse(item.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host != direct.Host || parsed.Path != direct.Path {
			return nil, errors.New("更新清单安装包地址不受信任")
		}
	}
	if item.URL != "" && item.URL != base {
		return nil, errors.New("更新清单安装包地址不匹配")
	}
	return []string{"https://gh-proxy.com/" + base, base}, nil
}

func sourceLabel(source string) string {
	if strings.Contains(source, "gh-proxy.com") {
		return "国内镜像"
	}
	return "GitHub"
}
