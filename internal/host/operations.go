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
	"strings"
	"time"
)

type Operation struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	State           string            `json:"state"`
	Started         time.Time         `json:"started"`
	Finished        *time.Time        `json:"finished,omitempty"`
	Error           string            `json:"error,omitempty"`
	Envelope        json.RawMessage   `json:"envelope,omitempty"`
	Scope           *runtimeScope     `json:"scope,omitempty"`
	RuntimeID       string            `json:"runtimeOperationId,omitempty"`
	Transaction     *hostTransaction  `json:"transaction,omitempty"`
	Input           *hostInputRequest `json:"input,omitempty"`
	TransportID     string            `json:"transportId,omitempty"`
	MaterialSaved   bool              `json:"materialSaved,omitempty"`
	MaterialPending bool              `json:"materialPending,omitempty"`
	write, host     bool
	inputBlocked    bool
	coreChange      *coreConfigChange
}

type operationRecord struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Started       time.Time         `json:"started"`
	State         string            `json:"state"`
	Host          bool              `json:"host"`
	Scope         *runtimeScope     `json:"scope,omitempty"`
	RuntimeID     string            `json:"runtimeOperationId,omitempty"`
	Transaction   *hostTransaction  `json:"transaction,omitempty"`
	TransportID   string            `json:"transportId,omitempty"`
	MaterialSaved bool              `json:"materialSaved,omitempty"`
	CoreChange    *coreConfigChange `json:"coreConfig,omitempty"`
}

type rpcResponse struct {
	V           int             `json:"v"`
	ID          string          `json:"id"`
	OK          bool            `json:"ok"`
	Exit        int             `json:"exit"`
	Error       string          `json:"error"`
	Code        string          `json:"code"`
	Envelope    json.RawMessage `json:"envelope"`
	Scope       *runtimeScope   `json:"scope,omitempty"`
	OperationID string          `json:"operation_id,omitempty"`
	DataDir     string          `json:"data_dir,omitempty"`
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
		m.jobs[r.ID] = &Operation{ID: r.ID, Name: r.Name, Started: r.Started, State: state, host: r.Host, write: true, Scope: r.Scope, RuntimeID: r.RuntimeID, Transaction: r.Transaction, TransportID: r.TransportID, MaterialSaved: r.MaterialSaved, coreChange: r.CoreChange}
	}
	return nil
}

