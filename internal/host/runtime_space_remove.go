package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

type spaceRemovalRecord struct {
	Operation               spaceRemovalOperation
	Runtime                 InstalledRuntime
	Token, RegistrationHash string
	WasServing, StoreReady  bool
	Receipt                 *spaceRemovalReceipt
}

func (m *Manager) removeDesktopSpace(ctx context.Context, id, name string, query bool) (result json.RawMessage, err error) {
	op := strings.TrimPrefix(id, "desktop-")
	name = strings.TrimSpace(name)
	if !hostOperationID.MatchString(op) || !query && (!desktopSpaceName.MatchString(name) ||
		strings.EqualFold(name, "main") || !filepath.IsLocal(name)) {
		return nil, errors.New("空间移除请求 ID 或空间名称无效")
	}
	uncertain := query
	defer func() {
		if err != nil && uncertain {
			err = &unconfirmedError{id: id, cause: err}
		}
	}()
	takeover, err := m.lockRuntimeTakeover(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, takeover.Close()) }()
	root, err := canonicalSessionDirectory(m.DataRoot)
	if err != nil {
		return nil, err
	}
	profile, err := selectedRuntimeLoginProfile(root)
	if err != nil {
		return nil, err
	}
	if err := checkModelUninstallPath(filepath.Join(profile, "session.json")); err != nil {
		return nil, err
	}
	identity, err := deviceProfileIdentity(profile)
	if err != nil {
		return nil, err
	}
	info, err := m.Installed()
	if err != nil {
		return nil, err
	}
	if err := m.verifyInstalled(info); err != nil {
		return nil, err
	}
	address, port, err := m.runtimeAccountEndpoint()
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(root, "runtime", "autostart")
	pending := filepath.Join(directory, "profile-remove-client2.json")
	archived := filepath.Join(directory, "profile-remove-client2-"+op+".json")
	markerPath := filepath.Join(directory, "maintenance.json")
	for _, path := range []string{pending, archived, markerPath} {
		if err := checkModelUninstallPath(path); err != nil {
			return nil, err
		}
	}
	var record spaceRemovalRecord
	var recordBytes, marker []byte
	if query {
		completed, readErr := readCredentialConfig(archived, runtimeMaintenanceLimit)
		if readErr == nil {
			record, err = parseSpaceRemovalRecord(completed, op, root, profile, identity)
			if err != nil || record.Receipt == nil {
				return nil, errors.Join(errors.New("无法核实已归档的空间移除回执"), err)
			}
			return renderSpaceRemoval(record.Operation)
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return nil, readErr
		}
		recordBytes, err = readCredentialConfig(pending, runtimeMaintenanceLimit)
		if err != nil {
			return nil, errors.Join(errors.New("缺少原空间移除请求，只能核对原操作"), err)
		}
		record, err = parseSpaceRemovalRecord(recordBytes, op, root, profile, identity)
		if err != nil {
			return nil, err
		}
		if info != record.Runtime {
			return nil, errors.New("请恢复原空间移除操作使用的 Runtime 版本")
		}
	} else {
		if err := requireAbsentRuntimeAccountFile(pending); err != nil {
			return nil, err
		}
		if err := requireAbsentRuntimeAccountFile(archived); err != nil {
			return nil, err
		}
		if err := m.requireNoDeviceMaintenance(); err != nil {
			return nil, err
		}
	}
	if err := checkOtherSpaceRemovalMaintenance(directory); err != nil {
		return nil, err
	}
	registration, coordinator, err := spaceRemovalRegistration(root, directory, port)
	if err != nil {
		return nil, err
	}
	if query && record.RegistrationHash != registration {
		return nil, errors.New("空间移除事务的 Runtime 注册已改变")
	}
	var target string
	var targetInfo os.FileInfo
	if !query {
		if m.ClientVersion == "" {
			return nil, errors.New("缺少 Client2 空间移除协调器版本")
		}
		target = filepath.Join(root, "accounts", name)
		relative, relErr := filepath.Rel(target, profile)
		if relErr != nil || filepath.IsLocal(relative) || samePath(target, root) {
			return nil, errors.New("请先切换到另一个空间，当前空间和主库不可移除")
		}
		targetInfo, err = inspectSpaceRemovalTarget(target)
		if err != nil {
			return nil, err
		}
		targetIdentity, err := deviceProfileIdentity(target)
		if err != nil {
			return nil, err
		}
		if identity.AccountID != nil && targetIdentity.AccountID != nil && *identity.AccountID == *targetIdentity.AccountID ||
			identity.OperationID != "" && identity.OperationID == targetIdentity.OperationID {
			return nil, errors.New("目标目录与当前空间属于同一账户身份，请核对后使用原宿主处理")
		}
		record = spaceRemovalRecord{
			Operation: spaceRemovalOperation{Schema: 1, ID: op, Key: op, Root: root, Profile: profile,
				Identity: identity, Name: name, TargetIdentity: targetIdentity},
			Runtime: info, RegistrationHash: registration,
		}
		record.Operation.Fingerprint, err = record.Operation.fingerprint()
		if err != nil {
			return nil, err
		}
		record.Token, err = controlUUID()
		if err != nil {
			return nil, err
		}
		h, _, healthErr := m.Health(ctx)
		if healthErr == nil {
			if h.ManagedID != "" {
				return nil, errors.New("请先由当前监督宿主停机")
			}
			if err := m.verifyDeviceConfigOwner(ctx, h, info, profile); err != nil {
				return nil, err
			}
			if err := m.stopDetached(ctx, h); err != nil {
				return nil, err
			}
			record.WasServing, record.StoreReady = true, h.StoreReady
		} else if !IsUnavailable(healthErr) {
			return nil, healthErr
		}
	}
	unchanged := func() error {
		current, err := m.Installed()
		if err != nil || current != info {
			return errors.Join(errors.New("空间移除事务的 Runtime 选用版本已改变"), err)
		}
		if err := m.verifyInstalled(info); err != nil {
			return err
		}
		currentProfile, err := selectedRuntimeLoginProfile(root)
		if err != nil || currentProfile != profile {
			return errors.Join(errors.New("空间移除事务的原账户选择已改变"), err)
		}
		if err := checkModelUninstallPath(filepath.Join(profile, "session.json")); err != nil {
			return err
		}
		currentIdentity, err := deviceProfileIdentity(profile)
		if err != nil || !reflect.DeepEqual(currentIdentity, identity) {
			return errors.Join(errors.New("空间移除事务的原账户身份已改变"), err)
		}
		currentRegistration, _, err := spaceRemovalRegistration(root, directory, port)
		if err != nil || currentRegistration != registration {
			return errors.Join(errors.New("空间移除事务的 Runtime 注册已改变"), err)
		}
		return checkOtherSpaceRemovalMaintenance(directory)
	}
	if !query {
		err = withStoppedSpaceRemoval(ctx, root, profile, target, address, func() error {
			if err := unchanged(); err != nil {
				return err
			}
			if err := verifySpaceRemovalTarget(target, targetInfo, record.Operation.TargetIdentity); err != nil {
				return err
			}
			if err := m.requireNoDeviceMaintenance(); err != nil {
				return err
			}
			if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			if err := checkRuntimeAccountDirectory(directory); err != nil {
				return err
			}
			birth, err := hostControlBirth(ctx)
			if err != nil {
				return err
			}
			marker, err = json.Marshal(runtimeMaintenanceMarker{Schema: 1, ID: coordinator, Token: record.Token, Phase: "Paused",
				ExpectedProfile: profile, ExpectedVersion: m.ClientVersion, OwnerPID: os.Getpid(), OwnerBirth: birth})
			if err != nil {
				return err
			}
			recordBytes, err = json.Marshal(record)
			if err != nil {
				return err
			}
			uncertain = true
			if err := writeRuntimeAccountRecord(pending, recordBytes, nil); err != nil {
				return err
			}
			return writeRuntimeAccountRecord(markerPath, marker, nil)
		})
		if err != nil {
			return nil, err
		}
	} else {
		_, statErr := os.Lstat(markerPath)
		if statErr == nil || record.Receipt == nil {
			err = withStoppedSpaceRemoval(ctx, root, profile, "", address, func() error {
				if err := unchanged(); err != nil {
					return err
				}
				marker, err = claimPausedRuntimeMarker(ctx, markerPath, root, profile, record.Token)
				return err
			})
			if err != nil {
				return nil, err
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, statErr
		}
	}
	if record.Receipt == nil {
		if err := unchanged(); err != nil {
			return nil, err
		}
		if !query {
			if err := verifySpaceRemovalTarget(target, targetInfo, record.Operation.TargetIdentity); err != nil {
				return nil, err
			}
		}
		current, err := readCredentialConfig(pending, runtimeMaintenanceLimit)
		if err != nil || !bytes.Equal(current, recordBytes) {
			return nil, errors.Join(errors.New("原空间移除请求在发送前已改变"), err)
		}
		receipt, err := m.exchangeSpaceRemoval(ctx, record, marker, query)
		if err != nil {
			return nil, err
		}
		if !query {
			if err := requireAbsentRuntimeAccountFile(target); err != nil {
				return nil, err
			}
		}
		record.Receipt = &receipt
		committed, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		if err := writeRuntimeAccountRecord(pending, committed, recordBytes); err != nil {
			return nil, err
		}
		recordBytes = committed
	}
	if err := unchanged(); err != nil {
		return nil, err
	}
	if marker != nil {
		if err := removeOwnedRuntimeAccountRecord(markerPath, marker); err != nil {
			return nil, err
		}
	} else if err := requireAbsentRuntimeAccountFile(markerPath); err != nil {
		return nil, err
	}
	if record.WasServing {
		if err := m.restoreSpaceRemovalOwner(ctx, record); err != nil {
			return nil, fmt.Errorf("空间移除已有完成回执，原 Runtime 恢复失败: %w", err)
		}
	}
	if err := unchanged(); err != nil {
		return nil, err
	}
	if err := requireAbsentRuntimeAccountFile(archived); err != nil {
		return nil, err
	}
	current, err := readCredentialConfig(pending, runtimeMaintenanceLimit)
	if err != nil || !bytes.Equal(current, recordBytes) {
		return nil, errors.Join(errors.New("空间移除记录在归档前已改变"), err)
	}
	if err := publishSessionFile(pending, archived); err != nil {
		return nil, err
	}
	return renderSpaceRemoval(record.Operation)
}

