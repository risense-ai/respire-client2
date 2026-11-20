//go:build linux || windows || (darwin && cgo)

package host

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

type runtimeRegisterMaterial struct {
	Account string `json:"keyring_account"`
	Backend string `json:"keyring_backend"`
	Salt    string `json:"kdf_salt"`
	Wrapped string `json:"wrapped_urk"`
	Nonce   string `json:"urk_nonce"`
	Super   string `json:"super_password"`
}

type runtimeRegisterParams struct {
	Version  int                     `json:"schema_version"`
	Binding  runtimeControlBinding   `json:"binding"`
	Identity runtimeAuthIdentity     `json:"identity"`
	Server   string                  `json:"addr"`
	User     string                  `json:"user"`
	Password string                  `json:"password"`
	Material runtimeRegisterMaterial `json:"material"`
}

func prepareDesktopRegistration(ctx context.Context, operation, profile, suppliedSuper string) (runtimeRegisterMaterial, error) {
	var material runtimeRegisterMaterial
	raw, err := readCredentialConfig(filepath.Join(profile, "session.json"), controlFrameLimit)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return material, err
	}
	defer clear(raw)
	identity, err := runtimeAuthIdentityOf(raw)
	if err != nil {
		return material, err
	}
	if len(raw) != 0 {
		var source map[string]json.RawMessage
		if err := json.Unmarshal(raw, &source); err != nil {
			return material, err
		}
		for _, field := range []string{"super", "pass", "secret", "secret_key"} {
			if _, present := source[field]; present {
				return material, errors.New("请先解锁旧账户并完成本机凭据迁移，再注册")
			}
		}
	}
	if identity.Kind == "unbound" {
		if err := json.Unmarshal(raw, &material); err != nil {
			return material, err
		}
		alias, ok := strings.CutPrefix(material.Account, "login-")
		if _, err := hex.DecodeString(alias); !ok || len(alias) != 32 || err != nil || material.Backend != runtimeLoginBackend() {
			return material, errors.New("离线账户的注册密钥槽无效")
		}
		secret, err := nativeRuntimeCredentialStore(ctx).readSlot(material.Backend, "rsrs", "super:"+material.Account, false)
		if err != nil {
			return material, err
		}
		if secret == nil || *secret == "" || suppliedSuper != "" && *secret != suppliedSuper {
			return material, errors.New("Super Key 与当前离线账户不匹配")
		}
		material.Super = *secret
		return material, nil
	}
	if suppliedSuper != "" {
		return material, errors.New("新账户注册由 Client2 生成 Super Key")
	}
	return newRuntimeVaultMaterial(operation)
}

func newRuntimeVaultMaterial(operation string) (runtimeRegisterMaterial, error) {
	var urk [32]byte
	defer clear(urk[:])
	if _, err := rand.Read(urk[:]); err != nil {
		return runtimeRegisterMaterial{}, err
	}
	return wrapRuntimeVaultKey(operation, urk[:])
}

func wrapRuntimeVaultKey(operation string, urk []byte) (runtimeRegisterMaterial, error) {
	if len(urk) != 32 {
		return runtimeRegisterMaterial{}, errors.New("账户数据密钥长度无效")
	}
	var material runtimeRegisterMaterial
	var entropy [18]byte
	var salt [16]byte
	var nonce [12]byte
	defer clear(entropy[:])
	for _, part := range [][]byte{entropy[:], salt[:], nonce[:]} {
		if _, err := rand.Read(part); err != nil {
			return material, err
		}
	}
	key, err := hkdf.Key(sha256.New, entropy[:], salt[:], "rsrs:kek:v4", len(urk))
	if err != nil {
		return material, err
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return material, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return material, err
	}
	var code strings.Builder
	code.WriteString("A3")
	for i := 0; i < len(entropy); i += 3 {
		code.WriteByte('-')
		code.WriteString(hex.EncodeToString(entropy[i : i+3]))
	}
	return runtimeRegisterMaterial{
		Account: "login-" + strings.ReplaceAll(operation, "-", ""), Backend: runtimeLoginBackend(),
		Salt: hex.EncodeToString(salt[:]), Nonce: hex.EncodeToString(nonce[:]), Super: code.String(),
		Wrapped: "rsrs:v1:" + hex.EncodeToString(aead.Seal(nil, nonce[:], urk, nil)),
	}, nil
}

