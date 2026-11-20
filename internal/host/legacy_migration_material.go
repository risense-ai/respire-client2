//go:build linux || windows || (darwin && cgo)

package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type legacyVaultFactors struct {
	Version      string  `json:"version"`
	Password     *string `json:"password,omitempty"`
	AccountKey   *string `json:"account_secret,omitempty"`
	SuperKey     *string `json:"super_password,omitempty"`
	LegacySecret *string `json:"secret_key,omitempty"`
}

type legacyMigrationCandidate struct {
	SessionDigest string             `json:"session_sha256"`
	Factors       legacyVaultFactors `json:"factors"`
}

type legacyMigrationCustody struct {
	Alias            string  `json:"keyring_account"`
	Backend          string  `json:"keyring_backend"`
	LegacyBackend    *string `json:"legacy_secret_backend"`
	PasswordRetained bool    `json:"login_password_retained"`
}

type legacyMigrationMaterial struct {
	Candidates []legacyMigrationCandidate `json:"candidates"`
	Custody    *legacyMigrationCustody    `json:"custody"`
	profile    legacyMigrationProfile
	password   *string
}

func readLegacyMigrationMaterial(profile legacyMigrationProfile, store hostCredentialStore) (legacyMigrationMaterial, error) {
	material := legacyMigrationMaterial{profile: profile, Candidates: []legacyMigrationCandidate{}}
	if err := profileDirectory(profile.Source); err != nil {
		return material, err
	}
	source, err := legacySourceID(profile.Source)
	if err != nil || source != profile.SourceID {
		return material, errors.Join(errors.New("旧库来源已改变"), err)
	}
	raw, err := readCredentialConfig(filepath.Join(profile.Source, "session.json"), controlFrameLimit)
	if errors.Is(err, os.ErrNotExist) {
		return material, nil
	}
	if err != nil {
		return material, err
	}
	defer clear(raw)
	var session struct {
		runtimeSession
		WrappedURK       string          `json:"wrapped_urk"`
		LegacyBackendRaw json.RawMessage `json:"legacy_secret_backend"`
	}
	if err := json.Unmarshal(raw, &session); err != nil {
		return material, fmt.Errorf("旧账户会话无效: %w", err)
	}
	if session.WrappedURK == "" && session.LegacyPass == nil && session.LegacySuper == nil &&
		session.LegacySecret == nil && session.SecretKey == nil {
		return material, nil
	}
	version := uint64(1)
	if session.Version != nil {
		version = *session.Version
	}
	if version < 1 || version > 4 {
		return material, errors.New("旧账户密钥版本不受支持")
	}
	base, err := deviceConfigHome()
	if err != nil {
		return material, err
	}
	relative, err := filepath.Rel(base, profile.Source)
	if err != nil {
		return material, err
	}
	var namespaces []string
	switch strings.SplitN(relative, string(filepath.Separator), 2)[0] {
	case ".respire":
		namespaces = []string{"respire", "memocap", "1memory"}
	case ".onememory":
		namespaces = []string{"1memory", "memocap", "respire"}
	case ".rsrs":
		namespaces = []string{"rsrs", "respire", "1memory"}
	default:
		return material, errors.New("旧账户不在已识别的源目录中")
	}
	alias := session.Alias
	if alias == "" {
		alias = strings.TrimSpace(session.User)
		if alias == "" {
			alias = "local"
		}
	}
	passes, err := readLegacyMigrationSlots(store, namespaces, "pass", alias, session.LegacyPass)
	if err != nil {
		return material, err
	}
	if len(passes) > 0 {
		material.password = &passes[0]
	}
	primary := passes
	if version != 1 {
		inline := []*string{session.LegacySuper}
		if version == 4 {
			inline = []*string{session.SecretKey, session.LegacySuper}
		}
		primary, err = readLegacyMigrationSlots(store, namespaces, "super", alias, inline...)
		if err != nil {
			return material, err
		}
		if provided := os.Getenv("RSRS_SUPER"); provided != "" {
			primary = append(primary, provided)
		}
	}
	if len(primary) == 0 {
		return material, errors.New("缺少旧库原始凭据")
	}
	var legacy *string
	if version == 1 || version == 3 {
		legacy = session.LegacySecret
		if version == 3 {
			legacy = session.SecretKey
		}
		if len(session.LegacyBackendRaw) > 0 {
			var backend string
			if json.Unmarshal(session.LegacyBackendRaw, &backend) != nil || backend == "" || !sessionAlias.MatchString(session.Alias) {
				return material, errors.New("旧账户凭据别名无效")
			}
			legacy, err = store.readSlot(backend, "rsrs", "legacy-secret:"+session.Alias, false)
			if err != nil {
				return material, err
			}
		}
		if legacy == nil || *legacy == "" {
			return material, errors.New("缺少旧库原始 Secret Key")
		}
	}
	digest := controlDigest(raw)
	seen := map[string]bool{}
	for _, value := range primary {
		if seen[value] {
			continue
		}
		seen[value] = true
		factors := legacyVaultFactors{Version: fmt.Sprintf("v%d", version)}
		if version == 1 {
			factors.Password, factors.AccountKey = &value, legacy
		} else {
			factors.SuperKey, factors.LegacySecret = &value, legacy
		}
		material.Candidates = append(material.Candidates, legacyMigrationCandidate{SessionDigest: digest, Factors: factors})
	}
	return material, nil
}

