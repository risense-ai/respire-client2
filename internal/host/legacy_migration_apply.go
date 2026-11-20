//go:build linux || windows || (darwin && cgo)

package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

type legacyMigrationJournal struct {
	Operation  legacyMigrationOperation `json:"operation"`
	Runtime    InstalledRuntime         `json:"runtime"`
	Token      string                   `json:"token"`
	WasServing bool                     `json:"was_serving"`
	StoreReady bool                     `json:"store_ready"`
	Terminal   *legacyMigrationResult   `json:"terminal,omitempty"`
}

func (m *Manager) desktopLegacyMigration(ctx context.Context, id string, args []string, query bool) (raw json.RawMessage, err error) {
	opID := strings.TrimPrefix(id, "desktop-")
	if !hostOperationID.MatchString(opID) {
		return nil, errors.New("旧库迁移需要有效的请求 ID")
	}
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
	markerPath := filepath.Join(directory, "maintenance.json")
	journalPath := filepath.Join(directory, "migration-client2-"+opID+".json")
	var journal legacyMigrationJournal
	var journalBytes, markerBytes []byte
	unknown := func(cause error) (json.RawMessage, error) {
		if outcome, ok := cause.(*unconfirmedError); ok {
			cause = outcome.cause
		}
		return nil, &unconfirmedError{id: id, cause: cause}
	}
	if query {
		if err := profileDirectory(directory); err != nil {
			return unknown(err)
		}
		journalBytes, err = readCredentialConfig(journalPath, runtimeMaintenanceLimit)
		if err != nil || json.Unmarshal(journalBytes, &journal) != nil {
			return unknown(errors.Join(errors.New("无法读取旧库迁移原请求"), err))
		}
		op := journal.Operation
		fingerprint, err := op.fingerprint()
		if err != nil || op.Schema != 1 || op.OperationID != opID || op.Key != opID ||
			op.Root != root || op.Profile != profile || !reflect.DeepEqual(op.Identity, identity) ||
			!validHash(op.SourceID) || strings.ToLower(op.SourceID) != op.SourceID || !validMigrationAccount(op.Account) ||
			op.Fingerprint != fingerprint || journal.Runtime != info || !hostOperationID.MatchString(journal.Token) {
			return unknown(errors.New("旧库迁移原请求与当前安装或账户不匹配"))
		}
		if journal.Terminal != nil {
			if err := validateTerminalLegacyMigration(*journal.Terminal, op); err != nil {
				return unknown(err)
			}
		}
		markerBytes, err = m.claimLegacyMigrationMarker(ctx, journal, markerPath, address)
		if err != nil {
			return unknown(err)
		}
	} else {
		if len(args) != 2 {
			return nil, errors.New("请提供旧库来源 ID 和新账户名称")
		}
		sourceID, account := args[0], strings.TrimSpace(args[1])
		if !validHash(sourceID) || strings.ToLower(sourceID) != sourceID || !validMigrationAccount(account) {
			return nil, errors.New("旧库来源 ID 或新账户名称无效")
		}
		if err := m.requireNoDeviceMaintenance(); err != nil {
			return nil, err
		}
		mode, err := m.WorkspaceMode()
		if err != nil {
			return nil, err
		}
		if mode != "normal" {
			return nil, errors.New("旧库迁移需要开启工作区写入")
		}
		if m.ClientVersion == "" {
			return nil, errors.New("缺少 Client2 维护版本")
		}
		op := legacyMigrationOperation{Schema: 1, OperationID: opID, Key: opID, Root: root,
			Profile: profile, Identity: identity, SourceID: sourceID, Account: account}
		op.Fingerprint, err = op.fingerprint()
		if err != nil {
			return nil, err
		}
		journal.Operation, journal.Runtime = op, info
		journal.Token, err = controlUUID()
		if err != nil {
			return nil, err
		}
	}

	store := nativeRuntimeCredentialStore(ctx)
	var material legacyMigrationMaterial
	if journal.Terminal == nil {
		source, err := m.selectedLegacyMigrationSource(journal.Operation.SourceID)
		if err != nil {
			if query {
				return unknown(err)
			}
			return nil, err
		}
		material, err = readLegacyMigrationMaterial(source, store)
		if err != nil {
			if query {
				return unknown(err)
			}
			return nil, err
		}
	}
	if !query {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return nil, err
		}
		if err := profileDirectory(directory); err != nil {
			return nil, err
		}
		health, _, err := m.Health(ctx)
		if err == nil {
			if err := m.verifyDeviceConfigOwner(ctx, health, info, profile); err != nil {
				return nil, err
			}
			if health.ManagedID != "" {
				return nil, errors.New("请先由当前监督宿主停止 Runtime")
			}
			if err := m.stopDetached(ctx, health); err != nil {
				return nil, err
			}
			journal.WasServing, journal.StoreReady = true, health.StoreReady
		} else if !IsUnavailable(err) {
			return nil, err
		}
		birth, err := hostControlBirth(ctx)
		if err != nil {
			return nil, err
		}
		err = m.admitDeviceConfig(ctx, root, profile, profile, address, port, true, func(coordinator string) error {
			marker := runtimeMaintenanceMarker{Schema: 1, ID: coordinator, Token: journal.Token,
				Phase: "Paused", ExpectedProfile: profile, ExpectedVersion: m.ClientVersion, OwnerPID: os.Getpid(), OwnerBirth: birth}
			journalBytes, err = json.Marshal(journal)
			if err != nil {
				return err
			}
			if err := writeRuntimeAccountRecord(journalPath, journalBytes, nil); err != nil {
				return err
			}
			markerBytes, err = json.Marshal(marker)
			if err != nil {
				return err
			}
			return writeRuntimeAccountRecord(markerPath, markerBytes, nil)
		})
		if err != nil {
			return unknown(err)
		}
	}
	if journal.Terminal == nil {
		image := filepath.Join(m.runtimeDir(info.Version), runtimeBinary())
		control, err := m.prepareRuntimeAccountControl(ctx, image, info.SHA256, profile, identity.AccountID, markerBytes)
		if err != nil {
			return unknown(err)
		}
		method := migrateLegacyMethod
		if query {
			method = migrateResultMethod
		}
		result, err := m.exchangeLegacyMigration(ctx, control, journal.Operation, method, &material)
		if err != nil {
			return unknown(err)
		}
		if result.Outcome == "credentials_required" {
			if err := material.retainSelected(result.Details, store); err != nil {
				return unknown(err)
			}
			control, err = m.prepareRuntimeAccountControl(ctx, image, info.SHA256, profile, identity.AccountID, markerBytes)
			if err != nil {
				return unknown(err)
			}
			result, err = m.exchangeLegacyMigration(ctx, control, journal.Operation, migrateResultMethod, &material)
			if err != nil || result.Outcome == "credentials_required" {
				return unknown(errors.Join(errors.New("旧库迁移未确认钥匙串交付结果"), err))
			}
		}
		journal.Terminal = &result
		next, err := json.Marshal(journal)
		if err != nil {
			return unknown(err)
		}
		if err := writeRuntimeAccountRecord(journalPath, next, journalBytes); err != nil {
			return unknown(err)
		}
	}
	if len(markerBytes) != 0 {
		if err := removeOwnedRuntimeAccountRecord(markerPath, markerBytes); err != nil {
			return unknown(err)
		}
	}
	if journal.Terminal.Outcome != "schema_rejected" {
		raw, err = json.Marshal(map[string]any{"command": "migrate", "status": "ok",
			"summary": journal.Terminal.Details, "details": journal.Terminal.Details})
		if err != nil {
			return unknown(err)
		}
	}
	if journal.WasServing {
		h, _, err := m.Health(ctx)
		if IsUnavailable(err) {
			h, err = m.startDetachedLocked(ctx, info)
		}
		if err == nil {
			err = m.verifyDeviceConfigOwner(ctx, h, info, profile)
		}
		if err == nil && journal.StoreReady && !h.StoreReady {
			err = m.unlockVerifiedRuntime(ctx, h, store, identity.SessionDigest, false)
		}
		if err != nil {
			return raw, fmt.Errorf("迁移已有最终回执，但原库服务未能恢复: %w", err)
		}
	}
	if journal.Terminal.Outcome == "schema_rejected" {
		return nil, errors.New("旧库格式不兼容，原数据库已保留")
	}
	return raw, nil
}

