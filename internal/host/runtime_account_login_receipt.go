//go:build linux || windows || (darwin && cgo)

package host

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// The Runtime must have selected the same target it reports before Client2
// attempts to serve the new account. An applied receipt is still pending until
// the new Runtime opens that database and confirms the account.
func verifyRuntimeLoginTarget(record runtimeAccountRecord, receipt runtimeLoginReceipt) (string, error) {
	if receipt.TargetProfile == nil || receipt.AccountID == nil || receipt.SessionDigest == nil {
		return "", errors.New("Runtime login target is missing")
	}
	target := *receipt.TargetProfile
	actual, err := canonicalSessionDirectory(target)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(record.Root, actual)
	if err != nil || !filepath.IsLocal(relative) || !samePath(actual, target) {
		return "", errors.New("Runtime login target is outside its installation or was redirected")
	}
	chosen, err := selectedRuntimeLoginProfile(record.Root)
	if err != nil {
		return "", err
	}
	if !samePath(actual, chosen) {
		return "", errors.New("Runtime login receipt does not match the selected account")
	}
	session, err := readCredentialConfig(filepath.Join(actual, "session.json"), controlFrameLimit)
	if err != nil {
		return "", err
	}
	identity, err := runtimeAuthIdentityOf(session)
	if err != nil {
		return "", err
	}
	if identity.Kind != "bound" || identity.AccountID == nil || *identity.AccountID != *receipt.AccountID ||
		identity.SessionDigest != *receipt.SessionDigest || *identity.AccountID != controlDigest([]byte(record.User)) {
		return "", errors.New("Runtime login target session identity differs from its receipt")
	}
	return actual, nil
}

func selectedRuntimeLoginProfile(root string) (string, error) {
	selected := root
	raw, err := readCredentialConfig(filepath.Join(root, "client.json"), 1<<20)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err == nil {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return "", errors.New("Cannot verify the selected Runtime account in client.json")
		}
		if field, present := fields["data_dir"]; present {
			var directory *string
			if json.Unmarshal(field, &directory) != nil || directory == nil ||
				(*directory != "" && !filepath.IsAbs(*directory)) {
				return "", errors.New("Invalid selected Runtime account path")
			}
			if *directory != "" {
				selected = *directory
			}
		}
	}
	chosen, err := canonicalSessionDirectory(selected)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, chosen)
	if err != nil || !filepath.IsLocal(relative) || !samePath(chosen, selected) {
		return "", errors.New("Selected Runtime account is outside its installation or was redirected")
	}
	return chosen, nil
}

type runtimeLoginReceipt struct {
	OperationID   string  `json:"operation_id"`
	Outcome       string  `json:"outcome"`
	SourceProfile string  `json:"source_profile"`
	TargetProfile *string `json:"target_profile"`
	AccountID     *string `json:"account_id"`
	SessionDigest *string `json:"session_sha256"`
	SuperPassword *string `json:"super_password"`
	Login         *struct {
		RequiredInputs []string `json:"required_inputs"`
		User           string   `json:"user"`
		Server         string   `json:"addr"`
		Directory      *string  `json:"dir"`
		SuperIssued    *string  `json:"super_issued"`
	} `json:"login"`
}

type runtimeLoginHandoff struct {
	Kind            string          `json:"kind"`
	Challenge       *string         `json:"payload"`
	ChallengeDigest *string         `json:"sha256"`
	CandidateDigest *string         `json:"candidate_sha256"`
	Session         json.RawMessage `json:"session"`
	Slots           []string        `json:"slots"`
}

// Only the receipt can drive desktop state. The handoff is consumed in the
// host, before any account result is exposed outside the login coordinator.
type runtimeLoginResult struct {
	Receipt runtimeLoginReceipt  `json:"receipt"`
	Handoff *runtimeLoginHandoff `json:"handoff"`
}

