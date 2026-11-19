//go:build linux || windows || (darwin && cgo)

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ncruces/go-sqlite3"
)

const runtimeMaintenanceLimit = 16 * 1024

type runtimeLoginIntent struct {
	OperationID string
	User        string
	Server      string
}

type runtimeAccountRecord struct {
	Schema      int    `json:"schema"`
	OperationID string `json:"operation_id"`
	Root        string `json:"root"`
	Source      string `json:"source"`
	Register    bool   `json:"register"`
	User        string `json:"user"`
	Server      string `json:"addr"`
	Token       string `json:"maintenance_token"`
	Original    struct {
		Identity   runtimeAuthIdentity `json:"identity"`
		WasServing bool                `json:"was_serving"`
		StoreReady bool                `json:"store_ready"`
	} `json:"original"`
	Sent bool `json:"sent"`
}

type runtimeAccountAdmission struct {
	Marker, Record []byte
	Path           string
	Identity       runtimeAuthIdentity
}

func runtimeRegistrationCoordinator(root string, registration []byte) (string, error) {
	if registration == nil {
		return "host-" + controlDigest([]byte(root)), nil
	}
	var record struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(registration, &record) != nil || record.ID == "" {
		return "", errors.New("Invalid Runtime registration coordinator")
	}
	return record.ID, nil
}

// The caller keeps takeover through the account exchange. An interrupted
// transaction retains the marker for explicit recovery.
func (m *Manager) beginOfflineRuntimeAccountMaintenance(ctx context.Context, profilePath, issuer string, intent runtimeLoginIntent) (runtimeAccountAdmission, *sqlite3.Conn, error) {
	var admission runtimeAccountAdmission
	if !runtimeInstanceID.MatchString(intent.OperationID) || intent.OperationID[14] != '4' ||
		!strings.ContainsRune("89ab", rune(intent.OperationID[19])) || intent.User == "" || intent.Server == "" {
		return admission, nil, errors.New("Invalid Runtime login intent")
	}
	takeover, err := m.lockRuntimeTakeover(ctx)
	if err != nil {
		return admission, nil, err
	}
	admission, err = m.publishPausedRuntimeMaintenance(ctx, profilePath, issuer, intent)
	if err != nil {
		return admission, nil, errors.Join(err, takeover.Close())
	}
	return admission, takeover, nil
}

