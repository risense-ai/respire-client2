//go:build linux || windows || (darwin && cgo)

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
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
	const operation = "00000000-0000-4000-8000-000000000001"
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
	authorization := runtimeLoginAuthorization{Kind: "password", Password: &password}
	// Contradictory options exercise admission without contacting a server.
	continuation := runtimeLoginContinuation{MigrateLocal: true, MigrateVault: true}
	session := filepath.Join(root, "session.json")
	if err := os.WriteFile(session, []byte(`{"user":"changed-after-admission"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.prepareInitialRuntimeLogin(ctx, image, hash, admission, authorization, continuation, nil, nil, nil); err == nil {
		t.Fatal("changed profile admitted the original login")
	}
	if err := os.Remove(session); err != nil {
		t.Fatal(err)
	}
	control, params, err := manager.prepareInitialRuntimeLogin(ctx, image, hash, admission, authorization, continuation, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	changedIntent := params
	changedIntent.User = "different-user"
	if _, err := manager.sendInitialRuntimeLogin(ctx, control, changedIntent, &admission); err == nil {
		t.Fatal("changed account intent was sent")
	}
	reply, err := manager.sendInitialRuntimeLogin(ctx, control, params, &admission)
	if _, duplicateErr := manager.sendInitialRuntimeLogin(ctx, control, params, &admission); duplicateErr == nil {
		t.Fatal("sent operation was readmitted")
	}
	var unconfirmed *unconfirmedError
	if !errors.As(err, &unconfirmed) || unconfirmed.id != operation ||
		reply.Code != "account_outcome_unknown" || !strings.Contains(reply.Error, "local import and explicit vault migration are separate operations") {
		t.Fatalf("Runtime did not accept the exact Paused authorization and reject invalid login intent: %v (%s)", err, reply.Error)
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
}
