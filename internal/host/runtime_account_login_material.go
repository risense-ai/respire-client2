//go:build linux || windows || (darwin && cgo)

package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func readRuntimeLoginMaterial(ctx context.Context, profile string) (json.RawMessage, error) {
	raw, err := readCredentialConfig(filepath.Join(profile, "session.json"), controlFrameLimit)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	var session runtimeSession
	if json.Unmarshal(raw, &session) != nil {
		return nil, errors.New("Invalid original Runtime account session")
	}
	if session.Version == nil {
		return nil, nil
	}
	version := *session.Version
	if version < 1 || version > 4 {
		return nil, errors.New("Unsupported original Runtime vault version")
	}
	store := nativeRuntimeCredentialStore(ctx)
	var super, pass, legacy *string
	if (version == 1 || version == 3) && session.LegacyBackend == "" {
		account, err := session.credentialAccount()
		if err != nil {
			return nil, err
		}
		if version == 1 {
			pass, legacy = session.LegacyPass, session.LegacySecret
			if pass == nil {
				pass, err = store.readSlot(session.Backend, "rsrs", "pass:"+account, true)
			}
		} else {
			super, legacy = session.LegacySuper, session.SecretKey
			if super == nil {
				super, err = store.readSlot(session.Backend, "rsrs", "super:"+account, false)
			}
		}
		if err != nil {
			return nil, err
		}
	} else {
		super, pass, legacy, err = session.hostFactors(store)
		if err != nil {
			return nil, err
		}
	}
	factors := map[string]string{"version": fmt.Sprintf("v%d", version)}
	if version == 1 {
		if pass == nil || legacy == nil || *legacy == "" {
			return nil, errors.New("Original Runtime v1 credentials are missing")
		}
		factors["password"], factors["account_secret"] = *pass, *legacy
	} else {
		if super == nil || *super == "" {
			return nil, errors.New("Original Runtime super key is missing")
		}
		factors["super_password"] = *super
		if version == 3 {
			if legacy == nil || *legacy == "" {
				return nil, errors.New("Original Runtime v3 Secret Key is missing")
			}
			factors["secret_key"] = *legacy
		}
	}
	return json.Marshal(map[string]any{"session_sha256": controlDigest(raw), "factors": factors})
}

func readRuntimeLoginTargetMaterial(ctx context.Context, root, source, user, server string) (json.RawMessage, error) {
	directories := []string{source, root}
	accounts := filepath.Join(root, "accounts")
	entries, err := os.ReadDir(accounts)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			directories = append(directories, filepath.Join(accounts, entry.Name()))
		}
	}
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	for _, directory := range directories {
		raw, err := readCredentialConfig(filepath.Join(directory, "session.json"), controlFrameLimit)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var identity struct {
			User   string `json:"user"`
			Server string `json:"addr"`
		}
		err = json.Unmarshal(raw, &identity)
		clear(raw)
		if err != nil {
			return nil, errors.New("Invalid existing Runtime account session")
		}
		if identity.User == user && strings.TrimRight(strings.TrimSpace(identity.Server), "/") == server {
			return readRuntimeLoginMaterial(ctx, directory)
		}
	}
	return nil, nil
}
