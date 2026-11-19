//go:build linux || windows || (darwin && cgo)

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
)

// The host coordinator retains the installation lock and must query the
// original operation before continuing an unconfirmed transmission.
func (m *Manager) continueRuntimeLoginCode(ctx context.Context, imagePath, imageDigest string,
	admission runtimeAccountAdmission, receipt runtimeLoginReceipt, code string) (runtimeLoginResult, error) {
	var record runtimeAccountRecord
	if json.Unmarshal(admission.Record, &record) != nil || !record.Sent || record.Register ||
		!hostOperationID.MatchString(record.OperationID) || receipt.OperationID != record.OperationID ||
		receipt.SourceProfile != record.Source || receipt.Outcome != "awaiting_input" ||
		receipt.Login == nil || len(receipt.Login.RequiredInputs) != 1 || receipt.Login.RequiredInputs[0] != "totp" ||
		receipt.Login.User != record.User || receipt.Login.Server != strings.TrimRight(strings.TrimSpace(record.Server), "/") {
		return runtimeLoginResult{}, errors.New("Runtime login code requires its original awaiting-input receipt")
	}
	if len(code) != 6 || strings.IndexFunc(code, func(c rune) bool { return c < '0' || c > '9' }) >= 0 {
		return runtimeLoginResult{}, errors.New("Two-step login code must have six digits")
	}
	sentRecord, err := readCredentialConfig(admission.Path, runtimeMaintenanceLimit)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	if !bytes.Equal(sentRecord, admission.Record) {
		return runtimeLoginResult{}, errors.New("Original Runtime login record changed")
	}
	account := "login-" + strings.ReplaceAll(record.OperationID, "-", "")
	backend := runtimeLoginBackend()
	private, err := nativeRuntimeCredentialStore(ctx).readSlot(backend, "rsrs", "login-control:"+account, false)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	var custody runtimeLoginCustody
	var params runtimeLoginParams
	if private == nil || json.Unmarshal([]byte(*private), &custody) != nil || json.Unmarshal(custody.Request, &params) != nil ||
		params.Authorization.Kind != "password" || params.Authorization.Password == nil || params.Authorization.TOTP != nil ||
		params.Material.Account != account || params.Material.Backend != backend ||
		params.User != record.User || params.Server != record.Server ||
		params.Binding.InstallationRoot != record.Root || params.Binding.Profile != record.Source ||
		!reflect.DeepEqual(params.Identity, record.Original.Identity) ||
		params.Challenge == nil || controlDigest([]byte(params.Challenge.Payload)) != params.Challenge.Digest {
		return runtimeLoginResult{}, errors.New("Original two-step login credentials or challenge changed")
	}
	metadata, err := readCredentialConfig(loginCustodyPath(record), runtimeMaintenanceLimit)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	expected, err := loginCustodyMetadata(record, account, backend, &params.Challenge.Digest)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	if !bytes.Equal(metadata, expected) {
		return runtimeLoginResult{}, errors.New("Runtime login challenge is not paired with its original operation")
	}
	identity, err := verifyRuntimeLoginSource(record)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	control, err := m.prepareRuntimeAccountControl(ctx, imagePath, imageDigest, record.Source, identity.AccountID, admission.Marker)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	if control.binding.InstallationRoot != record.Root || control.binding.Profile != record.Source {
		return runtimeLoginResult{}, errors.New("Runtime login continuation moved to another installation")
	}
	params.Binding = control.binding
	params.Authorization.TOTP = &code
	argument, err := json.Marshal(params)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	defer clear(argument)
	requestID, err := controlUUID()
	if err != nil {
		return runtimeLoginResult{}, err
	}
	result, err := m.exchangeRuntimeLogin(ctx, control, record, requestID, argument)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	if result.Handoff != nil && result.Handoff.Kind == "challenge" {
		return runtimeLoginResult{}, &unconfirmedError{id: record.OperationID, cause: errors.New("Runtime unexpectedly replaced the submitted login challenge")}
	}
	return result, nil
}