func readLegacyMigrationSlots(store hostCredentialStore, namespaces []string, slot, alias string, inline ...*string) ([]string, error) {
	var values []string
	keep := func(value *string) {
		if value != nil && (slot == "pass" || *value != "") {
			values = append(values, *value)
		}
	}
	for _, value := range inline {
		keep(value)
	}
	for _, service := range namespaces {
		for _, backend := range store.backends {
			value, err := store.read(backend, service, slot+":"+alias)
			if err != nil {
				return nil, err
			}
			keep(value)
		}
	}
	return values, nil
}

func (m *legacyMigrationMaterial) retainSelected(details json.RawMessage, store hostCredentialStore) error {
	var selection struct {
		State         string `json:"state"`
		Index         *uint  `json:"candidate_index"`
		SessionDigest string `json:"session_sha256"`
	}
	decoder := json.NewDecoder(bytes.NewReader(details))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&selection); err != nil {
		return errors.New("旧库迁移的凭据回执无效")
	}
	if selection.State != "credentials_required" || selection.Index == nil || *selection.Index >= uint(len(m.Candidates)) {
		return errors.New("旧库迁移未指明有效的原始凭据")
	}
	candidate := m.Candidates[*selection.Index]
	if selection.SessionDigest != candidate.SessionDigest {
		return errors.New("旧库迁移凭据的源会话已改变")
	}
	source, err := legacySourceID(m.profile.Source)
	if err != nil || source != m.profile.SourceID {
		return errors.Join(errors.New("旧库来源在保存凭据前改变"), err)
	}
	session, err := readCredentialConfig(filepath.Join(m.profile.Source, "session.json"), controlFrameLimit)
	if err != nil {
		return err
	}
	defer clear(session)
	if controlDigest(session) != candidate.SessionDigest {
		return errors.New("旧库源会话在保存凭据前改变")
	}
	backend := ""
	for _, value := range store.backends {
		if value != "kernel" {
			backend = value
			break
		}
	}
	if backend == "" {
		return errors.New("旧库迁移需要可持久保存的系统钥匙串")
	}
	alias := "legacy-" + source[:16]
	factors := candidate.Factors
	password := m.password
	legacy := factors.LegacySecret
	if factors.Password != nil {
		password, legacy = factors.Password, factors.AccountKey
	}
	for _, slot := range []struct {
		Name  string
		Value *string
	}{{"super", factors.SuperKey}, {"pass", password}, {"legacy-secret", legacy}} {
		if slot.Value == nil {
			continue
		}
		key := slot.Name + ":" + alias
		if err := store.preserve("rsrs", key, *slot.Value); err != nil {
			return err
		}
		saved, err := store.read(backend, "rsrs", key)
		if err != nil || saved == nil || *saved != *slot.Value {
			return errors.Join(errors.New("旧库迁移凭据未能写入系统钥匙串"), err)
		}
	}
	custody := legacyMigrationCustody{Alias: alias, Backend: backend, PasswordRetained: password != nil}
	if legacy != nil {
		custody.LegacyBackend = &backend
	}
	m.Candidates, m.Custody = []legacyMigrationCandidate{candidate}, &custody
	return nil
}
