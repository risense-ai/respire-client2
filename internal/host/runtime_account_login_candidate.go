//go:build linux || windows || (darwin && cgo)

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type runtimeLoginCustodyAck struct {
	CandidateDigest string `json:"candidate_sha256"`
}

func runtimeLoginCandidateSlots(handoff runtimeLoginHandoff, params runtimeLoginParams) (json.RawMessage, map[string]string, error) {
	if !validRuntimeLoginInputs([]string{"host_credentials"}, &handoff) {
		return nil, nil, errors.New("Invalid Runtime login credential handoff")
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, bytes.TrimSpace(handoff.Session), "", "  ") != nil || controlDigest(pretty.Bytes()) != *handoff.CandidateDigest {
		return nil, nil, errors.New("Runtime login candidate digest differs from the handoff")
	}
	var session struct {
		User    string `json:"user"`
		Address string `json:"addr"`
		Account string `json:"keyring_account"`
		Backend string `json:"keyring_backend"`
		Version int    `json:"vault_version"`
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(handoff.Session, &session) != nil || json.Unmarshal(handoff.Session, &fields) != nil ||
		session.User != params.User || session.Address != strings.TrimRight(strings.TrimSpace(params.Server), "/") ||
		session.Account != params.Material.Account || session.Backend != params.Material.Backend {
		return nil, nil, errors.New("Runtime login candidate changed the original account or credential backend")
	}
	for _, key := range []string{"pass", "super", "secret", "secret_key"} {
		if _, present := fields[key]; present {
			return nil, nil, errors.New("Runtime login candidate contains a plaintext credential")
		}
	}

	password := params.Continuation.LegacyPassword
	if password == nil && params.Authorization.Kind == "password" {
		password = params.Authorization.Password
	}
	slots := map[string]string{}
	factors := map[string]string{"version": fmt.Sprintf("v%d", session.Version)}
	if password != nil {
		slots["pass"] = *password
	}
	switch session.Version {
	case 1:
		if password == nil || params.Continuation.AccountSecret == nil || *params.Continuation.AccountSecret == "" {
			return nil, nil, errors.New("Original v1 password or Account Secret is missing")
		}
		factors["password"], factors["account_secret"] = *password, *params.Continuation.AccountSecret
		slots["legacy-secret"] = *params.Continuation.AccountSecret
	case 2, 3, 4:
		if params.SuperPassword == nil || *params.SuperPassword == "" {
			return nil, nil, errors.New("Original super key is missing")
		}
		factors["super_password"], slots["super"] = *params.SuperPassword, *params.SuperPassword
		if session.Version == 3 {
			if params.Continuation.SecretKey == nil || *params.Continuation.SecretKey == "" {
				return nil, nil, errors.New("Original v3 Secret Key is missing")
			}
			factors["secret_key"], slots["legacy-secret"] = *params.Continuation.SecretKey, *params.Continuation.SecretKey
		}
	default:
		return nil, nil, errors.New("Unsupported Runtime login candidate vault version")
	}
	if len(slots) != len(handoff.Slots) {
		return nil, nil, errors.New("Runtime login credential slot plan differs")
	}
	for _, slot := range handoff.Slots {
		if slot == "legacy_secret" {
			slot = "legacy-secret"
		}
		if _, present := slots[slot]; !present {
			return nil, nil, errors.New("Runtime login credential slot plan differs")
		}
	}
	candidate, err := json.Marshal(map[string]any{"session_sha256": *handoff.CandidateDigest, "factors": factors})
	if err != nil {
		return nil, nil, err
	}
	return candidate, slots, nil
}

func persistRuntimeLoginCandidate(ctx context.Context, record runtimeAccountRecord, sent runtimeLoginParams,
	handoff runtimeLoginHandoff) (runtimeLoginParams, error) {
	account, backend := sent.Material.Account, sent.Material.Backend
	store := nativeRuntimeCredentialStore(ctx)
	slot := "login-control:" + account
	original, err := store.readSlot(backend, "rsrs", slot, false)
	if err != nil {
		return runtimeLoginParams{}, err
	}
	var custody runtimeLoginCustody
	var params runtimeLoginParams
	if original == nil || json.Unmarshal([]byte(*original), &custody) != nil ||
		json.Unmarshal(custody.Request, &params) != nil || params.CustodyAck != nil {
		return runtimeLoginParams{}, errors.New("Runtime login candidate has no original credential record")
	}
	sent.Binding, sent.Authorization.TOTP = params.Binding, params.Authorization.TOTP
	expected, err := json.Marshal(sent)
	if err != nil {
		return runtimeLoginParams{}, err
	}
	defer clear(expected)
	if !bytes.Equal(expected, custody.Request) {
		return runtimeLoginParams{}, errors.New("Runtime login candidate does not match the original credential request")
	}
	candidate, slots, err := runtimeLoginCandidateSlots(handoff, params)
	if err != nil {
		return runtimeLoginParams{}, err
	}
	defer clear(candidate)
	for name, value := range slots {
		existing, err := store.readSlot(backend, "rsrs", name+":"+account, true)
		if err != nil {
			return runtimeLoginParams{}, err
		}
		if existing != nil && *existing != value {
			return runtimeLoginParams{}, errors.New("Runtime login credential slot conflicts with the original value")
		}
	}
	var challengeDigest *string
	if params.Challenge != nil {
		challengeDigest = &params.Challenge.Digest
	}
	metadata, err := loginCustodyMetadata(record, account, backend, challengeDigest, nil)
	if err != nil {
		return runtimeLoginParams{}, err
	}
	nextMetadata, err := loginCustodyMetadata(record, account, backend, challengeDigest, handoff.Session)
	if err != nil {
		return runtimeLoginParams{}, err
	}
	params.CustodyAck = &runtimeLoginCustodyAck{CandidateDigest: *handoff.CandidateDigest}
	custody.Request, err = json.Marshal(params)
	if err != nil {
		return runtimeLoginParams{}, err
	}
	defer clear(custody.Request)
	custody.Candidate, custody.Slots = candidate, slots
	next, err := json.Marshal(custody)
	if err != nil {
		return runtimeLoginParams{}, err
	}
	defer clear(next)
	if err := writeRuntimeAccountRecord(loginCustodyPath(record), nextMetadata, metadata); err != nil {
		return runtimeLoginParams{}, err
	}
	if err := store.replace("rsrs", slot, original, string(next)); err != nil {
		return runtimeLoginParams{}, err
	}
	for name, value := range slots {
		if err := store.preserve("rsrs", name+":"+account, value); err != nil {
			return runtimeLoginParams{}, err
		}
	}
	return params, nil
}

func (m *Manager) acknowledgeRuntimeLoginCandidate(ctx context.Context, originalControl runtimeAccountControl,
	admission runtimeAccountAdmission, record runtimeAccountRecord, sent runtimeLoginParams,
	handoff runtimeLoginHandoff) (runtimeLoginResult, error) {
	unknown := func(cause error) (runtimeLoginResult, error) {
		return runtimeLoginResult{}, &unconfirmedError{id: record.OperationID, cause: cause}
	}
	params, err := persistRuntimeLoginCandidate(ctx, record, sent, handoff)
	if err != nil {
		return unknown(err)
	}
	identity, err := verifyRuntimeLoginSource(record)
	if err != nil {
		return unknown(err)
	}
	digest, ok := originalControl.grant["image_sha256"].(string)
	if !ok {
		return unknown(errors.New("Original Runtime image identity is missing"))
	}
	control, err := m.prepareRuntimeAccountControl(ctx, originalControl.image, digest, record.Source, identity.AccountID, admission.Marker)
	if err != nil {
		return unknown(err)
	}
	if control.binding.InstallationRoot != record.Root || control.binding.Profile != record.Source {
		return unknown(errors.New("Runtime custody acknowledgement moved to another installation"))
	}
	params.Binding, params.Identity = control.binding, identity
	argument, err := json.Marshal(params)
	if err != nil {
		return unknown(err)
	}
	defer clear(argument)
	requestID, err := controlUUID()
	if err != nil {
		return unknown(err)
	}
	result, err := m.exchangeRuntimeLogin(ctx, control, record, requestID, argument)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	if result.Handoff != nil {
		return unknown(errors.New("Runtime requested another handoff after acknowledging verified custody"))
	}
	return result, nil
}
