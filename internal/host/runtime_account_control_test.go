//go:build linux || windows || (darwin && cgo)

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRuntimeAuthSessionIdentity(t *testing.T) {
	const operation = "00000000-0000-4000-8000-000000000001"
	const factors = `"kdf_salt":"salt","wrapped_urk":"wrap","urk_nonce":"nonce","keyring_account":"login-test","keyring_backend":"native","auth_operation_id":"` + operation + `"`
	for _, tc := range []struct {
		name, session, kind string
		invalid             bool
	}{
		{name: "missing", kind: "absent"},
		{name: "named account", session: `{"user":"original user"}`, kind: "bound"},
		{name: "keygen", session: `{"vault_version":4,` + factors + `}`, kind: "unbound"},
		{name: "offline v1", session: `{"vault_version":1,"user":"","addr":"","secret":"legacy",` + factors + `}`, kind: "unbound"},
		{name: "offline v4", session: `{"vault_version":4,"user":"","addr":"",` + factors + `}`, kind: "unbound"},
		{name: "unexpected field", session: `{"vault_version":4,"token":"extra",` + factors + `}`, invalid: true},
		{name: "no user or keygen", session: `{}`, invalid: true},
		{name: "null is not missing", session: `null`, invalid: true},
		{name: "null user is not absent", session: `{"user":null}`, invalid: true},
		{name: "invalid UUID version", session: `{"vault_version":4,"kdf_salt":"salt","wrapped_urk":"wrap","urk_nonce":"nonce","keyring_account":"login-test","keyring_backend":"native","auth_operation_id":"00000000-0000-1000-8000-000000000001"}`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var original []byte
			if tc.name != "missing" {
				original = []byte(tc.session)
			}
			identity, err := runtimeAuthIdentityOf(original)
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid session admitted an account identity")
				}
				return
			}
			if err != nil || identity.Kind != tc.kind {
				t.Fatalf("wrong account identity: %v", err)
			}
			if original != nil && identity.SessionDigest != controlDigest(original) {
				t.Fatal("account identity was not bound to the exact session bytes")
			}
			if tc.kind == "bound" && (identity.AccountID == nil || *identity.AccountID != controlDigest([]byte("original user"))) {
				t.Fatal("named account hash differs")
			}
			if tc.kind == "unbound" && (identity.OperationID != operation || identity.AccountID != nil) {
				t.Fatal("unbound session lost its original operation")
			}
		})
	}
}

func TestRuntimeLoginReceiptBindsPrivateChallenge(t *testing.T) {
	record := runtimeAccountRecord{
		OperationID: "00000000-0000-4000-8000-000000000001",
		Source:      t.TempDir(),
		User:        "fixture",
		Server:      "https://respire.example.invalid/",
	}
	const challenge = "private challenge fixture"
	digest := controlDigest([]byte(challenge))
	decode := func(operation, digest string, inputs []string) (runtimeLoginResult, error) {
		t.Helper()
		summary := map[string]any{
			"receipt": map[string]any{
				"operation_id": operation, "outcome": "awaiting_input", "source_profile": record.Source,
				"login": map[string]any{"user": record.User, "addr": strings.TrimSuffix(record.Server, "/"), "required_inputs": inputs},
			},
			"handoff": map[string]any{"kind": "challenge", "payload": challenge, "sha256": digest},
		}
		raw, err := json.Marshal(map[string]any{"command": "auth-account", "status": "pending", "summary": summary})
		if err != nil {
			t.Fatal(err)
		}
		return readRuntimeLoginResult(rpcResponse{Envelope: raw}, record)
	}
	result, err := decode(record.OperationID, digest, []string{"totp"})
	if err != nil || result.Handoff == nil || result.Handoff.Challenge == nil || *result.Handoff.Challenge != challenge {
		t.Fatalf("valid challenge did not reach the private coordinator: %v", err)
	}
	public, err := json.Marshal(result.Receipt)
	if err != nil || bytes.Contains(public, []byte(challenge)) {
		t.Fatalf("account receipt contains the private challenge: %v", err)
	}
	for _, test := range []struct {
		operation, digest string
		inputs            []string
	}{
		{"00000000-0000-4000-8000-000000000002", digest, []string{"totp"}},
		{record.OperationID, strings.Repeat("0", 64), []string{"totp"}},
		{record.OperationID, digest, []string{"totp", "totp"}},
		{record.OperationID, digest, []string{"host_credentials"}},
	} {
		if _, err := decode(test.operation, test.digest, test.inputs); err == nil {
			t.Fatal("invalid challenge accepted")
		}
	}
}

