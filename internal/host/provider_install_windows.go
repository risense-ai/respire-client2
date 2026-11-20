package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (m *Manager) installModelEngines(ctx context.Context) (result json.RawMessage, err error) {
	catalog, err := m.downloadWinMLCatalog(ctx)
	if err != nil {
		return nil, err
	}
	install, err := m.lockRuntime(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, install.Close()) }()

	takeover, err := m.lockRuntimeTakeover(ctx)
	if err != nil {
		return nil, err
	}
	root, err := canonicalSessionDirectory(m.DataRoot)
	if err != nil {
		return nil, errors.Join(err, takeover.Close())
	}
	markerPath := filepath.Join(root, "runtime", "autostart", "maintenance.json")
	var marker []byte
	var info InstalledRuntime
	var previous HealthInfo
	var profile string
	stopped := false
	defer func() {
		// The private child must have exited before we release Paused authority.
		if err != nil && strings.Contains(err.Error(), "exit unconfirmed") {
			err = &unconfirmedError{cause: errors.Join(err, takeover.Close())}
			return
		}
		if marker != nil {
			if removeErr := removeOwnedRuntimeAccountRecord(markerPath, marker); removeErr != nil {
				err = &unconfirmedError{cause: errors.Join(err, removeErr, takeover.Close())}
				return
			}
		}
		if !stopped {
			err = errors.Join(err, takeover.Close())
			return
		}
		restart, cancel := context.WithTimeout(context.WithoutCancel(ctx), 120*time.Second)
		defer cancel()
		active, restoreErr := m.startDetachedLocked(restart, info)
		restoreErr = errors.Join(restoreErr, takeover.Close())
		if restoreErr == nil {
			restoreErr = m.confirmProviderRuntime(restart, info, previous, active, profile)
		}
		if restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("恢复安装前的 Runtime: %w", restoreErr))
		}
	}()
	if err := m.requireNoDeviceMaintenance(); err != nil {
		return nil, err
	}
	info, err = m.Installed()
	if err != nil {
		return nil, err
	}
	if err := m.verifyInstalled(info); err != nil {
		return nil, err
	}
	config, selected, err := m.readDeviceConfig()
	if err != nil {
		return nil, err
	}
	profile, err = canonicalSessionDirectory(selected.profile)
	if err != nil {
		return nil, err
	}
	identity, err := deviceProfileIdentity(profile)
	if err != nil {
		return nil, err
	}
	address, port, err := m.runtimeAccountEndpoint()
	if err != nil {
		return nil, err
	}
	directory, err := deviceProfilePath(filepath.Dir(markerPath))
	if err != nil {
		return nil, err
	}
	coordinator, err := providerMaintenanceCoordinator(directory, root, port)
	if err != nil {
		return nil, err
	}
	if m.ClientVersion == "" {
		return nil, errors.New("WinML 安装缺少 Client2 版本")
	}
	birth, err := hostControlBirth(ctx)
	if err != nil {
		return nil, err
	}
	token, err := controlUUID()
	if err != nil {
		return nil, err
	}
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	active, _, probeErr := m.Health(probe)
	cancel()
	if probeErr == nil {
		if active.ManagedID != "" {
			return nil, errors.New("请先由当前监督宿主停止 Runtime")
		}
		if err := m.verifyDeviceConfigOwner(ctx, active, info, profile); err != nil {
			return nil, err
		}
		drain, cancel := context.WithTimeout(ctx, 35*time.Second)
		err := m.stopDetached(drain, active)
		cancel()
		if err != nil {
			return nil, err
		}
		previous, stopped = active, true
	} else if !IsUnavailable(probeErr) {
		return nil, probeErr
	}
	if err := m.archiveStoppedEndpoint(ctx, address); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	paused, err := json.Marshal(runtimeMaintenanceMarker{
		Schema: 1, ID: coordinator, Token: token, Phase: "Paused", ExpectedProfile: profile,
		ExpectedVersion: m.ClientVersion, OwnerPID: os.Getpid(), OwnerBirth: birth,
	})
	if err != nil {
		return nil, err
	}
	configLock, err := lockDeviceConfig(ctx, filepath.Join(root, "inference.json"))
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, configLock.Close()) }()
	if err := withProviderLibraryLocks(ctx, root, profile, func() error {
		if err := requireClosedRuntimePort(ctx, address); err != nil {
			return err
		}
		if err := writeRuntimeAccountRecord(markerPath, paused, nil); err != nil {
			return err
		}
		marker = paused
		return nil
	}); err != nil {
		return nil, err
	}
	checkBinding := func() error {
		currentMarker, err := readCredentialConfig(markerPath, runtimeMaintenanceLimit)
		if err != nil || !bytes.Equal(currentMarker, marker) {
			return errors.Join(errors.New("WinML 安装期间 Runtime 维护所有权已改变"), err)
		}
		current, _, err := m.readDeviceConfig()
		if err != nil || !bytes.Equal(current, config) {
			return errors.Join(errors.New("WinML 安装期间 Runtime 配置已改变"), err)
		}
		currentIdentity, err := deviceProfileIdentity(profile)
		if err != nil {
			return err
		}
		if currentIdentity.Kind != identity.Kind || currentIdentity.SessionDigest != identity.SessionDigest {
			return errors.New("WinML 安装期间 Runtime 账户已改变")
		}
		currentRuntime, err := m.Installed()
		if err != nil || currentRuntime != info {
			return errors.Join(errors.New("WinML 安装期间 Runtime 选版已改变"), err)
		}
		return nil
	}
	if err := checkBinding(); err != nil {
		return nil, err
	}
	plan, err := m.providerPreparation(ctx, info, profile, identity.AccountID, marker, "")
	if err != nil {
		return nil, err
	}
	if !plan.matchesCatalog(root, catalog, false) {
		return nil, errors.New("Runtime WinML 安装计划与 Client2 固定的 catalog 不匹配")
	}
	if err := withProviderLibraryLocks(ctx, root, profile, func() error {
		if err := checkBinding(); err != nil {
			return err
		}
		// WinML's dynamic provider downloads have no pin in the current contract.
		return catalog.publish(root)
	}); err != nil {
		return nil, err
	}
	verified, err := m.providerPreparation(ctx, info, profile, identity.AccountID, marker, plan.Selection)
	if err != nil {
		return nil, fmt.Errorf("WinML catalog 已安装，Runtime 核验失败: %w", err)
	}
	if err := checkBinding(); err != nil {
		return nil, err
	}
	if !verified.matchesCatalog(root, catalog, true) {
		return nil, errors.New("Runtime 未确认固定的 WinML catalog 已通过校验")
	}
	return providerInstallReceipt(verified)
}

