package desktop

import (
	"encoding/json"
	"testing"

	"respire-client2/internal/host"
)

// The POST /api/* shape check must carry one JSON field per patchable
// preference key: the completion step of the onboarding wizard posts only
// onboarding keys, and a shape check without them rejects a valid patch
// with `json: unknown field "onboardingCompleted"`.
func TestDecodeActionBodyAcceptsEveryPreferenceKey(t *testing.T) {
	var prefs host.Preferences
	targets := preferencePatchTargets(&prefs)
	body := map[string]any{}
	for _, key := range preferencePatchKeys {
		switch targets[key].(type) {
		case *bool:
			body[key] = true
		case *int:
			body[key] = 1
		case *string:
			body[key] = "x"
		default:
			t.Fatalf("preference key %s has unsupported target type %T", key, targets[key])
		}
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeActionBody(data); err != nil {
		t.Fatalf("shape check rejected the full preference patch: %v", err)
	}
}

func TestDecodeActionBodyAcceptsWizardCompletionPatch(t *testing.T) {
	input, err := decodeActionBody([]byte(`{"onboardingCompleted":true,"onboardingStep":"done"}`))
	if err != nil {
		t.Fatalf("wizard completion patch must pass the shape check: %v", err)
	}
	if !input.OnboardingCompleted || input.OnboardingStep != "done" {
		t.Fatalf("decoded fields mismatch, got %+v", input)
	}
}

func TestDecodeActionBodyStillRejectsUnknownAndTrailing(t *testing.T) {
	if _, err := decodeActionBody([]byte(`{"noSuchField":1}`)); err == nil {
		t.Fatal("unknown fields must stay rejected")
	}
	if _, err := decodeActionBody([]byte(`{"theme":"dark"} {"theme":"light"}`)); err == nil {
		t.Fatal("trailing JSON objects must stay rejected")
	}
}
