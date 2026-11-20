//go:build linux || windows || (darwin && cgo)

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type deviceConfigReview struct {
	OriginalRevision  *string             `json:"original_revision"`
	CandidateRevision string              `json:"candidate_revision"`
	Intent            deviceConfigIntent  `json:"intent"`
	SourceProfile     string              `json:"source_profile"`
	TargetProfile     string              `json:"target_profile"`
	SourceIdentity    runtimeAuthIdentity `json:"source_identity"`
}

type deviceConfigValidation struct {
	deviceConfigReview
	SchemaVersion int                   `json:"schema_version"`
	Binding       runtimeControlBinding `json:"binding"`
	Identity      runtimeAuthIdentity   `json:"identity"`
	Original      *string               `json:"original_config"`
}

func (v deviceConfigValidation) request(id string) (runtimeControlRequest, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return runtimeControlRequest{}, err
	}
	defer clear(raw)
	return runtimeControlRequest{V: 1, ID: id, Method: "control.config_validate", Args: []string{string(raw)}}, nil
}

func deviceProfileIdentity(profile string) (runtimeAuthIdentity, error) {
	data, err := readCredentialConfig(filepath.Join(profile, "session.json"), controlFrameLimit)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return runtimeAuthIdentity{}, err
	}
	return runtimeAuthIdentityOf(data)
}

func (m *Manager) verifyDeviceConfigOwner(ctx context.Context, health HealthInfo, info InstalledRuntime, profile string) error {
	if err := m.verifySelectedHealth(health, info); err != nil {
		return err
	}
	birth, err := runtimeControlProcessBirth(ctx, int(health.PID))
	if err != nil {
		return err
	}
	prefix := map[string]string{"windows": "windows-filetime-", "linux": "linux-starttime-", "darwin": "darwin-lstart-"}[runtime.GOOS]
	if birth == "" || health.ProcessStartID != prefix+birth {
		return errors.New("Runtime 监听报告的进程与实际启动时间不匹配")
	}
	root, err := canonicalSessionDirectory(m.DataRoot)
	if err != nil {
		return err
	}
	profile, err = canonicalSessionDirectory(profile)
	if err != nil {
		return err
	}
	identity, err := deviceProfileIdentity(profile)
	if err != nil {
		return err
	}
	account := controlDigest([]byte(profile))
	if identity.AccountID != nil {
		account = *identity.AccountID
	}
	if !health.StartupComplete || health.ImageSHA256 != info.SHA256 ||
		!samePath(health.ImagePath, health.Exe) || !samePath(health.DataDir, profile) ||
		health.InstallationID != controlDigest([]byte(root)) || health.AccountID != account ||
		!runtimeInstanceID.MatchString(health.InstanceID) || health.Generation == 0 {
		return errors.New("Runtime 未确认当前配置的安装、映像和账户身份")
	}
	return nil
}

func (m *Manager) validateDeviceConfig(ctx context.Context, intent deviceConfigIntent) (json.RawMessage, error) {
	return m.changeDeviceConfig(ctx, intent, nil)
}

