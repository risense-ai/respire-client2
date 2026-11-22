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
		"runtimeChannel": "stable", "clientChannel": "stable", "cliChannel": "dev",
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
	patched, err := patchPreferences(base, []byte(`{"theme":"light","layout":"compact","runtimeChannel":"dev"}`))
	if err != nil {
		t.Fatal(err)
	}
	if patched.Theme != "light" || patched.Layout != "compact" {
		t.Fatalf("patched fields must apply, got %+v", patched)
	}
	if patched.RuntimeChannel != "dev" || patched.CLIChannel != "dev" || patched.ClientChannel != "stable" {
		t.Fatalf("channel patch must only change the requested component, got %+v", patched)
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

func TestPreferencePatchContracts(t *testing.T) {
	targets := preferencePatchTargets(&host.Preferences{})
	if len(targets) != len(preferencePatchKeys) {
		t.Fatalf("preference key / field count: %d / %d", len(preferencePatchKeys), len(targets))
	}
	for _, key := range preferencePatchKeys {
		if _, exists := targets[key]; !exists {
			t.Fatalf("no preference patch target for %s", key)
		}
		body := []byte(`{"` + key + `":null}`)
		if _, err := decodeActionBody(body); err != nil {
			t.Fatalf("strict action parser rejects %q: %v", key, err)
		}
	}
	old := host.Preferences{ModelMirror: "auto", Theme: "dark"}
	for _, patch := range []string{"auto", "https://mirror.example/weights", "mirror.example"} {
		t.Run(patch, func(t *testing.T) {
			body, err := json.Marshal(map[string]string{"modelMirror": patch})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeActionBody(body); err != nil {
				t.Fatal(err)
			}
			got, err := patchPreferences(old, body)
			if err != nil || got.ModelMirror != patch || got.Theme != old.Theme {
				t.Fatalf("did not round trip the mirror patch: %+v, %v", got, err)
			}
		})
	}
	for _, patch := range []string{`{"modelMiror":"auto"}`, `{"extra":true}`} {
		if _, err := decodeActionBody([]byte(patch)); err == nil {
			t.Fatalf("strict parser accepted %s", patch)
		}
	}
	for _, invalid := range []string{"file:///tmp/cache", "https://user:secret@mirror.example", "https://mirror.example?key=value"} {
		body, err := json.Marshal(map[string]string{"modelMirror": invalid})
		if err != nil {
			t.Fatal(err)
		}
		got, err := patchPreferences(old, body)
		if err == nil || got != old {
			t.Fatalf("invalid preference changed settings: %+v, %v", got, err)
		}
	}
}
