//go:build linux || windows || (darwin && cgo)

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
)

type runtimeLogoutIntent struct {
	Kind string `json:"kind"`
	Full bool   `json:"full"`
}

// Uses the existing identity-write namespace to exclude competing host writers.
type runtimeLogoutRecord struct {
	Schema      int                   `json:"schema_version"`
	Root        string                `json:"root"`
	Profile     string                `json:"profile"`
	ID          string                `json:"operation_id"`
	Key         string                `json:"key"`
	Source      runtimeAuthIdentity   `json:"source_identity"`
	Intent      runtimeLogoutIntent   `json:"operation"`
	Token       string                `json:"token"`
	Coordinator string                `json:"coordinator"`
	WasServing  bool                  `json:"was_serving"`
	StoreReady  bool                  `json:"store_ready"`
	Sent        bool                  `json:"sent"`
	InstanceID  string                `json:"client2_original_instance,omitempty"`
	ImageDigest string                `json:"client2_image_sha256,omitempty"`
	LaunchID    string                `json:"client2_restore_instance,omitempty"`
	Receipt     *runtimeLogoutReceipt `json:"client2_receipt,omitempty"`
}

func (m *Manager) LocalSessionHasToken() (bool, error) {
	_, config, err := m.readDeviceConfig()
	if err != nil {
		return false, err
	}
	raw, err := readCredentialConfig(filepath.Join(config.profile, "session.json"), controlFrameLimit)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer clear(raw)
	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &session); err != nil {
		return false, errors.New("无法读取本机会话状态")
	}
	return session.Token != "", nil
}

func (r runtimeLogoutRecord) fingerprint() (string, error) {
	raw, err := json.Marshal(r.Source)
	if err != nil {
		return "", err
	}
	var identity map[string]string
	if err := json.Unmarshal(raw, &identity); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(map[string]any{
		"schema_version": 1, "method": "control.identity.write", "root": r.Root, "profile": r.Profile,
		"source_identity": identity, "operation": map[string]any{"kind": r.Intent.Kind, "full": r.Intent.Full},
	}); err != nil {
		return "", err
	}
	// serde_json emits these Unicode code points as UTF-8, preserving escaped backslashes.
	rustJSON := strings.NewReplacer(`\\`, `\\`, `\u2028`, "\u2028", `\u2029`, "\u2029")
	return controlDigest([]byte(rustJSON.Replace(strings.TrimSuffix(buf.String(), "\n")))), nil
}

func (m *Manager) desktopRuntimeLogout(ctx context.Context, id string, full, query bool) (json.RawMessage, error) {
	return m.runtimeLogout(ctx, id, full, query, nativeRuntimeCredentialStore(ctx))
}

