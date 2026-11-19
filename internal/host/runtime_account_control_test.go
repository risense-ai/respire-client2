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
	if err := endpoint.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err := manager.lockRuntimeTakeover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	directory := filepath.Join(root, "runtime", "autostart")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	birth, err := hostControlBirth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	const operation = "00000000-0000-4000-8000-000000000001"
	path := filepath.Join(directory, "maintenance.json")
	marker, err := json.Marshal(runtimeMaintenanceMarker{
		Schema: 1, ID: "host-" + controlDigest([]byte(root)), Token: operation,
		Phase: "Paused", ExpectedProfile: root, ExpectedVersion: "2.0.0-dev.20",
		OwnerPID: os.Getpid(), OwnerBirth: birth,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, marker, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.prepareRuntimeAccountControl(context.Background(), image, hash, root, nil, nil); err == nil {
		t.Fatal("Client2 accepted a maintenance marker without the original ownership bytes")
	}
	changed := append(bytes.Clone(marker), '\n')
	if _, err := manager.prepareRuntimeAccountControl(context.Background(), image, hash, root, nil, changed); err == nil {
		t.Fatal("Client2 accepted stale maintenance bytes")
	}
	control, err := manager.prepareRuntimeAccountControl(context.Background(), image, hash, root, nil, marker)
	if err != nil {
		t.Fatal(err)
	}
	backend := "native"
	if runtime.GOOS == "linux" {
		// Windows and Darwin use native; Linux uses its Secret Service.
		backend = "secret_service"
	}
	params, err := json.Marshal(map[string]any{
		"schema_version": 1, "binding": control.binding, "identity": runtimeAuthIdentity{Kind: "absent"},
		"addr": "https://respire.example.invalid", "user": "isolated-client2",
		"authorization": map[string]any{"kind": "password", "password": "isolated fixture"},
		"material":      map[string]any{"keyring_account": "login-" + strings.ReplaceAll(operation, "-", ""), "keyring_backend": backend},
		// Runtime rejects these contradictory options before it can contact a server.
		"continuation": map[string]any{"migrate_local": true, "migrate_vault": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestID, err := controlUUID()
	if err != nil {
		t.Fatal(err)
	}
	reply, err := manager.exchangeRuntimeControl(context.Background(), control.image, control.port, control.grant,
		runtimeControlRequest{V: 1, ID: requestID, Method: "control.auth.login", Args: []string{string(params)},
			OperationID: operation, IdempotencyKey: operation})
	var unconfirmed *unconfirmedError
	if !errors.As(err, &unconfirmed) || unconfirmed.id != operation ||
		reply.Code != "account_outcome_unknown" || !strings.Contains(reply.Error, "local import and explicit vault migration are separate operations") {
		t.Fatalf("Runtime did not accept the exact Paused authorization and reject invalid login intent: %v (%s)", err, reply.Error)
	}
	if _, err := os.Stat(filepath.Join(root, ".login-recovery.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid intent should not create a login journal: %v", err)
	}
}
