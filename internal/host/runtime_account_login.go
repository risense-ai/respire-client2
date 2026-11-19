//go:build linux || windows || (darwin && cgo)

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
)

type runtimeLoginAuthorization struct {
	Kind     string  `json:"kind"`
	Password *string `json:"password,omitempty"`
	Token    *string `json:"token,omitempty"`
	TOTP     *string `json:"totp,omitempty"`
	Session  *string `json:"session_id,omitempty"`
}

type runtimeLoginContinuation struct {
	MigrateLocal    bool `json:"migrate_local"`
	MigrateVault    bool `json:"migrate_vault"`
	OfferLocalMerge bool `json:"offer_local_merge"`
}

type runtimeLoginMaterial struct {
	Account        string          `json:"keyring_account"`
	Backend        string          `json:"keyring_backend"`
	Source         json.RawMessage `json:"source"`
	TargetOriginal json.RawMessage `json:"target_original"`
}

type runtimeLoginParams struct {
	Version       int                       `json:"schema_version"`
	Binding       runtimeControlBinding     `json:"binding"`
	Identity      runtimeAuthIdentity       `json:"identity"`
	Server        string                    `json:"addr"`
	User          string                    `json:"user"`
	Authorization runtimeLoginAuthorization `json:"authorization"`
	SuperPassword *string                   `json:"super_password,omitempty"`
	Continuation  runtimeLoginContinuation  `json:"continuation"`
	Material      runtimeLoginMaterial      `json:"material"`
}

// The coordinator retains takeover. The original session is rechecked before
// constructing the request, and Runtime repeats that check with its library lock.
func (m *Manager) prepareInitialRuntimeLogin(ctx context.Context, imagePath, imageDigest string, admission runtimeAccountAdmission,
	authorization runtimeLoginAuthorization, continuation runtimeLoginContinuation, super *string,
	source, target json.RawMessage) (runtimeAccountControl, runtimeLoginParams, error) {
	var control runtimeAccountControl
	var params runtimeLoginParams
	var record runtimeAccountRecord
	if json.Unmarshal(admission.Record, &record) != nil || record.Schema != 1 || record.Sent || record.Register ||
		!hostOperationID.MatchString(record.OperationID) {
		return control, params, errors.New("Runtime login requires the original unsent account record")
	}
	current, err := readCredentialConfig(filepath.Join(record.Source, "session.json"), controlFrameLimit)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return control, params, err
	}
	identity, err := runtimeAuthIdentityOf(current)
	if err != nil {
		return control, params, err
	}
	if !reflect.DeepEqual(record.Original.Identity, identity) {
		return control, params, errors.New("Runtime login source session changed before admission")
	}
	control, err = m.prepareRuntimeAccountControl(ctx, imagePath, imageDigest, record.Source, identity.AccountID, admission.Marker)
	if err != nil {
		return control, params, err
	}
	if control.binding.InstallationRoot != record.Root || control.binding.Profile != record.Source {
		return control, params, errors.New("Runtime login scope differs from the recorded installation")
	}
	backend := "native"
	if runtime.GOOS == "linux" {
		backend = "secret_service"
	}
	params = runtimeLoginParams{
		Version: 1, Binding: control.binding, Identity: identity, Server: record.Server, User: record.User,
		Authorization: authorization, Continuation: continuation, SuperPassword: super,
		Material: runtimeLoginMaterial{
			Account: "login-" + strings.ReplaceAll(record.OperationID, "-", ""), Backend: backend,
			Source: source, TargetOriginal: target,
		},
	}
	return control, params, nil
}

// The coordinator persists credential custody before calling this method.
func (m *Manager) sendInitialRuntimeLogin(ctx context.Context, control runtimeAccountControl, params runtimeLoginParams,
	admission *runtimeAccountAdmission) (rpcResponse, error) {
	var record runtimeAccountRecord
	if json.Unmarshal(admission.Record, &record) != nil || record.Sent ||
		!hostOperationID.MatchString(record.OperationID) || record.User != params.User || record.Server != params.Server ||
		!reflect.DeepEqual(record.Original.Identity, params.Identity) || !reflect.DeepEqual(control.binding, params.Binding) ||
		control.binding.InstallationRoot != record.Root || control.binding.Profile != record.Source {
		return rpcResponse{}, errors.New("Runtime login intent no longer matches its original operation")
	}
	requestID, err := controlUUID()
	if err != nil {
		return rpcResponse{}, err
	}
	argument, err := json.Marshal(params)
	if err != nil {
		return rpcResponse{}, err
	}
	defer clear(argument)
	if err := admission.markLoginSent(); err != nil {
		return rpcResponse{}, err
	}
	return m.exchangeRuntimeControl(ctx, control.image, control.port, control.grant, runtimeControlRequest{
		V: 1, ID: requestID, Method: "control.auth.login", Args: []string{string(argument)},
		OperationID: record.OperationID, IdempotencyKey: record.OperationID,
	})
}
