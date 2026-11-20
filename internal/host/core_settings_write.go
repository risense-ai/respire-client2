package host

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

var errCoreConfigRejected = errors.New("Runtime 拒绝核心配置")

type coreConfigCandidate struct {
	ReadHeat     any `json:"read_heat"`
	RecallFusion any `json:"recall_fusion"`
}

type coreConfigChange struct {
	Profile     string              `json:"profile"`
	Scope       runtimeScope        `json:"scope"`
	Before      string              `json:"before"`
	OfflineFile string              `json:"offline_file,omitempty"`
	Revision    uint64              `json:"revision"`
	Key         string              `json:"key"`
	Target      coreConfigCandidate `json:"target"`
}

type coreConfigResult struct {
	State     string              `json:"state"`
	Revision  *uint64             `json:"revision"`
	Candidate coreConfigCandidate `json:"candidate"`
	Effective struct {
		ReadHeat     *bool          `json:"read_heat"`
		RecallFusion map[string]any `json:"recall_fusion"`
	} `json:"effective"`
	Capabilities struct {
		ReadHeat     int `json:"read_heat_contract"`
		RecallFusion int `json:"recall_fusion_contract"`
	} `json:"capabilities"`
	Applies string `json:"applies"`
}

func readCoreConfigFile(profile string) (map[string]json.RawMessage, string, error) {
	path := filepath.Join(profile, "agent.json")
	data, err := readCredentialConfig(path, 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, "", err
	}
	if settings == nil {
		return nil, "", errors.New("agent.json 必须是 JSON 对象")
	}
	return settings, fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func (c coreConfigChange) value() any {
	if c.Key == "read_heat" {
		return c.Target.ReadHeat
	}
	return c.Target.RecallFusion
}

func (m *Manager) coreConfigProfile() (string, error) {
	device, err := readAgentSettingsObject(filepath.Join(m.DataRoot, "client.json"))
	if err != nil {
		return "", err
	}
	path, err := desktopProfilePath(m.DataRoot, device)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(path)
}

func (m *Manager) coreConfigScope(ctx context.Context, profile string) (runtimeScope, error) {
	h, _, err := m.Health(ctx)
	if err != nil {
		return runtimeScope{}, err
	}
	if !samePath(h.InstallationRoot, m.DataRoot) || !samePath(h.DataDir, profile) ||
		!validHash(h.AccountID) || !runtimeInstanceID.MatchString(h.InstanceID) || h.Generation == 0 {
		return runtimeScope{}, errors.New("Runtime 的安装根、账户或数据目录与 Client2 当前配置不匹配")
	}
	return runtimeScope{h.AccountID, h.InstanceID, h.Generation}, nil
}

func (m *Manager) requestCoreConfig(ctx context.Context, id, method string, scope runtimeScope, params map[string]any) (json.RawMessage, error) {
	params["schema_version"] = 1
	payload, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	reply, err := m.rpcResponse(ctx, method, []string{string(payload)}, id, &scope)
	if err != nil {
		return nil, err
	}
	if reply.Scope == nil || *reply.Scope != scope || reply.OperationID != id ||
		reply.Exit < 0 || (reply.Exit == 0) != reply.OK {
		return nil, errors.New("核心配置回执与原请求不匹配")
	}
	err = responseError(reply)
	if !reply.OK && reply.Code == "core_config_rejected" && err != nil {
		return nil, fmt.Errorf("%w: %w", errCoreConfigRejected, err)
	}
	return reply.Envelope, err
}

func configResult(raw json.RawMessage, state string) (coreConfigResult, error) {
	var response struct {
		Summary coreConfigResult `json:"summary"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return response.Summary, err
	}
	result := response.Summary
	if result.State != state || result.Revision == nil || result.Applies != "next_query" ||
		result.Capabilities.ReadHeat != 1 || result.Capabilities.RecallFusion != 1 ||
		result.Effective.ReadHeat == nil || result.Effective.RecallFusion == nil {
		return result, errors.New("核心配置回执缺少协议要求的状态或能力")
	}
	return result, nil
}

func (m *Manager) setDesktopCoreConfig(ctx context.Context, id, setting string) (json.RawMessage, error) {
	key, value, _ := strings.Cut(setting, "=")
	key = strings.TrimSpace(key)
	var parsed any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return nil, fmt.Errorf("核心配置需要有效的 JSON 值: %w", err)
	}
	switch key {
	case "read_heat":
		if _, ok := parsed.(bool); !ok {
			return nil, errors.New("read_heat 必须是布尔值")
		}
	case "recall_fusion":
		if _, ok := parsed.(map[string]any); !ok {
			return nil, errors.New("recall_fusion 必须是 JSON 对象")
		}
	default:
		return nil, errors.New("未知核心配置")
	}
	lock, err := m.lockRuntimeTakeover(ctx)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	profile, err := m.coreConfigProfile()
	if err != nil {
		return nil, err
	}
	settings, digest, err := readCoreConfigFile(profile)
	if err != nil {
		return nil, err
	}
	settings[key] = json.RawMessage(value)
	candidateJSON, err := json.Marshal(map[string]json.RawMessage{"read_heat": settings["read_heat"], "recall_fusion": settings["recall_fusion"]})
	if err != nil {
		return nil, err
	}
	var target coreConfigCandidate
	if err := json.Unmarshal(candidateJSON, &target); err != nil {
		return nil, err
	}
	change := &coreConfigChange{Profile: profile, Before: digest, Key: key, Target: target}
	scope, err := m.coreConfigScope(ctx, profile)
	if IsUnavailable(err) {
		return m.saveOfflineCoreConfig(ctx, id, change, settings)
	}
	if err != nil {
		return nil, err
	}
	change.Scope = scope
	raw, err := m.requestCoreConfig(ctx, id+"-validate", "core_config.validate", scope, map[string]any{"candidate": target})
	if err != nil {
		return nil, err
	}
	validated, err := configResult(raw, "validated")
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(validated.Candidate, target) {
		return nil, errors.New("Runtime 验证结果与原核心配置参数不一致")
	}
	_, currentDigest, err := readCoreConfigFile(profile)
	if err != nil {
		return nil, err
	}
	if currentDigest != digest {
		return nil, errors.New("验证期间核心配置文件已变化，未提交给 Runtime")
	}
	change.Revision = *validated.Revision
	if err := m.recordCoreConfigChange(id, change); err != nil {
		return nil, err
	}
	raw, err = m.requestCoreConfig(ctx, id, "core_config.apply", scope, map[string]any{"candidate": target, "expected_revision": change.Revision})
	if err != nil {
		if errors.Is(err, errCoreConfigRejected) {
			return nil, err
		}
		return nil, &unconfirmedError{id, err}
	}
	return m.finishDesktopCoreConfig(ctx, id, change, raw)
}

func (m *Manager) recordCoreConfigChange(id string, change *coreConfigChange) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[id]
	job.coreChange = change
	err := m.persistOperationsLocked()
	if err != nil {
		job.coreChange = nil
	}
	return err
}

func (m *Manager) recoverDesktopCoreConfig(ctx context.Context, job Operation) (json.RawMessage, error) {
	change := job.coreChange
	if change == nil {
		return nil, errors.New("原核心配置操作在提交给 Runtime 之前中断")
	}
	if change.Key != "read_heat" && change.Key != "recall_fusion" {
		return nil, &unconfirmedError{job.ID, errors.New("原核心配置操作的键无效")}
	}
	lock, err := m.lockRuntimeTakeover(ctx)
	if err != nil {
		return nil, &unconfirmedError{job.ID, err}
	}
	defer lock.Close()
	if change.OfflineFile != "" {
		profile, err := m.coreConfigProfile()
		if err != nil || !samePath(profile, change.Profile) {
			return nil, &unconfirmedError{job.ID, errors.Join(errors.New("无法确认原核心配置账户目录"), err)}
		}
		_, actual, err := readCoreConfigFile(profile)
		if err != nil || actual != change.OfflineFile {
			return nil, &unconfirmedError{job.ID, errors.Join(errors.New("未找到原离线核心配置的写入结果"), err)}
		}
		return offlineCoreConfigResult(change)
	}
	raw, err := m.inspectResult(ctx, job.ID, &change.Scope, errors.New("Runtime 未提供原核心配置回执"))
	if err != nil {
		return nil, err
	}
	return m.finishDesktopCoreConfig(ctx, job.ID, change, raw)
}

// An offline save is a persisted setting for the next Runtime startup.
func (m *Manager) saveOfflineCoreConfig(ctx context.Context, id string, change *coreConfigChange, settings map[string]json.RawMessage) (json.RawMessage, error) {
	if change.Target.ReadHeat != nil {
		if _, ok := change.Target.ReadHeat.(bool); !ok {
			return nil, errors.New("read_heat 必须是布尔值")
		}
	}
	if change.Target.RecallFusion != nil {
		fusion, ok := change.Target.RecallFusion.(map[string]any)
		if !ok {
			return nil, errors.New("recall_fusion 必须是 JSON 对象")
		}
		if _, err := desktopRecallFusion(fusion); err != nil {
			return nil, err
		}
	}
	rootLock, err := lockRuntimeDatabase(ctx, m.DataRoot, 0)
	if err != nil {
		return nil, err
	}
	defer rootLock.Close()
	if !samePath(change.Profile, m.DataRoot) {
		profileLock, err := lockRuntimeDatabase(ctx, change.Profile, 0)
		if err != nil {
			return nil, err
		}
		defer profileLock.Close()
	}
	if _, err := os.Lstat(filepath.Join(m.DataRoot, "runtime", "autostart", "maintenance.json")); err == nil {
		return nil, errors.New("存在 Runtime 维护事务，未写入离线核心配置")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if _, _, err := m.Health(ctx); !IsUnavailable(err) {
		return nil, errors.Join(errors.New("无法确认 Runtime 仍未运行"), err)
	}
	_, actual, err := readCoreConfigFile(change.Profile)
	if err != nil {
		return nil, err
	}
	if actual != change.Before {
		return nil, errors.New("原核心配置文件已变化，未覆盖")
	}
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, err
	}
	change.OfflineFile = fmt.Sprintf("%x", sha256.Sum256(append(encoded, '\n')))
	if err := m.recordCoreConfigChange(id, change); err != nil {
		return nil, err
	}
	if err := writeJSON(filepath.Join(change.Profile, "agent.json"), settings); err != nil {
		return nil, &unconfirmedError{id, err}
	}
	return offlineCoreConfigResult(change)
}

func offlineCoreConfigResult(change *coreConfigChange) (json.RawMessage, error) {
	return json.Marshal(map[string]any{"command": "agent-config", "status": "ok", "summary": map[string]any{
		"action": "updated", "key": change.Key, "value": change.value(),
		"core_application": map[string]string{"state": "pending", "reason": "runtime_not_running"},
	}})
}

func (m *Manager) finishDesktopCoreConfig(ctx context.Context, id string, change *coreConfigChange, raw json.RawMessage) (json.RawMessage, error) {
	result, err := configResult(raw, "applied")
	if err != nil {
		return nil, &unconfirmedError{id, err}
	}
	if *result.Revision == 0 || *result.Revision-1 != change.Revision ||
		!reflect.DeepEqual(result.Candidate, change.Target) {
		return nil, &unconfirmedError{id, errors.New("核心配置应用回执与原参数或修订号不一致")}
	}
	profile, err := m.coreConfigProfile()
	if err != nil {
		return nil, &unconfirmedError{id, err}
	}
	if !samePath(profile, change.Profile) {
		return nil, &unconfirmedError{id, errors.New("当前账户目录已变化，未写入配置")}
	}
	scope, err := m.coreConfigScope(ctx, profile)
	if err != nil {
		return nil, &unconfirmedError{id, err}
	}
	if scope != change.Scope {
		return nil, &unconfirmedError{id, errors.New("Runtime 实例或账户已变化，未写入配置")}
	}
	settings, digest, err := readCoreConfigFile(profile)
	if err != nil {
		return nil, &unconfirmedError{id, err}
	}
	value, err := json.Marshal(change.value())
	if err != nil {
		return nil, &unconfirmedError{id, err}
	}
	var saved any
	if existing := settings[change.Key]; len(existing) != 0 {
		if err := json.Unmarshal(existing, &saved); err != nil {
			return nil, &unconfirmedError{id, err}
		}
	}
	if !reflect.DeepEqual(saved, change.value()) {
		if digest != change.Before {
			return nil, &unconfirmedError{id, errors.New("原核心配置文件已变化，未覆盖")}
		}
		current, err := m.requestCoreConfig(ctx, "core-readback-"+rand.Text(), "core_config.read", scope, map[string]any{})
		if err != nil {
			return nil, &unconfirmedError{id, err}
		}
		state, err := configResult(current, "current")
		if err != nil {
			return nil, &unconfirmedError{id, err}
		}
		if *state.Revision != *result.Revision || !reflect.DeepEqual(state.Candidate, result.Candidate) {
			return nil, &unconfirmedError{id, errors.New("Runtime 已应用其他核心配置，未覆盖配置文件")}
		}
		settings[change.Key] = value
		if err := writeJSON(filepath.Join(profile, "agent.json"), settings); err != nil {
			return nil, &unconfirmedError{id, err}
		}
	}
	application := struct {
		coreConfigResult
		Scope runtimeScope `json:"scope"`
	}{result, scope}
	return json.Marshal(map[string]any{"command": "agent-config", "status": "ok", "summary": map[string]any{
		"action": "updated", "key": change.Key, "value": change.value(), "core_application": application,
	}})
}
