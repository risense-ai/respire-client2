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
		Providers:      []updater.Provider{newReleaseProvider(d.m.HTTPClient, d.m.Preferences, stable, dev)},
		Window:         updater.WindowNone,
	})
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
	d.mu.Lock()
	d.updateCancel = cancel
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.updateCancel = nil
		d.mu.Unlock()
		cancel()
	}()
	r, err := d.app.Updater.Check(ctx)
	if err == nil && r == nil {
		d.mu.Lock()
		d.clientVersion = ""
		d.mu.Unlock()
	}
	if err == nil && r != nil {
		d.mu.Lock()
		d.clientVersion, d.clientNotes, d.clientSource = r.Version, r.Notes, r.Channel
		d.mu.Unlock()
		if download {
			err = d.app.Updater.DownloadAndInstall(ctx)
			if err == nil && d.canRestartClient() {
				err = d.app.Updater.Restart(ctx)
			}
		}
	}
	d.mu.Lock()
	d.clientError = ""
	if errors.Is(err, context.Canceled) {
		err = errors.New("更新已取消")
	}
	if err != nil {
		d.clientError = err.Error()
	} else {
		d.clientNotes, d.clientSource = "", d.m.Preferences().ClientChannel
	}
	d.mu.Unlock()
	return err
}

// cancelUpdate unblocks a running Check/Download without replacing its lock owner.
func (d *desktop) cancelUpdate() error {
	d.mu.Lock()
	cancel := d.updateCancel
	d.mu.Unlock()
	if cancel == nil {
		return errors.New("没有正在执行的客户端更新")
	}
	cancel()
	return nil
}

func (d *desktop) canRestartClient() bool {
	return !d.m.HasActiveOperations() && !d.m.TaskSnapshot().Busy
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