func TestPublishedRuntimeRequiresExactClient2Maintenance(t *testing.T) {
	image := os.Getenv("RESPIRE_NATIVE_ACCOUNT_RUNTIME")
	if image == "" {
		t.Skip("set RESPIRE_NATIVE_ACCOUNT_RUNTIME to the published dev.27 Runtime")
	}
	image, err := filepath.Abs(image)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := fileHash(image)
	if err != nil {
		t.Fatal(err)
	}
	root, err := canonicalSessionDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	manager := Manager{DataRoot: root, Endpoint: "http://" + endpoint.Addr().String()}
	defer endpoint.Close()
	ctx := context.Background()
	const issuer = "2.0.0-dev.20"
	operation, err := controlUUID()
	if err != nil {
		t.Fatal(err)
	}
	intent := runtimeLoginIntent{OperationID: operation, User: "isolated-client2", Server: "https://respire.example.invalid"}
	path := filepath.Join(root, "runtime", "autostart", "maintenance.json")
	if _, lock, err := manager.beginOfflineRuntimeAccountMaintenance(ctx, root, issuer, intent); err == nil {
		lock.Close()
		t.Fatal("maintenance started while the Runtime port was in use")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed admission left a maintenance record: %v", err)
	}
	if err := endpoint.Close(); err != nil {
		t.Fatal(err)
	}
	library, err := lockRuntimeDatabase(ctx, root, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, second, beginErr := manager.beginOfflineRuntimeAccountMaintenance(ctx, root, issuer, intent)
	if err := library.Close(); err != nil {
		t.Fatal(err)
	}
	if beginErr == nil {
		second.Close()
		t.Fatal("maintenance started while another owner held the library lock")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed library lock check left a maintenance record: %v", err)
	}
	admission, lock, err := manager.beginOfflineRuntimeAccountMaintenance(ctx, root, issuer, intent)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, second, err := manager.beginOfflineRuntimeAccountMaintenance(ctx, root, issuer, intent); err == nil {
		second.Close()
		t.Fatal("second coordinator acquired the installation takeover lock")
	}
	if _, err := manager.publishPausedRuntimeMaintenance(ctx, root, issuer, intent); err == nil {
		t.Fatal("an existing maintenance record was overwritten")
	}
	marker := admission.Marker
	if current, err := os.ReadFile(path); err != nil || !bytes.Equal(current, marker) {
		t.Fatalf("original maintenance record was not retained: %v", err)
	}
	if _, err := manager.prepareRuntimeAccountControl(ctx, image, hash, root, nil, nil); err == nil {
		t.Fatal("Client2 accepted a maintenance marker without the original ownership bytes")
	}
	changed := append(bytes.Clone(marker), '\n')
	if _, err := manager.prepareRuntimeAccountControl(context.Background(), image, hash, root, nil, changed); err == nil {
		t.Fatal("Client2 accepted stale maintenance bytes")
	}
	password := "isolated fixture"
	super := "isolated recovery fixture"
	authorization := runtimeLoginAuthorization{Kind: "password", Password: &password}
	// Contradictory options exercise admission without contacting a server.
	continuation := runtimeLoginContinuation{MigrateLocal: true, MigrateVault: true}
	session := filepath.Join(root, "session.json")
	if err := os.WriteFile(session, []byte(`{"user":"changed-after-admission"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.prepareInitialRuntimeLogin(ctx, image, hash, admission, authorization, continuation, &super, nil, nil); err == nil {
		t.Fatal("changed profile admitted the original login")
	}
	if err := os.Remove(session); err != nil {
		t.Fatal(err)
	}
	control, params, err := manager.prepareInitialRuntimeLogin(ctx, image, hash, admission, authorization, continuation, &super, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	changedIntent := params
	changedIntent.User = "different-user"
	if _, err := manager.sendInitialRuntimeLogin(ctx, control, changedIntent, &admission); err == nil {
		t.Fatal("changed account intent was sent")
	}
	slot := "login-control:" + params.Material.Account
	store := nativeRuntimeCredentialStore(ctx)
	for _, slot := range []string{slot, "super:" + params.Material.Account, "pass:" + params.Material.Account} {
		t.Cleanup(func() {
			saved, err := store.readSlot(params.Material.Backend, "rsrs", slot, false)
			if err != nil {
				t.Error(err)
				return
			}
			if saved == nil {
				return
			}
			var cleanup *exec.Cmd
			switch runtime.GOOS {
			case "windows":
				cleanup = exec.Command("cmdkey.exe", "/delete:"+slot+".rsrs")
			case "darwin":
				cleanup = exec.Command("security", "delete-generic-password", "-s", "rsrs", "-a", slot)
			case "linux":
				cleanup = exec.Command("secret-tool", "clear", "service", "rsrs", "username", slot, "target", "default")
			}
			if err := cleanup.Run(); err != nil {
				t.Errorf("remove isolated login fixture: %v", err)
				return
			}
			if still, err := store.readSlot(params.Material.Backend, "rsrs", slot, false); err != nil || still != nil {
				t.Errorf("isolated login fixture credential was not removed: %v", err)
			}
		})
	}
	result, err := manager.sendInitialRuntimeLogin(ctx, control, params, &admission)
	if _, duplicateErr := manager.sendInitialRuntimeLogin(ctx, control, params, &admission); duplicateErr == nil {
		t.Fatal("sent operation was readmitted")
	}
	var unconfirmed *unconfirmedError
	if !errors.As(err, &unconfirmed) || unconfirmed.id != operation ||
		!strings.Contains(err.Error(), "account_outcome_unknown") || result.Receipt.OperationID != "" || result.Handoff != nil {
		t.Fatalf("Runtime did not accept the exact Paused authorization and reject invalid login intent: %v", err)
	}
	saved, credentialErr := store.readSlot(params.Material.Backend, "rsrs", slot, false)
	if credentialErr != nil || saved == nil {
		t.Fatalf("original login was not preserved in the native credential store: %v", credentialErr)
	}
	var custody struct {
		Request runtimeLoginParams `json:"request"`
	}
	if json.Unmarshal([]byte(*saved), &custody) != nil || custody.Request.Authorization.Password == nil ||
		*custody.Request.Authorization.Password != password || custody.Request.Binding.LaunchID != control.binding.LaunchID {
		t.Fatal("the system credential store did not retain the original login request")
	}
	metadata := filepath.Join(root, "runtime", "autostart", "account-login-"+operation+".json")
	nonsecret, err := readCredentialConfig(metadata, runtimeMaintenanceLimit)
	if err != nil || bytes.Contains(nonsecret, []byte(password)) {
		t.Fatalf("login metadata missing or contains private input: %v", err)
	}
	rawRecord, err := readCredentialConfig(admission.Path, runtimeMaintenanceLimit)
	if err != nil {
		t.Fatal(err)
	}
	var record runtimeAccountRecord
	if json.Unmarshal(rawRecord, &record) != nil || record.OperationID != operation || !record.Sent {
		t.Fatal("Runtime exchange did not retain the original sent operation")
	}
	if _, err := os.Stat(filepath.Join(root, ".login-recovery.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid intent should not create a login journal: %v", err)
	}

	// Validate the native checkpoint separately from the rejected Runtime request.
	challenge := "isolated challenge fixture"
	challengeDigest := controlDigest([]byte(challenge))
	handoff := runtimeLoginHandoff{Kind: "challenge", Challenge: &challenge, ChallengeDigest: &challengeDigest}
	originalRequest, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistRuntimeLoginChallenge(ctx, record, params, []byte("{}"), handoff); err == nil {
		t.Fatal("unrelated credential input was allowed to update the checkpoint")
	}
	if err := persistRuntimeLoginChallenge(ctx, record, params, originalRequest, handoff); err != nil {
		t.Fatal(err)
	}
	saved, err = store.readSlot(params.Material.Backend, "rsrs", slot, false)
	if err != nil || saved == nil || json.Unmarshal([]byte(*saved), &custody) != nil ||
		custody.Request.Challenge == nil || custody.Request.Challenge.Payload != challenge ||
		custody.Request.Challenge.Digest != challengeDigest {
		t.Fatalf("original two-step challenge was not recoverable from the credential store: %v", err)
	}
	nonsecret, err = readCredentialConfig(metadata, runtimeMaintenanceLimit)
	if err != nil || !bytes.Contains(nonsecret, []byte(challengeDigest)) || bytes.Contains(nonsecret, []byte(challenge)) {
		t.Fatalf("challenge metadata did not retain only its digest: %v", err)
	}
	continuationReceipt, err := json.Marshal(map[string]any{
		"operation_id": operation, "source_profile": root, "outcome": "awaiting_input",
		"login": map[string]any{"user": intent.User, "addr": intent.Server, "required_inputs": []string{"totp"}},
	})
	var waiting runtimeLoginReceipt
	if err != nil || json.Unmarshal(continuationReceipt, &waiting) != nil {
		t.Fatal("invalid test continuation receipt")
	}
	if _, err := manager.continueRuntimeLoginCode(ctx, image, hash, admission, waiting, "short"); err == nil {
		t.Fatal("invalid code reached the Runtime")
	}
	code := "098765"
	if _, err := manager.continueRuntimeLoginCode(ctx, image, hash, admission, waiting, code); !errors.As(err, &unconfirmed) ||
		unconfirmed.id != operation || !strings.Contains(err.Error(), "account_outcome_unknown") {
		t.Fatalf("Runtime did not receive the original continuation before rejecting fixture intent: %v", err)
	}
	reloaded, err := store.readSlot(params.Material.Backend, "rsrs", slot, false)
	if err != nil || reloaded == nil || *reloaded != *saved {
		t.Fatalf("transient verification code was retained in the original private custody: %v", err)
	}
	sessionCandidate, err := json.Marshal(map[string]any{
		"user": intent.User, "addr": intent.Server, "vault_version": 4,
		"keyring_account": params.Material.Account, "keyring_backend": params.Material.Backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, sessionCandidate, "", "  "); err != nil {
		t.Fatal(err)
	}
	digest := controlDigest(pretty.Bytes())
	candidate := runtimeLoginHandoff{Kind: "credentials", CandidateDigest: &digest, Session: sessionCandidate, Slots: []string{"pass", "super"}}
	if _, _, err := runtimeLoginCandidateSlots(candidate, params); err != nil {
		t.Fatal(err)
	}
	wrong := candidate
	wrong.Slots = []string{"super"}
	if _, _, err := runtimeLoginCandidateSlots(wrong, params); err == nil {
		t.Fatal("incomplete slot plan was accepted")
	}
	if _, err := manager.acknowledgeRuntimeLoginCandidate(ctx, control, admission, record, custody.Request, candidate); !errors.As(err, &unconfirmed) || unconfirmed.id != operation || !strings.Contains(err.Error(), "account_outcome_unknown") {
		t.Fatalf("Runtime did not receive candidate acknowledgement before rejecting the offline fixture: %v", err)
	}
	for name, want := range map[string]string{"super": super, "pass": password} {
		value, err := store.readSlot(params.Material.Backend, "rsrs", name+":"+params.Material.Account, true)
		if err != nil || value == nil || *value != want {
			t.Fatalf("verified native slot %s was not retained: %v", name, err)
		}
	}
}
