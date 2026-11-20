package host

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
)

type spaceInviteIntent struct {
	Kind     string  `json:"kind"`
	Note     *string `json:"note"`
	Readonly bool    `json:"readonly"`
}

type spaceInviteMaterial struct {
	Super   string  `json:"super_password"`
	Alias   string  `json:"keyring_account"`
	Backend string  `json:"keyring_backend"`
	Legacy  *string `json:"legacy_secret,omitempty"`
	User    string  `json:"-"`
	Address string  `json:"-"`
}

func parseSpaceInviteArgs(args []string) (*spaceInviteIntent, error) {
	intent := &spaceInviteIntent{Kind: "invite"}
	for _, arg := range args {
		if note, ok := strings.CutPrefix(arg, "--note="); ok && intent.Note == nil {
			note = strings.TrimSpace(note)
			if len(note) > 121 {
				return nil, errors.New("邀请备注过长")
			}
			intent.Note = &note
		} else if arg == "--readonly" && !intent.Readonly {
			intent.Readonly = true
		} else {
			return nil, errors.New("邀请参数无效")
		}
	}
	return intent, nil
}

func readSpaceInviteMaterial(profile, op string, identity runtimeAuthIdentity, store hostCredentialStore) (*spaceInviteMaterial, error) {
	raw, err := readCredentialConfig(filepath.Join(profile, "session.json"), controlFrameLimit)
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	var source exportVault
	if controlDigest(raw) != identity.SessionDigest || json.Unmarshal(raw, &source) != nil || source.User == "" || source.Server == "" {
		return nil, errors.New("邀请需要原空间所有者会话")
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, errors.New("空间会话格式无效")
	}
	for _, field := range []string{"pass", "super", "secret", "secret_key"} {
		if _, inline := snapshot[field]; inline {
			return nil, errors.New("请先完成原账户的旧会话密钥迁移，再创建邀请")
		}
	}
	version := resetVaultVersion(source)
	if version < 2 || version > 4 {
		return nil, errors.New("当前空间密钥格式不支持邀请")
	}
	factors, err := resetVaultFactors(store, source, nil)
	if err != nil {
		return nil, errors.New("无法取得当前空间的解密材料")
	}
	key, err := source.unwrapWithOriginalFactors(factors)
	if err != nil {
		return nil, errors.New("无法核对当前空间的解密材料")
	}
	clear(key)
	return &spaceInviteMaterial{
		Super: *factors.SuperPassword, Legacy: factors.AccountSecret,
		Alias: "login-" + strings.ReplaceAll(op, "-", ""), Backend: runtimeLoginBackend(), User: source.User, Address: source.Server,
	}, nil
}

type spaceInviteReceipt struct {
	ID                            string `json:"operation_id"`
	Fingerprint, Profile, Outcome string
	Identity                      runtimeAuthIdentity
	Operation                     spaceInviteIntent
	Code                          *string `json:"invite_code"`
	Session                       *string `json:"session_id"`
	Target                        *string `json:"target_profile"`
	TargetAccount                 *string `json:"target_account_id"`
	TargetDigest                  *string `json:"target_session_sha256"`
	Kick                          []json.RawMessage
	Remaining                     *int
}

func (m *Manager) exchangeSpaceInvite(ctx context.Context, image, digest string, marker []byte, r spaceKickRecord, material *spaceInviteMaterial, query bool) (json.RawMessage, error) {
	fingerprint, err := spaceInviteFingerprint(r)
	if err != nil {
		return nil, err
	}
	exchange := func(method string, code *string) (spaceInviteReceipt, error) {
		var bad spaceInviteReceipt
		control, err := m.prepareRuntimeAccountControl(ctx, image, digest, r.Profile, r.Identity.AccountID, marker)
		if err != nil {
			return bad, err
		}
		id, err := controlUUID()
		if err != nil {
			return bad, err
		}
		params, err := json.Marshal(map[string]any{
			"schema_version": 1, "binding": control.binding, "identity": r.Identity, "source_identity": r.Identity,
			"operation": r.Invite, "invite_code": code, "material": material,
		})
		if err != nil {
			return bad, err
		}
		defer clear(params)
		reply, transportErr := m.exchangeRuntimeControl(ctx, control.image, control.port, control.grant, runtimeControlRequest{
			V: 1, ID: id, Method: method, Args: []string{string(params)}, OperationID: r.ID, IdempotencyKey: r.ID,
		})
		defer clear(reply.Envelope)
		if transportErr != nil && (reply.Code != "space_rejected" || reply.Error != "") {
			return bad, errors.New("邀请结果未确认，请查询原请求")
		}
		return verifySpaceInviteReply(reply, r, fingerprint)
	}
	method := "control.space.invite"
	if query {
		method = "control.space.result"
	}
	result, err := exchange(method, nil)
	if err != nil {
		return nil, err
	}
	if result.Outcome == "rejected" {
		return spaceInviteEnvelope(r, result, "")
	}
	if query || result.Outcome != "pending" || result.Code == nil || result.Session == nil {
		return nil, errors.New("原邀请材料不在当前内存中，结果仍未确认，未重新签发")
	}
	space, err := verifySpaceInviteCode(r, result, material)
	if err != nil {
		return nil, err
	}
	// The result method can acknowledge the exact Authorized code without
	// performing another POST. The code never leaves the private frames here.
	final, err := exchange("control.space.result", result.Code)
	if err != nil {
		return nil, err
	}
	if final.Outcome != "committed" || final.Code == nil || final.Session == nil ||
		*final.Code != *result.Code || *final.Session != *result.Session {
		return nil, errors.New("Runtime 未提交原邀请，请核对原请求")
	}
	return spaceInviteEnvelope(r, final, space)
}

