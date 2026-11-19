//go:build linux || windows || (darwin && cgo)

package host

import "testing"

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
