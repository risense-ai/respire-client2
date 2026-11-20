//go:build linux || windows || (darwin && cgo)

package host

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

type recoveryCustody struct {
	Operation runtimeRecoveryOperation `json:"operation"`
	Input     runtimeRecoveryInput     `json:"input"`
}

func (m *Manager) desktopRuntimeKeyRecovery(ctx context.Context, args []string, secret, id, method string) (raw json.RawMessage, err error) {
	input, err := authParams(args, secret)
	if err != nil {
		return nil, err
	}
	op := strings.TrimPrefix(id, "desktop-")
	if !hostOperationID.MatchString(op) || m.ClientVersion == "" {
		return nil, errors.New("恢复需要有效的原请求身份")
	}
	takeover, err := m.lockRuntimeTakeover(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, takeover.Close()) }()
	root, err := canonicalSessionDirectory(m.DataRoot)
	if err != nil {
		return nil, err
	}
	source, err := selectedRuntimeLoginProfile(root)
	if err != nil {
		return nil, err
	}
	var custody recoveryCustody
	var user string
	controlMethod := "control.key.fivekeys"
	if method == "auth.reset" {
		controlMethod = "control.key.super_reset"
		custody, user, err = prepareDesktopKeyReset(ctx, source, op, input)
	} else {
		custody, err = prepareDesktopFivekeys(op, input)
		user = custody.Operation.User
	}
	if err != nil {
		return nil, err
	}
	return m.executeDesktopRuntimeKey(ctx, id, op, source, user, controlMethod, custody)
}

func prepareDesktopFivekeys(op string, input map[string]string) (recoveryCustody, error) {
	intent := runtimeRecoveryOperation{Kind: "fivekeys", Server: strings.TrimRight(strings.TrimSpace(input["addr"]), "/"),
		User: input["user"], Salt: input["kdf_salt"], WrappedURK: input["wrapped_urk"], Nonce: input["urk_nonce"], UseSuper: input["super"] != ""}
	password, hasPassword := input["password"]
	if intent.Salt == "" || intent.WrappedURK == "" || intent.Nonce == "" || (intent.Server == "") != (intent.User == "") ||
		(!intent.UseSuper && (!hasPassword || input["secret"] == "")) || (intent.User != "" && password == "") {
		return recoveryCustody{}, errors.New("恢复需要完整的密钥材料。在线恢复还需要服务器、账户和密码")
	}
	if input["totp"] != "" {
		return recoveryCustody{}, errors.New("请在 Runtime 请求两步验证码后续接原恢复事务")
	}
	if intent.UseSuper {
		if err := verifyRecoverySuper(intent, input["super"]); err != nil {
			return recoveryCustody{}, err
		}
	} else if err := verifyRecoveryLegacy(intent, password, input["secret"]); err != nil {
		return recoveryCustody{}, err
	}
	if intent.Server != "" {
		addr, err := url.Parse(intent.Server)
		if err != nil || addr.Hostname() == "" || (addr.Scheme != "https" && addr.Scheme != "http") || addr.User != nil || addr.RawQuery != "" || addr.Fragment != "" {
			return recoveryCustody{}, errors.New("恢复服务器地址无效")
		}
	}
	material := runtimeRecoveryMaterial{Account: "login-" + strings.ReplaceAll(op, "-", ""), Backend: runtimeLoginBackend()}
	custody := recoveryCustody{Operation: intent, Input: runtimeRecoveryInput{Password: &password, Material: material}}
	if intent.UseSuper {
		super := input["super"]
		custody.Input.SuperPassword = &super
	} else {
		legacy := input["secret"]
		custody.Input.AccountSecret = &legacy
	}
	return custody, nil
}