func (m *Manager) runtimeLogout(ctx context.Context, id string, full, query bool, store hostCredentialStore) (raw json.RawMessage, err error) {
	operation := strings.TrimPrefix(id, "desktop-")
	if !runtimeInstanceID.MatchString(operation) || operation[14] != '4' ||
		!strings.ContainsRune("89ab", rune(operation[19])) || m.ClientVersion == "" {
		return nil, errors.New("退出需要有效的请求身份和 Client2 版本")
	}
	takeover, err := m.lockRuntimeTakeover(ctx)
	if err != nil {
		return nil, err
	}
	unknown := query
	defer func() {
		err = errors.Join(err, takeover.Close())
		if err != nil && unknown {
			var pending *unconfirmedError
			if errors.As(err, &pending) {
				pending.id = id
			} else {
				err = &unconfirmedError{id: id, cause: err}
			}
		}
	}()
	address, port, err := m.runtimeAccountEndpoint()
	if err != nil {
		return nil, err
	}
	_, selected, err := m.readDeviceConfig()
	if err != nil {
		return nil, err
	}
	root, err := canonicalSessionDirectory(m.DataRoot)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(selected.profile, 0700); err != nil {
		return nil, err
	}
	profile, err := canonicalSessionDirectory(selected.profile)
	if err != nil || !samePath(profile, selected.profile) {
		return nil, errors.Join(errors.New("退出账户目录已重定向"), err)
	}
	directory := filepath.Join(root, "runtime", "autostart")
	if _, err := deviceProfilePath(directory); err != nil {
		return nil, err
	}
	path, markerPath := filepath.Join(directory, "identity-write.json"), filepath.Join(directory, "maintenance.json")
	stored, err := readCredentialConfig(path, runtimeMaintenanceLimit)
	if !query && err == nil {
		return nil, errors.New("已有原退出或会话撤销请求待核对，请查询原操作")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	intent := runtimeLogoutIntent{Kind: "logout", Full: full}
	original := runtimeLogoutRecord{Schema: 1, Root: root, Profile: profile, ID: operation, Key: operation, Intent: intent}
	if query && (json.Unmarshal(stored, &original) != nil || original.Schema != 1 || original.ID != operation ||
		original.Key != operation || original.Root != root || original.Profile != profile || original.Intent != intent ||
		!runtimeInstanceID.MatchString(original.Token) || (original.Sent && original.Coordinator == "") ||
		(original.StoreReady && !original.WasServing) || (original.Receipt != nil && !original.Sent) ||
		(original.WasServing && (!runtimeInstanceID.MatchString(original.InstanceID) || !validHash(original.ImageDigest)))) {
		return nil, errors.New("找不到与此请求、账户和退出类型匹配的原事务")
	}
	info, err := m.Installed()
	if err != nil {
		return nil, err
	}
	if err := m.verifyInstalled(info); err != nil {
		return nil, err
	}
	if original.WasServing && original.ImageDigest != info.SHA256 {
		return nil, errors.New("退出事务原 Runtime 映像已改变")
	}
	health, _, err := m.Health(ctx)
	serving := err == nil
	if serving {
		if health.ManagedID != "" {
			return nil, errors.New("当前 Runtime 由其他宿主管理，退出未更改其会话")
		}
		if err := m.verifyDeviceConfigOwner(ctx, health, info, profile); err != nil {
			return nil, err
		}
	} else if !IsUnavailable(err) {
		return nil, err
	}
	if !query {
		if err := m.requireNoDeviceMaintenance(); err != nil {
			return nil, err
		}
		if err := requireLogoutWritable(selected, profile); err != nil {
			return nil, err
		}
		original.Source, err = logoutSourceIdentity(profile)
		if err != nil {
			return nil, err
		}
		original.Token, err = controlUUID()
		if err != nil {
			return nil, err
		}
		if serving {
			original.WasServing, original.StoreReady = true, health.StoreReady
			original.InstanceID, original.ImageDigest = health.InstanceID, info.SHA256
		}
	}
	save := func() error {
		data, err := json.Marshal(original)
		if err != nil {
			return err
		}
		unknown = true
		if err := writeRuntimeAccountRecord(path, data, stored); err != nil {
			return err
		}
		stored = data
		return nil
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	if serving && original.Receipt == nil {
		if original.Sent || !original.WasServing || health.InstanceID != original.InstanceID {
			return nil, errors.New("当前 Runtime 不是原退出请求核验过的实例")
		}
		if err := m.requireNoDeviceMaintenance(); err != nil {
			return nil, err
		}
		current, err := deviceProfileIdentity(profile)
		if err != nil || !reflect.DeepEqual(current, original.Source) {
			return nil, errors.Join(errors.New("原退出会话在排空前已改变"), err)
		}
		if !query {
			if err := save(); err != nil {
				return nil, err
			}
		}
		if err := m.stopDetached(ctx, health); err != nil {
			return nil, err
		}
		serving = false
	}

	var marker []byte
	var current runtimeAuthIdentity
	if serving {
		if !original.WasServing || original.LaunchID == "" || health.InstanceID != original.LaunchID {
			return nil, errors.New("当前 Runtime 不属于原退出事务的恢复启动")
		}
		marker, err = readCredentialConfig(markerPath, runtimeMaintenanceLimit)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if marker != nil && !original.ownsMarker(marker, "Starting") {
			return nil, errors.New("原退出恢复启动的维护身份已改变")
		}
	} else {
		if err := withRuntimeLibraryLocks(ctx, root, profile, profile, func() error {
			if err := requireClosedRuntimePort(ctx, address); err != nil {
				return err
			}
			var err error
			current, err = deviceProfileIdentity(profile)
			if err != nil {
				return err
			}
			if !original.Sent && !reflect.DeepEqual(current, original.Source) {
				return errors.New("原退出会话在排空后已改变")
			}
			previous, err := readCredentialConfig(markerPath, runtimeMaintenanceLimit)
			if errors.Is(err, os.ErrNotExist) {
				return m.admitLockedDeviceConfig(ctx, root, address, port, true, func(coordinator string) error {
					if original.Coordinator != "" && original.Coordinator != coordinator {
						return errors.New("退出事务的 Runtime 协调器已改变")
					}
					original.Coordinator = coordinator
					birth, err := hostControlBirth(ctx)
					if err != nil {
						return err
					}
					marker, err = json.Marshal(runtimeMaintenanceMarker{
						Schema: 1, ID: coordinator, Token: original.Token, Phase: "Paused", ExpectedProfile: profile,
						ExpectedVersion: m.ClientVersion, OwnerPID: os.Getpid(), OwnerBirth: birth,
					})
					if err != nil {
						return err
					}
					return writeRuntimeAccountRecord(markerPath, marker, nil)
				})
			}
			if err != nil {
				return err
			}
			if !query || (!original.ownsMarker(previous, "Paused") &&
				(original.Receipt == nil || !original.ownsMarker(previous, "Starting"))) {
				return errors.New("原退出维护身份已改变")
			}
			record := runtimeAccountRecord{Root: root, Source: profile, Token: original.Token}
			marker, err = m.claimRuntimeLogin(ctx, record, previous, markerPath, profile)
			if err == nil && original.Coordinator == "" {
				var owned runtimeMaintenanceMarker
				if err := json.Unmarshal(marker, &owned); err != nil {
					return err
				}
				original.Coordinator = owned.ID
			}
			return err
		}); err != nil {
			return nil, err
		}
	}

	image := filepath.Join(m.runtimeDir(info.Version), runtimeBinary())
	if original.Receipt == nil {
		method := "control.identity.write_result"
		if !original.Sent {
			method = "control.identity.logout"
			original.Sent = true
			if err := save(); err != nil {
				return nil, err
			}
		}
		receipt, err := m.exchangeLogout(ctx, image, info, original, current, marker, method)
		if err != nil {
			return nil, err
		}
		original.Receipt = &receipt
		if err := save(); err != nil {
			return nil, err
		}
	}
	receipt := *original.Receipt
	if err := original.verifyResult(receipt); err != nil {
		return nil, err
	}
	verifySession := func() error {
		_, after, err := m.readDeviceConfig()
		if err != nil || !samePath(after.profile, profile) {
			return errors.Join(errors.New("退出后当前账户目录已改变"), err)
		}
		identity, err := deviceProfileIdentity(profile)
		if err != nil || !reflect.DeepEqual(identity, receipt.Identity) {
			return errors.Join(errors.New("退出回执与本机会话不符"), err)
		}
		return nil
	}
	if err := verifySession(); err != nil {
		return nil, err
	}
	if original.WasServing {
		if !serving {
			admission := runtimeAccountAdmission{Marker: marker}
			record := runtimeAccountRecord{Root: root, Source: profile, Token: original.Token}
			launch, err := m.prepareRuntimeProfileLaunch(ctx, image, info.SHA256, record, &admission, profile, receipt.Identity.AccountID)
			if err != nil {
				return nil, err
			}
			marker = admission.Marker
			original.LaunchID = launch.binding.LaunchID
			if err := save(); err != nil {
				return nil, err
			}
			cmd := runtimeServingCommand(launch.image, m.DataRoot, "127.0.0.1", strconv.FormatUint(port, 10), original.LaunchID)
			startup, cancel := context.WithTimeout(ctx, 90*time.Second)
			health, err = m.launchRuntimeServing(startup, cmd)
			cancel()
			if err != nil {
				return nil, err
			}
		}
		if err := m.verifyLogoutRuntime(ctx, health, info, original, receipt, store); err != nil {
			return nil, err
		}
	}
	finish := func() error {
		if err := verifySession(); err != nil {
			return err
		}
		if marker != nil {
			if err := removeOwnedRuntimeAccountRecord(markerPath, marker); err != nil {
				return err
			}
		}
		return removeOwnedRuntimeAccountRecord(path, stored)
	}
	if original.WasServing {
		err = finish()
	} else {
		err = withRuntimeLibraryLocks(ctx, root, profile, profile, func() error {
			if err := requireClosedRuntimePort(ctx, address); err != nil {
				return err
			}
			return finish()
		})
	}
	if err != nil {
		return nil, err
	}
	return logoutEnvelope(original, receipt)
}

func requireLogoutWritable(selected deviceConfigSnapshot, profile string) error {
	settings, err := readAgentSettingsObject(filepath.Join(profile, "agent.json"))
	if err != nil {
		return err
	}
	var mode string
	if raw := selected.fields["service_mode"]; raw != nil {
		if err := json.Unmarshal(raw, &mode); err != nil {
			return err
		}
	}
	if settings["readonly_team"] == true || mode == "readonly" || (mode == "" && settings["readonly"] == true) {
		return errors.New("当前账户为只读，不能退出会话")
	}
	return nil
}

func logoutSourceIdentity(profile string) (runtimeAuthIdentity, error) {
	raw, err := readCredentialConfig(filepath.Join(profile, "session.json"), controlFrameLimit)
	if errors.Is(err, os.ErrNotExist) {
		return runtimeAuthIdentity{Kind: "absent"}, nil
	}
	if err != nil {
		return runtimeAuthIdentity{}, err
	}
	defer clear(raw)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return runtimeAuthIdentity{}, errors.New("退出的原会话无效")
	}
	// dev.28 refuses to journal a session with inline credential material.
	for _, key := range []string{"pass", "super", "secret", "secret_key"} {
		if _, exists := fields[key]; exists {
			return runtimeAuthIdentity{}, errors.New("原会话仍含旧式内嵌凭据，需先完成宿主凭据迁移")
		}
	}
	return runtimeAuthIdentityOf(raw)
}

func (r runtimeLogoutRecord) ownsMarker(raw []byte, phase string) bool {
	var marker runtimeMaintenanceMarker
	return json.Unmarshal(raw, &marker) == nil && marker.Schema == 1 && marker.Phase == phase &&
		marker.Token == r.Token && marker.ExpectedProfile == r.Profile && marker.ID != "" &&
		(r.Coordinator == "" || marker.ID == r.Coordinator)
}

func (m *Manager) exchangeLogout(ctx context.Context, image string, info InstalledRuntime, r runtimeLogoutRecord, current runtimeAuthIdentity, marker []byte, method string) (runtimeLogoutReceipt, error) {
	grant, err := m.prepareRuntimeAccountControl(ctx, image, info.SHA256, r.Profile, current.AccountID, marker)
	if err != nil {
		return runtimeLogoutReceipt{}, err
	}
	params, err := json.Marshal(map[string]any{"schema_version": 1, "binding": grant.binding, "identity": current,
		"source_identity": r.Source, "operation": r.Intent})
	if err != nil {
		return runtimeLogoutReceipt{}, err
	}
	requestID, err := controlUUID()
	if err != nil {
		return runtimeLogoutReceipt{}, err
	}
	exchange, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	reply, err := m.exchangeRuntimeControl(exchange, grant.image, grant.port, grant.grant, runtimeControlRequest{
		V: 1, ID: requestID, Method: method, Args: []string{string(params)}, OperationID: r.ID, IdempotencyKey: r.Key,
	})
	if err != nil {
		return runtimeLogoutReceipt{}, err
	}
	return r.verifyReceipt(reply)
}

func (m *Manager) verifyLogoutRuntime(ctx context.Context, active HealthInfo, info InstalledRuntime, r runtimeLogoutRecord, receipt runtimeLogoutReceipt, store hostCredentialStore) error {
	if active.ManagedID != "" || active.InstanceID != r.LaunchID || active.InstanceID == r.InstanceID {
		return errors.New("恢复后的 Runtime 与本次退出启动授权不匹配")
	}
	if err := m.verifyDeviceConfigOwner(ctx, active, info, r.Profile); err != nil {
		return err
	}
	if r.Intent.Full {
		if active.StoreReady || active.CredentialState != "required" {
			return errors.New("全退出后 Runtime 未确认记忆库处于锁定状态")
		}
		return nil
	}
	if !r.StoreReady || active.StoreReady {
		return nil
	}
	unlock, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := m.unlockVerifiedRuntime(unlock, active, store, receipt.Identity.SessionDigest, false); err != nil {
		return err
	}
	current, _, err := m.Health(unlock)
	if err != nil {
		return err
	}
	if current.InstanceID != active.InstanceID || current.PID != active.PID || !current.StoreReady || current.CredentialState != "available" {
		return errors.New("普通退出后原账户记忆库未恢复就绪")
	}
	return m.verifyDeviceConfigOwner(unlock, current, info, r.Profile)
}

type runtimeLogoutReceipt struct {
	ID           string              `json:"operation_id"`
	Fingerprint  string              `json:"fingerprint"`
	Intent       runtimeLogoutIntent `json:"operation"`
	Outcome      string              `json:"outcome"`
	Path         string              `json:"path"`
	SessionFound *bool               `json:"session_found"`
	Digest       *string             `json:"session_sha256"`
	Identity     runtimeAuthIdentity `json:"identity"`
	Revoke       json.RawMessage     `json:"revoke"`
}

func (r runtimeLogoutRecord) verifyReceipt(reply rpcResponse) (runtimeLogoutReceipt, error) {
	var envelope struct {
		Command string
		Status  string
		Summary json.RawMessage
		Details json.RawMessage
		Errors  []string
		Items   []struct{ Name, Status, Value string }
	}
	var receipt runtimeLogoutReceipt
	if (reply.DataDir != "" && !samePath(reply.DataDir, r.Profile)) ||
		json.Unmarshal(reply.Envelope, &envelope) != nil || envelope.Command != "identity-write" ||
		envelope.Status != "ok" || string(envelope.Details) != "null" || len(envelope.Errors) != 0 {
		return receipt, errors.New("Runtime 未确认原退出操作")
	}
	control, store := 0, 0
	for _, item := range envelope.Items {
		switch item.Name {
		case "control_ready":
			if item.Status != "ok" || item.Value != "true" {
				return receipt, errors.New("退出控制通道未就绪")
			}
			control++
		case "store_ready":
			if item.Status != "skip" || item.Value != "false" {
				return receipt, errors.New("私有退出进程返回了意外的存储状态")
			}
			store++
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.Summary))
	decoder.DisallowUnknownFields()
	if control != 1 || store != 1 || decoder.Decode(&receipt) != nil {
		return receipt, errors.New("Runtime 未返回完整的私有退出回执")
	}
	return receipt, r.verifyResult(receipt)
}

func (r runtimeLogoutRecord) verifyResult(receipt runtimeLogoutReceipt) error {
	fingerprint, err := r.fingerprint()
	if err != nil {
		return err
	}
	if receipt.ID != r.ID || receipt.Fingerprint != fingerprint ||
		receipt.Intent != r.Intent || receipt.Outcome != "committed" || !samePath(receipt.Path, filepath.Join(r.Profile, "session.json")) ||
		receipt.SessionFound == nil || *receipt.SessionFound != (r.Source.Kind != "absent") || string(receipt.Revoke) != "null" {
		return errors.New("退出回执与原操作、类型或账户目录不符")
	}
	expected := r.Source
	if r.Intent.Full {
		expected = runtimeAuthIdentity{Kind: "absent"}
	}
	if expected.Kind != "absent" {
		if receipt.Digest == nil || !validHash(*receipt.Digest) {
			return errors.New("退出回执缺少有效会话摘要")
		}
		expected.SessionDigest = *receipt.Digest
	} else if receipt.Digest != nil {
		return errors.New("已删除会话的退出回执仍包含会话摘要")
	}
	if !reflect.DeepEqual(expected, receipt.Identity) {
		return errors.New("退出回执未确认预期的会话身份")
	}
	return nil
}

func logoutEnvelope(r runtimeLogoutRecord, receipt runtimeLogoutReceipt) (json.RawMessage, error) {
	mode, action, status := "session", "cleared", "ok"
	if r.Intent.Full {
		mode, action = "full", "deleted"
	}
	found := *receipt.SessionFound
	actions := []string{}
	if !found {
		action, status = "absent", "skip"
	} else if r.Intent.Full {
		actions = append(actions, "register or import five-key material to reconnect")
	} else {
		actions = append(actions, "login to reconnect")
	}
	return json.Marshal(map[string]any{"command": "logout", "status": status, "actions": actions,
		"summary": map[string]any{"mode": mode, "action": action, "session_found": found},
		"items":   []map[string]string{{"name": "session", "status": status, "value": action}},
		"details": map[string]any{"path": receipt.Path, "identity_deleted": r.Intent.Full && found, "credentials_cleared": !r.Intent.Full && found},
	})
}
