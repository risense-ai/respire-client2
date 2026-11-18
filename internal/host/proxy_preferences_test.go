package host

import (
	"encoding/json"
	"testing"
)

func TestDefaultPreferencesFollowSystemProxy(t *testing.T) {
	prefs := defaultPreferences()
	if prefs.ProxyMode != ProxyModeSystem {
		t.Fatalf("new installs must follow the system proxy, got %q", prefs.ProxyMode)
	}
	if err := prefs.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMigratePreferencesDefaultsProxyMode(t *testing.T) {
	var stored map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"language":"zh","theme":"dark"}`), &stored); err != nil {
		t.Fatal(err)
	}
	prefs := MigratePreferences(Preferences{}, stored)
	if prefs.ProxyMode != ProxyModeSystem {
		t.Fatalf("preferences written before proxy support must default to system, got %q", prefs.ProxyMode)
	}
}

func TestMigratePreferencesKeepsDirectChoice(t *testing.T) {
	prefs, err := loadPreferences([]byte(`{"proxyMode":"direct"}`))
	if err != nil {
		t.Fatal(err)
	}
	if prefs.ProxyMode != ProxyModeDirect {
		t.Fatalf("an explicit direct mode must survive migration, got %q", prefs.ProxyMode)
	}
}

func TestLoadPreferencesDefaultsProxyModeWhenMissing(t *testing.T) {
	prefs, err := loadPreferences([]byte(`{"theme":"dark"}`))
	if err != nil {
		t.Fatal(err)
	}
	if prefs.ProxyMode != ProxyModeSystem {
		t.Fatalf("files without a proxy mode must default to system, got %q", prefs.ProxyMode)
	}
}

func TestValidateRejectsUnknownProxyMode(t *testing.T) {
	prefs := defaultPreferences()
	prefs.ProxyMode = "sometimes"
	if err := prefs.validate(); err == nil {
		t.Fatal("unknown proxy modes must be rejected")
	}
}
