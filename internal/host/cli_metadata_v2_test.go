package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Mirrors stage-artifacts.mjs's frontend-only metadata, not a Runtime bundle.
func frontendMetadataFixture(target string) map[string]any {
	return map[string]any{
		"schema_version":  2,
		"artifact_layout": "cli-frontend-v2",
		"version":         "2.0.0-dev.1",
		"target":          target,
		"git_sha":         strings.Repeat("ab", 20),
		"binary_file":     "rsrs-" + target + binaryExtension(),
		"binary_sha256":   strings.Repeat("1", 64),
		"binary_size":     12345,
		"producer":        map[string]any{"run_id": "42809360720", "run_attempt": 1},
	}
}

func TestNormalizeCLIFrontendMetadata(t *testing.T) {
	target, err := runtimeTarget()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(frontendMetadataFixture(target))
	meta, err := NormalizeCLIMetadata(body, "v2.0.0-dev.1", target)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Schema != 2 || meta.Runtime != "" || meta.RuntimeHash != "" || meta.BinarySize != 12345 {
		t.Fatalf("frontend metadata gained a Runtime payload: %+v", meta)
	}
	cases := []struct {
		name, field string
		value       any
		want        string
	}{
		{"negative size", "binary_size", -1, "CLI release metadata mismatch"},
		{"null size", "binary_size", json.RawMessage(`null`), "CLI release metadata mismatch"},
		{"runtime hash contamination", "runtime_sha256", strings.Repeat("2", 64), "CLI release metadata mismatch"},
		{"short source", "git_sha", strings.Repeat("a", 39), "invalid CLI source commit"},
		{"long source", "git_sha", strings.Repeat("a", 41), "invalid CLI source commit"},
		{"Runtime role swap", "binary_file", "rsrs-runtime-" + target + binaryExtension(), "CLI release metadata mismatch"},
		{"null producer", "producer", json.RawMessage(`null`), "CLI release metadata mismatch"},
		{"array producer", "producer", []any{}, "CLI release metadata mismatch"},
		{"missing producer key", "producer", map[string]any{"run_id": "1"}, "CLI release metadata mismatch"},
		{"negative run_id", "producer", map[string]any{"run_id": -1, "run_attempt": 1}, "CLI release metadata mismatch"},
		{"fractional run_id", "producer", map[string]any{"run_id": 1.5, "run_attempt": 1}, "CLI release metadata mismatch"},
		{"boolean run_id", "producer", map[string]any{"run_id": true, "run_attempt": 1}, "CLI release metadata mismatch"},
		{"null run_id", "producer", map[string]any{"run_id": nil, "run_attempt": 1}, "CLI release metadata mismatch"},
		{"negative run_attempt", "producer", map[string]any{"run_id": "1", "run_attempt": -1}, "CLI release metadata mismatch"},
		{"fractional run_attempt", "producer", map[string]any{"run_id": "1", "run_attempt": 1.5}, "CLI release metadata mismatch"},
		{"boolean run_attempt", "producer", map[string]any{"run_id": "1", "run_attempt": true}, "CLI release metadata mismatch"},
		{"null run_attempt", "producer", map[string]any{"run_id": "1", "run_attempt": nil}, "CLI release metadata mismatch"},
		{"unknown schema", "schema_version", 3, "CLI release metadata mismatch"},
		{"missing schema", "schema_version", nil, "CLI release metadata mismatch"},
		{"wrong layout", "artifact_layout", "combined", "CLI release metadata mismatch"},
		{"missing layout", "artifact_layout", nil, "CLI release metadata mismatch"},
		{"wrong version", "version", "1.0.20", "CLI release metadata mismatch"},
		{"wrong target", "target", "wrong", "CLI release metadata mismatch"},
		{"wrong binary", "binary_file", "../rsrs", "CLI release metadata mismatch"},
		{"invalid hash", "binary_sha256", "not-a-sha256", "CLI release metadata mismatch"},
		{"missing hash", "binary_sha256", nil, "CLI release metadata mismatch"},
		{"invalid source", "git_sha", strings.Repeat("z", 40), "invalid CLI source commit"},
		{"missing source", "git_sha", nil, "CLI release metadata mismatch"},
		{"empty size", "binary_size", 0, "CLI release metadata mismatch"},
		{"unsafe size", "binary_size", int64(9007199254740992), "CLI release metadata mismatch"},
		{"bundled runtime", "runtime_file", "old-runtime.tar.gz", "CLI release metadata mismatch"},
		{"empty legacy alias", "runtime_sha256", "", "CLI release metadata mismatch"},
		{"unknown field", "unexpected", true, "CLI release metadata mismatch"},
		{"missing producer", "producer", nil, "CLI release metadata mismatch"},
		{"zero attempt", "producer", map[string]any{"run_id": "1", "run_attempt": 0}, "CLI release metadata mismatch"},
		{"invalid run", "producer", map[string]any{"run_id": "01", "run_attempt": 1}, "CLI release metadata mismatch"},
		{"unsafe run", "producer", map[string]any{"run_id": "9007199254740992", "run_attempt": 1}, "CLI release metadata mismatch"},
		{"extra producer field", "producer", map[string]any{"run_id": "1", "run_attempt": 1, "extra": true}, "CLI release metadata mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := frontendMetadataFixture(target)
			if tc.value == nil {
				delete(fixture, tc.field)
			} else {
				fixture[tc.field] = tc.value
			}
			body, _ := json.Marshal(fixture)
			if _, err := NormalizeCLIMetadata(body, "v2.0.0-dev.1", target); err == nil || err.Error() != tc.want {
				t.Fatalf("got %v; want %q", err, tc.want)
			}
		})
	}
}

