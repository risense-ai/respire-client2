//go:build linux || windows || (darwin && cgo)

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

// Keep the caller's installation lock through launch, explicit credential
// delivery and terminal Runtime verification.
func (m *Manager) startAndCompleteRuntimeLogin(ctx context.Context, imagePath, imageDigest string,
	admission *runtimeAccountAdmission, receipt runtimeLoginReceipt) (runtimeLoginReceipt, error) {
	unknown := func(err error) (runtimeLoginReceipt, error) {
		return runtimeLoginReceipt{}, &unconfirmedError{id: receipt.OperationID, cause: err}
	}
	grant, err := m.prepareRuntimeLoginLaunch(ctx, imagePath, imageDigest, admission, receipt)
	if err != nil {
		return unknown(err)
	}
	cmd := runtimeServingCommand(grant.image, m.DataRoot, "127.0.0.1", strconv.FormatUint(grant.port, 10), grant.binding.LaunchID)
	health, err := m.launchRuntimeServing(ctx, cmd)
	if err != nil {
		return unknown(err)
	}
	ready, err := m.unlockRuntimeLoginTarget(ctx, grant, *admission, receipt, health.PID)
	if err != nil {
		return unknown(err)
	}
	return m.completeRuntimeLogin(ctx, grant, admission, receipt, ready)
}

// The coordinator holds the same takeover lock as the admission.
func (m *Manager) prepareRuntimeLoginLaunch(ctx context.Context, imagePath, imageDigest string,
	admission *runtimeAccountAdmission, receipt runtimeLoginReceipt) (runtimeAccountControl, error) {
	var control runtimeAccountControl
	var record runtimeAccountRecord
	if json.Unmarshal(admission.Record, &record) != nil || !record.Sent ||
		receipt.OperationID != record.OperationID || receipt.SourceProfile != record.Source ||
		receipt.Outcome != "awaiting_verification" {
		return control, errors.New("Runtime target startup requires its original applied login receipt")
	}
	sent, err := readCredentialConfig(admission.Path, runtimeMaintenanceLimit)
	if err != nil {
		return control, err
	}
	if !bytes.Equal(sent, admission.Record) {
		return control, errors.New("Runtime login record changed before target startup")
	}
	target, err := verifyRuntimeLoginTarget(record, receipt)
	if err != nil {
		return control, err
	}
	return m.prepareRuntimeProfileLaunch(ctx, imagePath, imageDigest, record, admission, target, receipt.AccountID)
}

// A caller must first validate the transaction receipt and the selected account.
func (m *Manager) prepareRuntimeProfileLaunch(ctx context.Context, imagePath, imageDigest string,
	record runtimeAccountRecord, admission *runtimeAccountAdmission, target string, accountID *string) (runtimeAccountControl, error) {
	var control runtimeAccountControl
	address, _, err := m.runtimeAccountEndpoint()
	if err != nil {
		return control, err
	}
	if err := requireClosedRuntimePort(ctx, address); err != nil {
		return control, err
	}

	var marker runtimeMaintenanceMarker
	if json.Unmarshal(admission.Marker, &marker) != nil || marker.Schema != 1 || marker.Phase != "Paused" ||
		marker.Token != record.Token || (marker.ExpectedProfile != record.Source && marker.ExpectedProfile != target) ||
		marker.OwnerPID != os.Getpid() {
		return control, errors.New("Runtime target startup requires the original Paused owner")
	}
	birth, err := hostControlBirth(ctx)
	if err != nil {
		return control, err
	}
	if marker.OwnerBirth != birth {
		return control, errors.New("Runtime target startup owner changed")
	}
	marker.Phase, marker.ExpectedProfile = "Starting", target
	next, err := json.Marshal(marker)
	if err != nil {
		return control, err
	}
	directory := filepath.Join(record.Root, "runtime", "autostart")
	launch := filepath.Join(directory, "launch")
	if err := os.Mkdir(launch, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return control, err
	}
	canonical, err := canonicalSessionDirectory(launch)
	if err != nil {
		return control, err
	}
	if !samePath(canonical, launch) {
		return control, errors.New("Runtime launch directory was redirected")
	}
	if err := writeRuntimeAccountRecord(filepath.Join(directory, "maintenance.json"), next, admission.Marker); err != nil {
		return control, err
	}
	admission.Marker = next
	control, err = m.prepareRuntimeControlGrant(ctx, imagePath, imageDigest, target, accountID, next, "Starting")
	if err != nil {
		return runtimeAccountControl{}, err
	}
	grant, err := json.Marshal(control.grant)
	if err != nil {
		return runtimeAccountControl{}, err
	}
	if err := writeRuntimeAccountRecord(filepath.Join(launch, "launch-"+control.binding.LaunchID+".json"), grant, nil); err != nil {
		return runtimeAccountControl{}, err
	}
	return control, nil
}

