//go:build linux || windows || (darwin && cgo)

package host

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
)

func (m *Manager) desktopRuntimeAccountLogin(ctx context.Context, args []string, secret, id string, browser bool) (envelope json.RawMessage, err error) {
	input, err := authParams(args, secret)
	if err != nil {
		return nil, err
	}
	operation := strings.TrimPrefix(id, "desktop-")
	user, password := input["user"], input["password"]
	if !hostOperationID.MatchString(operation) || m.ClientVersion == "" {
		return nil, errors.New("登录请求缺少有效的操作身份")
	}
	if (browser && secret != "") || (!browser && (user == "" || password == "")) {
		return nil, errors.New("登录输入与授权方式不匹配")
	}
	server := input["addr"]
	if server == "" {
		server, err = m.DeviceServerAddress()
		if err != nil {
			return nil, err
		}
	}
	address, err := url.Parse(server)
	if err != nil || address.Hostname() == "" || (address.Scheme != "https" && address.Scheme != "http") ||
		address.User != nil || address.RawQuery != "" || address.Fragment != "" {
		return nil, errors.New("无效的登录服务器地址")
	}
	authorization := runtimeLoginAuthorization{Kind: "password", Password: &password}
	if totp := input["totp"]; totp != "" {
		authorization.TOTP = &totp
	}
	if browser {
		user, authorization, err = m.runtimeBrowserAuthorization(ctx, strings.TrimSuffix(server, "/"))
		if err != nil {
			return nil, err
		}
		server = strings.TrimSuffix(server, "/")
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
	profile, err := selectedRuntimeLoginProfile(root)
	if err != nil {
		return nil, err
	}
	// Record the native transaction before Runtime can act on it.
	if err := m.bindDesktopRuntimeLogin(id, operation, profile); err != nil {
		return nil, err
	}
	source, err := readRuntimeLoginMaterial(ctx, profile)
	if err != nil {
		return nil, err
	}
	defer clear(source)
	target, err := readRuntimeLoginTargetMaterial(ctx, root, profile, user, server)
	if err != nil {
		return nil, err
	}
	defer clear(target)
	var previous *HealthInfo
	health, _, err := m.Health(ctx)
	if err == nil {
		if err := m.verifySelectedHealth(health, info); err != nil {
			return nil, err
		}
		if health.ManagedID != "" {
			return nil, errors.New("登录前需要暂停当前 Runtime 的宿主协调器")
		}
		if !samePath(health.DataDir, profile) || health.ImageSHA256 != info.SHA256 {
			return nil, errors.New("运行中的 Runtime 与当前登录账户或已安装程序不一致")
		}
		if err := m.stopDetached(ctx, health); err != nil {
			return nil, err
		}
		previous = &health
	} else if !IsUnavailable(err) {
		return nil, err
	}
	intent := runtimeLoginIntent{OperationID: operation, User: user, Server: server}
	admission, err := m.publishPausedRuntimeMaintenance(ctx, profile, m.ClientVersion, intent, previous)
	if err != nil {
		return nil, err
	}
	unknown := func(cause error) (json.RawMessage, error) {
		receipt := runtimeLoginReceipt{OperationID: operation, SourceProfile: profile, Outcome: "unknown"}
		raw, err := desktopRuntimeLoginEnvelope(receipt)
		return raw, &unconfirmedError{id: operation, cause: errors.Join(cause, err)}
	}
	var super *string
	if value := input["super"]; value != "" {
		super = &value
	}
	image := filepath.Join(m.runtimeDir(info.Version), runtimeBinary())
	control, params, err := m.prepareInitialRuntimeLogin(ctx, image, info.SHA256, admission, authorization,
		runtimeLoginContinuation{OfferLocalMerge: true}, super, source, target)
	if err != nil {
		return unknown(err)
	}
	result, err := m.sendInitialRuntimeLogin(ctx, control, params, &admission)
	if err != nil {
		return unknown(err)
	}
	raw, err := m.finishDesktopRuntimeLogin(ctx, image, info.SHA256, &admission, result.Receipt)
	if err != nil && len(raw) == 0 {
		return unknown(err)
	}
	return raw, err
}

func (m *Manager) bindDesktopRuntimeLogin(id, operation, profile string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[id]
	if job == nil || job.State != "running" || job.Transaction != nil {
		return errors.New("Client2 登录操作无法绑定原账户身份")
	}
	job.Transaction = &hostTransaction{
		OperationID: operation, SourceProfile: profile, Kind: "account", RuntimeLogin: true,
	}
	if err := m.persistOperationsLocked(); err != nil {
		job.Transaction = nil
		return err
	}
	return nil
}

func (m *Manager) finishDesktopRuntimeLogin(ctx context.Context, image, digest string, admission *runtimeAccountAdmission, receipt runtimeLoginReceipt) (json.RawMessage, error) {
	switch receipt.Outcome {
	case "awaiting_input":
		raw, err := desktopRuntimeLoginEnvelope(receipt)
		return raw, &unconfirmedError{id: receipt.OperationID, cause: errors.Join(errors.New("登录需要补充输入"), err)}
	case "awaiting_verification":
		committed, err := m.startAndCompleteRuntimeLogin(ctx, image, digest, admission, receipt)
		if err != nil {
			return nil, err
		}
		receipt = committed
	}
	return desktopRuntimeLoginEnvelope(receipt)
}

func desktopRuntimeLoginEnvelope(receipt runtimeLoginReceipt) (json.RawMessage, error) {
	status := "ok"
	switch receipt.Outcome {
	case "awaiting_input":
		status = "pending"
	case "unknown":
		status = "fail"
	}
	receipt.SuperPassword = nil
	if receipt.Login != nil {
		receipt.Login.SuperIssued = nil
	}
	summary := struct {
		runtimeLoginReceipt
		RuntimeLogin bool `json:"runtime_login"`
	}{receipt, true}
	return json.Marshal(map[string]any{"command": "auth-account", "status": status, "summary": summary, "items": []any{}, "errors": []string{}})
}
