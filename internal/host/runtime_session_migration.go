//go:build windows || (darwin && cgo)

package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func (m *Manager) migrateNativeSession(ctx context.Context, owner HealthInfo, path string, original []byte, session runtimeSession, version uint64) ([]byte, error) {
	verifyOwner := func() error {
		if err := m.requireClearSessionRecovery(); err != nil {
			return err
		}
		current, _, err := m.Health(ctx)
		if err != nil {
			return err
		}
		if current != owner || owner.StoreReady {
			return errors.New("Runtime session owner changed during historical migration")
		}
		return verifyHostProcess(current)
	}
	if err := verifyOwner(); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(original, &fields); err != nil {
		return nil, errors.New("invalid historical session metadata")
	}
	if _, alreadyMigrated := fields["legacy_secret_backend"]; alreadyMigrated {
		return nil, errors.New("invalid historical credential backend")
	}
	if session.Backend != "" && session.Backend != "native" {
		return nil, errors.New("unsupported historical system credential backend")
	}
	alias := session.Alias
	if _, hasAlias := fields["keyring_account"]; hasAlias {
		if !sessionAlias.MatchString(alias) || len(alias) > 498 {
			return nil, errors.New("invalid historical credential alias")
		}
	} else {
		directory, err := canonicalSessionDirectory(filepath.Dir(path))
		if err != nil {
			return nil, err
		}
		hash := sha256.New()
		hash.Write([]byte(directory))
		hash.Write(original)
		alias = fmt.Sprintf("legacy-%x", hash.Sum(nil))
	}
	primary, factor, primarySlot := session.LegacyPass, session.LegacySecret, "pass"
	if version == 3 {
		primary, factor, primarySlot = session.LegacySuper, session.SecretKey, "super"
	}
	if primary == nil {
		account, err := session.nativeCredentialAccount()
		if err != nil {
			return nil, err
		}
		primary, err = readSystemCredential("rsrs", primarySlot+":"+account)
		if err != nil {
			return nil, err
		}
	}
	if primary == nil || factor == nil || *factor == "" || version == 3 && *primary == "" {
		return nil, errors.New("historical session factors are unavailable; original session retained")
	}
	slots := []struct{ slot, value string }{{primarySlot, *primary}, {"legacy-secret", *factor}}
	if version == 3 && session.LegacyPass != nil {
		slots = append(slots, struct{ slot, value string }{"pass", *session.LegacyPass})
	}
	for _, slot := range slots {
		if err := preserveSystemCredential("rsrs", slot.slot+":"+alias, slot.value); err != nil {
			return nil, err
		}
	}
	for _, field := range []string{"secret", "secret_key", "super", "pass"} {
		delete(fields, field)
	}
	fields["keyring_account"] = json.RawMessage(strconv.Quote(alias))
	fields["keyring_backend"] = json.RawMessage(`"native"`)
	fields["legacy_secret_backend"] = json.RawMessage(`"native"`)
	updated, err := json.MarshalIndent(fields, "", "  ")
	if err != nil || len(updated) > 65536 {
		return nil, errors.New("migrated session exceeds the Runtime limit")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".session-migration-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(temp.Name())
	_, writeErr := temp.Write(updated)
	if err := errors.Join(writeErr, temp.Sync(), temp.Close()); err != nil {
		return nil, err
	}
	if err := verifyOwner(); err != nil {
		return nil, err
	}
	stillOriginal, err := readCredentialConfig(path, 65536)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(stillOriginal, original) {
		return nil, errors.New("historical session changed before publication; verified credential slots retained")
	}
	if err := publishSessionFile(temp.Name(), path); err != nil {
		return nil, err
	}
	published, err := readCredentialConfig(path, 65536)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(published, updated) {
		return nil, errors.New("historical session changed after publication")
	}
	return published, nil
}

func (m *Manager) requireClearSessionRecovery() error {
	for _, name := range []string{"account-active.json", "key-control.json", "offline-keygen.json", "maintenance.json"} {
		_, err := os.Lstat(filepath.Join(m.DataRoot, "runtime", "autostart", name))
		if err == nil {
			return errors.New("historical session migration requires maintenance recovery first")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	bytes, err := readCredentialConfig(filepath.Join(m.DataRoot, ".login-recovery.json"), 65536)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var recovery struct {
		Version    int             `json:"schema_version"`
		Active     json.RawMessage `json:"active"`
		Operations []struct {
			Phase string `json:"phase"`
		} `json:"operations"`
	}
	if json.Unmarshal(bytes, &recovery) != nil || recovery.Version != 2 ||
		string(recovery.Active) != "null" || recovery.Operations == nil {
		return errors.New("historical session migration requires login recovery first")
	}
	for _, operation := range recovery.Operations {
		if operation.Phase != "committed" && operation.Phase != "rolled_back" {
			return errors.New("historical session migration requires login recovery first")
		}
	}
	return nil
}
