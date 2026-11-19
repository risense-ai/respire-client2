//go:build linux || windows || (darwin && cgo)

package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

type credentialReader func(service, account string) (*string, error)
type hostCredentialStore struct {
	backends []string
	read     func(backend, service, account string) (*string, error)
	preserve func(service, account, value string) error
	replace  func(service, account string, expected *string, value string) error
	remove   func(service, account, expected string) error
}

func (s hostCredentialStore) supports(name string) bool {
	return slices.Contains(s.backends, name)
}

func (s hostCredentialStore) readSlot(backend, service, account string, emptyOK bool) (*string, error) {
	if backend != "" {
		if !s.supports(backend) {
			return nil, errors.New("Unsupported Runtime credential backend")
		}
		return s.read(backend, service, account)
	}
	// Match the historical CLI's lookup order for credentials with no recorded backend.
	for _, candidate := range s.backends {
		value, err := s.read(candidate, service, account)
		if err != nil || value != nil && (emptyOK || *value != "") {
			return value, err
		}
	}
	return nil, nil
}

var sessionAlias = regexp.MustCompile(`^(legacy|login)-[a-zA-Z0-9-]+$`)

type runtimeSession struct {
	Version       *uint64 `json:"vault_version"`
	User          string  `json:"user"`
	Alias         string  `json:"keyring_account"`
	Backend       string  `json:"keyring_backend"`
	LegacyBackend string  `json:"legacy_secret_backend"`
	LegacyPass    *string `json:"pass"`
	LegacySuper   *string `json:"super"`
	LegacySecret  *string `json:"secret"`
	SecretKey     *string `json:"secret_key"`
}

type runtimeProviderCredential struct {
	Service string `json:"service"`
	Account string `json:"account"`
	Value   string `json:"value"`
}

func (m *Manager) unlockNativeRuntime(ctx context.Context, health HealthInfo, store hostCredentialStore) error {
	if _, _, err := m.runtimeAccountEndpoint(); err != nil {
		return err
	}
	if !samePath(health.InstallationRoot, m.DataRoot) || !runtimeInstanceID.MatchString(health.InstanceID) ||
		health.Generation == 0 || !validHash(health.AccountID) {
		return errors.New("Runtime unlock scope is invalid")
	}
	takeover, err := m.lockRuntimeTakeover(ctx)
	if err != nil {
		return err
	}
	defer takeover.Close()
	info, err := m.Installed()
	if err != nil {
		return err
	}
	if err := m.verifyInstalled(info); err != nil {
		return err
	}
	if err := m.verifySelectedHealth(health, info); err != nil {
		return err
	}
	if health.ImageSHA256 != info.SHA256 {
		return errors.New("Running Runtime image differs from the verified installation")
	}
	return m.unlockVerifiedRuntime(ctx, health, store, "")
}

