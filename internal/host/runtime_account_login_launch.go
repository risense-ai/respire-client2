//go:build linux || windows || (darwin && cgo)

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// The coordinator holds the same takeover lock as the admission.
func (m *Manager) prepareRuntimeLoginLaunch(ctx context.Context, imagePath, imageDigest string,
	admission *runtimeAccountAdmission, receipt runtimeLoginReceipt) (runtimeAccountControl, error) {
	var control runtimeAccountControl
	var record runtimeAccountRecord
	if json.Unmarshal(admission.Record, &record) != nil || !record.Sent || record.Register ||
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
	address, _, err := m.runtimeAccountEndpoint()
	if err != nil {
		return control, err
	}
	if err := requireClosedRuntimePort(ctx, address); err != nil {
		return control, err
	}

	var marker runtimeMaintenanceMarker
	if json.Unmarshal(admission.Marker, &marker) != nil || marker.Schema != 1 || marker.Phase != "Paused" ||
		marker.Token != record.Token || marker.ExpectedProfile != record.Source || marker.OwnerPID != os.Getpid() {
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
	control, err = m.prepareRuntimeControlGrant(ctx, imagePath, imageDigest, target, receipt.AccountID, next, "Starting")
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
