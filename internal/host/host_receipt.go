package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
)

// Persist only the public identity needed by the CLI's original-result commands.
type hostTransaction struct {
	OperationID   string `json:"operation_id"`
	SourceProfile string `json:"source_profile"`
	Kind          string `json:"kind"`
	Register      bool   `json:"register,omitempty"`
}

var hostOperationID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func transactionFromEnvelope(name string, raw json.RawMessage) *hostTransaction {
	var envelope struct {
		Summary hostTransaction `json:"summary"`
		Details json.RawMessage `json:"details"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil
	}
	transaction := envelope.Summary
	if transaction.OperationID == "" {
		if json.Unmarshal(envelope.Details, &transaction) != nil {
			return nil
		}
	}
	if !hostOperationID.MatchString(transaction.OperationID) || !filepath.IsAbs(transaction.SourceProfile) {
		return nil
	}
	switch name {
	case "auth_register", "auth_login", "login":
		transaction.Kind, transaction.Register = "account", name == "auth_register"
	case "auth_recover", "auth_reset":
		transaction.Kind, transaction.Register = "key", false
	default:
		return nil
	}
	return &transaction
}

func hostEnvelopeError(raw json.RawMessage) error {
	var envelope struct {
		Status  string `json:"status"`
		Summary struct {
			Outcome string `json:"outcome"`
		} `json:"summary"`
		Details json.RawMessage `json:"details"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	switch envelope.Summary.Outcome {
	case "rolled_back":
		return errors.New("原宿主事务已回滚，未完成请求的操作")
	case "pending", "awaiting_input", "awaiting_verification", "unknown":
		return &unconfirmedError{cause: errors.New("宿主事务尚未完成，请查询原事务；等待输入时可在操作记录中补充，未重新提交认证")}
	}
	var details struct {
		ErrorType string `json:"error_type"`
	}
	if bytes.HasPrefix(bytes.TrimSpace(envelope.Details), []byte("{")) {
		if err := json.Unmarshal(envelope.Details, &details); err != nil {
			return err
		}
	}
	if envelope.Status == "pending" || details.ErrorType == "outcome_unknown" || details.ErrorType == "handoff_pending" {
		return &unconfirmedError{cause: errors.New("宿主事务尚未确认，请查询原事务，未重新提交")}
	}
	return envelopeError(raw)
}

func (m *Manager) recoverHost(ctx context.Context, job Operation) (json.RawMessage, error) {
	transaction := job.Transaction
	if transaction == nil || !hostOperationID.MatchString(transaction.OperationID) || !filepath.IsAbs(transaction.SourceProfile) {
		return nil, &unconfirmedError{job.ID, errors.New("CLI 未返回可查询的结构化事务身份，请核对当前账户；不能以桌面请求 ID 代替宿主事务 ID")}
	}
	var args []string
	switch transaction.Kind {
	case "account":
		args = []string{"runtime", "account-result", "--operation-id=" + transaction.OperationID, "--source-profile=" + transaction.SourceProfile}
		if transaction.Register {
			args = append(args, "--register")
		}
	case "key":
		args = []string{"runtime", "key-result", "--operation-id=" + transaction.OperationID, "--source-profile=" + transaction.SourceProfile}
	default:
		return nil, &unconfirmedError{job.ID, errors.New("CLI 未提供此宿主事务的查询入口")}
	}
	raw, err := m.hostOperation(ctx, args, "", "runtime.result", job.ID)
	if len(raw) == 0 {
		return nil, &unconfirmedError{job.ID, err}
	}
	returned := transactionFromEnvelope(job.Name, raw)
	if returned == nil || *returned != *transaction {
		return nil, &unconfirmedError{job.ID, errors.New("宿主查询回执与原事务身份不匹配")}
	}
	return raw, err
}
