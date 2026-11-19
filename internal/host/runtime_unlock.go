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
	"strings"
)

type credentialReader func(service, account string) (*string, error)
type sessionMigrator func(context.Context, HealthInfo, string, []byte, runtimeSession, uint64) ([]byte, error)

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

func readCredentialConfig(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > max {
		return nil, errors.New("invalid credential configuration file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > max {
		return nil, errors.New("credential configuration exceeds the protocol limit")
	}
	return body, nil
}

func (m *Manager) unlockNativeRuntime(ctx context.Context, health HealthInfo, read credentialReader, migrate sessionMigrator) error {
	base, err := url.Parse(m.Endpoint)
	if err != nil || base.Scheme != "http" || base.Hostname() != "127.0.0.1" || base.Port() == "" ||
		base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Path != "" {
		return errors.New("Runtime unlock requires the local loopback endpoint")
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
	profile, err := filepath.EvalSymlinks(health.DataDir)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(m.DataRoot, profile)
	if err != nil || !filepath.IsLocal(relative) {
		return errors.New("Runtime unlock profile is outside the installation")
	}
	sessionPath := filepath.Join(profile, "session.json")
	original, err := readCredentialConfig(sessionPath, 65536)
	if err != nil {
		return fmt.Errorf("Read original Runtime session: %w", err)
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
		if migrate == nil {
			return errors.New("Historical session requires migration in the host credential store")
		}
		original, err = migrate(ctx, health, sessionPath, original, session, version)
		if err != nil {
			return err
		}
		session = runtimeSession{}
		if err := json.Unmarshal(original, &session); err != nil {
			return errors.New("Migrated Runtime session metadata is invalid")
		}
	}
	super, pass, legacy, err := session.nativeFactors(read)
	if err != nil {
		return err
	}
	providers, err := m.nativeProviderCredentials(profile, read)
	if err != nil {
		return fmt.Errorf("Read Runtime provider credentials: %w", err)
	}
	token, err := m.runtimeUnlockToken()
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"schema_version": 2, "profile": profile, "session_sha256": fmt.Sprintf("%x", sha256.Sum256(original)),
		"instance_id": health.InstanceID, "generation": health.Generation,
		"super_password": super, "login_password": pass, "legacy_secret": legacy,
		"provider_update": map[string]any{"kind": "replace", "credentials": providers},
	})
	if err != nil || len(body) > 65536 {
		return errors.New("Runtime unlock frame exceeds its protocol limit")
	}
	defer clear(body)
	current, _, err := m.Health(ctx)
	if err != nil {
		return err
	}
	if current != health {
		return errors.New("Runtime changed before credential delivery")
	}
	base.Path = "/api/runtime/unlock"
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
		} `json:"provider_status"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&receipt); err != nil ||
		receipt.Schema != 2 || !receipt.StoreReady || receipt.Provider.State != "ready" {
		return errors.New("Runtime did not confirm library unlock and provider readiness")
	}
	return nil
}

func (s runtimeSession) nativeCredentialAccount() (string, error) {
	account := strings.TrimSpace(s.User)
	if sessionAlias.MatchString(s.Alias) {
		account = s.Alias
	}
	if account == "" {
		account = "local"
	}
	if len(account) > 512 || strings.ContainsAny(account, "\x00\r\n") ||
		(s.Backend != "" && s.Backend != "native") {
		return "", errors.New("Original session native credential metadata is invalid")
	}
	return account, nil
}

func (s runtimeSession) nativeFactors(read credentialReader) (super, pass, legacy *string, err error) {
	version := uint64(1)
	if s.Version != nil {
		version = *s.Version
	}
	account, err := s.nativeCredentialAccount()
	if err != nil {
		return nil, nil, nil, err
	}
	if version == 1 || version == 3 {
		if s.LegacyBackend != "native" || !sessionAlias.MatchString(s.Alias) {
			return nil, nil, nil, errors.New("Original session requires historical credential migration")
		}
		legacy, err = read("rsrs", "legacy-secret:"+account)
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
			pass, err = read("rsrs", "pass:"+account)
		}
	} else if version < 4 && s.LegacySuper != nil && *s.LegacySuper != "" {
		super = s.LegacySuper
	} else {
		super, err = read("rsrs", "super:"+account)
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
			return "", errors.New("invalid Runtime provider credential reference")
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
			return nil, errors.New("invalid Runtime provider agent configuration")
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
		return nil, errors.New("invalid Runtime embedding credential configuration")
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
					return errors.New("invalid Runtime embedding credential reference")
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
