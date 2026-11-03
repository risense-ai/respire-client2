package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPreferencesMissingFileStartsWizard(t *testing.T) {
	prefs, err := loadPreferencesFile(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	if prefs.OnboardingVersion != 1 || prefs.OnboardingCompleted {
		t.Fatalf("fresh install must enter the wizard, got %+v", prefs)
	}
	if prefs.OnboardingStep != "language" || prefs.OnboardingSyncChoice != "" {
		t.Fatalf("unexpected first-run step state: %+v", prefs)
	}
}

func TestLoadPreferencesLegacyFileMigratesToCompleted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	legacy := `{"language":"zh","theme":"dark","layout":"compact","runtimeChannel":"stable","clientChannel":"stable","clientUpdateInterval":"6h","runtimeUpdateInterval":"6h","sidebarWidth":220}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	prefs, err := loadPreferencesFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !prefs.OnboardingCompleted || prefs.OnboardingStep != "done" {
		t.Fatalf("upgraded user must not re-enter the wizard, got %+v", prefs)
	}
	if prefs.Language != "zh" || prefs.ClientAutoRestart != true || prefs.RuntimeAutoRestart != true {
		t.Fatalf("existing settings must survive migration, got %+v", prefs)
	}
}

func TestLoadPreferencesKeepsInFlightWizardState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	stored := `{"onboardingVersion":1,"onboardingCompleted":false,"onboardingStep":"model","onboardingSyncChoice":"cloud","theme":"light"}`
	if err := os.WriteFile(path, []byte(stored), 0o600); err != nil {
		t.Fatal(err)
	}
	prefs, err := loadPreferencesFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if prefs.OnboardingCompleted || prefs.OnboardingStep != "model" || prefs.OnboardingSyncChoice != "cloud" {
		t.Fatalf("wizard state must be preserved verbatim, got %+v", prefs)
	}
}

func TestImportLegacyBackupMigratesToCompleted(t *testing.T) {
	var stored map[string]json.RawMessage
	legacy := []byte(`{"language":"en","theme":"system"}`)
	if err := json.Unmarshal(legacy, &stored); err != nil {
		t.Fatal(err)
	}
	prefs := MigratePreferences(defaultPreferences(), stored)
	if !prefs.OnboardingCompleted || prefs.OnboardingStep != "done" {
		t.Fatalf("imported legacy settings must count as onboarded, got %+v", prefs)
	}
}

func TestValidateOnboardingEnums(t *testing.T) {
	base := defaultPreferences()
	base.OnboardingCompleted = true
	if err := base.validate(); err != nil {
		t.Fatal(err)
	}
	for _, broken := range []func(*Preferences){
		func(p *Preferences) { p.OnboardingStep = "somewhere" },
		func(p *Preferences) { p.OnboardingSyncChoice = "maybe" },
		func(p *Preferences) { p.OnboardingVersion = 2 },
	} {
		p := defaultPreferences()
		broken(&p)
		if err := p.validate(); err == nil {
			t.Fatalf("expected validation failure for %+v", p)
		}
	}
	// An in-progress wizard save must pass validation, including the
	// agent-tools step appended after execute.
	running := defaultPreferences()
	running.OnboardingStep = "account"
	running.OnboardingSyncChoice = "offline"
	if err := running.validate(); err != nil {
		t.Fatal(err)
	}
	agents := defaultPreferences()
	agents.OnboardingStep = "agents"
	agents.OnboardingSyncChoice = "cloud"
	if err := agents.validate(); err != nil {
		t.Fatalf("agents step must validate: %v", err)
	}
}

func TestMigratePreferencesNormalizesBrokenWizardFields(t *testing.T) {
	prefs, err := loadPreferences([]byte(`{"onboardingVersion":1,"onboardingCompleted":false,"onboardingStep":"bogus","onboardingSyncChoice":"bogus"}`))
	if err != nil {
		t.Fatal(err)
	}
	if prefs.OnboardingStep != "done" || prefs.OnboardingSyncChoice != "" {
		t.Fatalf("broken wizard fields must normalize, got %+v", prefs)
	}
}