// The caller holds takeover and has verified the Runtime image and process.
func (m *Manager) unlockVerifiedRuntime(ctx context.Context, health HealthInfo, store hostCredentialStore, sessionDigest string) error {
	address, _, err := m.runtimeAccountEndpoint()
	if err != nil {
		return err
	}
	base := url.URL{Scheme: "http", Host: address, Path: "/api/runtime/unlock"}
	current, _, err := m.Health(ctx)
	if err != nil {
		return err
	}
	// The process holding the takeover lock before us may have opened this same library.
	observed := current
	observed.StoreReady = health.StoreReady
	observed.CredentialState = health.CredentialState
	if observed != health {
		return errors.New("Runtime identity changed while waiting for the takeover lock")
	}
	if current.StoreReady && current.CredentialState == "available" {
		return nil
	}
	if current != health {
		return errors.New("Runtime credential state changed while waiting for the takeover lock")
	}
	profile, err := filepath.EvalSymlinks(health.DataDir)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(m.DataRoot, profile)
	if err != nil || !filepath.IsLocal(relative) {
		return errors.New("Runtime unlock profile is outside the installation")
	}
	off, err := m.runtimeServiceOff(profile)
	if err != nil {
		return err
	}
	if off {
		return nil
	}
	sessionPath := filepath.Join(profile, "session.json")
	original, err := readCredentialConfig(sessionPath, 65536)
	if err != nil {
		return fmt.Errorf("Read original Runtime session: %w", err)
	}
	if sessionDigest != "" && controlDigest(original) != sessionDigest {
		return errors.New("Runtime login target session changed before credential delivery")
	}
	var session runtimeSession
	if err := json.Unmarshal(original, &session); err != nil {
		return errors.New("Original Runtime session metadata is invalid")
	}
	version := uint64(1)
	if session.Version != nil {
		version = *session.Version
	}
	if (version == 1 || version == 3) && session.LegacyBackend == "" {
		original, err = m.migrateHostSession(ctx, health, sessionPath, original, session, version, store)
		if err != nil {
			return err
		}
		session = runtimeSession{}
		if err := json.Unmarshal(original, &session); err != nil {
			return errors.New("Migrated Runtime session metadata is invalid")
		}
	}
	super, pass, legacy, err := session.hostFactors(store)
	if err != nil {
		return err
	}
	providers, err := m.nativeProviderCredentials(profile, func(service, account string) (*string, error) {
		return store.readSlot("", service, account, false)
	})
	providerUpdate := map[string]any{"kind": "replace", "credentials": providers}
	providerState, providerCode := "ready", ""
	if err != nil {
		// An incomplete snapshot must revoke provider access without changing vault authorization.
		providerState, providerCode = "unavailable", "platform_failure"
		var credentialError providerCredentialError
		if errors.As(err, &credentialError) {
			providerCode = string(credentialError)
		} else if errors.Is(err, os.ErrPermission) {
			providerCode = "access_denied"
		}
		providerUpdate = map[string]any{"kind": "unavailable", "code": providerCode}
	}
	token, err := m.runtimeUnlockToken()
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"schema_version": 2, "profile": profile, "session_sha256": fmt.Sprintf("%x", sha256.Sum256(original)),
		"instance_id": health.InstanceID, "generation": health.Generation,
		"super_password": super, "login_password": pass, "legacy_secret": legacy,
		"provider_update": providerUpdate,
	})
	if err != nil || len(body) > 65536 {
		return errors.New("Runtime unlock frame exceeds its protocol limit")
	}
	defer clear(body)
	current, _, err = m.Health(ctx)
	if err != nil {
		return err
	}
	if current != health {
		return errors.New("Runtime changed before credential delivery")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := m.localClient.Do(req)
	if err != nil {
		return fmt.Errorf("Runtime unlock delivery unconfirmed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Runtime rejected unlock (HTTP %d)", resp.StatusCode)
	}
	var receipt struct {
		Schema     int  `json:"schema_version"`
		StoreReady bool `json:"store_ready"`
		Provider   struct {
			State string `json:"state"`
			Code  string `json:"code"`
		} `json:"provider_status"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&receipt); err != nil ||
		receipt.Schema != 2 || !receipt.StoreReady || receipt.Provider.State != providerState ||
		receipt.Provider.Code != providerCode {
		return errors.New("Runtime did not confirm library unlock and the provider credential status")
	}
	if providerCode != "" {
		fmt.Fprintf(os.Stderr, "Runtime library unlocked; provider credentials unavailable (%s)\n", providerCode)
	}
	return nil
}

func (m *Manager) runtimeServiceOff(profile string) (bool, error) {
	var device struct {
		Mode string `json:"service_mode"`
	}
	if err := readServiceSettings(filepath.Join(m.DataRoot, "client.json"), &device); err != nil {
		return false, err
	}
	switch device.Mode {
	case "off":
		return true, nil
	case "normal", "readonly":
		return false, nil
	}
	var agent struct {
		MemoryOff bool `json:"memory_off"`
	}
	if err := readServiceSettings(filepath.Join(profile, "agent.json"), &agent); err != nil {
		return false, err
	}
	return agent.MemoryOff, nil
}

func readServiceSettings(path string, settings any) error {
	data, err := readCredentialConfig(path, 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("Read Runtime service settings: %w", err)
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) || json.Unmarshal(data, settings) != nil {
		return fmt.Errorf("Invalid Runtime service settings in %s", filepath.Base(path))
	}
	return nil
}

func (s runtimeSession) credentialAccount() (string, error) {
	account := strings.TrimSpace(s.User)
	if sessionAlias.MatchString(s.Alias) {
		account = s.Alias
	}
	if account == "" {
		account = "local"
	}
	if len(account) > 512 || strings.ContainsAny(account, "\x00\r\n") {
		return "", errors.New("Original session credential account is invalid")
	}
	return account, nil
}

func (s runtimeSession) hostFactors(store hostCredentialStore) (super, pass, legacy *string, err error) {
	version := uint64(1)
	if s.Version != nil {
		version = *s.Version
	}
	account, err := s.credentialAccount()
	if err != nil {
		return nil, nil, nil, err
	}
	if s.Backend != "" && !store.supports(s.Backend) {
		return nil, nil, nil, errors.New("Unsupported Runtime session credential backend")
	}
	if version == 1 || version == 3 {
		if !store.supports(s.LegacyBackend) || !sessionAlias.MatchString(s.Alias) {
			return nil, nil, nil, errors.New("Original session requires historical credential migration")
		}
		legacy, err = store.readSlot(s.LegacyBackend, "rsrs", "legacy-secret:"+account, false)
		if err != nil {
			return
		}
		if legacy == nil || *legacy == "" {
			return nil, nil, nil, errors.New("Historical secret is missing from the system credential store")
		}
	}
	if version < 2 {
		pass = s.LegacyPass
		if pass == nil {
			pass, err = store.readSlot(s.Backend, "rsrs", "pass:"+account, true)
		}
	} else if version < 4 && s.LegacySuper != nil && *s.LegacySuper != "" {
		super = s.LegacySuper
	} else {
		super, err = store.readSlot(s.Backend, "rsrs", "super:"+account, false)
		if err == nil && version >= 4 && (super == nil || *super == "") {
			super = s.SecretKey
		}
	}
	if err == nil && (version < 2 && pass == nil || version >= 2 && (super == nil || *super == "")) {
		err = errors.New("Original session key is missing; Runtime remains locked")
	}
	return
}

func (m *Manager) runtimeUnlockToken() (string, error) {
	if token := strings.TrimSpace(os.Getenv("RSRS_RPC_TOKEN")); token != "" {
		return token, nil
	}
	path := filepath.Join(m.DataRoot, "runtime", "token")
	body, err := readCredentialConfig(path, 65536)
	if errors.Is(err, os.ErrNotExist) {
		var random [32]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		token := hex.EncodeToString(random[:])
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			body, err = readCredentialConfig(path, 65536)
			if err != nil {
				return "", err
			}
		} else if err != nil {
			return "", err
		} else {
			_, writeErr := file.WriteString(token)
			if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
				return "", err
			}
			return token, nil
		}
	} else if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(body))
	if token == "" || strings.ContainsAny(token, "\x00\r\n") {
		return "", errors.New("Runtime host authentication token is invalid")
	}
	return token, nil
}

func (m *Manager) nativeProviderCredentials(profile string, readSlot credentialReader) ([]runtimeProviderCredential, error) {
	credentials := make([]runtimeProviderCredential, 0)
	seen := make(map[[2]string]bool)
	read := func(service, account string) (string, error) {
		if service == "" || account == "" || len(service) > 512 || len(account) > 512 ||
			strings.ContainsAny(service+account, "\x00\r\n") {
			return "", providerCredentialError("invalid_configuration")
		}
		key := [2]string{service, account}
		if seen[key] {
			return "", nil
		}
		seen[key] = true
		value, err := readSlot(service, account)
		if err != nil || value == nil {
			return "", err
		}
		if *value != "" {
			credentials = append(credentials, runtimeProviderCredential{service, account, *value})
		}
		return *value, nil
	}
	hosts := []string{"api.deepseek.com"}
	for _, slot := range []string{"typesafe", "ds", "last-backend", "ds-last-base"} {
		value, err := read("rsrs", "classify:"+slot)
		if err != nil {
			return nil, err
		}
		if slot == "ds-last-base" {
			hosts = append(hosts, providerHost(value))
		}
	}
	agent, err := readCredentialConfig(filepath.Join(profile, "agent.json"), 1<<20)
	if err == nil {
		var config struct {
			RecallAPIBase string `json:"recall_api_base"`
		}
		if json.Unmarshal(agent, &config) != nil {
			return nil, providerCredentialError("invalid_configuration")
		}
		hosts = append(hosts, providerHost(config.RecallAPIBase))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, host := range hosts {
		if host == "" {
			continue
		}
		for _, slot := range []string{"ds@", "ds-model@"} {
			if _, err := read("rsrs", "classify:"+slot+host); err != nil {
				return nil, err
			}
		}
	}
	file, err := readCredentialConfig(filepath.Join(m.DataRoot, "embedding-profiles-v1.json"), 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return credentials, nil
	}
	if err != nil {
		return nil, err
	}
	var configuration any
	if json.Unmarshal(file, &configuration) != nil {
		return nil, providerCredentialError("invalid_configuration")
	}
	var collect func(any) error
	collect = func(value any) error {
		switch fields := value.(type) {
		case map[string]any:
			service, hasService := fields["keyring_service"]
			account, hasAccount := fields["keyring_account"]
			if hasService || hasAccount {
				s, serviceOK := service.(string)
				a, accountOK := account.(string)
				if !serviceOK || !accountOK {
					return providerCredentialError("invalid_configuration")
				}
				if _, err := read(s, a); err != nil {
					return err
				}
			}
			for _, item := range fields {
				if err := collect(item); err != nil {
					return err
				}
			}
		case []any:
			for _, item := range fields {
				if err := collect(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := collect(configuration); err != nil {
		return nil, err
	}
	return credentials, nil
}

type providerCredentialError string

func (e providerCredentialError) Error() string { return "Runtime credential status: " + string(e) }

func providerHost(base string) string {
	base = strings.TrimSpace(base)
	if _, value, found := strings.Cut(base, "://"); found {
		base = value
	}
	if index := strings.IndexAny(base, "/?#"); index >= 0 {
		base = base[:index]
	}
	return base
}
