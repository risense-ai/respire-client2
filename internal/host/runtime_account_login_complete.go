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
	"strings"
)

// Complete only the original login against the same unlocked serving process.
// The caller retains takeover through completion and routing cleanup.
func (m *Manager) completeRuntimeLogin(ctx context.Context, launched runtimeAccountControl, admission *runtimeAccountAdmission,
	applied runtimeLoginReceipt, ready HealthInfo) (runtimeLoginReceipt, error) {
	var record runtimeAccountRecord
	if json.Unmarshal(admission.Record, &record) != nil || !record.Sent || record.Register ||
		!hostOperationID.MatchString(record.OperationID) || applied.OperationID != record.OperationID ||
		applied.SourceProfile != record.Source || applied.Outcome != "awaiting_verification" {
		return runtimeLoginReceipt{}, errors.New("Runtime login completion requires its original applied receipt")
	}
	unknown := func(cause error) (runtimeLoginReceipt, error) {
		return runtimeLoginReceipt{}, &unconfirmedError{id: record.OperationID, cause: cause}
	}
	sent, err := readCredentialConfig(admission.Path, runtimeMaintenanceLimit)
	if err != nil {
		return unknown(err)
	}
	if !bytes.Equal(sent, admission.Record) {
		return unknown(errors.New("Runtime login completion record changed"))
	}
	target, err := verifyRuntimeLoginTarget(record, applied)
	if err != nil {
		return unknown(err)
	}
	imageDigest, ok := launched.grant["image_sha256"].(string)
	if !ok || launched.binding.InstallationRoot != record.Root || launched.binding.Profile != target ||
		!ready.StartupComplete || !ready.StoreReady || ready.CredentialState != "available" ||
		ready.InstanceID != launched.binding.LaunchID || ready.AccountID != *applied.AccountID ||
		!samePath(ready.InstallationRoot, record.Root) || !samePath(ready.DataDir, target) ||
		!samePath(ready.ImagePath, launched.image) || ready.ImageSHA256 != imageDigest {
		return unknown(errors.New("Runtime login target is not the original unlocked owner"))
	}
	current, _, err := m.Health(ctx)
	if err != nil {
		return unknown(err)
	}
	if current != ready {
		return unknown(errors.New("Runtime login target changed before completion"))
	}
	if err := verifyHostProcess(current); err != nil {
		return unknown(err)
	}

	account := "login-" + strings.ReplaceAll(record.OperationID, "-", "")
	backend := runtimeLoginBackend()
	store := nativeRuntimeCredentialStore(ctx)
	private, err := store.readSlot(backend, "rsrs", "login-control:"+account, false)
	if err != nil {
		return unknown(err)
	}
	var custody runtimeLoginCustody
	var request runtimeLoginParams
	var candidate struct {
		Digest  string          `json:"session_sha256"`
		Factors json.RawMessage `json:"factors"`
	}
	if private == nil || json.Unmarshal([]byte(*private), &custody) != nil ||
		json.Unmarshal(custody.Request, &request) != nil || request.CustodyAck == nil ||
		request.Material.Account != account || request.Material.Backend != backend ||
		request.User != record.User || request.Server != record.Server ||
		request.Binding.InstallationRoot != record.Root || request.Binding.Profile != record.Source ||
		!reflect.DeepEqual(request.Identity, record.Original.Identity) ||
		json.Unmarshal(custody.Candidate, &candidate) != nil || len(candidate.Factors) == 0 ||
		candidate.Digest != *applied.SessionDigest || request.CustodyAck.CandidateDigest != candidate.Digest {
		return unknown(errors.New("Original Runtime login credential custody does not match the applied session"))
	}
	defer clear(custody.Request)
	defer clear(custody.Candidate)

	var marker runtimeMaintenanceMarker
	if json.Unmarshal(admission.Marker, &marker) != nil || marker.Schema != 1 || marker.Phase != "Starting" ||
		marker.Token != record.Token || marker.ExpectedProfile != target || marker.OwnerPID != os.Getpid() {
		return unknown(errors.New("Runtime login completion requires the original Starting owner"))
	}
	birth, err := hostControlBirth(ctx)
	if err != nil {
		return unknown(err)
	}
	if marker.OwnerBirth != birth {
		return unknown(errors.New("Runtime login completion owner changed"))
	}
	marker.Phase = "Verified"
	verified, err := json.Marshal(marker)
	if err != nil {
		return unknown(err)
	}
	path := filepath.Join(record.Root, "runtime", "autostart", "maintenance.json")
	if err := writeRuntimeAccountRecord(path, verified, admission.Marker); err != nil {
		return unknown(err)
	}
	admission.Marker = verified
	control, err := m.prepareRuntimeControlGrant(ctx, launched.image, imageDigest, target, applied.AccountID, verified, "Verified")
	if err != nil {
		return unknown(err)
	}
	password := request.Continuation.LegacyPassword
	if password == nil && request.Authorization.Kind == "password" {
		password = request.Authorization.Password
	}
	identity := runtimeAuthIdentity{Kind: "bound", AccountID: applied.AccountID, SessionDigest: *applied.SessionDigest}
	params, err := json.Marshal(map[string]any{
		"schema_version": 1, "binding": control.binding, "identity": identity,
		"candidate": custody.Candidate, "migration_password": password,
	})
	if err != nil {
		return unknown(err)
	}
	defer clear(params)
	requestID, err := controlUUID()
	if err != nil {
		return unknown(err)
	}
	reply, err := m.exchangeRuntimeControl(ctx, control.image, control.port, control.grant, runtimeControlRequest{
		V: 1, ID: requestID, Method: "control.auth.account_complete", Args: []string{string(params)},
		OperationID: record.OperationID, IdempotencyKey: record.OperationID,
	})
	if err != nil {
		return runtimeLoginReceipt{}, err
	}
	defer clear(reply.Envelope)
	result, err := readRuntimeLoginResult(reply, record)
	if err != nil {
		return unknown(err)
	}
	if result.Handoff != nil || result.Receipt.Outcome != "committed" ||
		!reflect.DeepEqual(result.Receipt.AccountID, applied.AccountID) ||
		!reflect.DeepEqual(result.Receipt.TargetProfile, applied.TargetProfile) {
		return unknown(errors.New("Runtime did not commit the original login target"))
	}
	if _, err := verifyRuntimeLoginTarget(record, result.Receipt); err != nil {
		return unknown(err)
	}
	return result.Receipt, nil
}