func spaceInviteFingerprint(r spaceKickRecord) (string, error) {
	identity := map[string]any{"kind": r.Identity.Kind, "account_id": r.Identity.AccountID, "session_sha256": r.Identity.SessionDigest}
	intent := map[string]any{"kind": "invite", "note": r.Invite.Note, "readonly": r.Invite.Readonly}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(map[string]any{"schema_version": 1, "method": "control.space", "root": r.Root,
		"profile": r.Profile, "source_identity": identity, "operation": intent}); err != nil {
		return "", err
	}
	return controlDigest(bytes.TrimSuffix(data.Bytes(), []byte("\n"))), nil
}

func verifySpaceInviteReply(reply rpcResponse, r spaceKickRecord, fingerprint string) (spaceInviteReceipt, error) {
	var envelope struct {
		Command, Status string
		Items           []struct{ Name, Value, Status string }
		Summary         spaceInviteReceipt
	}
	bad := errors.New("邀请回执与原事务不匹配")
	if json.Unmarshal(reply.Envelope, &envelope) != nil || envelope.Command != "space-control" ||
		(reply.DataDir != "" && !samePath(reply.DataDir, r.Profile)) || len(envelope.Items) != 2 ||
		envelope.Items[0].Name != "control_ready" || envelope.Items[0].Value != "true" || envelope.Items[0].Status != "ok" ||
		envelope.Items[1].Name != "store_ready" || envelope.Items[1].Value != "false" || envelope.Items[1].Status != "skip" {
		return spaceInviteReceipt{}, bad
	}
	s := envelope.Summary
	if s.ID != r.ID || s.Fingerprint != fingerprint || !samePath(r.Profile, s.Profile) || !sameSpaceKickIdentity(s.Identity, r.Identity) ||
		s.Operation.Kind != "invite" || s.Operation.Readonly != r.Invite.Readonly ||
		!sameSpaceInviteValue(s.Operation.Note, r.Invite.Note) || s.Target != nil || s.TargetAccount != nil ||
		s.TargetDigest != nil || len(s.Kick) != 0 {
		return spaceInviteReceipt{}, bad
	}
	switch s.Outcome {
	case "rejected":
		if reply.OK || reply.Exit != 2 || reply.Code != "space_rejected" || envelope.Status != "fail" ||
			s.Code != nil || s.Session != nil || s.Remaining != nil {
			return spaceInviteReceipt{}, bad
		}
	case "pending", "committed":
		status := "pending"
		if s.Outcome == "committed" {
			status = "ok"
		}
		if !reply.OK || reply.Exit != 0 || reply.Code != "" || envelope.Status != status ||
			s.Code == nil || s.Session == nil || !runtimeInstanceID.MatchString(*s.Session) {
			return spaceInviteReceipt{}, bad
		}
	default:
		return spaceInviteReceipt{}, errors.New("邀请结果未确认，未重新签发")
	}
	return s, nil
}

func sameSpaceInviteValue(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func verifySpaceInviteCode(r spaceKickRecord, result spaceInviteReceipt, material *spaceInviteMaterial) (string, error) {
	bad := errors.New("私有邀请材料与原事务不匹配")
	code, ok := strings.CutPrefix(*result.Code, "1mem-invite:")
	if !ok || len(*result.Code) > 32*1024 {
		return "", bad
	}
	decoded, err := hex.DecodeString(code)
	if err != nil {
		return "", bad
	}
	defer clear(decoded)
	var value struct {
		Space, Addr, User, Token, Super string
		Session                         string  `json:"session_id"`
		ID                              string  `json:"control_operation_id"`
		Fingerprint                     string  `json:"control_fingerprint"`
		Readonly                        *bool   `json:"readonly"`
		Legacy                          *string `json:"secret_key"`
	}
	name := filepath.Base(r.Profile)
	if samePath(r.Root, r.Profile) {
		name = material.User
	}
	if json.Unmarshal(decoded, &value) != nil || value.Space != name || value.Addr != material.Address ||
		value.User != material.User || strings.TrimSpace(value.Token) == "" || value.Super != material.Super ||
		!sameSpaceInviteValue(value.Legacy, material.Legacy) || value.Session != *result.Session || value.ID != r.ID ||
		value.Fingerprint != result.Fingerprint || value.Readonly == nil || *value.Readonly != r.Invite.Readonly {
		return "", bad
	}
	return name, nil
}

func spaceInviteEnvelope(r spaceKickRecord, result spaceInviteReceipt, space string) (json.RawMessage, error) {
	status := "ok"
	var details any
	if result.Outcome == "rejected" {
		status = "fail"
	} else {
		details = map[string]any{"code": result.Code, "session_id": result.Session, "readonly": r.Invite.Readonly, "space": space}
	}
	return json.Marshal(map[string]any{"command": "space", "status": status, "summary": map[string]any{
		"action": "invite", "outcome": result.Outcome, "operation_id": r.ID,
	}, "details": details})
}

func spaceMembershipPostcondition(raw json.RawMessage, invite bool, cause error) (json.RawMessage, error) {
	if !invite {
		return raw, cause
	}
	var result struct {
		Command, Status string
		Summary         json.RawMessage
		Details         map[string]json.RawMessage
	}
	if json.Unmarshal(raw, &result) != nil || result.Status != "ok" || result.Details["code"] == nil {
		return nil, cause
	}
	result.Details["warning"] = json.RawMessage(`"邀请已签发。请保存邀请材料，并检查 Runtime 恢复状态。"`)
	return json.Marshal(map[string]any{"command": result.Command, "status": "warn", "summary": result.Summary, "details": result.Details})
}
