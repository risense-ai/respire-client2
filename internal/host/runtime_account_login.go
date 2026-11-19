//go:build linux || windows || (darwin && cgo)

package host

import (
	"bytes"
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
	MigrateLocal    bool    `json:"migrate_local"`
	MigrateVault    bool    `json:"migrate_vault"`
	OfferLocalMerge bool    `json:"offer_local_merge"`
	LegacyPassword  *string `json:"legacy_password,omitempty"`
	AccountSecret   *string `json:"account_secret,omitempty"`
	SecretKey       *string `json:"secret_key,omitempty"`
	SourceSecret    *string `json:"source_secret,omitempty"`
	NewSuper        *string `json:"new_super,omitempty"`
	MergeLocal      *bool   `json:"merge_local,omitempty"`
}

type runtimeLoginMaterial struct {
	Account        string          `json:"keyring_account"`
	Backend        string          `json:"keyring_backend"`
	Source         json.RawMessage `json:"source"`
	TargetOriginal json.RawMessage `json:"target_original"`
}

type runtimeLoginChallenge struct {
	Payload string `json:"payload"`
	Digest  string `json:"sha256"`
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
	Challenge     *runtimeLoginChallenge    `json:"challenge,omitempty"`
	CustodyAck    *runtimeLoginCustodyAck   `json:"custody_ack,omitempty"`
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
	identity, err := verifyRuntimeLoginSource(record)
	if err != nil {
		return control, params, err
	}
	control, err = m.prepareRuntimeAccountControl(ctx, imagePath, imageDigest, record.Source, identity.AccountID, admission.Marker)
	if err != nil {
		return control, params, err
	}
	if control.binding.InstallationRoot != record.Root || control.binding.Profile != record.Source {
		return control, params, errors.New("Runtime login scope differs from the recorded installation")
	}
	params = runtimeLoginParams{
		Version: 1, Binding: control.binding, Identity: identity, Server: record.Server, User: record.User,
		Authorization: authorization, Continuation: continuation, SuperPassword: super,
		Material: runtimeLoginMaterial{
			Account: "login-" + strings.ReplaceAll(record.OperationID, "-", ""), Backend: runtimeLoginBackend(),
			Source: source, TargetOriginal: target,
		},
	}
	return control, params, nil
}

func verifyRuntimeLoginSource(record runtimeAccountRecord) (runtimeAuthIdentity, error) {
	current, err := readCredentialConfig(filepath.Join(record.Source, "session.json"), controlFrameLimit)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return runtimeAuthIdentity{}, err
	}
	identity, err := runtimeAuthIdentityOf(current)
	if err != nil {
		return runtimeAuthIdentity{}, err
	}
	if !reflect.DeepEqual(record.Original.Identity, identity) {
		return runtimeAuthIdentity{}, errors.New("Runtime login source session changed")
	}
	return identity, nil
}

func runtimeLoginBackend() string {
	if runtime.GOOS == "linux" {
		return "secret_service"
	}
	return "native"
}

type runtimeLoginCustody struct {
	Request   json.RawMessage   `json:"request"`
	Candidate json.RawMessage   `json:"candidate"`
	Slots     map[string]string `json:"slots"`
}

func loginCustodyMetadata(record runtimeAccountRecord, account, backend string, digest *string, candidate json.RawMessage) ([]byte, error) {
	return json.Marshal(map[string]any{
		"operation_id": record.OperationID, "root": record.Root, "source": record.Source, "account": account, "backend": backend,
		"candidate": candidate, "challenge_sha256": digest, "rolled_back": false, "slots_removed": false, "retired": false,
	})
}

func loginCustodyPath(record runtimeAccountRecord) string {
	return filepath.Join(record.Root, "runtime", "autostart", "account-login-"+record.OperationID+".json")
}

// Keep the nonsecret routing record compatible with CLI recovery. The request
// itself includes credentials and must only be saved in the system keyring.
func persistInitialRuntimeLogin(ctx context.Context, record runtimeAccountRecord, params runtimeLoginParams) error {
	account := "login-" + strings.ReplaceAll(record.OperationID, "-", "")
	backend := runtimeLoginBackend()
	if params.Material.Account != account || params.Material.Backend != backend || params.Challenge != nil {
		return errors.New("Runtime login credential slot differs from its original operation")
	}
	metadata, err := loginCustodyMetadata(record, account, backend, nil, nil)
	if err != nil {
		return err
	}
	params.Authorization.TOTP = nil
	encodedRequest, err := json.Marshal(params)
	if err != nil {
		return err
	}
	defer clear(encodedRequest)
	secrets, err := json.Marshal(runtimeLoginCustody{Request: encodedRequest, Slots: map[string]string{}})
	if err != nil {
		return err
	}
	defer clear(secrets)
	if err := writeRuntimeAccountRecord(loginCustodyPath(record), metadata, nil); err != nil {
		return err
	}
	return nativeRuntimeCredentialStore(ctx).preserve("rsrs", "login-control:"+account, string(secrets))
}

