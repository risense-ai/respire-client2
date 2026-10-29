package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

type Operation struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	State       string          `json:"state"`
	Started     time.Time       `json:"started"`
	Finished    *time.Time      `json:"finished,omitempty"`
	Error       string          `json:"error,omitempty"`
	Envelope    json.RawMessage `json:"envelope,omitempty"`
	write, host bool
}

type operationRecord struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Started time.Time `json:"started"`
	State   string    `json:"state"`
	Host    bool      `json:"host"`
}

type rpcResponse struct {
	V        int             `json:"v"`
	ID       string          `json:"id"`
	OK       bool            `json:"ok"`
	Error    string          `json:"error"`
	Code     string          `json:"code"`
	Envelope json.RawMessage `json:"envelope"`
}

var operationID = regexp.MustCompile(`^desktop-[0-9a-f-]{36}$`)

func (m *Manager) loadOperations() error {
	m.jobs = map[string]*Operation{}
	b, err := os.ReadFile(filepath.Join(m.Root, "requests.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var records []operationRecord
	if err = json.Unmarshal(b, &records); err != nil {
		return fmt.Errorf("读取操作记录: %w", err)
	}
	for _, r := range records {
		if !operationID.MatchString(r.ID) {
			return errors.New("无效的操作记录 ID")
		}
		state := r.State
		if state == "running" {
			state = "unconfirmed"
		}
		m.jobs[r.ID] = &Operation{ID: r.ID, Name: r.Name, Started: r.Started, State: state, host: r.Host, write: true}
	}
	return nil
}

func (m *Manager) persistOperationsLocked() error {
	var records []operationRecord
	for _, j := range m.jobs {
		if j.write {
			records = append(records, operationRecord{j.ID, j.Name, j.Started, j.State, j.host})
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Started.After(records[j].Started) })
	return writeJSON(filepath.Join(m.Root, "requests.json"), records)
}

func (m *Manager) Operations() []Operation {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]Operation, 0, len(m.jobs))
	for _, j := range m.jobs {
		copy := *j
		copy.Envelope = nil
		list = append(list, copy)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Started.After(list[j].Started) })
	return list
}

func (m *Manager) Operation(id string) (Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return Operation{}, errors.New("操作记录不存在")
	}
	return *job, nil
}

func (m *Manager) HasActiveOperations() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.State == "running" {
			return true
		}
	}
	return false
}

func (m *Manager) Acknowledge(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return nil
	}
	if job.State == "running" || job.State == "unconfirmed" {
		return errors.New("操作尚未确认，保留其请求记录")
	}
	if !job.write {
		delete(m.jobs, id)
	}
	return nil
}

func (m *Manager) RuntimeStatus(ctx context.Context) (json.RawMessage, error) {
	return m.executeRPC(ctx, "cli.exec", []string{"--json", "status"}, "status-"+fmt.Sprint(time.Now().UnixNano()))
}

func (m *Manager) Submit(name, id string, raw json.RawMessage, confirmed bool, secret string) (Operation, error) {
	if !operationID.MatchString(id) {
		return Operation{}, errors.New("无效的请求 ID")
	}
	spec, ok := operationSpec(name)
	if !ok {
		return Operation{}, errors.New("未知的客户端操作")
	}
	if spec.write && !confirmed {
		return Operation{}, errors.New("写操作需要明确提交")
	}
	if secret != "" && name != "classification_save" {
		return Operation{}, errors.New("此操作不接受密钥")
	}
	argv, err := spec.build(raw)
	if err != nil {
		return Operation{}, err
	}
	if name == "model_progress" {
		argv = append(argv, "false")
	}
	if name == "model_cancel" {
		argv = append(argv, "true")
	}
	if name == "inject_apply" {
		var input map[string]any
		if err = json.Unmarshal(raw, &input); err != nil {
			return Operation{}, err
		}
		if value, _ := input["revision"].(string); input["id"] == "codex" && value == "" {
			return Operation{}, errors.New("请先预览并取得有效修订版本")
		}
	}
	m.mu.Lock()
	if previous, exists := m.jobs[id]; exists {
		copy := *previous
		m.mu.Unlock()
		if previous.Name != name {
			return Operation{}, errors.New("请求 ID 已绑定其他操作")
		}
		return copy, nil
	}
	active := 0
	var oldest *Operation
	for key, j := range m.jobs {
		if j.State == "running" {
			active++
		} else if j.State != "unconfirmed" && (oldest == nil || j.Started.Before(oldest.Started)) {
			oldest = m.jobs[key]
		}
	}
	if active >= 8 {
		m.mu.Unlock()
		return Operation{}, errors.New("正在执行的操作过多，请稍后提交")
	}
	if len(m.jobs) >= 128 {
		if oldest == nil {
			m.mu.Unlock()
			return Operation{}, errors.New("操作记录已满，请先确认未完成的操作")
		}
		delete(m.jobs, oldest.ID)
	}
	job := &Operation{ID: id, Name: name, State: "running", Started: time.Now().UTC(), host: spec.host, write: spec.write}
	m.jobs[id] = job
	if job.write {
		if err = m.persistOperationsLocked(); err != nil {
			delete(m.jobs, id)
			m.mu.Unlock()
			return Operation{}, err
		}
	}
	copy := *job
	m.mu.Unlock()
	go m.runOperation(spec, argv, secret, id)
	return copy, nil
}

