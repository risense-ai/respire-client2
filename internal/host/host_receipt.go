package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

// Persist only the public identity needed by the CLI's original-result commands.
type hostTransaction struct {
	OperationID   string       `json:"operation_id"`
	SourceProfile string       `json:"source_profile"`
	Kind          string       `json:"kind"`
	Register      bool         `json:"register,omitempty"`
	Scope         runtimeScope `json:"scope,omitzero"`
	RequestBound  bool         `json:"request_bound,omitempty"`
}

func (transaction *hostTransaction) acceptsCoreResult(reply *hostTransaction) bool {
	return reply != nil && reply.Kind == "core_config" && reply.OperationID == transaction.OperationID &&
		(transaction.SourceProfile == "" || reply.SourceProfile == transaction.SourceProfile) &&
		(transaction.Scope == (runtimeScope{}) || reply.Scope == transaction.Scope)
}

var hostOperationID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func (job Operation) materialPending() bool {
	return job.Name == "auth_reset" && job.host && !job.MaterialSaved && job.Transaction != nil &&
		job.Transaction.Kind == "key" && (job.State == "done" || job.State == "unconfirmed")
}

// This receipt is separate from transport submission and ordinary operation acknowledgement.
func (m *Manager) AcknowledgeRecoveryMaterial(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[id]
	if job == nil || job.Name != "auth_reset" || !job.host || job.Transaction == nil || job.Transaction.Kind != "key" || job.State != "done" {
		return errors.New("只能确认原超级密码更换事务已签发的恢复材料")
	}
	if job.MaterialSaved {
		return nil
	}
	var envelope struct {
		Summary struct {
			Outcome string `json:"outcome"`
			Super   string `json:"super"`
		} `json:"summary"`
	}
	returned := transactionFromEnvelope(job.Name, job.Envelope)
	if json.Unmarshal(job.Envelope, &envelope) != nil || envelope.Summary.Outcome != "committed" ||
		envelope.Summary.Super == "" || returned == nil || *returned != *job.Transaction || hostEnvelopeError(job.Envelope) != nil {
		return errors.New("请先查询原事务并保存其签发的恢复材料")
	}
	job.MaterialSaved = true
	if err := m.persistOperationsLocked(); err != nil {
		job.MaterialSaved = false
		return err
	}
	return nil
}

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
	case "agent_set":
		if transaction.Kind != "core_config" {
			return nil
		}
		transaction.Register = false
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
	if transaction != nil && transaction.RequestBound && transaction.Kind == "core_config" {
		if transaction.OperationID != strings.TrimPrefix(job.ID, "desktop-") || !hostOperationID.MatchString(transaction.OperationID) {
			return nil, &unconfirmedError{job.ID, errors.New("核心配置预绑定身份不匹配")}
		}
		raw, err := m.hostOperation(ctx, nil, "", "core_config.result", job.ID)
		if err != nil && len(raw) == 0 {
			return nil, &unconfirmedError{job.ID, err}
		}
		if !transaction.acceptsCoreResult(transactionFromEnvelope(job.Name, raw)) {
			return raw, &unconfirmedError{job.ID, errors.New("核心配置查询尚未返回原事务身份")}
		}
		return raw, err
	}
	if transaction == nil || !hostOperationID.MatchString(transaction.OperationID) || !filepath.IsAbs(transaction.SourceProfile) {
		return nil, &unconfirmedError{job.ID, errors.New("CLI 未返回可查询的结构化事务身份，请核对当前账户；不能以桌面请求 ID 代替宿主事务 ID")}
	}
	var args []string
	switch transaction.Kind {
	case "core_config":
		if transaction.Scope.AccountID == "" || transaction.Scope.InstanceID == "" || transaction.Scope.Generation == 0 {
			return nil, &unconfirmedError{job.ID, errors.New("原核心配置事务缺少 Runtime scope，不能重新应用")}
		}
		context, encodeErr := json.Marshal(map[string]any{"scope": transaction.Scope,
			"operation_id": transaction.OperationID, "idempotency_key": transaction.OperationID})
		if encodeErr != nil {
			return nil, encodeErr
		}
		args = []string{"runtime", "core-config-result", "--context=" + string(context), "--source-profile=" + transaction.SourceProfile}
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
