package desktop

import (
	"encoding/json"
	"testing"

	"respire-client2/internal/host"
)

func TestPatchPreferencesKeepsUnsentFields(t *testing.T) {
	base := host.Preferences{}
	if err := json.Unmarshal([]byte(`{
		"autoRuntime": true, "autoUpdate": true,
		"runtimeChannel": "stable", "clientChannel": "stable",
		"layout": "sidebar", "theme": "dark", "language": "zh",
		"clientUpdateInterval": "6h", "runtimeUpdateInterval": "6h",
		"clientAutoUpdate": true, "clientAutoRestart": true,
		"runtimeAutoUpdate": true, "runtimeAutoRestart": true,
		"sidebarWidth": 240, "memoryIndexWidth": 260,
		"onboardingVersion": 1, "onboardingCompleted": false,
		"onboardingStep": "model", "onboardingSyncChoice": "cloud"
	}`), &base); err != nil {
		t.Fatal(err)
	}
	// A legacy settings form posts only the fields it renders.
	patched, err := patchPreferences(base, []byte(`{"theme":"light","layout":"compact"}`))
	if err != nil {
		t.Fatal(err)
	}
	if patched.Theme != "light" || patched.Layout != "compact" {
		t.Fatalf("patched fields must apply, got %+v", patched)
	}
	if !patched.RuntimeAutoRestart || !patched.RuntimeAutoUpdate {
		t.Fatalf("runtimeAutoRestart/runtimeAutoUpdate must survive the patch, got %+v", patched)
	}
	if patched.OnboardingStep != "model" || patched.OnboardingSyncChoice != "cloud" || !patched.ClientAutoUpdate {
		t.Fatalf("unrelated fields must survive the patch, got %+v", patched)
	}
}

func TestPatchPreferencesAppliesOnboardingFields(t *testing.T) {
	base := host.Preferences{OnboardingVersion: 1, OnboardingStep: "language"}
	patched, err := patchPreferences(base, []byte(`{"onboardingCompleted":true,"onboardingStep":"done","onboardingSyncChoice":"offline"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !patched.OnboardingCompleted || patched.OnboardingStep != "done" || patched.OnboardingSyncChoice != "offline" {
		t.Fatalf("onboarding patch failed, got %+v", patched)
	}
	if patched.OnboardingVersion != 1 || patched.OnboardingStep == "" {
		t.Fatalf("base fields must persist, got %+v", patched)
	}
}

func TestPatchPreferencesRejectsGarbage(t *testing.T) {
	if _, err := patchPreferences(host.Preferences{}, []byte(`{"sidebarWidth":"wide"}`)); err == nil {
		t.Fatal("type mismatch must fail")
	}
	if _, err := patchPreferences(host.Preferences{}, []byte(`not json`)); err == nil {
		t.Fatal("malformed body must fail")
	}
}
