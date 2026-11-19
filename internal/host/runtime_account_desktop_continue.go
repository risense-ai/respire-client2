//go:build linux || windows || (darwin && cgo)

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

func (m *Manager) withPendingRuntimeLogin(ctx context.Context, transaction *hostTransaction, recoverStarting bool,
	run func(string, string, runtimeAccountAdmission, runtimeAccountRecord) (json.RawMessage, error)) (raw json.RawMessage, err error) {
	if transaction == nil || !transaction.RuntimeLogin || !hostOperationID.MatchString(transaction.OperationID) {
		return nil, errors.New("原 Runtime 登录事务身份无效")
	}
	takeover, err := m.lockRuntimeTakeover(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, takeover.Close()) }()
	info, err := m.Installed()
	if err != nil {
		return nil, err
	}
	if err := m.verifyInstalled(info); err != nil {
		return nil, err
	}
	root, err := canonicalSessionDirectory(m.DataRoot)
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(root, "runtime", "autostart")
	path := filepath.Join(directory, "account-results", transaction.OperationID+".json")
	recordBytes, err := readCredentialConfig(path, runtimeMaintenanceLimit)
	if err != nil {
		return nil, err
	}
	var record runtimeAccountRecord
	if json.Unmarshal(recordBytes, &record) != nil || record.Schema != 1 || !record.Sent ||
		record.OperationID != transaction.OperationID || record.Root != root ||
		record.Source != transaction.SourceProfile || record.Register {
		return nil, errors.New("原 Runtime 登录操作记录已改变")
	}
	active, err := readCredentialConfig(filepath.Join(directory, "account-active.json"), runtimeMaintenanceLimit)
	if err != nil {
		return nil, err
	}
	var route struct {
		Schema      int    `json:"schema"`
		OperationID string `json:"operation_id"`
	}
	if json.Unmarshal(active, &route) != nil || route.Schema != 1 || route.OperationID != record.OperationID {
		return nil, errors.New("原 Runtime 登录操作已不再持有当前路由")
	}
	markerPath := filepath.Join(directory, "maintenance.json")
	marker, err := readCredentialConfig(markerPath, runtimeMaintenanceLimit)
	if err != nil {
		return nil, err
	}
	var owner runtimeMaintenanceMarker
	if json.Unmarshal(marker, &owner) != nil || owner.Schema != 1 || owner.Token != record.Token ||
		(owner.Phase != "Paused" && !(recoverStarting && owner.Phase == "Starting")) {
		return nil, errors.New("原 Runtime 登录操作需要恢复维护状态")
	}
	profile, _, candidate, err := runtimeLoginQueryScope(ctx, record)
	if err != nil {
		return nil, err
	}
	defer clear(candidate)
	if owner.Phase == "Starting" && (len(candidate) == 0 || owner.ExpectedProfile != profile) {
		return nil, errors.New("原 Runtime 启动记录与已选账户的密钥不匹配")
	}
	marker, err = m.claimRuntimeLogin(ctx, record, marker, markerPath, profile)
	if err != nil {
		return nil, err
	}
	image := filepath.Join(m.runtimeDir(info.Version), runtimeBinary())
	return run(image, info.SHA256, runtimeAccountAdmission{Marker: marker, Record: recordBytes, Path: path}, record)
}

// Keep the original login token when reclaiming maintenance. Replacing an
// interrupted Starting marker invalidates the unused startup grant.
func (m *Manager) claimRuntimeLogin(ctx context.Context, record runtimeAccountRecord, original []byte, path, selected string) ([]byte, error) {
	var owner runtimeMaintenanceMarker
	if json.Unmarshal(original, &owner) != nil || owner.Schema != 1 || (owner.Phase != "Paused" && owner.Phase != "Starting") ||
		owner.Token != record.Token || (owner.ExpectedProfile != record.Source && owner.ExpectedProfile != selected) ||
		owner.OwnerPID <= 0 || uint64(owner.OwnerPID) > uint64(^uint32(0)) || owner.OwnerBirth == "" {
		return nil, errors.New("原 Runtime 登录维护身份无效")
	}
	birth, err := hostControlBirth(ctx)
	if err != nil {
		return nil, err
	}
	sameOwner := owner.OwnerPID == os.Getpid() && owner.OwnerBirth == birth
	if sameOwner && owner.Phase == "Paused" && owner.ExpectedProfile == selected {
		return original, nil
	}
	if !sameOwner {
		previous, err := runtimeControlProcessBirth(ctx, owner.OwnerPID)
		if err != nil {
			return nil, err
		}
		if previous == owner.OwnerBirth {
			return nil, errors.New("原 Runtime 登录仍由存活的客户端控制")
		}
	}
	address, _, err := m.runtimeAccountEndpoint()
	if err != nil {
		return nil, err
	}
	if err := requireClosedRuntimePort(ctx, address); err != nil {
		return nil, err
	}
	owner.OwnerPID, owner.OwnerBirth = os.Getpid(), birth
	owner.Phase, owner.ExpectedProfile = "Paused", selected
	next, err := json.Marshal(owner)
	if err != nil {
		return nil, err
	}
	if err := writeRuntimeAccountRecord(path, next, original); err != nil {
		return nil, err
	}
	return next, nil
}

func (m *Manager) recoverDesktopRuntimeLogin(ctx context.Context, job Operation) (json.RawMessage, error) {
	raw, err := m.withPendingRuntimeLogin(ctx, job.Transaction, true, func(image, digest string, admission runtimeAccountAdmission, record runtimeAccountRecord) (json.RawMessage, error) {
		result, err := m.queryRuntimeLoginResult(ctx, image, digest, admission, record)
		if err != nil {
			return nil, err
		}
		if result.Handoff != nil || result.Receipt.Outcome == "committed" {
			return nil, errors.New("Runtime 登录需要核对原提交状态")
		}
		return m.finishDesktopRuntimeLogin(ctx, image, digest, &admission, result.Receipt)
	})
	if err != nil {
		err = &unconfirmedError{id: job.ID, cause: err}
	}
	return raw, err
}