func (m *Manager) executeDesktopRuntimeKey(ctx context.Context, id, op, source, user, controlMethod string, custody recoveryCustody) (json.RawMessage, error) {
	intent := custody.Operation
	info, err := m.Installed()
	if err != nil {
		return nil, err
	}
	if err := m.verifyInstalled(info); err != nil {
		return nil, err
	}
	var previous *HealthInfo
	health, _, err := m.Health(ctx)
	if err == nil {
		if err := m.verifySelectedHealth(health, info); err != nil {
			return nil, err
		}
		if health.ManagedID != "" || !samePath(health.DataDir, source) {
			return nil, errors.New("请先通过当前宿主暂停选定账户的 Runtime")
		}
		if err := m.stopDetached(ctx, health); err != nil {
			return nil, err
		}
		previous = &health
	} else if !IsUnavailable(err) {
		return nil, err
	}
	if err := m.bindDesktopRuntimeKey(id, op, source); err != nil {
		return nil, err
	}
	admission, err := m.publishPausedRuntimeMaintenance(ctx, source, m.ClientVersion,
		runtimeLoginIntent{OperationID: op, User: user, Server: intent.Server}, previous)
	if err != nil {
		return nil, err
	}
	var record runtimeAccountRecord
	if err := json.Unmarshal(admission.Record, &record); err != nil {
		return nil, err
	}
	image := filepath.Join(m.runtimeDir(info.Version), runtimeBinary())
	control, err := m.prepareRuntimeAccountControl(ctx, image, info.SHA256, source, record.Original.Identity.AccountID, admission.Marker)
	if err != nil {
		cleanup := discardUnsentRegistration(nativeRuntimeCredentialStore(ctx), record, &admission, nil, nil)
		if cleanup != nil {
			return nil, &unconfirmedError{id, errors.Join(err, cleanup)}
		}
		return nil, err
	}
	private, err := persistRecoveryCustody(ctx, record, custody, &admission)
	if err != nil {
		if admission.Path == "" {
			return nil, err
		}
		return nil, &unconfirmedError{id, err}
	}
	receipt, err := m.exchangeRuntimeRecovery(ctx, control, record, record.Original.Identity, intent, controlMethod, &custody.Input)
	if err != nil {
		return nil, &unconfirmedError{id, err}
	}
	result, err := m.finishDesktopRuntimeRecovery(ctx, image, info.SHA256, record, &admission, custody, private, receipt)
	if err != nil {
		return result, &unconfirmedError{id, err}
	}
	return result, nil
}

func verifyRecoverySuper(intent runtimeRecoveryOperation, super string) error {
	vault := exportVault{Salt: intent.Salt, Wrapped: intent.WrappedURK, Nonce: intent.Nonce}
	// The fivekeys format has no version field and accepts both v4 codes and v2 passphrases.
	if err := vault.verifyExportSuper(super); err == nil {
		return nil
	}
	version := uint64(2)
	vault.Version = &version
	urk, err := vault.unwrapWithOriginalFactors(runtimeRecoveryInput{SuperPassword: &super})
	clear(urk)
	return err
}

func verifyRecoveryLegacy(intent runtimeRecoveryOperation, password, secret string) error {
	vault := exportVault{Salt: intent.Salt, Wrapped: intent.WrappedURK, Nonce: intent.Nonce}
	urk, err := vault.unwrapWithOriginalFactors(runtimeRecoveryInput{Password: &password, AccountSecret: &secret})
	clear(urk)
	return err
}

func (m *Manager) bindDesktopRuntimeKey(id, operation, source string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[id]
	if job == nil || job.State != "running" || job.Transaction != nil {
		return errors.New("恢复操作无法绑定原账户身份")
	}
	job.Transaction = &hostTransaction{OperationID: operation, SourceProfile: source, Kind: "key", RuntimeKey: true}
	if err := m.persistOperationsLocked(); err != nil {
		job.Transaction = nil
		return err
	}
	return nil
}

func persistRecoveryCustody(ctx context.Context, record runtimeAccountRecord, custody recoveryCustody, admission *runtimeAccountAdmission) (private string, err error) {
	store := nativeRuntimeCredentialStore(ctx)
	material := custody.Input.Material
	created := make(map[string]string, 4)
	var metadata []byte
	defer func() {
		if err != nil {
			cleanup := discardUnsentRegistration(store, record, admission, metadata, created)
			if cleanup == nil {
				*admission = runtimeAccountAdmission{}
			}
			err = errors.Join(err, cleanup)
		}
	}()
	metadata, err = loginCustodyMetadata(record, material.Account, material.Backend, nil, nil)
	if err != nil {
		return "", err
	}
	if err := writeRuntimeAccountRecord(loginCustodyPath(record), metadata, nil); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(custody)
	if err != nil {
		return "", err
	}
	defer clear(encoded)
	private = string(encoded)
	slots := map[string]*string{"login-control:": &private, "pass:": custody.Input.Password, "super:": custody.Input.SuperPassword, "legacy-secret:": custody.Input.AccountSecret}
	if material.Reset != nil {
		slots = map[string]*string{"login-control:": &private, "super:": &material.Reset.Super}
	}
	for prefix, value := range slots {
		if value == nil || *value == "" {
			continue
		}
		key := prefix + material.Account
		current, err := store.readSlot(material.Backend, "rsrs", key, false)
		if err != nil {
			return "", err
		}
		if current == nil {
			created[key] = *value
		}
		if err := store.preserve("rsrs", key, *value); err != nil {
			return "", err
		}
	}
	return private, admission.markLoginSent()
}

