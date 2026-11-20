//go:build linux || windows || (darwin && cgo)

package host

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const offlineKeygenFile = "offline-keygen.json"

type runtimeOfflineKeygenRecord struct {
	Schema       int                 `json:"schema"`
	Operation    string              `json:"operation_id"`
	Force        bool                `json:"force"`
	Root         string              `json:"root"`
	Source       string              `json:"source"`
	Profile      string              `json:"profile"`
	Image        string              `json:"binary"`
	ImageDigest  string              `json:"image_sha256"`
	Token        string              `json:"token"`
	Coordinator  string              `json:"coordinator"`
	MarkerDigest string              `json:"marker_sha256"`
	NextMarker   string              `json:"pending_marker_sha256,omitempty"`
	Original     runtimeAuthIdentity `json:"original_identity"`
	Stage        string              `json:"stage"`
	Session      *string             `json:"session_sha256"`
	Material     json.RawMessage     `json:"material"`
	WasServing   bool                `json:"was_serving"`
	StoreReady   bool                `json:"store_ready"`
}

func (m *Manager) desktopOfflineKeygen(ctx context.Context, id string, query bool) (raw json.RawMessage, recovery string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	op := strings.TrimPrefix(id, "desktop-")
	if !hostOperationID.MatchString(op) || m.ClientVersion == "" {
		return nil, "", errors.New("离线初始化缺少有效的 Client2 请求身份")
	}
	uncertain := query
	defer func() {
		if err != nil && uncertain {
			err = &unconfirmedError{id: id, cause: err}
		}
	}()
	takeover, err := m.lockRuntimeTakeover(ctx)
	if err != nil {
		return nil, "", err
	}
	defer takeover.Close()
	root, err := canonicalSessionDirectory(m.DataRoot)
	if err != nil {
		return nil, "", err
	}
	profile, err := selectedRuntimeLoginProfile(root)
	if err != nil {
		return nil, "", err
	}
	directory := filepath.Join(root, "runtime", "autostart")
	markerPath := filepath.Join(directory, "maintenance.json")
	activePath := filepath.Join(directory, offlineKeygenFile)
	receiptPath := filepath.Join(directory, "offline-keygen-result-"+op+".json")
	markerArchive := filepath.Join(directory, "maintenance.keygen-"+op+".json")
	if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, "", err
	}
	if err := checkRuntimeAccountDirectory(directory); err != nil {
		return nil, "", err
	}

	info, err := m.Installed()
	if err != nil {
		return nil, "", err
	}
	if err := m.verifyOfflineKeygenRuntime(info); err != nil {
		return nil, "", err
	}
	image, err := canonicalSessionDirectory(filepath.Join(m.runtimeDir(info.Version), runtimeBinary()))
	if err != nil {
		return nil, "", err
	}
	record := runtimeOfflineKeygenRecord{
		Schema: 1, Operation: op, Root: root, Source: profile, Profile: profile, Image: image,
		ImageDigest: info.SHA256, Original: runtimeAuthIdentity{Kind: "absent"},
	}
	store := nativeRuntimeCredentialStore(ctx)
	if query {
		data, err := readCredentialConfig(receiptPath, runtimeMaintenanceLimit)
		if err == nil {
			committed, err := record.readOriginal(data)
			if err != nil || committed.Stage != "committed" {
				return nil, "", errors.New("原离线初始化终态凭证无效")
			}
			if err := committed.verifySession(); err != nil {
				return nil, "", err
			}
			if err := committed.verifyRetiredMarker(markerArchive); err != nil {
				return nil, "", err
			}
			return offlineKeygenEnvelope(committed)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, "", err
		}
		return m.queryOfflineKeygen(ctx, record, activePath, markerPath, markerArchive, receiptPath, store)
	}

	if err := requireUninitializedKeygenProfile(profile); err != nil {
		return nil, "", err
	}
	for _, name := range []string{offlineKeygenFile, "maintenance.json", "account-active.json", "key-control.json", "space-client2.json"} {
		if err := requireAbsentRuntimeAccountFile(filepath.Join(directory, name)); err != nil {
			return nil, "", err
		}
	}
	// A registered supervisor continues to own starts and stops for its library.
	registration, err := readCredentialConfig(filepath.Join(directory, "registration.json"), runtimeMaintenanceLimit)
	if err == nil {
		var state struct {
			Enabled  *bool `json:"enabled"`
			Stopped  *bool `json:"manually_stopped"`
			Retained *bool `json:"maintenance"`
		}
		if json.Unmarshal(registration, &state) != nil || state.Enabled == nil || *state.Enabled ||
			state.Stopped == nil || *state.Stopped || state.Retained == nil || *state.Retained {
			return nil, "", errors.New("请先通过原宿主解除 Runtime 的自动启动或维护")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	record.Coordinator, err = runtimeRegistrationCoordinator(root, registration)
	if err != nil {
		return nil, "", err
	}
	health, _, err := m.Health(ctx)
	if err == nil {
		if health.ManagedID != "" || !samePath(health.DataDir, profile) || health.ImageSHA256 != info.SHA256 ||
			health.AccountID != controlDigest([]byte(profile)) || health.StoreReady {
			return nil, "", errors.New("运行中的 Runtime 无法确认为当前未初始化的库")
		}
		if err := m.verifySelectedHealth(health, info); err != nil {
			return nil, "", err
		}
		if err := m.stopDetached(ctx, health); err != nil {
			return nil, "", err
		}
	} else if !IsUnavailable(err) {
		return nil, "", err
	}
	material, err := newRuntimeVaultMaterial(op)
	if err != nil {
		return nil, "", err
	}
	slot := "super:" + material.Account
	present, err := store.readSlot(material.Backend, "rsrs", slot, true)
	if err != nil {
		return nil, "", err
	}
	if present != nil {
		return nil, "", errors.New("离线初始化的系统凭据槽已存在")
	}
	record.Token, err = controlUUID()
	if err != nil {
		return nil, "", err
	}
	birth, err := hostControlBirth(ctx)
	if err != nil {
		return nil, "", err
	}
	marker, err := json.Marshal(runtimeMaintenanceMarker{Schema: 1, ID: record.Coordinator, Token: record.Token,
		Phase: "Paused", ExpectedProfile: profile, ExpectedVersion: "client2-" + m.ClientVersion, OwnerPID: os.Getpid(), OwnerBirth: birth})
	if err != nil {
		return nil, "", err
	}
	record.Stage, record.MarkerDigest = "sent", controlDigest(marker)
	record.Material, err = json.Marshal(map[string]any{
		"vault_version": 4, "auth_operation_id": op, "keyring_account": material.Account, "keyring_backend": material.Backend,
		"kdf_salt": material.Salt, "wrapped_urk": material.Wrapped, "urk_nonce": material.Nonce,
	})
	if err != nil {
		return nil, "", err
	}
	var data []byte
	if err := m.withStoppedKeygenLibrary(ctx, record, func() error {
		if err := requireUninitializedKeygenProfile(profile); err != nil {
			return err
		}
		uncertain = true
		var err error
		data, err = record.save(activePath, nil)
		if err != nil {
			return err
		}
		return writeRuntimeAccountRecord(markerPath, marker, nil)
	}); err != nil {
		return nil, "", err
	}
	// Keep the original fence and owned slot if confirmation is lost.
	if err := store.preserve("rsrs", slot, material.Super); err != nil {
		return nil, "", err
	}
	digest, err := m.exchangeOfflineKeygen(ctx, record, marker, material, record.Original, "control.auth.keygen")
	if err != nil {
		return nil, "", err
	}
	record.Session = &digest
	if err := m.finishOfflineKeygen(ctx, record, data, activePath, markerPath, markerArchive, receiptPath); err != nil {
		return nil, "", err
	}
	raw, _, err = offlineKeygenEnvelope(record)
	return raw, material.Super, err
}

func (m *Manager) verifyOfflineKeygenRuntime(info InstalledRuntime) error {
	raw, err := os.ReadFile(m.runtimeReceiptPath(info.Version))
	if err != nil {
		return err
	}
	var release sharedRuntimeReceipt
	var metadata struct {
		Capabilities map[string]string `json:"capabilities"`
	}
	if json.Unmarshal(raw, &release) != nil || release.Release == nil || release.Version != info.Version ||
		release.SHA256 != info.SHA256 ||
		json.Unmarshal([]byte(release.Release.JSON), &metadata) != nil || metadata.Capabilities["offline_keygen_handoff"] != "1" {
		return errors.New("已安装 Runtime 缺少经验证的离线初始化能力，请先升级")
	}
	return verifyReleaseTree(m.runtimeDir(info.Version), release)
}

func requireUninitializedKeygenProfile(profile string) error {
	if err := requireAbsentRuntimeAccountFile(filepath.Join(profile, "session.json")); err != nil {
		return err
	}
	for _, database := range []string{"rsrs.db", "onememory.db"} {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			_, err := os.Lstat(filepath.Join(profile, database+suffix))
			if err == nil {
				return errors.New("离线初始化只接受没有数据库文件的全新库")
			}
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) withStoppedKeygenLibrary(ctx context.Context, record runtimeOfflineKeygenRecord, work func() error) error {
	address, _, err := m.runtimeAccountEndpoint()
	if err != nil {
		return err
	}
	installation, err := lockRuntimeDatabase(ctx, record.Root, 0)
	if err != nil {
		return err
	}
	defer installation.Close()
	if record.Root != record.Profile {
		library, err := lockRuntimeDatabase(ctx, record.Profile, 0)
		if err != nil {
			return err
		}
		defer library.Close()
	}
	if err := requireStoppedM3Runtime(ctx, record.Root, address); err != nil {
		return err
	}
	selected, err := selectedRuntimeLoginProfile(record.Root)
	if err != nil {
		return err
	}
	if selected != record.Profile {
		return errors.New("当前库已切换，无法继续原离线初始化")
	}
	return work()
}

func (record runtimeOfflineKeygenRecord) readOriginal(data []byte) (runtimeOfflineKeygenRecord, error) {
	var saved runtimeOfflineKeygenRecord
	if json.Unmarshal(data, &saved) != nil || saved.Schema != record.Schema || saved.Operation != record.Operation ||
		saved.Root != record.Root || saved.Source != record.Source || saved.Profile != record.Profile ||
		saved.Image != record.Image || saved.ImageDigest != record.ImageDigest || saved.Original != record.Original ||
		!hostOperationID.MatchString(saved.Token) || saved.Coordinator == "" || !validHash(saved.MarkerDigest) ||
		saved.Force || saved.WasServing || saved.StoreReady ||
		(saved.NextMarker != "" && (!validHash(saved.NextMarker) || saved.Stage != "sent")) ||
		(saved.Stage == "sent" && saved.Session != nil) ||
		(saved.Stage == "committed" && (saved.Session == nil || !validHash(*saved.Session))) ||
		(saved.Stage != "sent" && saved.Stage != "committed") {
		return saved, errors.New("离线初始化原事务绑定已改变")
	}
	return saved, nil
}

func (record runtimeOfflineKeygenRecord) save(path string, original []byte) ([]byte, error) {
	data, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	return data, writeRuntimeAccountRecord(path, data, original)
}

func offlineKeygenEnvelope(record runtimeOfflineKeygenRecord) (json.RawMessage, string, error) {
	raw, err := json.Marshal(map[string]any{"command": "keygen", "status": "ok", "summary": map[string]any{
		"operation_id": record.Operation, "outcome": "committed", "path": filepath.Join(record.Profile, "session.json"), "session_sha256": record.Session,
	}})
	return raw, "", err
}

func keygenIdentity(profile string) (runtimeAuthIdentity, error) {
	raw, err := readCredentialConfig(filepath.Join(profile, "session.json"), controlFrameLimit)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return runtimeAuthIdentity{}, err
	}
	defer clear(raw)
	return runtimeAuthIdentityOf(raw)
}

func offlineKeygenAlias(operation string) string {
	return "login-" + strings.ReplaceAll(operation, "-", "")
}

func (record runtimeOfflineKeygenRecord) verifySession() error {
	identity, err := keygenIdentity(record.Profile)
	if err != nil {
		return err
	}
	if record.Session == nil || identity.Kind != "unbound" || identity.OperationID != record.Operation ||
		identity.SessionDigest != *record.Session {
		return errors.New("当前本机身份与原离线初始化回执不匹配")
	}
	return nil
}

func (m *Manager) exchangeOfflineKeygen(ctx context.Context, record runtimeOfflineKeygenRecord,
	marker []byte, material runtimeRegisterMaterial, identity runtimeAuthIdentity, method string) (string, error) {
	control, err := m.prepareRuntimeAccountControl(ctx, record.Image, record.ImageDigest, record.Profile, nil, marker)
	if err != nil {
		return "", err
	}
	requestID, err := controlUUID()
	if err != nil {
		return "", err
	}
	argument, err := json.Marshal(struct {
		Schema   int                     `json:"schema_version"`
		Binding  runtimeControlBinding   `json:"binding"`
		Identity runtimeAuthIdentity     `json:"identity"`
		Force    bool                    `json:"force"`
		Material runtimeRegisterMaterial `json:"material"`
	}{Schema: 1, Binding: control.binding, Identity: identity, Material: material})
	if err != nil {
		return "", err
	}
	defer clear(argument)
	reply, err := m.exchangeRuntimeControl(ctx, control.image, control.port, control.grant, runtimeControlRequest{
		V: 1, ID: requestID, Method: method, Args: []string{string(argument)},
		OperationID: record.Operation, IdempotencyKey: record.Operation,
	})
	defer clear(reply.Envelope)
	if err != nil {
		// The helper's private diagnostics may contain recovery material.
		return "", errors.New("Runtime 未确认原离线初始化，请在操作记录中查询")
	}
	var envelope struct {
		Command string `json:"command"`
		Status  string `json:"status"`
		Summary struct {
			Operation string `json:"operation_id"`
			Outcome   string `json:"outcome"`
			Path      string `json:"path"`
			Digest    string `json:"session_sha256"`
			runtimeRegisterMaterial
		} `json:"summary"`
	}
	if json.Unmarshal(reply.Envelope, &envelope) != nil || envelope.Command != "keygen" ||
		envelope.Status != "ok" || envelope.Summary.Outcome != "committed" || envelope.Summary.Operation != record.Operation ||
		envelope.Summary.Path != filepath.Join(record.Profile, "session.json") || !validHash(envelope.Summary.Digest) ||
		subtle.ConstantTimeCompare([]byte(envelope.Summary.Super), []byte(material.Super)) != 1 {
		return "", errors.New("Runtime 离线初始化回执与原事务不匹配")
	}
	record.Session = &envelope.Summary.Digest
	return *record.Session, record.verifySession()
}