func persistRuntimeLoginChallenge(ctx context.Context, record runtimeAccountRecord, params runtimeLoginParams,
	encodedRequest []byte, handoff runtimeLoginHandoff) error {
	if params.Authorization.TOTP != nil {
		params.Authorization.TOTP = nil
		var err error
		encodedRequest, err = json.Marshal(params)
		if err != nil {
			return err
		}
		defer clear(encodedRequest)
	}
	if !validRuntimeLoginInputs([]string{"totp"}, &handoff) {
		return errors.New("Invalid Runtime login challenge")
	}
	path := loginCustodyPath(record)
	account, backend := params.Material.Account, params.Material.Backend
	expectedMetadata, err := loginCustodyMetadata(record, account, backend, nil, nil)
	if err != nil {
		return err
	}
	store := nativeRuntimeCredentialStore(ctx)
	slot := "login-control:" + account
	original, err := store.readSlot(backend, "rsrs", slot, false)
	if err != nil {
		return err
	}
	var custody runtimeLoginCustody
	if original == nil || json.Unmarshal([]byte(*original), &custody) != nil || !bytes.Equal(custody.Request, encodedRequest) {
		return errors.New("Runtime login challenge has no matching original credential record")
	}
	params.Challenge = &runtimeLoginChallenge{Payload: *handoff.Challenge, Digest: *handoff.ChallengeDigest}
	custody.Request, err = json.Marshal(params)
	if err != nil {
		return err
	}
	defer clear(custody.Request)
	secrets, err := json.Marshal(custody)
	if err != nil {
		return err
	}
	defer clear(secrets)
	metadata, err := loginCustodyMetadata(record, account, backend, handoff.ChallengeDigest, nil)
	if err != nil {
		return err
	}
	if err := writeRuntimeAccountRecord(path, metadata, expectedMetadata); err != nil {
		return err
	}
	return store.replace("rsrs", slot, original, string(secrets))
}

// Accept only the original operation. Persist and read back custody before the
// send flag permits the private Runtime exchange.
func (m *Manager) sendInitialRuntimeLogin(ctx context.Context, control runtimeAccountControl, params runtimeLoginParams,
	admission *runtimeAccountAdmission) (runtimeLoginResult, error) {
	var record runtimeAccountRecord
	if json.Unmarshal(admission.Record, &record) != nil || record.Sent ||
		!hostOperationID.MatchString(record.OperationID) || record.User != params.User || record.Server != params.Server ||
		!reflect.DeepEqual(record.Original.Identity, params.Identity) || !reflect.DeepEqual(control.binding, params.Binding) ||
		control.binding.InstallationRoot != record.Root || control.binding.Profile != record.Source {
		return runtimeLoginResult{}, errors.New("Runtime login intent no longer matches its original operation")
	}
	requestID, err := controlUUID()
	if err != nil {
		return runtimeLoginResult{}, err
	}
	argument, err := json.Marshal(params)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	defer clear(argument)
	if err := persistInitialRuntimeLogin(ctx, record, params); err != nil {
		return runtimeLoginResult{}, err
	}
	if err := admission.markLoginSent(); err != nil {
		return runtimeLoginResult{}, err
	}
	result, err := m.exchangeRuntimeLogin(ctx, control, record, requestID, argument)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	if result.Handoff != nil && result.Handoff.Kind == "challenge" {
		if err := persistRuntimeLoginChallenge(ctx, record, params, argument, *result.Handoff); err != nil {
			return runtimeLoginResult{}, &unconfirmedError{id: record.OperationID, cause: err}
		}
		result.Handoff = nil
	}
	if result.Handoff != nil {
		return m.acknowledgeRuntimeLoginCandidate(ctx, control, *admission, record, params, *result.Handoff)
	}
	return result, nil
}

func (m *Manager) exchangeRuntimeLogin(ctx context.Context, control runtimeAccountControl, record runtimeAccountRecord,
	requestID string, argument []byte) (runtimeLoginResult, error) {
	reply, err := m.exchangeRuntimeControl(ctx, control.image, control.port, control.grant, runtimeControlRequest{
		V: 1, ID: requestID, Method: "control.auth.login", Args: []string{string(argument)},
		OperationID: record.OperationID, IdempotencyKey: record.OperationID,
	})
	if err != nil {
		return runtimeLoginResult{}, err
	}
	defer clear(reply.Envelope)
	result, err := readRuntimeLoginResult(reply, record)
	if err != nil {
		return runtimeLoginResult{}, &unconfirmedError{id: record.OperationID, cause: err}
	}
	if result.Receipt.Outcome == "awaiting_verification" || result.Receipt.Outcome == "committed" {
		if _, err := verifyRuntimeLoginTarget(record, result.Receipt); err != nil {
			return runtimeLoginResult{}, &unconfirmedError{id: record.OperationID, cause: err}
		}
	}
	return result, nil
}
