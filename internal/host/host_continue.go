package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type hostInputRequest struct {
	Method         string   `json:"method"`
	RequiredInputs []string `json:"required_inputs"`
	User           string   `json:"user,omitempty"`
	Addr           string   `json:"addr,omitempty"`
}

func pendingHostInput(job Operation) *hostInputRequest {
	if job.State != "unconfirmed" || job.inputBlocked || job.Transaction == nil || job.Transaction.Register ||
		(!job.Transaction.RuntimeLogin && !job.Transaction.RuntimeKey) {
		return nil
	}
	var envelope struct {
		Summary struct {
			Outcome        string   `json:"outcome"`
			RequiredInputs []string `json:"required_inputs"`
			Login          *struct {
				RequiredInputs []string `json:"required_inputs"`
				User           string   `json:"user"`
				Addr           string   `json:"addr"`
			} `json:"login"`
		} `json:"summary"`
	}
	if json.Unmarshal(job.Envelope, &envelope) != nil || envelope.Summary.Outcome != "awaiting_input" {
		return nil
	}
	if job.Transaction.Kind == "account" && envelope.Summary.Login != nil {
		login := envelope.Summary.Login
		return &hostInputRequest{"auth.login.continue", login.RequiredInputs, login.User, login.Addr}
	}
	if job.Transaction.Kind == "key" {
		return &hostInputRequest{Method: "auth.key.continue", RequiredInputs: envelope.Summary.RequiredInputs}
	}
	return nil
}

// Continue keeps the original desktop job and uses a separate one-shot transport ID.
func (m *Manager) Continue(id, transportID string, input json.RawMessage) (Operation, error) {
	if !operationID.MatchString(transportID) || transportID == id {
		return Operation{}, errors.New("续接需要独立的传输请求 ID")
	}
	if len(input) > 49152 || !json.Valid(input) || !bytes.HasPrefix(bytes.TrimSpace(input), []byte("{")) {
		return Operation{}, errors.New("补充输入必须是有效对象，且不超过 48 KiB")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[id]
	if job == nil || !job.host || job.State != "unconfirmed" || job.Transaction == nil {
		return Operation{}, errors.New("只能续接保留原宿主身份的未确认操作")
	}
	if job.TransportID == transportID {
		return Operation{}, errors.New("此传输请求已经提交，请查询原操作")
	}
	if !job.Transaction.RuntimeLogin && !job.Transaction.RuntimeKey {
		return Operation{}, errors.New("请通过原宿主查询或续接这笔历史事务，桌面未重新提交")
	}
	if pendingHostInput(*job) == nil {
		return Operation{}, errors.New("请先查询原操作，只有等待补充输入的事务可以续接")
	}
	previous := *job
	job.State, job.TransportID, job.Error, job.Finished = "running", transportID, "", nil
	job.inputBlocked = true
	if err := m.persistOperationsLocked(); err != nil {
		*job = previous
		return Operation{}, err
	}
	go m.runHostContinuation(previous, input)
	return *job, nil
}

func continuationParams(job Operation, input json.RawMessage) (string, map[string]any, error) {
	request := pendingHostInput(job)
	if request == nil || len(request.RequiredInputs) == 0 {
		return "", nil, errors.New("原事务没有可续接的输入请求")
	}
	var supplied map[string]json.RawMessage
	if err := json.Unmarshal(input, &supplied); err != nil {
		return "", nil, err
	}
	requested := map[string]bool{}
	for _, field := range request.RequiredInputs {
		if requested[field] {
			return "", nil, errors.New("原事务返回重复的补充输入字段")
		}
		requested[field] = true
	}
	params := map[string]any{"operation_id": job.Transaction.OperationID, "source_profile": job.Transaction.SourceProfile}
	values := map[string]any{}
	for field, raw := range supplied {
		if !requested[field] {
			return "", nil, fmt.Errorf("原事务没有请求输入 %s", field)
		}
		var value any
		if field == "merge_local" && request.Method == "auth.login.continue" {
			var merge bool
			if err := json.Unmarshal(raw, &merge); err != nil || string(raw) == "null" {
				return "", nil, errors.New("本地合并确认必须为布尔值")
			}
			value = merge
		} else {
			var text string
			if json.Unmarshal(raw, &text) != nil || text == "" || len(text) > 16384 || strings.ContainsRune(text, 0) {
				return "", nil, errors.New("补充输入格式无效")
			}
			if field == "totp" && (len(text) != 6 || strings.IndexFunc(text, func(c rune) bool { return c < '0' || c > '9' }) >= 0) {
				return "", nil, errors.New("两步验证代码必须是 6 位数字")
			}
			value = text
		}
		if request.Method == "auth.login.continue" {
			switch field {
			case "super_password", "totp":
				params[field] = value
			case "legacy_password", "account_secret", "source_secret", "secret_key", "new_super", "merge_local":
				values[field] = value
			default:
				return "", nil, errors.New("原事务请求了桌面不能构造的登录输入")
			}
		} else {
			switch field {
			case "password", "super_password", "account_secret", "totp":
				values[field] = value
			default:
				return "", nil, errors.New("原事务请求了桌面不能构造的密钥输入")
			}
		}
	}
	for field := range requested {
		if field == "host_credentials" && request.Method == "auth.login.continue" {
			continue // The host reads its stored credentials.
		}
		if _, ok := supplied[field]; !ok {
			return "", nil, fmt.Errorf("请提供原事务请求的 %s", field)
		}
	}
	if request.Method == "auth.login.continue" {
		params["continuation"] = values
	} else {
		params["input"] = values
	}
	return request.Method, params, nil
}

func (m *Manager) runHostContinuation(job Operation, input json.RawMessage) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw, err := m.recoverHost(ctx, job)
	confirmedInput := false
	var unknown *unconfirmedError
	if len(raw) > 0 && (err == nil || errors.As(err, &unknown)) {
		current := job
		current.Envelope = raw
		if pendingHostInput(current) != nil {
			var reply json.RawMessage
			var continueErr error
			if job.Transaction.RuntimeKey {
				reply, continueErr = m.resumeDesktopRuntimeRecovery(ctx, current, input)
			} else {
				reply, continueErr = m.continueDesktopRuntimeLogin(ctx, current, input)
			}
			err = continueErr
			if len(reply) != 0 {
				returned := transactionFromEnvelope(job.Name, reply)
				if returned == nil || *returned != *job.Transaction {
					err = &unconfirmedError{job.ID, errors.New("Runtime 续接回执与原登录事务不匹配")}
				} else {
					raw = reply
					confirmedInput = true
				}
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[job.ID]
	j.inputBlocked = !confirmedInput
	if len(raw) > 0 {
		j.Envelope = raw
	}
	finished := time.Now().UTC()
	j.Finished, j.State, j.Error = &finished, "done", ""
	if err != nil {
		j.State, j.Error = "failed", err.Error()
		if errors.As(err, &unknown) {
			unknown.id = job.ID
			j.State, j.Error = "unconfirmed", err.Error()
		}
	}
	if saveErr := m.persistOperationsLocked(); saveErr != nil {
		j.Error = errors.Join(err, saveErr).Error()
	}
}