func parseSpaceRemovalRecord(raw []byte, op, root, profile string, identity runtimeAuthIdentity) (spaceRemovalRecord, error) {
	var record spaceRemovalRecord
	if json.Unmarshal(raw, &record) != nil {
		return record, errors.New("原空间移除记录无效")
	}
	o := record.Operation
	fingerprint, err := o.fingerprint()
	if err != nil || o.Schema != 1 || o.ID != op || o.Key != op || o.Root != root ||
		o.Profile != profile || !reflect.DeepEqual(o.Identity, identity) || !desktopSpaceName.MatchString(o.Name) ||
		strings.EqualFold(o.Name, "main") || !filepath.IsLocal(o.Name) || fingerprint != o.Fingerprint ||
		!hostOperationID.MatchString(record.Token) || (record.StoreReady && !record.WasServing) ||
		(record.Receipt != nil && !o.matchesReceipt(*record.Receipt)) {
		return record, errors.Join(errors.New("原空间移除事务身份或回执已改变"), err)
	}
	return record, nil
}

func (m *Manager) restoreSpaceRemovalOwner(ctx context.Context, record spaceRemovalRecord) error {
	profile := record.Operation.Profile
	h, err := m.startDetachedLocked(ctx, record.Runtime)
	if err != nil {
		return err
	}
	if err := m.verifyDeviceConfigOwner(ctx, h, record.Runtime, profile); err != nil {
		return err
	}
	if h.ManagedID != "" {
		return errors.New("恢复期间发现其他宿主监督的 Runtime")
	}
	if record.StoreReady && !h.StoreReady {
		if err := m.unlockVerifiedRuntime(ctx, h, nativeRuntimeCredentialStore(ctx), record.Operation.Identity.SessionDigest, false); err != nil {
			return err
		}
		h, _, err = m.Health(ctx)
		if err != nil {
			return err
		}
		if err := m.verifyDeviceConfigOwner(ctx, h, record.Runtime, profile); err != nil {
			return err
		}
		if !h.StoreReady {
			return errors.New("原 Runtime 存储未恢复就绪")
		}
	}
	return nil
}