func (m *Manager) publishPausedRuntimeMaintenance(ctx context.Context, profilePath, issuer string, intent runtimeLoginIntent) (admission runtimeAccountAdmission, err error) {
	root, err := canonicalSessionDirectory(m.DataRoot)
	if err != nil {
		return admission, err
	}
	profile, err := canonicalSessionDirectory(profilePath)
	if err != nil {
		return admission, err
	}
	if issuer == "" {
		return admission, errors.New("Runtime account maintenance issuer is missing")
	}
	address, _, err := m.runtimeAccountEndpoint()
	if err != nil {
		return admission, err
	}
	if err := requireClosedRuntimePort(ctx, address); err != nil {
		return admission, err
	}
	installation, err := lockRuntimeDatabase(ctx, root, 0)
	if err != nil {
		return admission, fmt.Errorf("Runtime installation is still in use: %w", err)
	}
	defer func() { err = errors.Join(err, installation.Close()) }()
	if !samePath(profile, root) {
		account, err := lockRuntimeDatabase(ctx, profile, 0)
		if err != nil {
			return admission, fmt.Errorf("Runtime account database is still in use: %w", err)
		}
		defer func() { err = errors.Join(err, account.Close()) }()
	}
	directory := filepath.Join(root, "runtime", "autostart")
	if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return admission, err
	}
	actual, err := canonicalSessionDirectory(directory)
	if err != nil {
		return admission, err
	}
	if !samePath(actual, directory) {
		return admission, errors.New("Runtime account maintenance directory was redirected")
	}
	for _, name := range []string{"maintenance.json", "account-active.json", "key-control.json", "offline-keygen.json"} {
		_, err := os.Lstat(filepath.Join(directory, name))
		if err == nil {
			return admission, fmt.Errorf("Existing Runtime account operation requires recovery (%s)", name)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return admission, err
		}
	}
	registration, err := readCredentialConfig(filepath.Join(directory, "registration.json"), controlFrameLimit)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return admission, err
	}
	coordinator, err := runtimeRegistrationCoordinator(root, registration)
	if err != nil {
		return admission, err
	}
	birth, err := hostControlBirth(ctx)
	if err != nil {
		return admission, err
	}
	token, err := controlUUID()
	if err != nil {
		return admission, err
	}
	raw, err := json.Marshal(runtimeMaintenanceMarker{
		Schema: 1, ID: coordinator, Token: token, Phase: "Paused",
		ExpectedProfile: profile, ExpectedVersion: issuer, OwnerPID: os.Getpid(), OwnerBirth: birth,
	})
	if err != nil {
		return admission, err
	}
	if len(raw) > runtimeMaintenanceLimit {
		return admission, errors.New("Runtime account maintenance marker exceeds its protocol limit")
	}
	session, err := readCredentialConfig(filepath.Join(profile, "session.json"), controlFrameLimit)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return admission, err
	}
	identity, err := runtimeAuthIdentityOf(session)
	if err != nil {
		return admission, err
	}
	binding := runtimeAccountRecord{
		Schema: 1, Root: root, Source: profile, Token: token,
		OperationID: intent.OperationID, User: intent.User, Server: intent.Server,
	}
	binding.Original.Identity = identity
	record, err := json.Marshal(binding)
	if err != nil {
		return admission, err
	}
	results := filepath.Join(directory, "account-results")
	if err := os.Mkdir(results, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return admission, err
	}
	actual, err = canonicalSessionDirectory(results)
	if err != nil {
		return admission, err
	}
	if !samePath(actual, results) {
		return admission, errors.New("Runtime account results directory was redirected")
	}
	active, err := json.Marshal(map[string]any{"schema": 1, "operation_id": intent.OperationID})
	if err != nil {
		return admission, err
	}
	path := filepath.Join(results, intent.OperationID+".json")
	if err := writeRuntimeAccountRecord(path, record, nil); err != nil {
		return admission, err
	}
	if err := writeRuntimeAccountRecord(filepath.Join(directory, "account-active.json"), active, nil); err != nil {
		return admission, err
	}
	if err := writeRuntimeAccountRecord(filepath.Join(directory, "maintenance.json"), raw, nil); err != nil {
		return admission, err
	}
	return runtimeAccountAdmission{Marker: raw, Record: record, Path: path, Identity: identity}, nil
}

// The takeover lock serializes Client2 and CLI record writers.
func writeRuntimeAccountRecord(path string, data, expected []byte) error {
	if len(data) > runtimeMaintenanceLimit {
		return errors.New("Runtime account record exceeds its protocol limit")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".account-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, err = temp.Write(data)
	if err != nil {
		return errors.Join(err, temp.Close())
	}
	if err := errors.Join(temp.Sync(), temp.Close()); err != nil {
		return err
	}
	if expected == nil {
		return publishFreshRuntimeFile(temp.Name(), path)
	}
	original, err := readCredentialConfig(path, runtimeMaintenanceLimit)
	if err != nil {
		return err
	}
	if !bytes.Equal(original, expected) {
		return errors.New("Runtime account record changed before publication")
	}
	return publishSessionFile(temp.Name(), path)
}

func (a *runtimeAccountAdmission) markLoginSent() error {
	// Recover or query by the original ID if execution stops after this write.
	var record runtimeAccountRecord
	if json.Unmarshal(a.Record, &record) != nil || record.Sent {
		return errors.New("Runtime login operation was already sent or its original record is invalid")
	}
	record.Sent = true
	updated, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := writeRuntimeAccountRecord(a.Path, updated, a.Record); err != nil {
		return err
	}
	a.Record = updated
	return nil
}