func desktopRecoveryEnvelope(receipt runtimeRecoveryReceipt) (json.RawMessage, error) {
	status := "ok"
	if receipt.Outcome == "awaiting_input" {
		status = "pending"
	}
	summary := map[string]any{
		"operation_id": receipt.OperationID, "source_profile": receipt.Source, "outcome": receipt.Outcome,
		"runtime_key": true, "required_inputs": receipt.RequiredInputs,
	}
	if receipt.Super != nil && receipt.Outcome == "committed" {
		summary["super"] = *receipt.Super
	}
	return json.Marshal(map[string]any{"command": "key-control", "status": status, "summary": summary})
}

func (m *Manager) finishDesktopRuntimeRecovery(ctx context.Context, image, digest string, record runtimeAccountRecord,
	admission *runtimeAccountAdmission, custody recoveryCustody, private string, receipt runtimeRecoveryReceipt) (json.RawMessage, error) {
	if receipt.Outcome == "awaiting_input" {
		if receipt.Challenge != nil {
			custody.Input.Material.Challenge = receipt.Challenge
			stored, err := json.Marshal(custody)
			if err != nil {
				return nil, err
			}
			defer clear(stored)
			if err := nativeRuntimeCredentialStore(ctx).replace("rsrs", "login-control:"+custody.Input.Material.Account, &private, string(stored)); err != nil {
				return nil, err
			}
		}
		raw, err := desktopRecoveryEnvelope(receipt)
		return raw, errors.Join(errors.New("恢复需要原事务的补充输入"), err)
	}
	target, account, err := verifyRuntimeRecoveryTarget(record, receipt)
	if err != nil {
		return nil, err
	}
	if reset := custody.Input.Material.Reset; reset != nil && (receipt.Super == nil || *receipt.Super != reset.Super) {
		return nil, errors.New("Runtime 更换回执的 Super Key 与原私有材料不一致")
	}
	launch, err := m.prepareRuntimeProfileLaunch(ctx, image, digest, record, admission, target, receipt.AccountID)
	if err != nil {
		return nil, err
	}
	cmd := runtimeServingCommand(launch.image, m.DataRoot, "127.0.0.1", strconv.FormatUint(launch.port, 10), launch.binding.LaunchID)
	health, err := m.launchRuntimeServing(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if _, err := m.unlockRuntimeProfileTarget(ctx, launch, *admission, record, target, account, *receipt.SessionDigest, health.PID); err != nil {
		return nil, err
	}
	if custody.Operation.Kind == "super_reset" {
		if err := saveDesktopKeyResetReceipt(record, receipt); err != nil {
			return nil, err
		}
	}
	var marker runtimeMaintenanceMarker
	if err := json.Unmarshal(admission.Marker, &marker); err != nil {
		return nil, err
	}
	marker.Phase = "Verified"
	verified, err := json.Marshal(marker)
	if err != nil {
		return nil, err
	}
	markerPath := filepath.Join(record.Root, "runtime", "autostart", "maintenance.json")
	if err := writeRuntimeAccountRecord(markerPath, verified, admission.Marker); err != nil {
		return nil, err
	}
	admission.Marker = verified
	if custody.Operation.Kind == "super_reset" {
		if err := retireRuntimeLogin(ctx, record, admission.Marker, private); err != nil {
			return nil, err
		}
		return desktopRecoveryEnvelope(receipt)
	}
	return m.completeDesktopRuntimeRecovery(ctx, image, digest, record, *admission, target, receipt.Identity, custody, private)
}

func (m *Manager) completeDesktopRuntimeRecovery(ctx context.Context, image, digest string, record runtimeAccountRecord,
	admission runtimeAccountAdmission, target string, identity runtimeAuthIdentity, custody recoveryCustody, private string) (json.RawMessage, error) {
	control, err := m.prepareRuntimeControlGrant(ctx, image, digest, target, identity.AccountID, admission.Marker, "Verified")
	if err != nil {
		return nil, err
	}
	completed, err := m.exchangeRuntimeRecovery(ctx, control, record, identity, custody.Operation, "control.key.complete", &custody.Input)
	if err != nil {
		return nil, err
	}
	confirmed, _, err := verifyRuntimeRecoveryTarget(record, completed)
	if err != nil || completed.Outcome != "committed" || !samePath(confirmed, target) || completed.Identity.SessionDigest != identity.SessionDigest {
		return nil, errors.Join(err, errors.New("Runtime 未提交原恢复事务"))
	}
	if err := retireRuntimeLogin(ctx, record, admission.Marker, private); err != nil {
		return nil, err
	}
	return desktopRecoveryEnvelope(completed)
}