func (m *Manager) changeDeviceConfig(ctx context.Context, intent deviceConfigIntent, workspace *workspaceModeChange) (result json.RawMessage, err error) {
	takeover, err := m.lockRuntimeTakeover(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, takeover.Close()) }()
	if err := m.requireNoDeviceMaintenance(); err != nil {
		return nil, err
	}
	var mode string
	if workspace != nil {
		mode = workspace.mode
	}
	edit, err := m.reviewDeviceConfig(intent, mode)
	if err != nil {
		return nil, err
	}
	root, err := canonicalSessionDirectory(m.DataRoot)
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(root, "runtime", "autostart")
	if _, err := deviceProfilePath(directory); err != nil {
		return nil, err
	}
	health, _, err := m.Health(ctx)
	wasServing := err == nil
	if !wasServing && !IsUnavailable(err) {
		return nil, err
	}
	var info InstalledRuntime
	if workspace == nil || wasServing {
		info, err = m.Installed()
		if err != nil {
			return nil, err
		}
		if err := m.verifyInstalled(info); err != nil {
			return nil, err
		}
	}
	if wasServing {
		if err := m.verifyDeviceConfigOwner(ctx, health, info, edit.before.profile); err != nil {
			return nil, err
		}
		if health.ManagedID != "" {
			return nil, errors.New("请先由当前监督宿主停止 Runtime")
		}
	}
	address, port, err := m.runtimeAccountEndpoint()
	if err != nil {
		return nil, err
	}
	for _, profile := range []string{edit.before.profile, edit.after.profile} {
		if err := os.MkdirAll(profile, 0700); err != nil {
			return nil, err
		}
	}
	source, err := canonicalSessionDirectory(edit.before.profile)
	if err != nil || !samePath(source, edit.before.profile) {
		return nil, errors.Join(errors.New("源数据目录身份已改变"), err)
	}
	target, err := canonicalSessionDirectory(edit.after.profile)
	if err != nil || !samePath(target, edit.after.profile) {
		return nil, errors.Join(errors.New("目标数据目录身份已改变"), err)
	}
	sourceIdentity, err := deviceProfileIdentity(source)
	if err != nil {
		return nil, err
	}
	targetIdentity, err := deviceProfileIdentity(target)
	if err != nil {
		return nil, err
	}
	if m.ClientVersion == "" {
		return nil, errors.New("设备配置验证缺少 Client2 版本")
	}
	birth, err := hostControlBirth(ctx)
	if err != nil {
		return nil, err
	}
	token, err := controlUUID()
	if err != nil {
		return nil, err
	}
	var params deviceConfigValidation
	var requestID string
	if workspace == nil {
		params = deviceConfigValidation{
			deviceConfigReview: deviceConfigReview{CandidateRevision: controlDigest(edit.candidate), Intent: intent,
				SourceProfile: source, TargetProfile: target, SourceIdentity: sourceIdentity},
			SchemaVersion: 1, Identity: targetIdentity,
			Binding: runtimeControlBinding{root, target, token, targetIdentity.AccountID},
		}
		if edit.original != nil {
			value, digest := string(edit.original), controlDigest(edit.original)
			params.Original, params.OriginalRevision = &value, &digest
		}
		requestID, err = controlUUID()
		if err != nil {
			return nil, err
		}
		// The real launch grant substitutes a UUID of the same encoded width.
		preview, err := params.request(requestID)
		if err != nil {
			return nil, err
		}
		frame, err := json.Marshal(preview)
		if err != nil {
			return nil, err
		}
		size := len(frame)
		clear(frame)
		if size > controlFrameLimit {
			return nil, errors.New("设备配置超过 Runtime 私有验证帧上限")
		}
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	marker := runtimeMaintenanceMarker{Schema: 1, Token: token, ExpectedVersion: m.ClientVersion,
		OwnerPID: os.Getpid(), OwnerBirth: birth}
	markerPath := filepath.Join(directory, "maintenance.json")
	var maintenance runtimeAccountAdmission
	updateMarker := func(phase, profile string) error {
		marker.Phase, marker.ExpectedProfile = phase, profile
		next, err := json.Marshal(marker)
		if err != nil {
			return err
		}
		if err := writeRuntimeAccountRecord(markerPath, next, maintenance.Marker); err != nil {
			return err
		}
		maintenance.Marker = next
		return nil
	}
	image := filepath.Join(m.runtimeDir(info.Version), runtimeBinary())
	staged, starting := false, false
	var launchID string
	var launchedPID uint32
	defer func() {
		if err == nil || maintenance.Marker == nil {
			return
		}
		if workspace != nil && !wasServing && !staged {
			if cleanupErr := removeOwnedRuntimeAccountRecord(markerPath, maintenance.Marker); cleanupErr != nil {
				err = &unconfirmedError{cause: errors.Join(err, cleanupErr)}
			}
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 35*time.Second)
		defer cancel()
		if starting {
			active, _, probeErr := m.Health(cleanup)
			if probeErr == nil && (active.InstanceID != launchID || active.PID != launchedPID) {
				probeErr = errors.New("监听进程与本次配置启动授权不匹配")
			}
			if probeErr == nil {
				probeErr = m.verifyDeviceConfigOwner(cleanup, active, info, target)
			}
			if probeErr == nil {
				probeErr = m.stopDetached(cleanup, active)
			}
			if probeErr != nil {
				err = &unconfirmedError{cause: errors.Join(err, errors.New("目标 Runtime 退出未确认，保留配置候选和维护记录"),
					probeErr, updateMarker("Paused", target))}
				return
			}
		}
		if strings.Contains(err.Error(), "exit unconfirmed") {
			err = &unconfirmedError{cause: errors.Join(err, errors.New("配置验证进程退出未确认，保留配置候选和维护记录"),
				updateMarker("Paused", target))}
			return
		}
		if pauseErr := updateMarker("Paused", source); pauseErr != nil {
			err = &unconfirmedError{cause: errors.Join(err, pauseErr)}
			return
		}
		if staged {
			if restoreErr := edit.restore(cleanup); restoreErr != nil {
				err = &unconfirmedError{cause: errors.Join(err, restoreErr)}
				return
			}
		}
		err = fmt.Errorf("配置操作失败，原配置保留，维护已暂停以便核对: %w", err)
	}()

	var teamReadonly bool
	if workspace != nil {
		teamReadonly, err = workspace.check(root, target)
		if err != nil {
			return nil, err
		}
	}
	if wasServing {
		if err := m.stopDetached(ctx, health); err != nil {
			return nil, err
		}
	}
	if err := m.admitDeviceConfig(ctx, root, source, target, address, port, workspace == nil || wasServing, func(coordinator string) error {
		marker.ID = coordinator
		if workspace != nil {
			teamReadonly, err = workspace.check(root, target)
			if err != nil {
				return err
			}
			identity, err := deviceProfileIdentity(target)
			if err != nil || !reflect.DeepEqual(identity, targetIdentity) {
				return errors.Join(errors.New("工作方式写入前账户身份已改变"), err)
			}
		}
		if err := updateMarker("Paused", target); err != nil {
			return err
		}
		if workspace != nil {
			staged, err = edit.publish(ctx)
			return err
		}
		return nil
	}); err != nil {
		return nil, err
	}
	var off bool
	if workspace == nil {
		control, err := m.prepareRuntimeAccountControl(ctx, image, info.SHA256, target, targetIdentity.AccountID, maintenance.Marker)
		if err != nil {
			return nil, err
		}
		params.Binding = control.binding
		request, err := params.request(requestID)
		if err != nil {
			return nil, err
		}
		var publishErr error
		if staged, publishErr = edit.publish(ctx); publishErr != nil {
			return nil, publishErr
		}
		validation, cancel := context.WithTimeout(ctx, 10*time.Second)
		reply, err := m.exchangeRuntimeControl(validation, control.image, control.port, control.grant, request)
		cancel()
		if err != nil {
			return nil, err
		}
		off, err = verifyDeviceConfigReceipt(reply, params.deviceConfigReview, targetIdentity)
		if err != nil {
			return nil, err
		}
	} else {
		off = workspace.mode == "off"
	}
	if err := edit.verifyCandidate(source, target, sourceIdentity, targetIdentity); err != nil {
		return nil, err
	}

	applied := false
	idle := targetIdentity.Kind == "unbound" ||
		(targetIdentity.Kind == "absent" && !wasServing && source == target && sourceIdentity.Kind == "absent")
	if workspace != nil {
		idle = !wasServing
	}
	storeReady := false
	if !idle {
		// A private validation receipt cannot confirm a serving Runtime.
		record := runtimeAccountRecord{Root: root, Source: source, Token: token}
		launch, err := m.prepareRuntimeProfileLaunch(ctx, image, info.SHA256, record, &maintenance, target, targetIdentity.AccountID)
		if err != nil {
			return nil, err
		}
		cmd := runtimeServingCommand(launch.image, m.DataRoot, "127.0.0.1", strconv.FormatUint(launch.port, 10), launch.binding.LaunchID)
		launchID = launch.binding.LaunchID
		startup, cancel := context.WithTimeout(ctx, 90*time.Second)
		active, launchErr := m.launchRuntimeServing(startup, cmd)
		cancel()
		starting = cmd.Process != nil
		if starting {
			launchedPID = uint32(cmd.Process.Pid)
		}
		if launchErr != nil {
			return nil, launchErr
		}
		if err := m.verifyDeviceConfigOwner(ctx, active, info, target); err != nil {
			return nil, err
		}
		if active.InstanceID != launch.binding.LaunchID {
			return nil, errors.New("启动后的 Runtime 与配置授权不匹配")
		}
		if !active.StoreReady && !off && targetIdentity.AccountID != nil {
			unlock, cancel := context.WithTimeout(ctx, 30*time.Second)
			active, err = m.unlockRuntimeProfileTarget(unlock, launch, maintenance, record, target,
				*targetIdentity.AccountID, targetIdentity.SessionDigest, active.PID)
			cancel()
			if err != nil {
				return nil, err
			}
		}
		allowLocked := workspace != nil && targetIdentity.Kind != "bound" && !health.StoreReady
		if !active.StoreReady && !off && !allowLocked {
			return nil, errors.New("Runtime 未确认目标存储已就绪")
		}
		if workspace != nil && off && active.StoreReady {
			return nil, errors.New("停用工作区后 Runtime 存储仍处于打开状态")
		}
		storeReady = active.StoreReady
		if wasServing {
			applied = true
		} else {
			if err := m.stopDetached(ctx, active); err != nil {
				return nil, err
			}
			starting = false
		}
	}
	if workspace == nil || wasServing {
		current, err := m.Installed()
		if err != nil || current != info {
			return nil, errors.Join(errors.New("Runtime 选用版本已改变"), err)
		}
		if err := m.verifyInstalled(info); err != nil {
			return nil, err
		}
	}
	if !applied {
		if err := requireClosedRuntimePort(ctx, address); err != nil {
			return nil, err
		}
	}
	configLock, err := lockDeviceConfig(ctx, edit.path)
	if err != nil {
		return nil, err
	}
	defer configLock.Close()
	if err := edit.verifyCandidate(source, target, sourceIdentity, targetIdentity); err != nil {
		return nil, err
	}
	if workspace == nil {
		result, err = m.deviceConfigResult(ctx, edit.after, applied)
	} else {
		if applied {
			active, _, err := m.Health(ctx)
			if err != nil {
				return nil, err
			}
			if err := m.verifyDeviceConfigOwner(ctx, active, info, target); err != nil {
				return nil, err
			}
			if active.InstanceID != launchID || active.StoreReady != storeReady {
				return nil, errors.New("工作方式应用前 Runtime 实例或存储状态已改变")
			}
		}
		teamReadonly, err = workspace.check(root, target)
		if err != nil {
			return nil, err
		}
		result, err = workspace.result(teamReadonly, applied, storeReady)
	}
	if err != nil {
		return nil, err
	}
	if err := updateMarker("Verified", target); err != nil {
		return nil, err
	}
	if err := removeOwnedRuntimeAccountRecord(markerPath, maintenance.Marker); err != nil {
		return nil, err
	}
	return result, nil
}