func (m *Manager) runOperation(spec commandSpec, args []string, secret, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	var envelope json.RawMessage
	var err error
	if spec.host {
		envelope, err = m.hostOperation(ctx, args, secret)
	} else {
		method := spec.method
		if method == "" {
			method = "cli.exec"
			args = append([]string{"--json"}, args...)
		}
		envelope, err = m.executeRPC(ctx, method, args, id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[id]
	finished := time.Now().UTC()
	job.Finished = &finished
	job.Envelope = envelope
	job.State = "done"
	if err != nil {
		job.State = "failed"
		var unknown *unconfirmedError
		if errors.As(err, &unknown) {
			unknown.id = id
			job.State = "unconfirmed"
		}
		job.Error = err.Error()
	}
	if job.write {
		if saveErr := m.persistOperationsLocked(); saveErr != nil {
			job.Error = errors.Join(err, fmt.Errorf("保存请求记录失败: %w", saveErr)).Error()
		}
	}
}

type unconfirmedError struct {
	id    string
	cause error
}

func (e *unconfirmedError) Error() string {
	return fmt.Sprintf("请求 %s 的结果尚未确认，未自动重试；请检查操作记录和当前数据: %v", e.id, e.cause)
}
func (e *unconfirmedError) Unwrap() error { return e.cause }

func envelopeError(raw json.RawMessage) error {
	var result struct {
		Status string   `json:"status"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if result.Status == "" {
		return errors.New("Runtime 未返回有效状态")
	}
	if result.Status == "fail" {
		if len(result.Errors) > 0 {
			return errors.New(result.Errors[0])
		}
		return errors.New("Runtime 报告操作失败")
	}
	return nil
}

func (m *Manager) rpcResponse(ctx context.Context, method string, args []string, id string) (rpcResponse, error) {
	raw, err := m.localRequest(ctx, "POST", "/api/rpc", map[string]any{"v": 1, "id": id, "method": method, "args": args})
	var reply rpcResponse
	if err != nil {
		return reply, err
	}
	if err = json.Unmarshal(raw, &reply); err != nil {
		return reply, err
	}
	if reply.V != 1 || reply.ID != id {
		return reply, errors.New("Runtime 回执 ID 或协议版本不匹配")
	}
	return reply, nil
}

func (m *Manager) executeRPC(ctx context.Context, method string, args []string, id string) (json.RawMessage, error) {
	if _, _, err := m.Health(ctx); err != nil {
		return nil, err
	}
	reply, err := m.rpcResponse(ctx, method, args, id)
	if err != nil {
		return m.inspectResult(ctx, id, err)
	}
	if !reply.OK {
		if reply.Error != "" {
			return reply.Envelope, errors.New(reply.Error)
		}
	}
	return reply.Envelope, envelopeError(reply.Envelope)
}

func (m *Manager) inspectResult(ctx context.Context, id string, original error) (json.RawMessage, error) {
	for {
		reply, err := m.rpcResponse(ctx, "runtime.result", []string{id}, "inspect-"+id)
		if err != nil {
			return nil, &unconfirmedError{id, errors.Join(original, err)}
		}
		var envelope struct {
			Summary struct {
				State    string       `json:"state"`
				Response *rpcResponse `json:"response"`
			} `json:"summary"`
		}
		if err = json.Unmarshal(reply.Envelope, &envelope); err != nil {
			return nil, &unconfirmedError{id, err}
		}
		switch envelope.Summary.State {
		case "completed":
			result := envelope.Summary.Response
			if result == nil || result.ID != id {
				return nil, &unconfirmedError{id, errors.New("Runtime 缓存回执无效")}
			}
			if !result.OK {
				if result.Error != "" {
					return result.Envelope, errors.New(result.Error)
				}
			}
			return result.Envelope, envelopeError(result.Envelope)
		case "pending":
			select {
			case <-ctx.Done():
				return nil, &unconfirmedError{id, ctx.Err()}
			case <-time.After(time.Second):
			}
		default:
			return nil, &unconfirmedError{id, original}
		}
	}
}

// Recover inspects a saved request; it never submits its command again.
func (m *Manager) Recover(id string) (Operation, error) {
	job, err := m.Operation(id)
	if err != nil {
		return job, err
	}
	if job.State != "unconfirmed" {
		return job, nil
	}
	if job.host {
		return job, errors.New("宿主操作不能通过 Runtime 查询，请刷新对应页面确认当前状态")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	envelope, err := m.inspectResult(ctx, id, errors.New("客户端上次退出时操作尚未结束"))
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[id]
	j.Envelope = envelope
	if err == nil {
		j.State = "done"
		j.Error = ""
	} else {
		j.Error = err.Error()
		var unknown *unconfirmedError
		if !errors.As(err, &unknown) {
			j.State = "failed"
		}
	}
	if err = m.persistOperationsLocked(); err != nil {
		return *j, err
	}
	return *j, nil
}
