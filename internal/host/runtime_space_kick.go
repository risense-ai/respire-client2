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
)

type spaceKickIntent struct {
	Kind    string  `json:"kind"`
	Session *string `json:"session_id"`
	All     bool    `json:"all"`
}

type spaceKickRecord struct {
	ID, Root, Profile, Token string
	Identity                 runtimeAuthIdentity
	Intent                   spaceKickIntent
	Invite                   *spaceInviteIntent `json:",omitempty"`
	Runtime                  InstalledRuntime
	WasServing, StoreReady   bool
}

func (m *Manager) desktopSpaceMembership(ctx context.Context, id string, args []string, invite bool, recoveryName string, store hostCredentialStore) (raw json.RawMessage, err error) {
	query := recoveryName != ""
	if query {
		defer func() {
			var unknown *unconfirmedError
			if err != nil && len(raw) == 0 && !errors.As(err, &unknown) {
				err = &unconfirmedError{id: id, cause: err}
			}
		}()
	}
	op := strings.TrimPrefix(id, "desktop-")
	if !hostOperationID.MatchString(op) {
		return nil, errors.New("成员撤销需要有效请求 ID")
	}
	intent := spaceKickIntent{Kind: "kick", All: len(args) == 0}
	var invitation *spaceInviteIntent
	if invite {
		var err error
		invitation, err = parseSpaceInviteArgs(args)
		if err != nil {
			return nil, err
		}
	} else if len(args) > 0 {
		session := strings.TrimSpace(strings.TrimPrefix(args[0], "--session="))
		if !runtimeInstanceID.MatchString(session) {
			return nil, errors.New("成员会话 ID 无效")
		}
		intent.Session = &session
	}
	lock, err := m.lockRuntimeTakeover(ctx)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	root, err := canonicalSessionDirectory(m.DataRoot)
	if err != nil {
		return nil, err
	}
	profile, err := selectedRuntimeLoginProfile(root)
	if err != nil {
		return nil, err
	}
	if err := requireWritableSpace(root, profile); err != nil {
		return nil, err
	}
	identity, err := spaceKickIdentity(profile)
	if err != nil {
		return nil, err
	}
	info, err := m.Installed()
	if err != nil {
		return nil, err
	}
	if err := m.verifyInstalled(info); err != nil {
		return nil, err
	}

	directory := filepath.Join(root, "runtime", "autostart")
	if err := checkRuntimeAccountDirectory(directory); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	recordPath := filepath.Join(directory, "space-client2.json")
	markerPath := filepath.Join(directory, "maintenance.json")
	record := spaceKickRecord{ID: op, Root: root, Profile: profile, Identity: identity, Intent: intent, Invite: invitation, Runtime: info}
	var material *spaceInviteMaterial
	readMaterial := func() error {
		if !invite {
			return nil
		}
		var err error
		material, err = readSpaceInviteMaterial(profile, op, identity, store)
		return err
	}
	var recordBytes, marker []byte
	unknown := func(cause error) (json.RawMessage, error) {
		return nil, &unconfirmedError{id: id, cause: cause}
	}
	if query {
		recordBytes, err = readCredentialConfig(recordPath, runtimeMaintenanceLimit)
		if err != nil || json.Unmarshal(recordBytes, &record) != nil || record.ID != op || record.Root != root ||
			record.Profile != profile || record.Runtime != info || !sameSpaceKickIdentity(record.Identity, identity) ||
			(record.Invite != nil) != invite ||
			(!invite && (record.Intent.Kind != "kick" || record.Intent.All != (recoveryName == "space_kick_all") ||
				record.Intent.All != (record.Intent.Session == nil) ||
				(record.Intent.Session != nil && !runtimeInstanceID.MatchString(*record.Intent.Session)))) {
			return unknown(errors.New("无法确认原成员撤销事务，未重发请求"))
		}
		if err := readMaterial(); err != nil {
			return unknown(err)
		}
		marker, err = m.claimSpaceKickMarker(ctx, record, markerPath)
		if err != nil {
			return unknown(err)
		}
	} else {
		for _, name := range []string{"space-client2.json", "maintenance.json", "account-active.json", "key-control.json", "offline-keygen.json"} {
			if _, err := os.Lstat(filepath.Join(directory, name)); err == nil {
				return nil, errors.New("已有 Runtime 维护事务，请查询原操作")
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
		if m.ClientVersion == "" {
			return nil, errors.New("缺少 Client2 维护协调器版本")
		}
		if err := readMaterial(); err != nil {
			return nil, err
		}
		h, _, err := m.Health(ctx)
		if err == nil {
			if h.ManagedID != "" || !samePath(h.DataDir, profile) || h.AccountID != *identity.AccountID ||
				h.ImageSHA256 != info.SHA256 || h.RuntimeVersion != info.Version {
				return nil, errors.New("请先用原宿主停止当前 Runtime")
			}
			if err := m.verifySelectedHealth(h, info); err != nil {
				return nil, err
			}
			if err := m.stopDetached(ctx, h); err != nil {
				return nil, err
			}
			record.WasServing, record.StoreReady = true, h.StoreReady
		} else if !IsUnavailable(err) {
			return nil, err
		}
		record.Token, err = controlUUID()
		if err != nil {
			return nil, err
		}
		recordBytes, err = json.Marshal(record)
		if err != nil {
			return nil, err
		}
		marker, err = m.publishSpaceKickMarker(ctx, record, recordPath, markerPath, recordBytes)
		if err != nil {
			return unknown(err)
		}
	}
	image := filepath.Join(m.runtimeDir(info.Version), runtimeBinary())
	if invite {
		raw, err = m.exchangeSpaceInvite(ctx, image, info.SHA256, marker, record, material, query)
	} else {
		raw, err = m.exchangeSpaceKick(ctx, image, info.SHA256, marker, record, query)
	}
	if err != nil {
		return unknown(err)
	}
	current, err := spaceKickIdentity(profile)
	selected, selectionErr := selectedRuntimeLoginProfile(root)
	if err != nil || selectionErr != nil || selected != profile || !sameSpaceKickIdentity(identity, current) {
		return unknown(errors.New("成员变更后原账户选择已改变"))
	}
	if err := removeOwnedRuntimeAccountRecord(markerPath, marker); err != nil {
		return spaceMembershipPostcondition(raw, invite, err)
	}
	if err := removeOwnedRuntimeAccountRecord(recordPath, recordBytes); err != nil {
		return spaceMembershipPostcondition(raw, invite, fmt.Errorf("成员事务已有回执，清理事务失败: %w", err))
	}
	if record.WasServing {
		h, err := m.startDetachedLocked(ctx, info)
		if err == nil && (!samePath(h.DataDir, profile) || h.AccountID != *identity.AccountID ||
			!m.isDirectM3Runtime(h)) {
			err = errors.New("恢复的 Runtime 与原账户不匹配")
		}
		if err == nil && record.StoreReady && !h.StoreReady {
			err = m.unlockVerifiedRuntime(ctx, h, nativeRuntimeCredentialStore(ctx), identity.SessionDigest, false)
		}
		if err != nil {
			return spaceMembershipPostcondition(raw, invite, fmt.Errorf("成员事务已有回执，Runtime 恢复失败: %w", err))
		}
	}
	return raw, envelopeError(raw)
}

func (m *Manager) exchangeSpaceKick(ctx context.Context, image, digest string, marker []byte, record spaceKickRecord, query bool) (json.RawMessage, error) {
	identity, op := record.Identity, record.ID
	control, err := m.prepareRuntimeAccountControl(ctx, image, digest, record.Profile, identity.AccountID, marker)
	if err != nil {
		return nil, err
	}
	params, err := json.Marshal(map[string]any{
		"schema_version": 1, "binding": control.binding, "identity": identity, "source_identity": identity,
		"operation": record.Intent, "invite_code": nil,
	})
	if err != nil {
		return nil, err
	}
	requestID, err := controlUUID()
	if err != nil {
		return nil, err
	}
	method := "control.space.kick"
	if query {
		method = "control.space.result"
	}
	reply, exchangeErr := m.exchangeRuntimeControl(ctx, control.image, control.port, control.grant, runtimeControlRequest{
		V: 1, ID: requestID, Method: method, Args: []string{string(params)}, OperationID: op, IdempotencyKey: op,
	})
	// The transport returns the bound packet for a typed rejection as well.
	if exchangeErr != nil && (reply.Code != "space_rejected" || reply.Error != "") {
		return nil, errors.New("成员撤销结果未确认，请查询原请求")
	}
	return verifiedSpaceKickReply(reply, record)
}

func spaceKickIdentity(profile string) (runtimeAuthIdentity, error) {
	raw, err := readCredentialConfig(filepath.Join(profile, "session.json"), controlFrameLimit)
	if err != nil {
		return runtimeAuthIdentity{}, err
	}
	defer clear(raw)
	identity, err := runtimeAuthIdentityOf(raw)
	if err != nil || identity.Kind != "bound" || identity.AccountID == nil {
		return runtimeAuthIdentity{}, errors.New("成员撤销需要已登录的空间所有者")
	}
	return identity, nil
}

func sameSpaceKickIdentity(a, b runtimeAuthIdentity) bool {
	return a.Kind == "bound" && b.Kind == "bound" && a.AccountID != nil && b.AccountID != nil &&
		*a.AccountID == *b.AccountID && a.SessionDigest == b.SessionDigest
}

func requireWritableSpace(root, profile string) error {
	var agent struct {
		Readonly bool
		Team     bool `json:"readonly_team"`
	}
	var device struct {
		Mode string `json:"service_mode"`
	}
	if err := readServiceSettings(filepath.Join(profile, "agent.json"), &agent); err != nil {
		return err
	}
	if err := readServiceSettings(filepath.Join(root, "client.json"), &device); err != nil {
		return err
	}
	if agent.Team || device.Mode == "readonly" || (device.Mode != "normal" && device.Mode != "off" && agent.Readonly) {
		return errors.New("当前空间或设备为只读，无法更改成员")
	}
	return nil
}

func (m *Manager) stoppedSpaceKick(ctx context.Context, r spaceKickRecord, action func() ([]byte, error)) ([]byte, error) {
	address, _, err := m.runtimeAccountEndpoint()
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{filepath.Join(r.Root, "runtime"), r.Profile} {
		lock, err := lockRuntimeDatabase(ctx, dir, 0)
		if err != nil {
			return nil, err
		}
		defer lock.Close()
	}
	if err := requireStoppedM3Runtime(ctx, r.Root, address); err != nil {
		return nil, err
	}
	selected, err := selectedRuntimeLoginProfile(r.Root)
	if err != nil || selected != r.Profile {
		return nil, errors.New("成员事务的账户选择已改变")
	}
	identity, err := spaceKickIdentity(r.Profile)
	if err != nil || !sameSpaceKickIdentity(r.Identity, identity) {
		return nil, errors.New("成员事务的账户会话已改变")
	}
	if err := requireWritableSpace(r.Root, r.Profile); err != nil {
		return nil, err
	}
	return action()
}

func (m *Manager) publishSpaceKickMarker(ctx context.Context, r spaceKickRecord, recordPath, markerPath string, record []byte) ([]byte, error) {
	return m.stoppedSpaceKick(ctx, r, func() ([]byte, error) {
		directory := filepath.Dir(markerPath)
		if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if err := checkRuntimeAccountDirectory(directory); err != nil {
			return nil, err
		}
		reg, err := readCredentialConfig(filepath.Join(directory, "registration.json"), controlFrameLimit)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		coordinator, err := runtimeRegistrationCoordinator(r.Root, reg)
		if err != nil {
			return nil, err
		}
		birth, err := hostControlBirth(ctx)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(runtimeMaintenanceMarker{Schema: 1, ID: coordinator, Token: r.Token, Phase: "Paused",
			ExpectedProfile: r.Profile, ExpectedVersion: m.ClientVersion, OwnerPID: os.Getpid(), OwnerBirth: birth})
		if err != nil {
			return nil, err
		}
		if err := writeRuntimeAccountRecord(recordPath, record, nil); err != nil {
			return nil, err
		}
		return data, writeRuntimeAccountRecord(markerPath, data, nil)
	})
}

func (m *Manager) claimSpaceKickMarker(ctx context.Context, r spaceKickRecord, path string) ([]byte, error) {
	return m.stoppedSpaceKick(ctx, r, func() ([]byte, error) {
		return claimPausedRuntimeMarker(ctx, path, r.Root, r.Profile, r.Token)
	})
}

// The caller holds takeover and has verified the stopped owner and library locks.
func claimPausedRuntimeMarker(ctx context.Context, path, root, profile, token string) ([]byte, error) {
	raw, err := readCredentialConfig(path, runtimeMaintenanceLimit)
	var marker runtimeMaintenanceMarker
	if err != nil || json.Unmarshal(raw, &marker) != nil || marker.Schema != 1 || marker.Phase != "Paused" ||
		marker.Token != token || !hostOperationID.MatchString(token) || marker.ExpectedProfile != profile ||
		marker.OwnerPID <= 0 || marker.OwnerBirth == "" || marker.ExpectedVersion == "" {
		return nil, errors.Join(errors.New("原 Runtime 维护记录无效"), err)
	}
	registration, err := readCredentialConfig(filepath.Join(filepath.Dir(path), "registration.json"), controlFrameLimit)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	coordinator, err := runtimeRegistrationCoordinator(root, registration)
	if err != nil || coordinator != marker.ID {
		return nil, errors.New("原 Runtime 维护协调器已改变")
	}
	birth, err := hostControlBirth(ctx)
	if err != nil {
		return nil, err
	}
	if marker.OwnerPID == os.Getpid() && marker.OwnerBirth == birth {
		return raw, nil
	}
	previous, err := runtimeControlProcessBirth(ctx, marker.OwnerPID)
	if err != nil || previous == marker.OwnerBirth {
		return nil, errors.New("无法确认原 Runtime 维护协调器已退出")
	}
	marker.OwnerPID, marker.OwnerBirth = os.Getpid(), birth
	data, err := json.Marshal(marker)
	if err != nil {
		return nil, err
	}
	return data, writeRuntimeAccountRecord(path, data, raw)
}

func verifiedSpaceKickReply(reply rpcResponse, r spaceKickRecord) (json.RawMessage, error) {
	var envelope struct {
		Command, Status string
		Items           []struct{ Name, Value, Status string }
		Summary         struct {
			ID                            string `json:"operation_id"`
			Fingerprint, Profile, Outcome string
			Identity                      runtimeAuthIdentity
			Operation                     spaceKickIntent
			InviteCode                    *string `json:"invite_code"`
			SessionID                     *string `json:"session_id"`
			Target                        *string `json:"target_profile"`
			TargetAccount                 *string `json:"target_account_id"`
			TargetDigest                  *string `json:"target_session_sha256"`
			Kick                          []struct {
				ID    string `json:"session_id"`
				State string
			}
			Remaining *int
		}
	}
	bad := errors.New("成员撤销没有可验证的终态回执，请查询原请求")
	if json.Unmarshal(reply.Envelope, &envelope) != nil || envelope.Command != "space-control" ||
		(reply.DataDir != "" && !samePath(reply.DataDir, r.Profile)) || len(envelope.Items) != 2 ||
		envelope.Items[0].Name != "control_ready" || envelope.Items[0].Value != "true" || envelope.Items[0].Status != "ok" ||
		envelope.Items[1].Name != "store_ready" || envelope.Items[1].Value != "false" || envelope.Items[1].Status != "skip" {
		return nil, bad
	}
	// serde_json::Value serializes object keys in sorted order.
	intent := map[string]any{"kind": r.Intent.Kind, "session_id": r.Intent.Session, "all": r.Intent.All}
	identity := map[string]any{"kind": r.Identity.Kind, "account_id": r.Identity.AccountID, "session_sha256": r.Identity.SessionDigest}
	var fingerprint bytes.Buffer
	encoder := json.NewEncoder(&fingerprint)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(map[string]any{"schema_version": 1, "method": "control.space", "root": r.Root,
		"profile": r.Profile, "source_identity": identity, "operation": intent}); err != nil {
		return nil, err
	}
	s := envelope.Summary
	if s.ID != r.ID || s.Fingerprint != controlDigest(bytes.TrimSuffix(fingerprint.Bytes(), []byte("\n"))) ||
		!samePath(s.Profile, r.Profile) || !sameSpaceKickIdentity(s.Identity, r.Identity) ||
		s.Operation.Kind != r.Intent.Kind || s.Operation.All != r.Intent.All ||
		(s.Operation.Session == nil) != (r.Intent.Session == nil) ||
		(s.Operation.Session != nil && *s.Operation.Session != *r.Intent.Session) ||
		s.InviteCode != nil || s.SessionID != nil || s.Target != nil || s.TargetAccount != nil || s.TargetDigest != nil {
		return nil, bad
	}
	status := "ok"
	var revoked, failed int
	switch s.Outcome {
	case "rejected":
		if reply.OK || reply.Code != "space_rejected" || reply.Exit != 2 || envelope.Status != "fail" || len(s.Kick) != 0 || s.Remaining != nil {
			return nil, bad
		}
		status = "fail"
	case "committed":
		if !reply.OK || reply.Code != "" || reply.Exit != 0 || envelope.Status != "ok" || len(s.Kick) == 0 || s.Remaining == nil || *s.Remaining < 0 {
			return nil, bad
		}
		seen := map[string]bool{}
		for _, item := range s.Kick {
			if !runtimeInstanceID.MatchString(item.ID) || seen[item.ID] ||
				(!r.Intent.All && item.ID != *r.Intent.Session) {
				return nil, bad
			}
			seen[item.ID] = true
			switch item.State {
			case "revoked":
				revoked++
			case "absent", "rejected":
				failed++
			default:
				return nil, bad
			}
		}
		if failed != 0 {
			status = "warn"
		}
	default:
		return nil, bad
	}
	return json.Marshal(map[string]any{"command": "space", "status": status, "summary": map[string]any{
		"action": "kick", "outcome": s.Outcome, "operation_id": s.ID, "revoked": revoked, "failed": failed, "remaining": s.Remaining,
	}, "details": s.Kick})
}
