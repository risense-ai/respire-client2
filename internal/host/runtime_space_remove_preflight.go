package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

func checkOtherSpaceRemovalMaintenance(directory string) error {
	for _, name := range []string{"account-active.json", "key-control.json", "offline-keygen.json",
		"space-client2.json", filepath.Join("profile-removals", "pending.json")} {
		path := filepath.Join(directory, name)
		if err := checkModelUninstallPath(path); err != nil {
			return err
		}
		if err := requireAbsentRuntimeAccountFile(path); err != nil {
			return err
		}
	}
	return nil
}

func spaceRemovalRegistration(root, directory string, port uint64) (string, string, error) {
	path := filepath.Join(directory, "registration.json")
	if err := checkModelUninstallPath(path); err != nil {
		return "", "", err
	}
	raw, err := readCredentialConfig(path, controlFrameLimit)
	if errors.Is(err, os.ErrNotExist) {
		id, err := runtimeRegistrationCoordinator(root, nil)
		return "", id, err
	}
	if err != nil {
		return "", "", err
	}
	var registration struct {
		Enabled         *bool
		ManuallyStopped *bool `json:"manually_stopped"`
		Maintenance     *bool
	}
	id := deviceConfigRegistrationID(raw, root, port)
	if id == "" || json.Unmarshal(raw, &registration) != nil ||
		registration.Enabled == nil || *registration.Enabled ||
		registration.ManuallyStopped == nil || *registration.ManuallyStopped ||
		registration.Maintenance == nil || *registration.Maintenance {
		return "", "", errors.New("空间移除无法确认注册或服务状态，请使用原宿主")
	}
	return controlDigest(raw), id, nil
}

func inspectSpaceRemovalTarget(path string) (os.FileInfo, error) {
	if err := checkModelUninstallPath(path); err != nil {
		return nil, err
	}
	if err := checkRuntimeAccountDirectory(path); err != nil {
		return nil, err
	}
	canonical, err := canonicalSessionDirectory(path)
	if err != nil || canonical != path {
		return nil, errors.Join(errors.New("空间移除目标路径不精确"), err)
	}
	if err := checkModelUninstallPath(filepath.Join(path, "session.json")); err != nil {
		return nil, err
	}
	return os.Lstat(path)
}

func verifySpaceRemovalTarget(path string, original os.FileInfo, identity runtimeAuthIdentity) error {
	now, err := inspectSpaceRemovalTarget(path)
	if err != nil || !os.SameFile(original, now) {
		return errors.Join(errors.New("空间移除目标目录已改变"), err)
	}
	current, err := deviceProfileIdentity(path)
	if err != nil || !reflect.DeepEqual(identity, current) {
		return errors.Join(errors.New("空间移除目标会话身份已改变"), err)
	}
	return nil
}

// The child must acquire the same locks itself. Keep takeover and the Paused
// marker across the private exchange after the host preflight releases them.
func withStoppedSpaceRemoval(ctx context.Context, root, profile, target, address string, run func() error) (err error) {
	seen := map[string]bool{}
	for _, dir := range []string{filepath.Join(root, "runtime"), root, profile, target} {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		for _, file := range []string{"lock.db", "lock.db-journal", "lock.db-wal", "lock.db-shm"} {
			path := filepath.Join(dir, file)
			if err := checkModelUninstallPath(path); err != nil {
				return err
			}
			if _, err := profileFile(path); err != nil {
				return err
			}
		}
		lock, err := lockRuntimeDatabase(ctx, dir, 0)
		if err != nil {
			return fmt.Errorf("空间移除无法独占 Runtime 或涉及的数据目录 (%s): %w", dir, err)
		}
		defer func() { err = errors.Join(err, lock.Close()) }()
	}
	if err := requireClosedRuntimePort(ctx, address); err != nil {
		return err
	}
	if err := requireAbsentRuntimeAccountFile(filepath.Join(root, "runtime", "endpoint.json")); err != nil {
		return err
	}
	return run()
}