func (m *Manager) sendDesktopRegistration(ctx context.Context, image, digest string, admission *runtimeAccountAdmission,
	material runtimeRegisterMaterial, password string) (result runtimeLoginResult, err error) {
	var record runtimeAccountRecord
	if json.Unmarshal(admission.Record, &record) != nil || record.Schema != 1 || !record.Register || record.Sent {
		return runtimeLoginResult{}, errors.New("注册需要原始未发送事务")
	}
	store := nativeRuntimeCredentialStore(ctx)
	var metadata []byte
	created := make(map[string]string, 3)
	sent := false
	defer func() {
		if err != nil && !sent {
			cleanup := discardUnsentRegistration(store, record, admission, metadata, created)
			if cleanup == nil {
				*admission = runtimeAccountAdmission{}
			}
			err = errors.Join(err, cleanup)
		}
	}()
	identity, err := verifyRuntimeLoginSource(record)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	control, err := m.prepareRuntimeAccountControl(ctx, image, digest, record.Source, identity.AccountID, admission.Marker)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	account := "login-" + strings.ReplaceAll(record.OperationID, "-", "")
	backend := runtimeLoginBackend()
	if material.Backend != backend || !sessionAlias.MatchString(material.Account) || material.Super == "" ||
		identity.Kind != "unbound" && material.Account != account ||
		control.binding.InstallationRoot != record.Root || control.binding.Profile != record.Source {
		return runtimeLoginResult{}, errors.New("注册密钥槽与原事务不匹配")
	}
	params := runtimeRegisterParams{Version: 1, Binding: control.binding, Identity: identity,
		Server: record.Server, User: record.User, Password: password, Material: material}
	argument, err := json.Marshal(params)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	defer clear(argument)
	metadata, err = loginCustodyMetadata(record, account, backend, nil, nil)
	if err != nil {
		return runtimeLoginResult{}, err
	}
	if err := writeRuntimeAccountRecord(loginCustodyPath(record), metadata, nil); err != nil {
		return runtimeLoginResult{}, err
	}
	for _, slot := range []struct{ key, value string }{
		{"login-control:" + account, string(argument)}, {"super:" + material.Account, material.Super}, {"pass:" + material.Account, password},
	} {
		current, err := store.readSlot(backend, "rsrs", slot.key, false)
		if err != nil {
			return runtimeLoginResult{}, err
		}
		if current == nil {
			created[slot.key] = slot.value
		}
		if err := store.preserve("rsrs", slot.key, slot.value); err != nil {
			return runtimeLoginResult{}, err
		}
	}
	requestID, err := controlUUID()
	if err != nil {
		return runtimeLoginResult{}, err
	}
	if err := admission.markLoginSent(); err != nil {
		return runtimeLoginResult{}, err
	}
	sent = true
	reply, err := m.exchangeRuntimeControl(ctx, control.image, control.port, control.grant, runtimeControlRequest{
		V: 1, ID: requestID, Method: "control.auth.register", Args: []string{string(argument)},
		OperationID: record.OperationID, IdempotencyKey: record.OperationID,
	})
	if err != nil {
		return runtimeLoginResult{}, err
	}
	defer clear(reply.Envelope)
	return readRuntimeLoginResult(reply, record)
}

func discardUnsentRegistration(store hostCredentialStore, record runtimeAccountRecord, admission *runtimeAccountAdmission,
	metadata []byte, created map[string]string) error {
	actual, err := readCredentialConfig(admission.Path, runtimeMaintenanceLimit)
	if err != nil || !bytes.Equal(actual, admission.Record) {
		return errors.Join(err, errors.New("无法确认注册请求尚未发送，保留原事务"))
	}
	for key, value := range created {
		if err := store.remove("rsrs", key, value); err != nil {
			return err
		}
	}
	if len(metadata) != 0 {
		if err := removeOwnedRuntimeAccountRecord(loginCustodyPath(record), metadata); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	directory := filepath.Join(record.Root, "runtime", "autostart")
	active, err := json.Marshal(map[string]any{"schema": 1, "operation_id": record.OperationID})
	if err != nil {
		return err
	}
	if err := removeOwnedRuntimeAccountRecord(filepath.Join(directory, "account-active.json"), active); err != nil {
		return err
	}
	if err := removeOwnedRuntimeAccountRecord(admission.Path, admission.Record); err != nil {
		return err
	}
	return removeOwnedRuntimeAccountRecord(filepath.Join(directory, "maintenance.json"), admission.Marker)
}

// Verify the selected session and the keyring entries against the original request.
func registeredRuntimePassword(ctx context.Context, record runtimeAccountRecord, target, digest string, private *string) (*string, error) {
	var request runtimeRegisterParams
	if private == nil || json.Unmarshal([]byte(*private), &request) != nil || request.Version != 1 ||
		request.Binding.InstallationRoot != record.Root || request.Binding.Profile != record.Source ||
		request.User != record.User || request.Server != record.Server || request.Password == "" ||
		!reflect.DeepEqual(request.Identity, record.Original.Identity) {
		return nil, errors.New("原注册请求托管记录无效")
	}
	raw, err := readCredentialConfig(filepath.Join(target, "session.json"), controlFrameLimit)
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	var material runtimeRegisterMaterial
	if controlDigest(raw) != digest || json.Unmarshal(raw, &material) != nil || material.Super != "" {
		return nil, errors.New("原注册会话已改变")
	}
	material.Super = request.Material.Super
	if material != request.Material {
		return nil, errors.New("原注册会话与密钥托管记录不匹配")
	}
	store := nativeRuntimeCredentialStore(ctx)
	for _, slot := range []struct{ name, value string }{{"super:", material.Super}, {"pass:", request.Password}} {
		actual, err := store.readSlot(material.Backend, "rsrs", slot.name+material.Account, false)
		if err != nil {
			return nil, err
		}
		if actual == nil || *actual != slot.value {
			return nil, errors.New("原注册凭据已改变")
		}
	}
	return &request.Password, nil
}
