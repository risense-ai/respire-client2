package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
)

const profileRemoveMethod = "control.profile.remove"
const profileRemoveResultMethod = "control.profile.result"

type spaceRemovalOperation struct {
	Schema         int                 `json:"schema_version"`
	ID             string              `json:"operation_id"`
	Key            string              `json:"key"`
	Root           string              `json:"installation_root"`
	Profile        string              `json:"profile"`
	Identity       runtimeAuthIdentity `json:"identity"`
	Name           string              `json:"name"`
	TargetIdentity runtimeAuthIdentity `json:"target_identity"`
	Fingerprint    string              `json:"fingerprint"`
}

type spaceRemovalReceipt struct {
	ID          string              `json:"operation_id"`
	Fingerprint string              `json:"fingerprint"`
	Identity    runtimeAuthIdentity `json:"identity"`
	Outcome     string              `json:"outcome"`
}

func (o spaceRemovalOperation) fingerprint() (string, error) {
	// The Runtime uses serde_json's tuple encoding without HTML escaping.
	if strings.ContainsAny(o.Root+o.Profile, "\u2028\u2029") {
		return "", errors.New("空间移除路径无法与 Runtime 协议精确绑定")
	}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode([]any{profileRemoveMethod, o.Schema, o.Root, o.Profile, o.Identity, o.Name, o.TargetIdentity}); err != nil {
		return "", err
	}
	return controlDigest(bytes.TrimSuffix(data.Bytes(), []byte("\n"))), nil
}

func (o spaceRemovalOperation) matchesReceipt(r spaceRemovalReceipt) bool {
	return r.ID == o.ID && r.Fingerprint == o.Fingerprint && r.Outcome == "committed" &&
		reflect.DeepEqual(r.Identity, o.Identity)
}

func (m *Manager) exchangeSpaceRemoval(ctx context.Context, record spaceRemovalRecord, marker []byte, query bool) (spaceRemovalReceipt, error) {
	original := record.Operation
	image := filepath.Join(m.runtimeDir(record.Runtime.Version), runtimeBinary())
	control, err := m.prepareRuntimeAccountControl(ctx, image, record.Runtime.SHA256, original.Profile, original.Identity.AccountID, marker)
	if err != nil {
		return spaceRemovalReceipt{}, err
	}
	if control.binding.InstallationRoot != original.Root || control.binding.Profile != original.Profile {
		return spaceRemovalReceipt{}, errors.New("空间移除授权已改变原账户")
	}
	params, err := json.Marshal(map[string]any{"schema_version": 1, "binding": control.binding,
		"identity": original.Identity, "original": original})
	if err != nil {
		return spaceRemovalReceipt{}, err
	}
	id, err := controlUUID()
	if err != nil {
		return spaceRemovalReceipt{}, err
	}
	method := profileRemoveMethod
	if query {
		method = profileRemoveResultMethod
	}
	reply, err := m.exchangeRuntimeControl(ctx, control.image, control.port, control.grant, runtimeControlRequest{
		V: 1, ID: id, Method: method, Args: []string{string(params)}, OperationID: original.ID, IdempotencyKey: original.Key,
	})
	if err != nil {
		return spaceRemovalReceipt{}, err
	}
	return verifySpaceRemovalReceipt(reply, original)
}

func verifySpaceRemovalReceipt(reply rpcResponse, original spaceRemovalOperation) (spaceRemovalReceipt, error) {
	var envelope struct {
		Command, Status          string
		Details, Summary         json.RawMessage
		Errors, Actions, Related []json.RawMessage
		Items                    []struct{ Name, Status, Value string }
	}
	var receipt spaceRemovalReceipt
	if !reply.OK || reply.Exit != 0 || reply.OperationID != original.ID || reply.Code != "" || reply.Error != "" ||
		(reply.DataDir != "" && !samePath(reply.DataDir, original.Profile)) ||
		json.Unmarshal(reply.Envelope, &envelope) != nil || envelope.Command != "profile-control" || envelope.Status != "ok" ||
		string(envelope.Details) != "null" || len(envelope.Errors)+len(envelope.Actions)+len(envelope.Related) != 0 ||
		len(envelope.Items) != 2 ||
		envelope.Items[0].Name != "control_ready" || envelope.Items[0].Status != "ok" || envelope.Items[0].Value != "true" ||
		envelope.Items[1].Name != "store_ready" || envelope.Items[1].Status != "skip" || envelope.Items[1].Value != "false" {
		return receipt, errors.New("Runtime 空间移除没有可核验的完成回执")
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.Summary))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || !original.matchesReceipt(receipt) {
		return receipt, errors.New("Runtime 空间移除回执与原操作不匹配")
	}
	return receipt, nil
}

func renderSpaceRemoval(original spaceRemovalOperation) (json.RawMessage, error) {
	return json.Marshal(map[string]any{"command": "space", "status": "ok",
		"summary": map[string]string{"action": "remove", "name": original.Name},
		"items":   []map[string]string{{"name": "space", "status": "ok", "value": "deleted"}},
		"details": map[string]string{"operation_id": original.ID, "fingerprint": original.Fingerprint, "outcome": "committed"},
	})
}