func (m *Manager) queryRuntimeLoginResult(ctx context.Context, image, digest string, admission runtimeAccountAdmission,
	record runtimeAccountRecord) (runtimeLoginResult, error) {
	profile, identity, candidate, err := runtimeLoginQueryScope(ctx, record)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	defer clear(candidate)
	control, err := m.prepareRuntimeAccountControl(ctx, image, digest, profile, identity.AccountID, admission.Marker)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	params, err := json.Marshal(map[string]any{
		"schema_version": 1, "binding": control.binding, "identity": identity, "candidate": candidate,
	})
	if err != nil {
		return runtimeLoginResult{}, err
	}
	defer clear(params)
	requestID, err := controlUUID()
	if err != nil {
		return runtimeLoginResult{}, err
	}
	reply, err := m.exchangeRuntimeControl(ctx, control.image, control.port, control.grant, runtimeControlRequest{
		V: 1, ID: requestID, Method: "control.auth.account_result", Args: []string{string(params)},
		OperationID: record.OperationID, IdempotencyKey: record.OperationID,
	})
	if err != nil {
		return runtimeLoginResult{}, err
	}
	defer clear(reply.Envelope)
	result, err := readRuntimeLoginResult(reply, record)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	if result.Receipt.Outcome == "awaiting_verification" || result.Receipt.Outcome == "committed" {
		if _, err := verifyRuntimeLoginTarget(record, result.Receipt); err != nil {
			return runtimeLoginResult{}, err
		}
	}
	return result, nil
}

func runtimeLoginQueryScope(ctx context.Context, record runtimeAccountRecord) (string, runtimeAuthIdentity, json.RawMessage, error) {
	profile, err := selectedRuntimeLoginProfile(record.Root)
	if err != nil {
		return "", runtimeAuthIdentity{}, nil, err
	}
	raw, err := readCredentialConfig(filepath.Join(profile, "session.json"), controlFrameLimit)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", runtimeAuthIdentity{}, nil, err
	}
	identity, err := runtimeAuthIdentityOf(raw)
	clear(raw)
	if err != nil {
		return "", runtimeAuthIdentity{}, nil, err
	}
	if profile == record.Source && reflect.DeepEqual(identity, record.Original.Identity) {
		return profile, identity, nil, nil
	}
	if identity.Kind != "bound" || identity.AccountID == nil || *identity.AccountID != controlDigest([]byte(record.User)) {
		return "", runtimeAuthIdentity{}, nil, errors.New("Selected Runtime account does not match the original login")
	}
	account := "login-" + strings.ReplaceAll(record.OperationID, "-", "")
	backend := runtimeLoginBackend()
	private, err := nativeRuntimeCredentialStore(ctx).readSlot(backend, "rsrs", "login-control:"+account, false)
	if err != nil {
		return "", runtimeAuthIdentity{}, nil, err
	}
	var custody runtimeLoginCustody
	var request runtimeLoginParams
	var material struct {
		Digest string `json:"session_sha256"`
	}
	if private == nil || json.Unmarshal([]byte(*private), &custody) != nil ||
		json.Unmarshal(custody.Request, &request) != nil || json.Unmarshal(custody.Candidate, &material) != nil ||
		request.Material.Account != account || request.Material.Backend != backend ||
		request.User != record.User || request.Server != record.Server ||
		request.Binding.InstallationRoot != record.Root || request.Binding.Profile != record.Source ||
		!reflect.DeepEqual(request.Identity, record.Original.Identity) || request.CustodyAck == nil ||
		request.CustodyAck.CandidateDigest != material.Digest || material.Digest != identity.SessionDigest {
		return "", runtimeAuthIdentity{}, nil, errors.New("Selected Runtime session does not have the original verified credential custody")
	}
	clear(custody.Request)
	return profile, identity, custody.Candidate, nil
}

func (m *Manager) continueDesktopRuntimeLogin(ctx context.Context, job Operation, input json.RawMessage) (json.RawMessage, error) {
	pending := pendingHostInput(job)
	if pending == nil {
		return nil, &unconfirmedError{id: job.ID, cause: errors.New("原 Runtime 登录需要其请求的输入")}
	}
	if _, _, err := continuationParams(job, input); err != nil {
		return nil, &unconfirmedError{id: job.ID, cause: err}
	}
	var supplement runtimeLoginSupplement
	if err := json.Unmarshal(input, &supplement); err != nil {
		return nil, &unconfirmedError{id: job.ID, cause: errors.New("原 Runtime 登录补充输入无效")}
	}
	var envelope struct {
		Summary runtimeLoginReceipt `json:"summary"`
	}
	if json.Unmarshal(job.Envelope, &envelope) != nil {
		return nil, &unconfirmedError{id: job.ID, cause: errors.New("原 Runtime 登录输入回执无效")}
	}
	raw, err := m.withPendingRuntimeLogin(ctx, job.Transaction, false, func(image, digest string, admission runtimeAccountAdmission, _ runtimeAccountRecord) (json.RawMessage, error) {
		result, err := m.continueRuntimeLogin(ctx, image, digest, admission, envelope.Summary, supplement)
		if err != nil {
			return nil, err
		}
		return m.finishDesktopRuntimeLogin(ctx, image, digest, &admission, result.Receipt)
	})
	if err != nil {
		err = &unconfirmedError{id: job.ID, cause: err}
	}
	return raw, err
}
