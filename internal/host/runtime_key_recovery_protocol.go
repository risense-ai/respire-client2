//go:build linux || windows || (darwin && cgo)

package host

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
)

type runtimeRecoveryOperation struct {
	Kind       string `json:"kind"`
	Server     string `json:"addr"`
	User       string `json:"user"`
	Salt       string `json:"kdf_salt"`
	WrappedURK string `json:"wrapped_urk"`
	Nonce      string `json:"urk_nonce"`
	UseSuper   bool   `json:"use_super"`
}

type runtimeRecoveryMaterial struct {
	Account   string  `json:"keyring_account"`
	Backend   string  `json:"keyring_backend"`
	Challenge *string `json:"challenge,omitempty"`
}

type runtimeRecoveryInput struct {
	Password      *string                 `json:"password,omitempty"`
	SuperPassword *string                 `json:"super_password,omitempty"`
	AccountSecret *string                 `json:"account_secret,omitempty"`
	TOTP          *string                 `json:"totp,omitempty"`
	Material      runtimeRecoveryMaterial `json:"material"`
}

type runtimeRecoveryReceipt struct {
	OperationID          string                   `json:"operation_id"`
	Fingerprint          string                   `json:"fingerprint"`
	Operation            runtimeRecoveryOperation `json:"operation"`
	Outcome              string                   `json:"outcome"`
	Source               string                   `json:"source_profile"`
	Target               *string                  `json:"target_profile"`
	AccountID            *string                  `json:"account_id"`
	SessionDigest        *string                  `json:"session_sha256"`
	Identity             runtimeAuthIdentity      `json:"identity"`
	RequiredInputs       []string                 `json:"required_inputs"`
	VerificationRequired bool                     `json:"verification_required"`
	Super                *string                  `json:"super_password"`
	Challenge            *string                  `json:"challenge"`
}

func (m *Manager) exchangeRuntimeRecovery(ctx context.Context, control runtimeAccountControl, record runtimeAccountRecord,
	identity runtimeAuthIdentity, operation runtimeRecoveryOperation, method string, input *runtimeRecoveryInput) (runtimeRecoveryReceipt, error) {
	argument, err := json.Marshal(map[string]any{
		"schema_version": 1, "binding": control.binding, "identity": identity,
		"source_identity": record.Original.Identity, "operation": operation, "input": input,
	})
	if err != nil {
		return runtimeRecoveryReceipt{}, err
	}
	defer clear(argument)
	id, err := controlUUID()
	if err != nil {
		return runtimeRecoveryReceipt{}, err
	}
	response, err := m.exchangeRuntimeControl(ctx, control.image, control.port, control.grant, runtimeControlRequest{
		V: 1, ID: id, Method: method, Args: []string{string(argument)}, OperationID: record.OperationID, IdempotencyKey: record.OperationID,
	})
	if err != nil {
		return runtimeRecoveryReceipt{}, err
	}
	defer clear(response.Envelope)
	return readRuntimeRecoveryReceipt(response, record, operation)
}

func readRuntimeRecoveryReceipt(response rpcResponse, record runtimeAccountRecord, operation runtimeRecoveryOperation) (runtimeRecoveryReceipt, error) {
	var result struct {
		Command string                 `json:"command"`
		Status  string                 `json:"status"`
		Summary runtimeRecoveryReceipt `json:"summary"`
	}
	if json.Unmarshal(response.Envelope, &result) != nil || result.Command != "key-control" || !response.OK {
		return runtimeRecoveryReceipt{}, errors.New("无法确认 Runtime 恢复回执，须查询原请求")
	}
	receipt := result.Summary
	if receipt.OperationID != record.OperationID || !samePath(receipt.Source, record.Source) ||
		receipt.Operation != operation || !validHash(receipt.Fingerprint) || receipt.Super != nil {
		return runtimeRecoveryReceipt{}, errors.New("Runtime 恢复回执与原操作不匹配")
	}
	switch receipt.Outcome {
	case "awaiting_input":
		if result.Status != "pending" || receipt.VerificationRequired || len(receipt.RequiredInputs) == 0 {
			return runtimeRecoveryReceipt{}, errors.New("Runtime 恢复补充输入回执无效")
		}
	case "pending", "committed":
		if receipt.Outcome == "pending" && (!receipt.VerificationRequired || result.Status != "pending") ||
			receipt.Outcome == "committed" && (receipt.VerificationRequired || result.Status != "ok") ||
			receipt.Target == nil || !filepath.IsAbs(*receipt.Target) || receipt.SessionDigest == nil || !validHash(*receipt.SessionDigest) ||
			receipt.Identity.SessionDigest != *receipt.SessionDigest || receipt.Challenge != nil {
			return runtimeRecoveryReceipt{}, errors.New("Runtime 恢复回执没有完整的目标会话身份")
		}
		if operation.User == "" {
			if operation.Server != "" || receipt.AccountID != nil || receipt.Identity.Kind != "unbound" ||
				receipt.Identity.OperationID != record.OperationID {
				return runtimeRecoveryReceipt{}, errors.New("离线恢复回执与原事务身份不匹配")
			}
		} else if receipt.AccountID == nil || *receipt.AccountID != controlDigest([]byte(operation.User)) ||
			receipt.Identity.Kind != "bound" || !reflect.DeepEqual(receipt.Identity.AccountID, receipt.AccountID) {
			return runtimeRecoveryReceipt{}, errors.New("在线恢复回执与原账户身份不匹配")
		}
	default:
		return runtimeRecoveryReceipt{}, errors.New("Runtime 未确认原恢复事务")
	}
	return receipt, nil
}

func verifyRuntimeRecoveryTarget(record runtimeAccountRecord, receipt runtimeRecoveryReceipt) (target, account string, err error) {
	if receipt.Target == nil || receipt.SessionDigest == nil {
		return "", "", errors.New("Runtime 恢复目标缺少会话身份")
	}
	target, err = selectedRuntimeLoginProfile(record.Root)
	if err != nil {
		return "", "", err
	}
	if !samePath(target, *receipt.Target) {
		return "", "", errors.New("Runtime 恢复目标与选定的账户目录不匹配")
	}
	raw, err := readCredentialConfig(filepath.Join(target, "session.json"), controlFrameLimit)
	if err != nil {
		return "", "", err
	}
	defer clear(raw)
	identity, err := runtimeAuthIdentityOf(raw)
	if err != nil {
		return "", "", err
	}
	if !reflect.DeepEqual(identity, receipt.Identity) || identity.SessionDigest != *receipt.SessionDigest {
		return "", "", errors.New("Runtime 恢复目标会话已改变")
	}
	if receipt.AccountID != nil {
		return target, *receipt.AccountID, nil
	}
	return target, controlDigest([]byte(target)), nil
}
