//go:build linux || windows || (darwin && cgo)

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
)

func (m *Manager) queryOfflineKeygen(ctx context.Context, binding runtimeOfflineKeygenRecord,
	activePath, markerPath, markerArchive, receiptPath string, store hostCredentialStore) (json.RawMessage, string, error) {
	data, err := readCredentialConfig(activePath, runtimeMaintenanceLimit)
	if err != nil {
		return nil, "", err
	}
	record, err := binding.readOriginal(data)
	if err != nil {
		return nil, "", err
	}
	if record.Stage == "sent" {
		marker, err := m.claimOfflineKeygenMarker(ctx, &record, &data, activePath, markerPath)
		if err != nil {
			return nil, "", err
		}
		identity, err := keygenIdentity(record.Profile)
		if err != nil {
			return nil, "", err
		}
		if identity.Kind != "absent" && (identity.Kind != "unbound" || identity.OperationID != record.Operation) {
			return nil, "", errors.New("当前身份不属于原离线初始化")
		}
		var material runtimeRegisterMaterial
		if err := json.Unmarshal(record.Material, &material); err != nil {
			return nil, "", err
		}
		if material.Super != "" || material.Account != offlineKeygenAlias(record.Operation) || material.Backend != runtimeLoginBackend() {
			return nil, "", errors.New("原离线初始化的凭据绑定已改变")
		}
		secret, err := store.readSlot(material.Backend, "rsrs", "super:"+material.Account, false)
		if err != nil {
			return nil, "", err
		}
		if secret == nil || *secret == "" {
			return nil, "", errors.New("找不到原离线初始化的系统凭据槽")
		}
		material.Super = *secret
		digest, err := m.exchangeOfflineKeygen(ctx, record, marker, material, identity, "control.auth.result")
		if err != nil {
			return nil, "", err
		}
		record.Session = &digest
	}
	if err := m.finishOfflineKeygen(ctx, record, data, activePath, markerPath, markerArchive, receiptPath); err != nil {
		return nil, "", err
	}
	return offlineKeygenEnvelope(record)
}

func (m *Manager) claimOfflineKeygenMarker(ctx context.Context, record *runtimeOfflineKeygenRecord,
	data *[]byte, activePath, markerPath string) (marker []byte, err error) {
	err = m.withStoppedKeygenLibrary(ctx, *record, func() error {
		original, err := readCredentialConfig(markerPath, runtimeMaintenanceLimit)
		if err != nil {
			return err
		}
		var owner runtimeMaintenanceMarker
		digest := controlDigest(original)
		if (digest != record.MarkerDigest && digest != record.NextMarker) ||
			json.Unmarshal(original, &owner) != nil || owner.Schema != 1 || owner.Phase != "Paused" ||
			owner.Token != record.Token || owner.ID != record.Coordinator || owner.ExpectedProfile != record.Profile ||
			owner.OwnerPID <= 0 || uint64(owner.OwnerPID) > uint64(^uint32(0)) || owner.OwnerBirth == "" {
			return errors.New("原离线初始化维护记录已改变")
		}
		birth, err := hostControlBirth(ctx)
		if err != nil {
			return err
		}
		if owner.OwnerPID == os.Getpid() && owner.OwnerBirth == birth {
			if record.NextMarker != "" {
				record.MarkerDigest, record.NextMarker = digest, ""
				*data, err = record.save(activePath, *data)
				if err != nil {
					return err
				}
			}
			marker = original
			return nil
		}
		previous, err := runtimeControlProcessBirth(ctx, owner.OwnerPID)
		if err != nil || previous == owner.OwnerBirth {
			return errors.New("无法确认原离线初始化协调器已退出")
		}
		owner.OwnerPID, owner.OwnerBirth = os.Getpid(), birth
		marker, err = json.Marshal(owner)
		if err != nil {
			return err
		}
		// Save both legal digests before replacing the old owner's marker.
		record.MarkerDigest, record.NextMarker = digest, controlDigest(marker)
		*data, err = record.save(activePath, *data)
		if err != nil {
			return err
		}
		if err := writeRuntimeAccountRecord(markerPath, marker, original); err != nil {
			return err
		}
		record.MarkerDigest, record.NextMarker = record.NextMarker, ""
		*data, err = record.save(activePath, *data)
		return err
	})
	return marker, err
}

func (m *Manager) finishOfflineKeygen(ctx context.Context, record runtimeOfflineKeygenRecord,
	data []byte, activePath, markerPath, markerArchive, receiptPath string) error {
	return m.withStoppedKeygenLibrary(ctx, record, func() error {
		if err := record.verifySession(); err != nil {
			return err
		}
		current, err := readCredentialConfig(activePath, runtimeMaintenanceLimit)
		if err != nil || !bytes.Equal(data, current) {
			return errors.New("离线初始化原事务记录已改变")
		}
		if err := requireAbsentRuntimeAccountFile(receiptPath); err != nil {
			return err
		}
		marker, err := readCredentialConfig(markerPath, runtimeMaintenanceLimit)
		switch {
		case err == nil:
			if controlDigest(marker) != record.MarkerDigest {
				return errors.New("离线初始化维护记录已改变")
			}
			if err := requireAbsentRuntimeAccountFile(markerArchive); err != nil {
				return err
			}
		case errors.Is(err, os.ErrNotExist) && record.Stage == "committed":
			if err := record.verifyRetiredMarker(markerArchive); err != nil {
				return err
			}
			marker = nil
		default:
			return err
		}
		if record.Stage == "sent" {
			record.Stage = "committed"
			if _, err := record.save(activePath, data); err != nil {
				return err
			}
		}
		// Startup remains fenced by the active record until both moves complete.
		if marker != nil {
			if err := publishSessionFile(markerPath, markerArchive); err != nil {
				return err
			}
		}
		return publishSessionFile(activePath, receiptPath)
	})
}

func (record runtimeOfflineKeygenRecord) verifyRetiredMarker(path string) error {
	data, err := readCredentialConfig(path, runtimeMaintenanceLimit)
	if err != nil || controlDigest(data) != record.MarkerDigest {
		return errors.New("原离线初始化的归档维护记录无效")
	}
	return nil
}
