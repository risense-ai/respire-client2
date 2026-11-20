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

func (m *Manager) resumeDesktopRuntimeRecovery(ctx context.Context, job Operation, supplement json.RawMessage) (raw json.RawMessage, err error) {
	defer func() {
		if err != nil {
			err = &unconfirmedError{job.ID, err}
		}
	}()
	transaction := job.Transaction
	if transaction == nil || !transaction.RuntimeKey || !hostOperationID.MatchString(transaction.OperationID) {
		return nil, errors.New("恢复需要原始事务身份")
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
	if json.Unmarshal(recordBytes, &record) != nil || record.Schema != 1 || record.Register || !record.Sent ||
		record.OperationID != transaction.OperationID || !samePath(record.Root, root) || !samePath(record.Source, transaction.SourceProfile) {
		return nil, errors.New("原始恢复记录不匹配")
	}
	active, err := readCredentialConfig(filepath.Join(directory, "account-active.json"), runtimeMaintenanceLimit)
	if errors.Is(err, os.ErrNotExist) && job.Name == "auth_reset" && len(supplement) == 0 {
		receipt, err := readDesktopKeyResetReceipt(ctx, record)
		if err != nil {
			return nil, err
		}
		return desktopRecoveryEnvelope(receipt)
	}
	if err != nil {
		return nil, err
	}
	var route struct {
		Schema      int    `json:"schema"`
		OperationID string `json:"operation_id"`
	}
	if json.Unmarshal(active, &route) != nil || route.Schema != 1 || route.OperationID != record.OperationID {
		return nil, errors.New("原始恢复操作已不再持有当前路由")
	}
	markerPath := filepath.Join(directory, "maintenance.json")
	marker, err := readCredentialConfig(markerPath, runtimeMaintenanceLimit)
	if err != nil {
		return nil, err
	}
	var owner runtimeMaintenanceMarker
	if json.Unmarshal(marker, &owner) != nil || owner.Token != record.Token {
		return nil, errors.New("原始恢复操作已不再持有维护标记")
	}
	account := "login-" + strings.ReplaceAll(record.OperationID, "-", "")
	backend := runtimeLoginBackend()
	private, err := nativeRuntimeCredentialStore(ctx).readSlot(backend, "rsrs", "login-control:"+account, false)
	if err != nil {
		return nil, err
	}
	var custody recoveryCustody
	expectedKind := "fivekeys"
	if job.Name == "auth_reset" {
		expectedKind = "super_reset"
	}
	if private == nil || json.Unmarshal([]byte(*private), &custody) != nil || custody.Operation.Kind != expectedKind ||
		custody.Input.Material.Account != account || custody.Input.Material.Backend != backend ||
		custody.Operation.Server != record.Server || expectedKind == "fivekeys" && custody.Operation.User != record.User ||
		expectedKind == "super_reset" && (custody.Input.Material.Reset == nil ||
			custody.Input.Material.Reset.Account != account || custody.Input.Material.Reset.Backend != backend) {
		return nil, errors.New("原始恢复凭据托管与事务不匹配")
	}
	profile, err := selectedRuntimeLoginProfile(root)
	if err != nil {
		return nil, err
	}
	current, err := readCredentialConfig(filepath.Join(profile, "session.json"), controlFrameLimit)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	defer clear(current)
	identity, err := runtimeAuthIdentityOf(current)
	if err != nil {
		return nil, err
	}
	original := samePath(profile, record.Source) && reflect.DeepEqual(identity, record.Original.Identity)
	if !original {
		var session runtimeSession
		correctOwner := record.User == "" && identity.Kind == "unbound" && identity.OperationID == record.OperationID ||
			record.User != "" && identity.Kind == "bound" && identity.AccountID != nil && *identity.AccountID == controlDigest([]byte(record.User))
		if !correctOwner || json.Unmarshal(current, &session) != nil || session.Alias != account || session.Backend != backend {
			return nil, errors.New("当前会话与原恢复操作不匹配")
		}
	}
	marker, err = m.claimRuntimeLogin(ctx, record, marker, markerPath, profile)
	if err != nil {
		return nil, err
	}
	admission := runtimeAccountAdmission{Marker: marker, Record: recordBytes, Path: path}
	image := filepath.Join(m.runtimeDir(info.Version), runtimeBinary())
	if owner.Phase == "Verified" {
		if original || len(supplement) != 0 {
			return nil, errors.New("已验证的恢复事务只能确认原目标")
		}
		health, _, err := m.Health(ctx)
		if err != nil {
			return nil, err
		}
		expectedAccount := controlDigest([]byte(profile))
		if identity.AccountID != nil {
			expectedAccount = *identity.AccountID
		}
		if !health.StartupComplete || !health.StoreReady || health.AccountID != expectedAccount ||
			!samePath(health.InstallationRoot, root) || !samePath(health.DataDir, profile) || health.ImageSHA256 != info.SHA256 {
			return nil, errors.New("已验证的恢复目标未由原 Runtime 解锁")
		}
		if err := verifyHostProcess(health); err != nil {
			return nil, err
		}
		if expectedKind == "super_reset" {
			receipt, err := readDesktopKeyResetReceipt(ctx, record)
			if err != nil {
				return nil, err
			}
			if err := retireRuntimeLogin(ctx, record, admission.Marker, *private); err != nil {
				return nil, err
			}
			return desktopRecoveryEnvelope(receipt)
		}
		return m.completeDesktopRuntimeRecovery(ctx, image, info.SHA256, record, admission, profile, identity, custody, *private)
	}
	control, err := m.prepareRuntimeAccountControl(ctx, image, info.SHA256, profile, identity.AccountID, marker)
	if err != nil {
		return nil, err
	}
	receipt, err := m.exchangeRuntimeRecovery(ctx, control, record, identity, custody.Operation, "control.key.result", &custody.Input)
	if err != nil {
		return nil, err
	}
	if len(supplement) != 0 {
		if expectedKind != "fivekeys" {
			return nil, errors.New("Super Key 更换只能查询已提交的原事务")
		}
		_, values, err := continuationParams(job, supplement)
		if err != nil {
			return nil, err
		}
		fields, ok := values["input"].(map[string]any)
		code, hasCode := fields["totp"].(string)
		if !ok || len(fields) != 1 || !hasCode || receipt.Outcome != "awaiting_input" || len(receipt.RequiredInputs) != 1 || receipt.RequiredInputs[0] != "totp" || custody.Input.Material.Challenge == nil {
			return nil, errors.New("恢复事务未请求此续接，或原两步验证材料缺失")
		}
		custody.Input.TOTP = &code
		receipt, err = m.exchangeRuntimeRecovery(ctx, control, record, identity, custody.Operation, "control.key.fivekeys", &custody.Input)
		custody.Input.TOTP = nil
		if err != nil {
			return nil, err
		}
	}
	return m.finishDesktopRuntimeRecovery(ctx, image, info.SHA256, record, &admission, custody, *private, receipt)
}
