package desktop

import (
	"context"
	"errors"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
	"respire-client2/internal/host"
)

func (d *desktop) initUpdater() error {
	config := github.Config{
		Repository: "risense-ai/respire-client2", ChecksumAsset: "SHA256SUMS", HTTPClient: d.m.HTTPClient,
		AssetMatcher: func(req updater.CheckRequest, assets []github.ReleaseAsset) int {
			for i, asset := range assets {
				if asset.Name == host.ClientAsset(req.Platform, req.Arch) {
					return i
				}
			}
			return -1
		},
	}
	stable, err := github.New(config)
	if err != nil {
		return err
	}
	config.Prerelease = true
	dev, err := github.New(config)
	if err != nil {
		return err
	}
	return d.app.Updater.Init(updater.Config{
		CurrentVersion: version,
		Providers:      []updater.Provider{checkedProvider{Provider: stable, dev: dev, preferences: d.m.Preferences}},
		Window:         updater.WindowNone,
	})
}

// The upstream GitHub provider permits a missing checksum sidecar. Our releases do not.
type checkedProvider struct {
	updater.Provider
	dev         updater.Provider
	preferences func() host.Preferences
}

func (p checkedProvider) Check(ctx context.Context, req updater.CheckRequest) (*updater.Release, error) {
	source := p.Provider
	if p.preferences().ClientChannel == "dev" {
		source = p.dev
	}
	r, err := source.Check(ctx, req)
	if err != nil || r == nil {
		return r, err
	}
	if r.Verification == nil || r.Verification.DigestAlgo != "sha256" || len(r.Verification.Digest) != 32 {
		return nil, errors.New("客户端发布缺少有效的 SHA256SUMS，拒绝更新")
	}
	return r, nil
}

func (d *desktop) checkClient(download bool) error {
	if !d.updateMu.TryLock() {
		return errors.New("客户端更新操作正在执行")
	}
	defer d.updateMu.Unlock()
	if d.app.Updater.State() == updater.StateReady {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	r, err := d.app.Updater.Check(ctx)
	if err == nil && r == nil {
		d.mu.Lock()
		d.clientVersion = ""
		d.mu.Unlock()
	}
	if err == nil && r != nil {
		d.mu.Lock()
		d.clientVersion = r.Version
		d.mu.Unlock()
		if download {
			err = d.app.Updater.DownloadAndInstall(ctx)
		}
	}
	d.mu.Lock()
	d.clientError = ""
	if err != nil {
		d.clientError = err.Error()
	}
	d.mu.Unlock()
	return err
}

func (d *desktop) savePreferences(p host.Preferences) error {
	if p.ClientChannel != d.m.Preferences().ClientChannel {
		if !d.updateMu.TryLock() {
			return errors.New("客户端更新正在执行，请稍后修改更新通道")
		}
		defer d.updateMu.Unlock()
		if d.app.Updater.State() == updater.StateReady {
			return errors.New("请先重启应用已下载的客户端更新，再切换客户端通道")
		}
	}
	if err := d.m.SavePreferences(p); err != nil {
		return err
	}
	d.applyAppearance()
	return nil
}