func (m *Manager) persistOperationsLocked() error {
	var records []operationRecord
	for _, j := range m.jobs {
		if j.write {
			records = append(records, operationRecord{ID: j.ID, Name: j.Name, Started: j.Started, State: j.State, Host: j.host, Scope: j.Scope, RuntimeID: j.RuntimeID, Transaction: j.Transaction, TransportID: j.TransportID, MaterialSaved: j.MaterialSaved, CoreChange: j.coreChange})
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
		copy.Input = pendingHostInput(copy)
		copy.MaterialPending = copy.materialPending()
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
	copy := *job
	copy.Input = pendingHostInput(copy)
	copy.MaterialPending = copy.materialPending()
	return copy, nil
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
	scope, err := m.requestScope(ctx)
	if err != nil {
		return nil, err
	}
	id := "status-" + fmt.Sprint(time.Now().UnixNano())
	reply, err := m.rpcResponse(ctx, "cli.exec", []string{"--json", "status"}, id, scope)
	if err != nil {
		return m.inspectResult(ctx, id, scope, err)
	}
	if reply.Scope == nil || *reply.Scope != *scope || reply.OperationID != id {
		return nil, &unconfirmedError{id, errors.New("Runtime 业务回复作用域或操作 ID 与原请求不匹配，请查询原请求")}
	}
	var status struct {
		Command string `json:"command"`
		Status  string `json:"status"`
		Summary struct {
			CredentialState string `json:"credential_state"`
			StoreReady      *bool  `json:"store_ready"`
		} `json:"summary"`
		Errors []string `json:"errors"`
	}
	// A locked store is a valid read-only status, but other RPC warnings still fail.
	if !reply.OK && reply.Exit == 2 && reply.Code == "" && reply.Error == "" &&
		json.Unmarshal(reply.Envelope, &status) == nil && status.Command == "status" &&
		status.Status == "warn" && status.Summary.CredentialState == "required" &&
		status.Summary.StoreReady != nil && !*status.Summary.StoreReady && len(status.Errors) == 0 {
		return reply.Envelope, nil
	}
	return reply.Envelope, responseError(reply)
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
	if secret != "" && name != "classification_save" && !strings.HasPrefix(spec.method, "auth.") {
		return Operation{}, errors.New("此操作不接受密钥")
	}
	argv, err := spec.build(raw)
	if err != nil {
		return Operation{}, err
	}
	if name == "agent_set" {
		var input struct {
			Setting string `json:"setting"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return Operation{}, err
		}
		key, _, _ := strings.Cut(input.Setting, "=")
		if key = strings.TrimSpace(key); key == "read_heat" || key == "recall_fusion" {
			if !hostOperationID.MatchString(strings.TrimPrefix(id, "desktop-")) {
				return Operation{}, errors.New("核心配置需要有效 UUID")
			}
			spec.method, spec.host = "desktop.core-config", false
		} else if isDesktopAgentTextSetting(key) {
			spec.method, spec.host = "desktop.agent-text-setting", false
		} else if isWorkspaceModeSetting(key) {
			spec.method = "desktop.workspace-setting"
		} else {
			return Operation{}, errors.New("未知的桌面代理设置")
		}
	}
	if name == "model_progress" {
		argv = append(argv, "false")
	}
	if name == "model_cancel" {
		argv = append(argv, "true")
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
		} else if j.State != "unconfirmed" && !j.materialPending() && (oldest == nil || j.Started.Before(oldest.Started)) {
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Account operations wait for human authorization without a wall-clock deadline.
	if !strings.HasPrefix(spec.method, "auth.") {
		timedCtx, timedCancel := context.WithTimeout(ctx, 30*time.Minute)
		defer timedCancel()
		ctx = timedCtx
	}
	var envelope json.RawMessage
	var err error
	switch {
	case spec.method == "model.apply-engine":
		envelope, err = applyEngine(ctx, m, args, id)
	case spec.method == "desktop.device-config":
		envelope, err = m.desktopDeviceConfig(ctx, deviceConfigIntent{})
	case spec.method == "desktop.server-set":
		envelope, err = m.desktopDeviceConfig(ctx, deviceConfigIntent{Addr: &args[0]})
	case spec.method == "desktop.autosync-set":
		enabled := args[0] == "true"
		envelope, err = m.desktopDeviceConfig(ctx, deviceConfigIntent{Autosync: &enabled})
	case spec.method == "desktop.cure-auto-set":
		enabled := args[0] == "true"
		envelope, err = m.desktopDeviceConfig(ctx, deviceConfigIntent{CureAuto: &enabled})
	case spec.method == "desktop.data-dir-set":
		envelope, err = m.desktopDeviceConfig(ctx, deviceConfigIntent{DataDir: &args[0]})
	case spec.method == "desktop.agent-config":
		envelope, err = m.desktopAgentSettings()
	case spec.method == "desktop.classification", spec.method == "desktop.classification-save":
		envelope, err = m.desktopClassificationConfig(ctx, args, secret, spec.method == "desktop.classification-save")
	case spec.method == "desktop.core-config":
		envelope, err = m.setDesktopCoreConfig(ctx, id, strings.TrimPrefix(args[1], "--set="))
	case spec.method == "desktop.keys-export":
		envelope, err = m.desktopKeysExport(ctx, args)
	case spec.method == "desktop.account-list", spec.method == "desktop.space-list":
		envelope, err = m.desktopProfileList(spec.method == "desktop.space-list")
	case spec.method == "desktop.space-members":
		envelope, err = m.desktopSpaceMembers(args)
	case spec.method == "desktop.migration-list":
		envelope, err = m.desktopLegacyProfiles()
	case spec.method == "desktop.migration-apply":
		envelope, err = m.desktopLegacyMigration(ctx, id, args, false)
	case spec.method == "desktop.account-use", spec.method == "desktop.space-use":
		envelope, err = m.desktopProfileSwitch(ctx, args[0], spec.method == "desktop.space-use")
	case spec.method == "desktop.space-create":
		envelope, err = m.desktopSpaceCreate(ctx, args[0])
	case spec.method == "auth.login", spec.method == "auth.login.browser", spec.method == "auth.register":
		envelope, err = m.desktopRuntimeAccountLogin(ctx, args, secret, id, spec.method)
	case spec.method == "auth.recover", spec.method == "auth.reset":
		envelope, err = m.desktopRuntimeKeyRecovery(ctx, args, secret, id, spec.method)
	case spec.method == "desktop.agent-text-setting":
		envelope, err = m.setDesktopAgentTextSetting(ctx, id, strings.TrimPrefix(args[1], "--set="))
	case spec.method == "desktop.workspace-setting":
		envelope, err = m.setDesktopWorkspaceMode(ctx, strings.TrimPrefix(args[1], "--set="))
	case spec.method == "desktop.model-uninstall":
		envelope, err = m.uninstallM3(ctx)
	case spec.method == "desktop.space-kick":
		envelope, err = m.desktopSpaceKick(ctx, id, args, "")
	case spec.host:
		envelope, err = m.hostOperation(ctx, args, secret, spec.method, id)
	default:
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
	if spec.host {
		returned := transactionFromEnvelope(job.Name, envelope)
		if job.Transaction != nil && job.Transaction.RequestBound {
			if returned == nil && (len(envelope) > 0 || err == nil) {
				var rejection struct {
					Status  string `json:"status"`
					Summary struct {
						Reason string `json:"reason"`
					} `json:"summary"`
				}
				if json.Unmarshal(envelope, &rejection) != nil || rejection.Status != "fail" || rejection.Summary.Reason != "invalid_input" {
					err = &unconfirmedError{id, errors.Join(err, errors.New("核心配置回执缺少原事务身份"))}
				}
			} else if returned != nil {
				if !job.Transaction.acceptsCoreResult(returned) {
					err = &unconfirmedError{id, errors.New("核心配置回执与预绑定请求不匹配")}
				} else {
					returned.RequestBound = true
					job.Transaction = returned
				}
			}
		} else if job.Transaction != nil && (job.Transaction.RuntimeLogin || job.Transaction.RuntimeKey) {
			if returned != nil && *returned != *job.Transaction {
				err = &unconfirmedError{id, errors.New("登录回执与预绑定的原事务身份不匹配")}
			}
		} else {
			job.Transaction = returned
		}
	}
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
	if result.Status == "pending" {
		return &unconfirmedError{cause: errors.New("操作仍在执行，请查询原请求，未自动重放")}
	}
	if result.Status == "fail" {
		if len(result.Errors) > 0 {
			return errors.New(result.Errors[0])
		}
		return errors.New("Runtime 报告操作失败")
	}
	return nil
}

func (m *Manager) rpcResponse(ctx context.Context, method string, args []string, id string, scope *runtimeScope) (rpcResponse, error) {
	raw, err := m.localRequest(ctx, "POST", "/api/rpc", map[string]any{"v": 1, "id": id, "method": method, "args": args, "scope": scope})
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
	scope, err := m.requestScope(ctx)
	if err != nil {
		return nil, err
	}
	return m.executeRPCScoped(ctx, method, args, id, scope)
}

func (m *Manager) executeRPCScoped(ctx context.Context, method string, args []string, id string, scope *runtimeScope) (json.RawMessage, error) {
	parent, _, _ := strings.Cut(id, "/")
	m.mu.Lock()
	if job := m.jobs[parent]; job != nil {
		job.Scope, job.RuntimeID = scope, id
		if job.write {
			if err := m.persistOperationsLocked(); err != nil {
				m.mu.Unlock()
				return nil, err
			}
		}
	}
	m.mu.Unlock()
	reply, err := m.rpcResponse(ctx, method, args, id, scope)
	if err != nil {
		return m.inspectResult(ctx, id, scope, err)
	}
	switch method {
	case "runtime.status", "assets.status", "model.catalog", "model.progress", "cli.progress", "runtime.result":
	default:
		if reply.Scope == nil || *reply.Scope != *scope || reply.OperationID != id {
			return nil, &unconfirmedError{id, errors.New("Runtime 业务回复作用域或操作 ID 与原请求不匹配，请查询原请求")}
		}
	}
	return reply.Envelope, responseError(reply)
}

func responseError(reply rpcResponse) error {
	if !reply.OK && reply.Error != "" {
		return errors.New(reply.Error)
	}
	if err := envelopeError(reply.Envelope); err != nil {
		return err
	}
	if !reply.OK {
		return errors.New("Runtime 拒绝请求")
	}
	return nil
}

func (m *Manager) inspectResult(ctx context.Context, id string, scope *runtimeScope, original error) (json.RawMessage, error) {
	if scope == nil || scope.AccountID == "" || scope.InstanceID == "" || scope.Generation == 0 {
		return nil, &unconfirmedError{id, errors.New("原请求未保留完整作用域，不能使用当前身份替代查询")}
	}
	for {
		reply, err := m.rpcResponse(ctx, "runtime.result", []string{id}, "inspect-"+id, scope)
		if err != nil {
			return nil, &unconfirmedError{id, errors.Join(original, err)}
		}
		if err = responseError(reply); err != nil {
			return reply.Envelope, &unconfirmedError{id, err}
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
		case "completed", "failed":
			result := envelope.Summary.Response
			if result == nil || result.V != 1 || result.ID != id || result.Scope == nil || *result.Scope != *scope || result.OperationID != id || (envelope.Summary.State == "completed") != result.OK {
				return nil, &unconfirmedError{id, errors.New("Runtime 缓存回执无效")}
			}
			return result.Envelope, responseError(*result)
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
	m.mu.Lock()
	original := m.jobs[id]
	if original == nil {
		m.mu.Unlock()
		return Operation{}, errors.New("操作记录不存在")
	}
	job := *original
	if job.State != "unconfirmed" && !job.materialPending() {
		m.mu.Unlock()
		return job, nil
	}
	original.State = "running"
	original.inputBlocked = true
	m.mu.Unlock()
	var err error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Host result queries can drain and restore Runtime and verify native custody.
	// Like account submissions, let the CLI own their lifecycle deadlines.
	if !job.host {
		timedCtx, timedCancel := context.WithTimeout(ctx, 30*time.Second)
		defer timedCancel()
		ctx = timedCtx
	}
	var envelope json.RawMessage
	switch {
	case job.Name == "migration_apply":
		envelope, err = m.desktopLegacyMigration(ctx, id, nil, true)
	case job.Name == "server_set", job.Name == "autosync_set", job.Name == "cure_auto_set", job.Name == "data_dir_set",
		job.Name == "account_use", job.Name == "space_use":
		err = &unconfirmedError{id, errors.New("请核对本机设备配置、Runtime 状态和维护记录")}
	case job.Name == "space_create":
		err = &unconfirmedError{id, errors.New("请核对 client.json、目标目录的 space_owner.json 和 Runtime 维护记录，再处理原创建操作")}
	case job.Name == "agent_set" && job.host && job.Transaction == nil:
		err = &unconfirmedError{id, errors.New("请核对工作区设置和 Runtime 维护记录")}
	case job.Name == "agent_set" && !job.host:
		envelope, err = m.recoverDesktopCoreConfig(ctx, job)
	case job.Name == "model_uninstall":
		err = &unconfirmedError{id, errors.New("请核对 BGE-M3 模型目录及 .bge-m3-uninstall 暂存目录")}
	case job.Name == "space_kick", job.Name == "space_kick_all":
		envelope, err = m.desktopSpaceKick(ctx, id, nil, job.Name)
	case job.host:
		envelope, err = m.recoverHost(ctx, job)
	default:
		runtimeID := job.RuntimeID
		if runtimeID == "" {
			runtimeID = id
		}
		envelope, err = m.inspectResult(ctx, runtimeID, job.Scope, errors.New("客户端上次退出时操作尚未结束"))
		if err == nil && job.Name == "model_engine_apply" {
			err = &unconfirmedError{id, errors.New("原模型步骤已完成；整项应用还需核对模型当前状态，未自动继续或恢复其他步骤")}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[id]
	if len(envelope) > 0 {
		if j.Transaction != nil && j.Transaction.RequestBound {
			returned := transactionFromEnvelope(j.Name, envelope)
			if j.Transaction.acceptsCoreResult(returned) {
				returned.RequestBound = true
				j.Transaction = returned
			}
		}
		j.Envelope = envelope
		j.inputBlocked = false
	}
	if err == nil {
		j.State = "done"
		j.Error = ""
	} else {
		j.Error = err.Error()
		var unknown *unconfirmedError
		if errors.As(err, &unknown) {
			unknown.id = id
			j.State, j.Error = "unconfirmed", err.Error()
		} else {
			j.State = "failed"
		}
	}
	if err = m.persistOperationsLocked(); err != nil {
		return *j, err
	}
	copy := *j
	copy.Input = pendingHostInput(copy)
	copy.MaterialPending = copy.materialPending()
	return copy, nil
}