// The caller retains takeover from account admission through target verification.
func (m *Manager) unlockRuntimeLoginTarget(ctx context.Context, control runtimeAccountControl,
	admission runtimeAccountAdmission, receipt runtimeLoginReceipt, startedPID uint32) (HealthInfo, error) {
	var record runtimeAccountRecord
	if json.Unmarshal(admission.Record, &record) != nil || !record.Sent ||
		receipt.OperationID != record.OperationID || receipt.SourceProfile != record.Source ||
		receipt.Outcome != "awaiting_verification" {
		return HealthInfo{}, errors.New("Runtime target unlock requires its original applied login receipt")
	}
	target, err := verifyRuntimeLoginTarget(record, receipt)
	if err != nil {
		return HealthInfo{}, err
	}
	if control.binding.ExpectedAccountID == nil {
		return HealthInfo{}, errors.New("Runtime target unlock is missing its original account identity")
	}
	return m.unlockRuntimeProfileTarget(ctx, control, admission, record, target, *receipt.AccountID, *receipt.SessionDigest, startedPID)
}

func (m *Manager) unlockRuntimeProfileTarget(ctx context.Context, control runtimeAccountControl,
	admission runtimeAccountAdmission, record runtimeAccountRecord, target, accountID, sessionDigest string, startedPID uint32) (HealthInfo, error) {
	expectedDigest, ok := control.grant["image_sha256"].(string)
	if !ok || !validHash(expectedDigest) || control.binding.InstallationRoot != record.Root ||
		control.binding.Profile != target || control.binding.ExpectedAccountID != nil &&
		*control.binding.ExpectedAccountID != accountID || startedPID == 0 {
		return HealthInfo{}, errors.New("Runtime target unlock is missing its original launch identity")
	}
	markerPath := filepath.Join(record.Root, "runtime", "autostart", "maintenance.json")
	currentMarker, err := readCredentialConfig(markerPath, runtimeMaintenanceLimit)
	if err != nil {
		return HealthInfo{}, err
	}
	var marker runtimeMaintenanceMarker
	if !bytes.Equal(currentMarker, admission.Marker) || json.Unmarshal(currentMarker, &marker) != nil ||
		marker.Phase != "Starting" || marker.Token != record.Token || marker.ExpectedProfile != target {
		return HealthInfo{}, errors.New("Runtime target unlock requires its exact Starting marker")
	}
	health, _, err := m.Health(ctx)
	if err != nil {
		return HealthInfo{}, err
	}
	if !health.StartupComplete || health.PID != startedPID || health.InstanceID != control.binding.LaunchID ||
		health.AccountID != accountID || health.Generation == 0 ||
		!samePath(health.InstallationRoot, record.Root) || !samePath(health.DataDir, target) ||
		!samePath(health.ImagePath, control.image) || health.ImageSHA256 != expectedDigest {
		return HealthInfo{}, errors.New("Running Runtime does not match the authorized target launch")
	}
	if err := verifyHostProcess(health); err != nil {
		return HealthInfo{}, err
	}
	if err := m.unlockVerifiedRuntime(ctx, health, nativeRuntimeCredentialStore(ctx), sessionDigest, false); err != nil {
		return HealthInfo{}, err
	}
	ready, _, err := m.Health(ctx)
	if err != nil {
		return HealthInfo{}, err
	}
	health.StoreReady = true
	health.CredentialState = "available"
	if ready != health {
		return HealthInfo{}, errors.New("Runtime did not confirm the same unlocked target instance")
	}
	return ready, nil
}
