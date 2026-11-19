//go:build linux || windows || (darwin && cgo)

package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ncruces/go-sqlite3"
)

const runtimeMaintenanceLimit = 16 * 1024

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
func (m *Manager) beginOfflineRuntimeAccountMaintenance(ctx context.Context, profilePath, issuer string) ([]byte, *sqlite3.Conn, error) {
	takeover, err := m.lockRuntimeTakeover(ctx)
	if err != nil {
		return nil, nil, err
	}
	marker, err := m.publishPausedRuntimeMaintenance(ctx, profilePath, issuer)
	if err != nil {
		return nil, nil, errors.Join(err, takeover.Close())
	}
	return marker, takeover, nil
}

func (m *Manager) publishPausedRuntimeMaintenance(ctx context.Context, profilePath, issuer string) (raw []byte, err error) {
	root, err := canonicalSessionDirectory(m.DataRoot)
	if err != nil {
		return nil, err
	}
	profile, err := canonicalSessionDirectory(profilePath)
	if err != nil {
		return nil, err
	}
	if issuer == "" {
		return nil, errors.New("Runtime account maintenance issuer is missing")
	}
	address, _, err := m.runtimeAccountEndpoint()
	if err != nil {
		return nil, err
	}
	if err := requireClosedRuntimePort(ctx, address); err != nil {
		return nil, err
	}
	installation, err := lockRuntimeDatabase(ctx, root, 0)
	if err != nil {
		return nil, fmt.Errorf("Runtime installation is still in use: %w", err)
	}
	defer func() { err = errors.Join(err, installation.Close()) }()
	if !samePath(profile, root) {
		account, err := lockRuntimeDatabase(ctx, profile, 0)
		if err != nil {
			return nil, fmt.Errorf("Runtime account database is still in use: %w", err)
		}
		defer func() { err = errors.Join(err, account.Close()) }()
	}
	directory := filepath.Join(root, "runtime", "autostart")
	if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	actual, err := canonicalSessionDirectory(directory)
	if err != nil {
		return nil, err
	}
	if !samePath(actual, directory) {
		return nil, errors.New("Runtime account maintenance directory was redirected")
	}
	for _, name := range []string{"maintenance.json", "account-active.json", "key-control.json", "offline-keygen.json"} {
		_, err := os.Lstat(filepath.Join(directory, name))
		if err == nil {
			return nil, fmt.Errorf("Existing Runtime account operation requires recovery (%s)", name)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	registration, err := readCredentialConfig(filepath.Join(directory, "registration.json"), controlFrameLimit)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	coordinator, err := runtimeRegistrationCoordinator(root, registration)
	if err != nil {
		return nil, err
	}
	birth, err := hostControlBirth(ctx)
	if err != nil {
		return nil, err
	}
	token, err := controlUUID()
	if err != nil {
		return nil, err
	}
	raw, err = json.Marshal(runtimeMaintenanceMarker{
		Schema: 1, ID: coordinator, Token: token, Phase: "Paused",
		ExpectedProfile: profile, ExpectedVersion: issuer, OwnerPID: os.Getpid(), OwnerBirth: birth,
	})
	if err != nil {
		return nil, err
	}
	if len(raw) > runtimeMaintenanceLimit {
		return nil, errors.New("Runtime account maintenance marker exceeds its protocol limit")
	}
	temp, err := os.CreateTemp(directory, ".maintenance-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(temp.Name())
	_, err = temp.Write(raw)
	if err != nil {
		return nil, errors.Join(err, temp.Close())
	}
	if err := errors.Join(temp.Sync(), temp.Close()); err != nil {
		return nil, err
	}
	if err := publishFreshRuntimeFile(temp.Name(), filepath.Join(directory, "maintenance.json")); err != nil {
		return nil, fmt.Errorf("Publish Runtime account maintenance: %w", err)
	}
	return raw, nil
}
