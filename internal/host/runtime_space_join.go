package host

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var spaceJoinName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,40}$`)

type spaceJoinIntent struct {
	InviteDigest, Space, Address, User string
	Session, TokenDigest               string
	Readonly, Legacy                   bool
}

type spaceJoinInput struct {
	Code, Super string
	Legacy      *string
}

func parseSpaceJoinArgs(args []string, op string) (*spaceJoinIntent, *spaceJoinInput, error) {
	bad := errors.New("空间邀请码格式无效")
	if len(args) != 1 || !hostOperationID.MatchString(op) {
		return nil, nil, bad
	}
	code := strings.TrimSpace(args[0])
	encoded := strings.TrimPrefix(code, "1mem-invite:")
	if len(encoded) == 0 || len(encoded) > 32*1024 {
		return nil, nil, bad
	}
	payload, err := hex.DecodeString(encoded)
	if err != nil {
		return nil, nil, bad
	}
	defer clear(payload)
	var value struct {
		Space, Addr, User, Token, Super string
		Session                         string `json:"session_id"`
		Readonly                        bool
		Legacy                          *string `json:"secret_key"`
	}
	if json.Unmarshal(payload, &value) != nil || strings.TrimSpace(value.Token) == "" || strings.TrimSpace(value.Super) == "" {
		return nil, nil, bad
	}
	if value.Legacy != nil && strings.TrimSpace(*value.Legacy) == "" {
		return nil, nil, bad
	}
	intent := &spaceJoinIntent{InviteDigest: controlDigest([]byte(code)), Space: strings.TrimSpace(value.Space),
		Address: strings.TrimRight(strings.TrimSpace(value.Addr), "/"), User: strings.TrimSpace(value.User),
		Session: value.Session, TokenDigest: controlDigest([]byte(value.Token)), Readonly: value.Readonly, Legacy: value.Legacy != nil}
	if !intent.valid() {
		return nil, nil, bad
	}
	return intent, &spaceJoinInput{Code: code, Super: value.Super, Legacy: value.Legacy}, nil
}

func (j spaceJoinIntent) valid() bool {
	address, err := url.Parse(j.Address)
	return err == nil && address.Hostname() != "" && (address.Scheme == "https" || address.Scheme == "http") &&
		address.User == nil && address.RawQuery == "" && address.Fragment == "" &&
		spaceJoinName.MatchString(j.Space) && filepath.IsLocal(j.Space) && !strings.EqualFold(j.Space, "main") && j.User != "" &&
		validHash(j.InviteDigest) && validHash(j.TokenDigest) && runtimeInstanceID.MatchString(j.Session)
}

func (j spaceJoinIntent) wire() map[string]any {
	return map[string]any{"kind": "join", "invite_sha256": j.InviteDigest, "space": j.Space,
		"addr": j.Address, "user": j.User, "readonly": j.Readonly}
}

func (j spaceJoinIntent) requireAbsentTarget(root string) error {
	accounts := filepath.Join(root, "accounts")
	if err := checkRuntimeAccountDirectory(accounts); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := requireAbsentRuntimeAccountFile(filepath.Join(accounts, j.Space)); err != nil {
		return errors.New("受邀空间目录已存在，无法导入")
	}
	return nil
}

func joinMaterial(store hostCredentialStore, r spaceKickRecord, input *spaceJoinInput) (spaceInviteMaterial, error) {
	material := spaceInviteMaterial{Alias: "login-" + strings.ReplaceAll(r.ID, "-", ""), Backend: runtimeLoginBackend()}
	if !store.supports(material.Backend) {
		return material, errors.New("加入空间需要持久的系统密钥后端")
	}
	superSlot, legacySlot := "super:"+material.Alias, "legacy-secret:"+material.Alias
	if input != nil {
		material.Super, material.Legacy = input.Super, input.Legacy
		if err := store.preserve("rsrs", superSlot, material.Super); err != nil {
			return material, errors.New("无法保管受邀空间密钥")
		}
		if material.Legacy != nil {
			if err := store.preserve("rsrs", legacySlot, *material.Legacy); err != nil {
				return material, errors.New("无法保管受邀空间的旧版密钥")
			}
		}
		return material, nil
	}
	super, err := store.readSlot(material.Backend, "rsrs", superSlot, false)
	if err != nil || super == nil {
		return material, errors.New("原加入事务的受邀密钥不可用")
	}
	material.Super = *super
	if r.Join.Legacy {
		material.Legacy, err = store.readSlot(material.Backend, "rsrs", legacySlot, false)
		if err != nil || material.Legacy == nil {
			return material, errors.New("原加入事务的旧版密钥不可用")
		}
	}
	return material, nil
}

func (m *Manager) exchangeSpaceJoin(ctx context.Context, image, digest string, marker []byte, r spaceKickRecord, input *spaceJoinInput, store hostCredentialStore, query bool) (json.RawMessage, error) {
	method := "control.space.join"
	var code *string
	if query {
		method = "control.space.result"
	} else {
		if input == nil || controlDigest([]byte(input.Code)) != r.Join.InviteDigest {
			return nil, errors.New("缺少原邀请输入")
		}
		if err := r.Join.requireAbsentTarget(r.Root); err != nil {
			return nil, err
		}
		code = &input.Code
	}
	control, err := m.prepareRuntimeAccountControl(ctx, image, digest, r.Profile, r.Identity.AccountID, marker)
	if err != nil {
		return nil, err
	}
	material, err := joinMaterial(store, r, input)
	if err != nil {
		return nil, err
	}
	id, err := controlUUID()
	if err != nil {
		return nil, err
	}
	params, err := json.Marshal(map[string]any{
		"schema_version": 1, "binding": control.binding, "identity": r.Identity, "source_identity": r.Identity,
		"operation": r.Join.wire(), "invite_code": code, "material": material,
	})
	if err != nil {
		return nil, err
	}
	defer clear(params)
	reply, exchangeErr := m.exchangeRuntimeControl(ctx, control.image, control.port, control.grant, runtimeControlRequest{
		V: 1, ID: id, Method: method, Args: []string{string(params)}, OperationID: r.ID, IdempotencyKey: r.ID,
	})
	defer clear(reply.Envelope)
	if exchangeErr != nil && (reply.Code != "space_rejected" || reply.Error != "") {
		return nil, errors.New("加入空间结果未确认，请查询原请求")
	}
	raw, rejected, err := verifiedSpaceJoinReply(reply, r, material)
	if err != nil {
		return nil, err
	}
	if rejected {
		if err := r.Join.requireAbsentTarget(r.Root); err != nil {
			return nil, err
		}
		if material.Legacy != nil {
			if err := store.remove("rsrs", "legacy-secret:"+material.Alias, *material.Legacy); err != nil {
				return nil, errors.New("未能清理已拒绝事务的旧版受邀密钥")
			}
		}
		if err := store.remove("rsrs", "super:"+material.Alias, material.Super); err != nil {
			return nil, errors.New("未能清理已拒绝事务的受邀密钥")
		}
	}
	return raw, nil
}

func verifiedSpaceJoinReply(reply rpcResponse, r spaceKickRecord, material spaceInviteMaterial) (json.RawMessage, bool, error) {
	bad := errors.New("加入空间没有可验证的终态回执，请查询原请求")
	var envelope struct {
		Command, Status string
		Items           []struct{ Name, Value, Status string }
		Summary         struct {
			ID                            string `json:"operation_id"`
			Fingerprint, Profile, Outcome string
			Identity                      runtimeAuthIdentity
			Operation                     struct {
				Kind, Space, Addr, User string
				InviteDigest            string `json:"invite_sha256"`
				Readonly                bool
			}
			Code          *string `json:"invite_code"`
			Session       *string `json:"session_id"`
			Target        *string `json:"target_profile"`
			TargetAccount *string `json:"target_account_id"`
			TargetDigest  *string `json:"target_session_sha256"`
			Kick          []json.RawMessage
			Remaining     *int
		}
	}
	if json.Unmarshal(reply.Envelope, &envelope) != nil || envelope.Command != "space-control" ||
		(reply.DataDir != "" && !samePath(reply.DataDir, r.Profile)) || len(envelope.Items) != 2 ||
		envelope.Items[0].Name != "control_ready" || envelope.Items[0].Value != "true" || envelope.Items[0].Status != "ok" ||
		envelope.Items[1].Name != "store_ready" || envelope.Items[1].Value != "false" || envelope.Items[1].Status != "skip" {
		return nil, false, bad
	}
	fingerprint, err := spaceOperationFingerprint(r, r.Join.wire())
	if err != nil {
		return nil, false, err
	}
	s, j := envelope.Summary, r.Join
	if s.ID != r.ID || s.Fingerprint != fingerprint || !samePath(s.Profile, r.Profile) ||
		!sameSpaceKickIdentity(s.Identity, r.Identity) || s.Operation.Kind != "join" ||
		s.Operation.InviteDigest != j.InviteDigest || s.Operation.Space != j.Space || s.Operation.Addr != j.Address ||
		s.Operation.User != j.User || s.Operation.Readonly != j.Readonly ||
		s.Code != nil || s.Session != nil || len(s.Kick) != 0 || s.Remaining != nil {
		return nil, false, bad
	}
	status := "ok"
	rejected := s.Outcome == "rejected"
	target := filepath.Join(r.Root, "accounts", j.Space)
	if rejected {
		if reply.OK || reply.Exit != 2 || reply.Code != "space_rejected" || envelope.Status != "fail" ||
			s.Target != nil || s.TargetAccount != nil || s.TargetDigest != nil {
			return nil, false, bad
		}
		status = "fail"
	} else {
		if s.Outcome != "committed" || !reply.OK || reply.Exit != 0 || reply.Code != "" || envelope.Status != "ok" ||
			s.Target == nil || !samePath(target, *s.Target) || s.TargetAccount == nil ||
			*s.TargetAccount != controlDigest([]byte(j.User)) || s.TargetDigest == nil || !validHash(*s.TargetDigest) {
			return nil, false, bad
		}
		if err := verifySpaceJoinTarget(target, *s.TargetDigest, j, material); err != nil {
			return nil, false, err
		}
	}
	var details any
	if !rejected {
		details = map[string]any{"space": j.Space, "addr": j.Address, "user": j.User, "readonly": j.Readonly,
			"dir": target, "keyring": true, "hint": "space use " + j.Space}
	}
	result, err := json.Marshal(map[string]any{"command": "space", "status": status, "summary": map[string]any{
		"action": "join", "outcome": s.Outcome, "operation_id": r.ID, "space": j.Space,
	}, "details": details})
	return result, rejected, err
}

func verifySpaceJoinTarget(target, digest string, j *spaceJoinIntent, material spaceInviteMaterial) error {
	bad := errors.New("受邀账户会话或权限与原邀请不匹配")
	if err := checkRuntimeAccountDirectory(target); err != nil {
		return err
	}
	raw, err := readCredentialConfig(filepath.Join(target, "session.json"), controlFrameLimit)
	if err != nil {
		return err
	}
	defer clear(raw)
	var session struct {
		User, Addr, Token string
		Session           string `json:"session_id"`
		Alias             string `json:"keyring_account"`
		Backend           string `json:"keyring_backend"`
		LegacyBackend     string `json:"legacy_secret_backend"`
		Version           int    `json:"vault_version"`
	}
	if controlDigest(raw) != digest || json.Unmarshal(raw, &session) != nil || session.User != j.User ||
		session.Addr != j.Address || controlDigest([]byte(session.Token)) != j.TokenDigest || session.Session != j.Session ||
		session.Alias != material.Alias || session.Backend != material.Backend ||
		session.Version < 2 || session.Version > 4 ||
		(session.Version == 3 && (session.LegacyBackend != material.Backend || material.Legacy == nil)) {
		return bad
	}
	gate, err := readCredentialConfig(filepath.Join(target, "agent.json"), controlFrameLimit)
	if j.Readonly {
		if err != nil || !bytes.Equal(gate, []byte(`{"readonly":true,"readonly_team":true}`)) {
			return bad
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return bad
	}
	return nil
}
