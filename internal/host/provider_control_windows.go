package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

type providerPreparation struct {
	Selection string `json:"selection_sha256"`
	Assets    struct {
		Complete bool              `json:"complete"`
		Missing  []json.RawMessage `json:"missing"`
		Files    []struct {
			Name       string `json:"name"`
			State      string `json:"state"`
			Version    string `json:"version"`
			Path       string `json:"path"`
			Size       uint64 `json:"expected_size"`
			Digest     string `json:"sha256"`
			ActualSize uint64 `json:"actual_size"`
			ActualHash string `json:"actual_sha256"`
		} `json:"assets"`
		Backend *struct {
			State    string  `json:"state"`
			Reason   *string `json:"reason"`
			Evidence *struct {
				Contract   int    `json:"contract"`
				Status     string `json:"status"`
				Engine     string `json:"engine"`
				Ready      bool   `json:"ready"`
				Fresh      bool   `json:"fresh_session"`
				Inferred   bool   `json:"inference_completed"`
				Provider   string `json:"provider"`
				Dimensions uint64 `json:"dimensions"`
			} `json:"evidence"`
		} `json:"backend_load"`
	} `json:"assets"`
	details json.RawMessage
}

func (p providerPreparation) matchesCatalog(root string, catalog winMLCatalog, verified bool) bool {
	target := filepath.Join(root, "engines", "winml-"+winMLVersion, winMLCatalogName)
	for _, file := range p.Assets.Files {
		if file.Name == "windows-ml-catalog" {
			return file.Version == winMLVersion && samePath(file.Path, target) &&
				file.Size == catalog.size && file.Digest == catalog.digest &&
				(!verified || file.State == "complete" && file.ActualSize == catalog.size && file.ActualHash == catalog.digest)
		}
	}
	return false
}

func (m *Manager) providerPreparation(ctx context.Context, version InstalledRuntime, profile string, account *string, marker []byte, selection string) (providerPreparation, error) {
	var plan providerPreparation
	image := filepath.Join(m.runtimeDir(version.Version), runtimeBinary())
	control, err := m.prepareRuntimeAccountControl(ctx, image, version.SHA256, profile, account, marker)
	if err != nil {
		return plan, err
	}
	id, err := controlUUID()
	if err != nil {
		return plan, err
	}
	command, method, budget := "model-install-plan", "control.model_install_plan", 10*time.Second
	var expected *string
	if selection != "" {
		command, method, budget = "model-install-verify", "control.model_install_verify", 185*time.Second
		expected = &selection
	}
	params, err := json.Marshal(struct {
		Schema    int                   `json:"schema_version"`
		Binding   runtimeControlBinding `json:"binding"`
		Intent    map[string]string     `json:"intent"`
		Selection *string               `json:"selection_sha256"`
	}{1, control.binding, map[string]string{"for_engine": "npu"}, expected})
	if err != nil {
		return plan, err
	}
	bounded, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	reply, err := m.exchangeRuntimeControl(bounded, control.image, control.port, control.grant,
		runtimeControlRequest{V: 1, ID: id, Method: method, Args: []string{string(params)}})
	if err != nil {
		return plan, err
	}
	if reply.DataDir != "" && !samePath(reply.DataDir, profile) {
		return plan, errors.New("WinML Runtime 回执来自其他数据目录")
	}
	return parseProviderPreparation(reply.Envelope, command, selection)
}

func parseProviderPreparation(raw json.RawMessage, command, selection string) (providerPreparation, error) {
	var plan providerPreparation
	var envelope struct {
		Command string          `json:"command"`
		Status  string          `json:"status"`
		Details json.RawMessage `json:"details"`
		Errors  []string        `json:"errors"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Command != command || envelope.Status != "ok" || len(envelope.Errors) != 0 ||
		json.Unmarshal(envelope.Details, &plan) != nil || !validHash(plan.Selection) || plan.Assets.Missing == nil || len(plan.Assets.Files) == 0 {
		return plan, errors.New("Runtime 缺少有效的 WinML 资源核验回执")
	}
	if selection == "" && plan.Assets.Backend != nil || selection != "" && (plan.Selection != selection || plan.Assets.Backend == nil) {
		return plan, errors.New("WinML Runtime 核验回执与计划不匹配")
	}
	plan.details = envelope.Details
	return plan, nil
}

func providerInstallReceipt(plan providerPreparation) (json.RawMessage, error) {
	backend := plan.Assets.Backend
	evidence := backend.Evidence
	complete := plan.Assets.Complete && len(plan.Assets.Missing) == 0
	for _, file := range plan.Assets.Files {
		complete = complete && file.Name != "" && file.State == "complete"
	}
	verified := complete && backend.State == "loaded" && backend.Reason == nil && evidence != nil &&
		evidence.Contract == 1 && evidence.Status == "loaded" && evidence.Engine == "npu" && evidence.Ready &&
		evidence.Fresh && evidence.Inferred && evidence.Dimensions > 0 && evidence.Provider != ""
	status := "ok"
	var verificationErr error
	providers := []string{}
	if verified {
		providers = append(providers, evidence.Provider)
	} else {
		status = "fail"
		reason := "缺少完整的 NPU 推理证据"
		if backend.Reason != nil && *backend.Reason != "" {
			reason = *backend.Reason
		}
		verificationErr = fmt.Errorf("WinML catalog 已安装，Runtime 未确认 NPU 就绪 (%s)", reason)
	}
	result, err := json.Marshal(map[string]any{
		"command": "model", "status": status,
		"summary": map[string]any{"providers": providers, "npu_ready": verified, "catalog_installed": true},
		"details": map[string]any{"catalog_version": winMLVersion, "verified": verified, "preparation": plan.details},
	})
	return result, errors.Join(verificationErr, err)
}
