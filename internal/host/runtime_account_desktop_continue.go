//go:build linux || windows || (darwin && cgo)

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func (m *Manager) withPendingRuntimeLogin(ctx context.Context, transaction *hostTransaction,
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
		owner.Phase != "Paused" || owner.ExpectedProfile != record.Source {
		return nil, errors.New("原 Runtime 登录操作需要恢复维护状态")
	}
	profile, err := selectedRuntimeLoginProfile(root)
	if err != nil {
		return nil, err
	}
	if profile != record.Source {
		return nil, errors.New("当前 Runtime 账户已变化，需要恢复原登录操作")
	}
	marker, err = m.claimPausedRuntimeLogin(ctx, record, marker, markerPath)
	if err != nil {
		return nil, err
	}
	image := filepath.Join(m.runtimeDir(info.Version), runtimeBinary())
	return run(image, info.SHA256, runtimeAccountAdmission{Marker: marker, Record: recordBytes, Path: path}, record)
}

// Retain the original token when a new Client2 process queries the same Paused
// login. A reused PID is distinct only when its native birth identity differs.
func (m *Manager) claimPausedRuntimeLogin(ctx context.Context, record runtimeAccountRecord, original []byte, path string) ([]byte, error) {
	var owner runtimeMaintenanceMarker
	if json.Unmarshal(original, &owner) != nil || owner.Schema != 1 || owner.Phase != "Paused" ||
		owner.Token != record.Token || owner.ExpectedProfile != record.Source ||
		owner.OwnerPID <= 0 || uint64(owner.OwnerPID) > uint64(^uint32(0)) || owner.OwnerBirth == "" {
		return nil, errors.New("原 Runtime 登录维护身份无效")
	}
	birth, err := hostControlBirth(ctx)
	if err != nil {
		return nil, err
	}
	if owner.OwnerPID == os.Getpid() && owner.OwnerBirth == birth {
		return original, nil
	}
	previous, err := runtimeControlProcessBirth(ctx, owner.OwnerPID)
	if err != nil {
		return nil, err
	}
	if previous == owner.OwnerBirth {
		return nil, errors.New("原 Runtime 登录仍由存活的客户端控制")
	}
	address, _, err := m.runtimeAccountEndpoint()
	if err != nil {
		return nil, err
	}
	if err := requireClosedRuntimePort(ctx, address); err != nil {
		return nil, err
	}
	if _, err := verifyRuntimeLoginSource(record); err != nil {
		return nil, err
	}
	owner.OwnerPID, owner.OwnerBirth = os.Getpid(), birth
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
	raw, err := m.withPendingRuntimeLogin(ctx, job.Transaction, func(image, digest string, admission runtimeAccountAdmission, record runtimeAccountRecord) (json.RawMessage, error) {
		result, err := m.queryRuntimeLoginResult(ctx, image, digest, admission, record)
		if err != nil {
			return nil, err
		}
		if result.Receipt.Outcome != "awaiting_input" || result.Handoff != nil {
			return nil, errors.New("Runtime 登录需要核对原提交状态")
		}
		public, err := desktopRuntimeLoginEnvelope(result.Receipt)
		return public, errors.Join(err, errors.New("登录需要补充输入"))
	})
	if err != nil {
		err = &unconfirmedError{id: job.ID, cause: err}
	}
	return raw, err
}

func (m *Manager) queryRuntimeLoginResult(ctx context.Context, image, digest string, admission runtimeAccountAdmission,
	record runtimeAccountRecord) (runtimeLoginResult, error) {
	identity, err := verifyRuntimeLoginSource(record)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	control, err := m.prepareRuntimeAccountControl(ctx, image, digest, record.Source, identity.AccountID, admission.Marker)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	params, err := json.Marshal(map[string]any{"schema_version": 1, "binding": control.binding, "identity": identity})
	if err != nil {
		return runtimeLoginResult{}, err
	}
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
	return readRuntimeLoginResult(reply, record)
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
	raw, err := m.withPendingRuntimeLogin(ctx, job.Transaction, func(image, digest string, admission runtimeAccountAdmission, _ runtimeAccountRecord) (json.RawMessage, error) {
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