// The current listener and these locks decide whether historical records can
// be archived. Runtime rechecks the Paused grant after taking its own locks.
func (m *Manager) admitDeviceConfig(ctx context.Context, root, source, target, address string, port uint64, archiveRecords bool, pause func(string) error) (err error) {
	for i, profile := range []string{root, source, target} {
		if (i > 0 && samePath(profile, root)) || (i == 2 && samePath(target, source)) {
			continue
		}
		lock, lockErr := lockRuntimeDatabase(ctx, profile, 0)
		if lockErr != nil {
			return fmt.Errorf("无法独占 Runtime 安装或配置任务涉及的库 (%s): %w", profile, lockErr)
		}
		defer func() { err = errors.Join(err, lock.Close()) }()
	}
	if err := requireClosedRuntimePort(ctx, address); err != nil {
		return err
	}
	if err := m.requireNoDeviceMaintenance(); err != nil {
		return err
	}
	if !archiveRecords {
		coordinator, err := runtimeRegistrationCoordinator(root, nil)
		if err != nil {
			return err
		}
		return pause(coordinator)
	}
	coordinator, err := deviceConfigCoordinator(root, port)
	if err != nil {
		return err
	}
	endpoint := filepath.Join(root, "runtime", "endpoint.json")
	if info, err := os.Lstat(endpoint); err == nil {
		if err := archiveRuntimeRecord(endpoint, info); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return pause(coordinator)
}

func deviceConfigCoordinator(root string, port uint64) (string, error) {
	path := filepath.Join(root, "runtime", "autostart", "registration.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return runtimeRegistrationCoordinator(root, nil)
	}
	if err != nil {
		return "", err
	}
	if info.Mode().IsRegular() && info.Size() <= controlFrameLimit {
		bytes, err := readCredentialConfig(path, controlFrameLimit)
		if err != nil {
			return "", err
		}
		if id := deviceConfigRegistrationID(bytes, root, port); id != "" {
			return id, nil
		}
	}
	if err := archiveRuntimeRecord(path, info); err != nil {
		return "", err
	}
	return runtimeRegistrationCoordinator(root, nil)
}

// dev.28 checks this tuple even for private, non-supervised maintenance.
func deviceConfigRegistrationID(raw []byte, root string, port uint64) string {
	var registration map[string]json.RawMessage
	if !utf8.Valid(raw) || json.Unmarshal(raw, &registration) != nil || string(registration["schema"]) != "1" {
		return ""
	}
	readString := func(fields map[string]json.RawMessage, key string) string {
		var value string
		if json.Unmarshal(fields[key], &value) != nil {
			return ""
		}
		return value
	}
	owner := readString(registration, "owner")
	installation := readString(registration, "installation")
	if owner == "" || filepath.Clean(installation) != filepath.Clean(root) {
		return ""
	}
	for _, component := range strings.Split(filepath.ToSlash(installation), "/") {
		if component == ".." {
			return ""
		}
	}
	platform := runtime.GOOS
	if platform == "darwin" {
		platform = "macos"
	}
	id := controlDigest([]byte("respire-runtime-v1\x00" + root + "\x00" + owner))[:24]
	if readString(registration, "platform") != platform || readString(registration, "id") != id {
		return ""
	}
	var environment map[string]json.RawMessage
	if json.Unmarshal(registration["environment"], &environment) != nil {
		return ""
	}
	rawPort, rawRoot := readString(environment, "RSRS_RPC_PORT"), readString(environment, "RSRS_DATA_DIR")
	registeredPort, err := strconv.ParseUint(strings.TrimPrefix(rawPort, "+"), 10, 16)
	if err != nil || registeredPort != port || rawRoot == "" {
		return ""
	}
	registeredRoot, err := canonicalSessionDirectory(rawRoot)
	if err != nil || registeredRoot != root {
		return ""
	}
	return id
}

func (e deviceConfigEdit) verifyCandidate(source, target string, sourceIdentity, targetIdentity runtimeAuthIdentity) error {
	current, err := deviceConfigBytes(e.path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, e.candidate) {
		return errors.New("client.json 候选配置已改变")
	}
	for _, entry := range []struct {
		profile  string
		identity runtimeAuthIdentity
	}{{source, sourceIdentity}, {target, targetIdentity}} {
		current, err := canonicalSessionDirectory(entry.profile)
		if err != nil || current != entry.profile {
			return errors.Join(errors.New("配置验证期间数据目录已改变"), err)
		}
		identity, err := deviceProfileIdentity(current)
		if err != nil || !reflect.DeepEqual(identity, entry.identity) {
			return errors.Join(errors.New("配置验证期间账户身份已改变"), err)
		}
	}
	return nil
}

func verifyDeviceConfigReceipt(reply rpcResponse, review deviceConfigReview, target runtimeAuthIdentity) (bool, error) {
	var envelope struct {
		Command string
		Status  string
		Summary json.RawMessage
		Details json.RawMessage
		Errors  []string
		Items   []struct{ Name, Status, Value string }
	}
	if json.Unmarshal(reply.Envelope, &envelope) != nil || envelope.Command != "config-validate" ||
		envelope.Status != "ok" || string(envelope.Details) != "null" || len(envelope.Errors) != 0 {
		return false, errors.New("Runtime 配置验证回执无效")
	}
	if reply.DataDir != "" && !samePath(reply.DataDir, review.TargetProfile) {
		return false, errors.New("Runtime 配置验证返回了不同的数据目录")
	}
	ready, stopped := 0, 0
	for _, item := range envelope.Items {
		switch item.Name {
		case "control_ready":
			if item.Status != "ok" || item.Value != "true" {
				return false, errors.New("Runtime 配置验证控制通道未就绪")
			}
			ready++
		case "store_ready":
			if item.Status != "skip" || item.Value != "false" {
				return false, errors.New("Runtime 配置验证返回了意外的存储状态")
			}
			stopped++
		}
	}
	var receipt struct {
		deviceConfigReview
		TargetIdentity runtimeAuthIdentity `json:"target_identity"`
		OffMode        *bool               `json:"off_mode"`
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.Summary))
	decoder.DisallowUnknownFields()
	if ready != 1 || stopped != 1 || decoder.Decode(&receipt) != nil || receipt.OffMode == nil ||
		!reflect.DeepEqual(receipt.deviceConfigReview, review) || !reflect.DeepEqual(receipt.TargetIdentity, target) {
		return false, errors.New("Runtime 配置验证回执与原始配置或账户身份不符")
	}
	return *receipt.OffMode, nil
}