type cliMetadataTransport func(*http.Request) (*http.Response, error)

func (f cliMetadataTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestLatestCLIBuildFrontendMetadata(t *testing.T) {
	target, err := runtimeTarget()
	if err != nil {
		t.Fatal(err)
	}
	name := "cli-build-" + target + ".json"
	url := "https://github.com/risense-ai/respire-cli/releases/download/v2.0.0-dev.1/" + name
	for _, foreign := range []bool{false, true} {
		assetURL := url
		if foreign {
			assetURL = "https://example.com/" + name
		}
		metadataReads := 0
		transport := cliMetadataTransport(func(r *http.Request) (*http.Response, error) {
			var data []byte
			switch r.URL.String() {
			case "https://api.github.com/repos/risense-ai/respire-cli/releases?per_page=20":
				data, _ = json.Marshal([]ReleaseInfo{{
					Tag:        "v2.0.0-dev.1",
					Prerelease: true,
					Assets:     []ReleaseAsset{{Name: name, URL: assetURL}},
				}})
			case url:
				metadataReads++
				data, _ = json.Marshal(frontendMetadataFixture(target))
			default:
				t.Fatalf("unexpected request: %s", r.URL)
			}
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(string(data))),
				Header:     make(http.Header),
			}, nil
		})
		m := &Manager{
			prefs:      defaultPreferences(),
			HTTPClient: &http.Client{Transport: transport},
		}
		_, meta, err := m.LatestCLIBuild(context.Background())
		if foreign {
			if err == nil || metadataReads != 0 {
				t.Fatalf("foreign source accepted: %v", err)
			}
		} else if err != nil || meta.Schema != 2 || metadataReads != 1 {
			t.Fatalf("resolver: %+v %v", meta, err)
		}
	}
}

func TestInstallFrontendCLIFromNormalizedMetadata(t *testing.T) {
	buildPayload(t, "2.0.0-dev.1")
	payload, err := readBootstrapPayload()
	if err != nil {
		t.Fatal(err)
	}
	target, err := runtimeTarget()
	if err != nil {
		t.Fatal(err)
	}
	fixture := frontendMetadataFixture(target)
	fixture["binary_sha256"] = payload.Manifest.CLI.SHA256
	body, _ := json.Marshal(fixture)
	meta, err := NormalizeCLIMetadata(body, "v2.0.0-dev.1", target)
	if err != nil {
		t.Fatal(err)
	}
	// The bootstrap fetch plan maps only these CLI fields. Runtime ownership
	// and compatibility stay with respire-runtime, never a CLI archive.
	payload.Manifest.CLI.Version = meta.Version
	payload.Manifest.CLI.SHA256 = meta.BinaryHash
	payload.Manifest.Runtime.Version = "2.0.0-dev.6"
	m := newInstallTestManager(t)
	stubCLIProbe(t, cliProbeFixture{tmpVersion: meta.Version, destAbsent: true})
	if err := m.installBootstrapCLI(context.Background(), &payload); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyCLIMetadataStillRequiresRuntimeArchive(t *testing.T) {
	target, err := runtimeTarget()
	if err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{"runtime_file", "runtime_sha256"} {
		fixture := frontendMetadataFixture(target)
		fixture["schema_version"] = 1
		delete(fixture, "artifact_layout")
		delete(fixture, "binary_size")
		delete(fixture, "producer")
		fixture["runtime_file"] = "rsrs-" + target + "-runtime.tar.gz"
		fixture["runtime_sha256"] = strings.Repeat("2", 64)
		delete(fixture, missing)
		body, _ := json.Marshal(fixture)
		if _, err := NormalizeCLIMetadata(body, "v2.0.0-dev.1", target); err == nil || err.Error() != "CLI release metadata mismatch" {
			t.Fatalf("legacy metadata missing %s: %v", missing, err)
		}
	}
}

func TestNormalizeCLIFrontendMetadataRejectsMalformedJSON(t *testing.T) {
	target, err := runtimeTarget()
	if err != nil {
		t.Fatal(err)
	}
	_, err = NormalizeCLIMetadata([]byte(`{"schema_version":`), "v2.0.0-dev.1", target)
	var syntaxError *json.SyntaxError
	if !errors.As(err, &syntaxError) {
		t.Fatalf("malformed JSON must produce a syntax error, got %v", err)
	}
	cases := []struct {
		name, field string
		value       any
	}{
		{"string schema", "schema_version", "2"},
		{"array target", "target", []any{}},
		{"numeric source", "git_sha", 123},
		{"boolean hash", "binary_sha256", true},
		{"string size", "binary_size", "12345"},
		{"fractional size", "binary_size", 1.5},
		{"boolean size", "binary_size", true},
		{"overflow size", "binary_size", json.RawMessage(`9223372036854775808`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := frontendMetadataFixture(target)
			fixture[tc.field] = tc.value
			body, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			_, err = NormalizeCLIMetadata(body, "v2.0.0-dev.1", target)
			var typeError *json.UnmarshalTypeError
			if !errors.As(err, &typeError) || typeError.Field != tc.field {
				t.Fatalf("invalid %s must produce its JSON type error, got %v", tc.field, err)
			}
		})
	}
}