func readRuntimeLoginResult(reply rpcResponse, record runtimeAccountRecord) (runtimeLoginResult, error) {
	var result runtimeLoginResult
	var envelope struct {
		Command string          `json:"command"`
		Status  string          `json:"status"`
		Summary json.RawMessage `json:"summary"`
		Errors  []string        `json:"errors"`
	}
	if json.Unmarshal(reply.Envelope, &envelope) != nil || envelope.Command != "auth-account" || len(envelope.Errors) != 0 {
		return result, errors.New("Invalid Runtime login response envelope")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(envelope.Summary, &fields) != nil || fields == nil {
		return result, errors.New("Invalid Runtime login response")
	}
	if _, wrapped := fields["receipt"]; wrapped {
		if json.Unmarshal(envelope.Summary, &result) != nil {
			return runtimeLoginResult{}, errors.New("Invalid wrapped Runtime login receipt")
		}
	} else {
		// Runtime emits an unwrapped receipt when no login continuation exists.
		_, handoff := fields["handoff"]
		if json.Unmarshal(envelope.Summary, &result.Receipt) != nil || result.Receipt.Login != nil || handoff {
			return runtimeLoginResult{}, errors.New("Invalid unwrapped Runtime login receipt")
		}
	}
	receipt := result.Receipt
	if receipt.OperationID != record.OperationID || receipt.SourceProfile != record.Source ||
		!filepath.IsAbs(receipt.SourceProfile) || receipt.SuperPassword != nil ||
		(receipt.TargetProfile != nil && !filepath.IsAbs(*receipt.TargetProfile)) {
		return runtimeLoginResult{}, errors.New("Runtime login receipt did not match the admitted account operation")
	}
	if receipt.Login != nil && (receipt.Login.User != record.User ||
		receipt.Login.Server != strings.TrimRight(strings.TrimSpace(record.Server), "/") ||
		(receipt.Login.Directory != nil && !filepath.IsAbs(*receipt.Login.Directory))) {
		return runtimeLoginResult{}, errors.New("Runtime login receipt changed the original account or server")
	}
	switch receipt.Outcome {
	case "awaiting_input":
		if envelope.Status != "pending" || receipt.Login == nil || receipt.AccountID != nil ||
			receipt.SessionDigest != nil || receipt.Login.SuperIssued != nil ||
			!validRuntimeLoginInputs(receipt.Login.RequiredInputs, result.Handoff) {
			return runtimeLoginResult{}, errors.New("Runtime login returned invalid continuation requirements")
		}
	case "awaiting_verification", "committed":
		if envelope.Status != "ok" || result.Handoff != nil || receipt.TargetProfile == nil ||
			receipt.AccountID == nil || !validHash(*receipt.AccountID) ||
			receipt.SessionDigest == nil || !validHash(*receipt.SessionDigest) {
			return runtimeLoginResult{}, errors.New("Runtime login target is missing its verified account identity")
		}
	default:
		return runtimeLoginResult{}, errors.New("Runtime login returned an unexpected outcome")
	}
	return result, nil
}

func validRuntimeLoginInputs(inputs []string, handoff *runtimeLoginHandoff) bool {
	if len(inputs) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, input := range inputs {
		if seen[input] {
			return false
		}
		seen[input] = true
		switch input {
		case "totp", "host_credentials", "super_password", "legacy_password", "account_secret", "source_secret", "secret_key", "new_super", "merge_local":
		default:
			return false
		}
	}
	if handoff == nil {
		return !seen["host_credentials"]
	}
	if len(inputs) != 1 {
		return false
	}
	switch handoff.Kind {
	case "challenge":
		return seen["totp"] && handoff.Challenge != nil && handoff.ChallengeDigest != nil &&
			handoff.CandidateDigest == nil && len(handoff.Session) == 0 && len(handoff.Slots) == 0 &&
			controlDigest([]byte(*handoff.Challenge)) == *handoff.ChallengeDigest
	case "credentials":
		slots := map[string]bool{}
		for _, slot := range handoff.Slots {
			if slots[slot] || (slot != "super" && slot != "pass" && slot != "legacy_secret") {
				return false
			}
			slots[slot] = true
		}
		return seen["host_credentials"] && handoff.CandidateDigest != nil && validHash(*handoff.CandidateDigest) &&
			handoff.Challenge == nil && handoff.ChallengeDigest == nil && len(handoff.Slots) > 0 &&
			len(handoff.Session) > 0 && handoff.Session[0] == '{'
	}
	return false
}
