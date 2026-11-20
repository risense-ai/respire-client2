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
	if json.Unmarshal(admission.Record, &record) != nil || !record.Sent ||
		!hostOperationID.MatchString(record.OperationID) || applied.OperationID != record.OperationID ||
		applied.SourceProfile != record.Source || applied.Outcome != "awaiting_verification" {
		return runtimeLoginReceipt{}, errors.New("Runtime login completion requires its original applied receipt")
	}
	unknown := func(cause error) (runtimeLoginReceipt, error) {
		return runtimeLoginReceipt{}, &unconfirmedError{id: record.OperationID, cause: cause}
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

	identity := runtimeAuthIdentity{Kind: "bound", AccountID: applied.AccountID, SessionDigest: *applied.SessionDigest}
	return m.finalizeRuntimeLogin(ctx, launched.image, imageDigest, admission, record, target, identity)
}

func (m *Manager) finalizeRuntimeLogin(ctx context.Context, image, imageDigest string, admission *runtimeAccountAdmission,
	record runtimeAccountRecord, target string, identity runtimeAuthIdentity) (runtimeLoginReceipt, error) {
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
	account := "login-" + strings.ReplaceAll(record.OperationID, "-", "")
	backend := runtimeLoginBackend()
	store := nativeRuntimeCredentialStore(ctx)
	private, err := store.readSlot(backend, "rsrs", "login-control:"+account, false)
	if err != nil {
		return unknown(err)
	}
	var custody runtimeLoginCustody
	var password *string
	if record.Register {
		password, err = registeredRuntimePassword(ctx, record, target, identity.SessionDigest, private)
		if err != nil {
			return unknown(err)
		}
	} else {
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
			candidate.Digest != identity.SessionDigest || request.CustodyAck.CandidateDigest != candidate.Digest {
			return unknown(errors.New("Original Runtime login credential custody does not match the applied session"))
		}
		password = request.Continuation.LegacyPassword
		if password == nil && request.Authorization.Kind == "password" {
			password = request.Authorization.Password
		}
	}
	defer clear(custody.Request)
	defer clear(custody.Candidate)

	var marker runtimeMaintenanceMarker
	if json.Unmarshal(admission.Marker, &marker) != nil || marker.Schema != 1 || (marker.Phase != "Starting" && marker.Phase != "Verified") ||
		marker.Token != record.Token || marker.ExpectedProfile != target || marker.OwnerPID != os.Getpid() {
		return unknown(errors.New("Runtime login completion requires the original maintenance owner"))
	}
	birth, err := hostControlBirth(ctx)
	if err != nil {
		return unknown(err)
	}
	if marker.OwnerBirth != birth {
		return unknown(errors.New("Runtime login completion owner changed"))
	}
	if marker.Phase == "Starting" {
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
	}
	control, err := m.prepareRuntimeControlGrant(ctx, image, imageDigest, target, identity.AccountID, admission.Marker, "Verified")
	if err != nil {
		return unknown(err)
	}
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
		!reflect.DeepEqual(result.Receipt.AccountID, identity.AccountID) ||
		result.Receipt.TargetProfile == nil || !samePath(*result.Receipt.TargetProfile, target) ||
		result.Receipt.SessionDigest == nil || *result.Receipt.SessionDigest != identity.SessionDigest {
		return unknown(errors.New("Runtime did not commit the original login target"))
	}
	if _, err := verifyRuntimeLoginTarget(record, result.Receipt); err != nil {
		return unknown(err)
	}
	if err := retireRuntimeLogin(store, record, admission.Marker, *private); err != nil {
		return unknown(err)
	}
	return result.Receipt, nil
}

// A committed login keeps its serving credentials. Retire only its own
// transient request, then allow ordinary Runtime startup again.
func retireRuntimeLogin(store hostCredentialStore, record runtimeAccountRecord, marker []byte, private string) error {
	if record.Schema != 1 || !hostOperationID.MatchString(record.OperationID) || !hostOperationID.MatchString(record.Token) {
		return errors.New("Committed login has an invalid operation identity")
	}
	directory := filepath.Join(record.Root, "runtime", "autostart")
	if err := checkRuntimeAccountDirectory(directory); err != nil {
		return err
	}
	markerPath := filepath.Join(directory, "maintenance.json")
	actualMarker, err := readCredentialConfig(markerPath, runtimeMaintenanceLimit)
	if err != nil {
		return err
	}
	var owner runtimeMaintenanceMarker
	if !bytes.Equal(actualMarker, marker) || json.Unmarshal(marker, &owner) != nil ||
		owner.Schema != 1 || owner.Phase != "Verified" || owner.Token != record.Token {
		return errors.New("Committed login does not own the current maintenance marker")
	}
	activePath := filepath.Join(directory, "account-active.json")
	active, err := readCredentialConfig(activePath, runtimeMaintenanceLimit)
	if err != nil {
		return err
	}
	var route runtimeAccountRoute
	if json.Unmarshal(active, &route) != nil || route.Schema != 1 || route.OperationID != record.OperationID {
		return errors.New("Committed login does not own the active account route")
	}
	results := filepath.Join(directory, "account-results")
	if err := checkRuntimeAccountDirectory(results); err != nil {
		return err
	}
	sent, err := readCredentialConfig(filepath.Join(results, record.OperationID+".json"), runtimeMaintenanceLimit)
	if err != nil {
		return err
	}
	var actual runtimeAccountRecord
	record.Sent = true
	if json.Unmarshal(sent, &actual) != nil || !reflect.DeepEqual(actual, record) {
		return errors.New("Committed login record does not match the sent operation")
	}
	account := "login-" + strings.ReplaceAll(record.OperationID, "-", "")
	path := loginCustodyPath(record)
	before, err := readCredentialConfig(path, runtimeMaintenanceLimit)
	if err != nil {
		return err
	}
	var state map[string]json.RawMessage
	if json.Unmarshal(before, &state) != nil || state == nil {
		return errors.New("Committed login credential record is invalid")
	}
	for key, expected := range map[string]string{
		"operation_id": record.OperationID, "root": record.Root, "source": record.Source,
		"account": account, "backend": runtimeLoginBackend(),
	} {
		var actual string
		if json.Unmarshal(state[key], &actual) != nil || actual != expected {
			return errors.New("Committed login credential record belongs to another operation")
		}
	}
	if string(state["rolled_back"]) != "false" || string(state["retired"]) != "false" || state["client2_terminal_maintenance"] != nil {
		return errors.New("Committed login credential record has a different outcome")
	}
	state["slots_removed"] = json.RawMessage("true")
	retiring, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := writeRuntimeAccountRecord(path, retiring, before); err != nil {
		return err
	}
	if err := store.remove("rsrs", "login-control:"+account, private); err != nil {
		return err
	}
	// Publish the proof only after the private request is retired. It contains digests only.
	proof, err := json.Marshal(runtimeTerminalMaintenance{Schema: 1, MarkerSHA256: controlDigest(marker),
		RouteSHA256: controlDigest(active), RecordSHA256: controlDigest(sent)})
	if err != nil {
		return err
	}
	state["client2_terminal_maintenance"] = proof
	state["retired"] = json.RawMessage("true")
	retired, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := writeRuntimeAccountRecord(path, retired, retiring); err != nil {
		return err
	}
	return archiveTerminalRuntimeMaintenance(record.Root)
}

func removeOwnedRuntimeAccountRecord(path string, expected []byte) error {
	current, err := readCredentialConfig(path, runtimeMaintenanceLimit)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, expected) {
		return errors.New("Runtime account record changed before cleanup")
	}
	return os.Remove(path)
}
