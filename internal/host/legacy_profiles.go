package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const currentSessionNamespace = "rsrs:v1:"

type legacyMigrationProfile struct {
	SourceID   string  `json:"source_id"`
	Source     string  `json:"source"`
	User       string  `json:"user"`
	Account    string  `json:"account"`
	MigratedTo *string `json:"migrated_to"`
}

type legacyMigrationReceipt struct {
	SourceID    string `json:"source_identity"`
	Destination string `json:"destination"`
	Complete    bool   `json:"complete"`
}

func readLegacyMetadata(path string, value any) error {
	raw, err := readCredentialConfig(path, 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer clear(raw)
	if err := json.Unmarshal(raw, value); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func profileSubdirectories(path string) ([]string, error) {
	if err := profileDirectory(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var directories []string
	for _, entry := range entries {
		directory := filepath.Join(path, entry.Name())
		if err := profileDirectory(directory); err != nil {
			return nil, err
		}
		if entry.IsDir() {
			directories = append(directories, directory)
		}
	}
	return directories, nil
}

func legacySourceID(profile string) (string, error) {
	canonical, err := canonicalSessionDirectory(profile)
	if err != nil {
		return "", err
	}
	normalized := []byte(filepath.ToSlash(canonical))
	if runtime.GOOS == "windows" {
		for i, char := range normalized {
			if 'A' <= char && char <= 'Z' {
				normalized[i] = char + ('a' - 'A')
			}
		}
	}
	return controlDigest(normalized), nil
}

func (m *Manager) legacyMigrationProfiles() ([]legacyMigrationProfile, error) {
	home, err := deviceConfigHome()
	if err != nil {
		return nil, err
	}
	if err := profileDirectory(home); err != nil {
		return nil, err
	}
	if err := profileDirectory(m.DataRoot); err != nil {
		return nil, err
	}
	accounts, err := profileSubdirectories(filepath.Join(m.DataRoot, "accounts"))
	if err != nil {
		return nil, err
	}
	destinations := append([]string{m.DataRoot}, accounts...)
	profiles := []legacyMigrationProfile{}
	for _, label := range []string{"respire", "onememory", "rsrs"} {
		homeRoot := filepath.Join(home, "."+label)
		if err := profileDirectory(homeRoot); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		candidates, err := profileSubdirectories(filepath.Join(homeRoot, "accounts"))
		if err != nil {
			return nil, err
		}
		for _, profile := range append([]string{homeRoot}, candidates...) {
			legacyDB, err := profileFile(filepath.Join(profile, "onememory.db"))
			if err != nil {
				return nil, err
			}
			var session *struct {
				User            string `json:"user"`
				CryptoNamespace string `json:"crypto_namespace"`
				WrappedURK      string `json:"wrapped_urk"`
			}
			if err := readLegacyMetadata(filepath.Join(profile, "session.json"), &session); err != nil {
				return nil, err
			}
			if session == nil && !legacyDB {
				continue
			}
			user := ""
			if session != nil {
				if session.CryptoNamespace == currentSessionNamespace || strings.HasPrefix(session.WrappedURK, currentSessionNamespace) {
					continue
				}
				if label == "rsrs" && !legacyDB && session.WrappedURK == "" {
					continue
				}
				user = session.User
			}
			sourceID, err := legacySourceID(profile)
			if err != nil {
				return nil, err
			}
			name := "main"
			if profile != homeRoot {
				name = filepath.Base(profile)
			}
			name = strings.Map(func(ch rune) rune {
				if ('a' <= ch && ch <= 'z') || ('A' <= ch && ch <= 'Z') || ('0' <= ch && ch <= '9') || ch == '-' || ch == '_' {
					return ch
				}
				return '-'
			}, name)
			row := legacyMigrationProfile{SourceID: sourceID, Source: profile, User: user, Account: "imported-" + sourceID[:8] + "-" + name}
			receipt := legacyMigrationReceipt{}
			if err := readLegacyMetadata(filepath.Join(profile, ".rsrs-migrated.json"), &receipt); err != nil {
				return nil, err
			}
			if receipt.Complete && receipt.SourceID == sourceID && receipt.Destination != "" {
				row.MigratedTo = &receipt.Destination
			} else {
				for _, destination := range destinations {
					receipt = legacyMigrationReceipt{}
					if err := readLegacyMetadata(filepath.Join(destination, ".rsrs-migration.json"), &receipt); err != nil {
						return nil, err
					}
					if receipt.Complete && receipt.SourceID == sourceID {
						row.MigratedTo = &destination
						break
					}
				}
			}
			profiles = append(profiles, row)
		}
	}
	return profiles, nil
}

func (m *Manager) desktopLegacyProfiles() (json.RawMessage, error) {
	profiles, err := m.legacyMigrationProfiles()
	if err != nil {
		return nil, err
	}
	details := map[string]any{"profiles": profiles}
	return json.Marshal(map[string]any{"command": "migrate", "status": "ok", "summary": details, "details": details})
}
