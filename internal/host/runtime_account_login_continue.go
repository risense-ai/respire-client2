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

type runtimeLoginSupplement struct {
	TOTP           *string `json:"totp"`
	SuperPassword  *string `json:"super_password"`
	LegacyPassword *string `json:"legacy_password"`
	AccountSecret  *string `json:"account_secret"`
	SecretKey      *string `json:"secret_key"`
	SourceSecret   *string `json:"source_secret"`
	NewSuper       *string `json:"new_super"`
	MergeLocal     *bool   `json:"merge_local"`
}

func (input runtimeLoginSupplement) validate(required []string) error {
	text := map[string]*string{
		"totp": input.TOTP, "super_password": input.SuperPassword, "legacy_password": input.LegacyPassword,
		"account_secret": input.AccountSecret, "secret_key": input.SecretKey, "source_secret": input.SourceSecret,
		"new_super": input.NewSuper,
	}
	if !validRuntimeLoginInputs(required, nil) {
		return errors.New("Invalid Runtime login supplement requirements")
	}
	requested := map[string]bool{}
	for _, key := range required {
		requested[key] = true
	}
	for key, value := range text {
		if requested[key] != (value != nil) || value != nil && *value == "" {
			return errors.New("Runtime login requires the requested supplemental input")
		}
	}
	if requested["merge_local"] != (input.MergeLocal != nil) {
		return errors.New("Runtime login requires the original local merge choice")
	}
	if input.TOTP != nil && (len(*input.TOTP) != 6 || strings.IndexFunc(*input.TOTP, func(c rune) bool { return c < '0' || c > '9' }) >= 0) {
		return errors.New("Two-step login code must have six digits")
	}
	return nil
}

// The host coordinator retains the installation lock and must query the
// original operation before continuing an unconfirmed transmission.
func (m *Manager) continueRuntimeLogin(ctx context.Context, imagePath, imageDigest string,
	admission runtimeAccountAdmission, receipt runtimeLoginReceipt, supplement runtimeLoginSupplement) (runtimeLoginResult, error) {
	var record runtimeAccountRecord
	if json.Unmarshal(admission.Record, &record) != nil || !record.Sent || record.Register ||
		!hostOperationID.MatchString(record.OperationID) || receipt.OperationID != record.OperationID ||
		receipt.SourceProfile != record.Source || receipt.Outcome != "awaiting_input" ||
		receipt.Login == nil ||
		receipt.Login.User != record.User || receipt.Login.Server != strings.TrimRight(strings.TrimSpace(record.Server), "/") {
		return runtimeLoginResult{}, errors.New("Runtime login continuation requires its original awaiting-input receipt")
	}
	if err := supplement.validate(receipt.Login.RequiredInputs); err != nil {
		return runtimeLoginResult{}, err
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
	store := nativeRuntimeCredentialStore(ctx)
	slot := "login-control:" + account
	private, err := store.readSlot(backend, "rsrs", slot, false)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	var custody runtimeLoginCustody
	var params runtimeLoginParams
	if private == nil || json.Unmarshal([]byte(*private), &custody) != nil || json.Unmarshal(custody.Request, &params) != nil ||
		params.Authorization.TOTP != nil || params.CustodyAck != nil ||
		params.Material.Account != account || params.Material.Backend != backend ||
		params.User != record.User || params.Server != record.Server ||
		params.Binding.InstallationRoot != record.Root || params.Binding.Profile != record.Source ||
		!reflect.DeepEqual(params.Identity, record.Original.Identity) {
		return runtimeLoginResult{}, errors.New("Original Runtime login credentials changed")
	}
	defer clear(custody.Request)
	auth := params.Authorization
	password := auth.Kind == "password" && auth.Password != nil && auth.Token == nil && auth.Session == nil
	browser := auth.Kind == "browser" && auth.Password == nil && auth.Token != nil && *auth.Token != "" &&
		params.Challenge == nil && supplement.TOTP == nil
	if !password && !browser {
		return runtimeLoginResult{}, errors.New("Original Runtime login authorization changed")
	}
	var challengeDigest *string
	if params.Challenge != nil {
		if controlDigest([]byte(params.Challenge.Payload)) != params.Challenge.Digest {
			return runtimeLoginResult{}, errors.New("Original two-step login challenge changed")
		}
		challengeDigest = &params.Challenge.Digest
	}
	if supplement.TOTP != nil && challengeDigest == nil {
		return runtimeLoginResult{}, errors.New("Original two-step login challenge is missing")
	}
	metadata, err := readCredentialConfig(loginCustodyPath(record), runtimeMaintenanceLimit)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	expected, err := loginCustodyMetadata(record, account, backend, challengeDigest, nil)
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
	if err := persistRuntimeLoginSupplement(store, slot, private, custody, &params, supplement); err != nil {
		return runtimeLoginResult{}, err
	}
	params.Binding = control.binding
	params.Authorization.TOTP = supplement.TOTP
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
	if result.Handoff == nil {
		return result, nil
	}
	if result.Handoff.Kind == "challenge" {
		return runtimeLoginResult{}, &unconfirmedError{id: record.OperationID, cause: errors.New("Runtime unexpectedly replaced the submitted login challenge")}
	}
	return m.acknowledgeRuntimeLoginCandidate(ctx, control, admission, record, params, *result.Handoff)
}

func persistRuntimeLoginSupplement(store hostCredentialStore, slot string, original *string, custody runtimeLoginCustody,
	params *runtimeLoginParams, input runtimeLoginSupplement) error {
	for destination, value := range map[**string]*string{
		&params.SuperPassword: input.SuperPassword, &params.Continuation.LegacyPassword: input.LegacyPassword,
		&params.Continuation.AccountSecret: input.AccountSecret, &params.Continuation.SecretKey: input.SecretKey,
		&params.Continuation.SourceSecret: input.SourceSecret, &params.Continuation.NewSuper: input.NewSuper,
	} {
		if value != nil {
			*destination = value
		}
	}
	if input.MergeLocal != nil {
		params.Continuation.MergeLocal = input.MergeLocal
	}
	next, err := json.Marshal(params)
	if err != nil {
		return err
	}
	defer clear(next)
	if bytes.Equal(next, custody.Request) {
		return nil
	}
	custody.Request = next
	secrets, err := json.Marshal(custody)
	if err != nil {
		return err
	}
	defer clear(secrets)
	return store.replace("rsrs", slot, original, string(secrets))
}
