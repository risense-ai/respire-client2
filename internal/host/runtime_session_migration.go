//go:build linux || windows || (darwin && cgo)

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

func (m *Manager) migrateHostSession(ctx context.Context, owner HealthInfo, path string, original []byte, session runtimeSession, version uint64, store hostCredentialStore) ([]byte, error) {
	verifyOwner := func() error {
		current, _, err := m.Health(ctx)
		if err != nil {
			return err
		}
		if current != owner || owner.StoreReady {
			return errors.New("Runtime session owner changed during historical migration")
		}
		if err := verifyHostProcess(current); err != nil {
			return err
		}
		return m.requireClearSessionRecovery(ctx, owner, path, original)
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
	if session.Backend != "" && !store.supports(session.Backend) {
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
		account, err := session.credentialAccount()
		if err != nil {
			return nil, err
		}
		primary, err = store.readSlot(session.Backend, "rsrs", primarySlot+":"+account, version == 1)
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
		if err := store.preserve("rsrs", slot.slot+":"+alias, slot.value); err != nil {
			return nil, err
		}
	}
	for _, field := range []string{"secret", "secret_key", "super", "pass"} {
		delete(fields, field)
	}
	fields["keyring_account"] = json.RawMessage(strconv.Quote(alias))
	fields["keyring_backend"] = json.RawMessage(strconv.Quote(store.backends[0]))
	fields["legacy_secret_backend"] = json.RawMessage(strconv.Quote(store.backends[0]))
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

func (m *Manager) requireClearSessionRecovery(ctx context.Context, owner HealthInfo, path string, original []byte) error {
	for _, name := range []string{"account-active.json", "key-control.json", "offline-keygen.json", "maintenance.json"} {
		_, err := os.Lstat(filepath.Join(m.DataRoot, "runtime", "autostart", name))
		if err == nil {
			return errors.New("historical session migration requires maintenance recovery first")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	data, err := readCredentialConfig(filepath.Join(m.DataRoot, ".login-recovery.json"), controlFrameLimit)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	// A remote operation can still need its original session even without an active local recovery.
	var journal struct {
		Operations []struct {
			Phase string `json:"phase"`
		} `json:"operations"`
	}
	if json.Unmarshal(data, &journal) != nil {
		return errors.New("Invalid historical login recovery journal")
	}
	for _, operation := range journal.Operations {
		if operation.Phase != "committed" && operation.Phase != "rolled_back" {
			return errors.New("Historical session has an unfinished login operation")
		}
	}
	return m.readRuntimeLoginRecovery(ctx, owner, path, original)
}