func providerMaintenanceCoordinator(directory, root string, port uint64) (string, error) {
	raw, err := readCredentialConfig(filepath.Join(directory, "registration.json"), controlFrameLimit)
	if errors.Is(err, os.ErrNotExist) {
		return runtimeRegistrationCoordinator(root, nil)
	}
	if err != nil {
		return "", err
	}
	var policy struct {
		Enabled         *bool `json:"enabled"`
		ManuallyStopped *bool `json:"manually_stopped"`
	}
	id := deviceConfigRegistrationID(raw, root, port)
	if id == "" || json.Unmarshal(raw, &policy) != nil || policy.Enabled == nil || policy.ManuallyStopped == nil {
		return "", errors.New("WinML 安装无法确认 Runtime 监督登记")
	}
	if *policy.Enabled && !*policy.ManuallyStopped {
		return "", errors.New("请先由当前监督宿主停止 Runtime")
	}
	return id, nil
}

func withProviderLibraryLocks(ctx context.Context, root, profile string, action func() error) (err error) {
	runDir := filepath.Join(root, "runtime")
	for _, dir := range []string{runDir, root} {
		lock, err := lockRuntimeDatabase(ctx, dir, 0)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, lock.Close()) }()
	}
	if !samePath(profile, root) && !samePath(profile, runDir) {
		lock, err := lockRuntimeDatabase(ctx, profile, 0)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, lock.Close()) }()
	}
	return action()
}

func (m *Manager) confirmProviderRuntime(ctx context.Context, version InstalledRuntime, previous, active HealthInfo, profile string) error {
	if err := m.verifyDeviceConfigOwner(ctx, active, version, profile); err != nil {
		return err
	}
	if active.InstanceID == previous.InstanceID || active.AccountID != previous.AccountID || active.ManagedID != "" {
		return errors.New("恢复后的 Runtime 实例与安装前的账户或管理方式不匹配")
	}
	if err := m.unlockIfNeeded(active); err != nil {
		return err
	}
	if previous.StoreReady && !active.StoreReady {
		current, _, err := m.Health(ctx)
		if err != nil {
			return err
		}
		if err := m.verifyDeviceConfigOwner(ctx, current, version, profile); err != nil {
			return err
		}
		if current.InstanceID != active.InstanceID || !current.StoreReady {
			return errors.New("恢复后的 Runtime 存储未就绪")
		}
	}
	return nil
}