func (m *Manager) claimLegacyMigrationMarker(ctx context.Context, journal legacyMigrationJournal, path, address string) (raw []byte, err error) {
	raw, err = readCredentialConfig(path, runtimeMaintenanceLimit)
	if errors.Is(err, os.ErrNotExist) && journal.Terminal != nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var marker runtimeMaintenanceMarker
	if json.Unmarshal(raw, &marker) != nil || marker.Schema != 1 || marker.Phase != "Paused" ||
		marker.Token != journal.Token || marker.ExpectedProfile != journal.Operation.Profile ||
		marker.ID == "" || marker.OwnerPID <= 0 || marker.OwnerBirth == "" {
		return nil, errors.New("原旧库迁移维护记录不匹配")
	}
	root, profile := journal.Operation.Root, journal.Operation.Profile
	locks := []string{filepath.Join(root, "runtime"), root}
	if !samePath(profile, root) {
		locks = append(locks, profile)
	}
	for _, directory := range locks {
		lock, lockErr := lockRuntimeDatabase(ctx, directory, 0)
		if lockErr != nil {
			return nil, lockErr
		}
		defer func() { err = errors.Join(err, lock.Close()) }()
	}
	if err := requireStoppedM3Runtime(ctx, root, address); err != nil {
		return nil, err
	}
	birth, err := hostControlBirth(ctx)
	if err != nil {
		return nil, err
	}
	if marker.OwnerPID == os.Getpid() && marker.OwnerBirth == birth {
		return raw, nil
	}
	previous, err := runtimeControlProcessBirth(ctx, marker.OwnerPID)
	if err != nil || previous == marker.OwnerBirth {
		return nil, errors.Join(errors.New("原旧库迁移协调器退出仍未确认"), err)
	}
	marker.OwnerPID, marker.OwnerBirth = os.Getpid(), birth
	data, err := json.Marshal(marker)
	if err != nil {
		return nil, err
	}
	return data, writeRuntimeAccountRecord(path, data, raw)
}

func validMigrationAccount(name string) bool {
	return name != "" && name != "main" && !strings.ContainsFunc(name, func(ch rune) bool {
		return !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_')
	})
}

func (m *Manager) selectedLegacyMigrationSource(id string) (legacyMigrationProfile, error) {
	rows, err := m.legacyMigrationProfiles()
	if err != nil {
		return legacyMigrationProfile{}, err
	}
	for _, row := range rows {
		if row.SourceID == id {
			return row, nil
		}
	}
	return legacyMigrationProfile{}, errors.New("旧库来源已不存在或不符合迁移条件")
}
