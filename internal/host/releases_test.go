package host

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestStableChannelMissingOnlyOn404(t *testing.T) {
	if !stableChannelMissing(&httpStatusError{code: http.StatusNotFound}) {
		t.Fatal("404 must count as a missing stable release")
	}
	if stableChannelMissing(&httpStatusError{code: http.StatusForbidden}) {
		t.Fatal("non-404 statuses must not count")
	}
	if stableChannelMissing(errors.New("connect refused")) {
		t.Fatal("non-status errors must not count")
	}
	// The raw transport text stays stable: diagnostics quote it verbatim.
	err := error(&httpStatusError{code: http.StatusNotFound})
	if err.Error() != "release source HTTP 404" {
		t.Fatalf("status error text changed: %q", err.Error())
	}
}

func TestDefaultRuntimeChannelIsDev(t *testing.T) {
	// The stable runtime train is gated on G1 acceptance; until a full
	// release exists, /releases/latest 404s and a stable default would
	// block every fresh install. Revert this default when stable opens.
	prefs := defaultPreferences()
	if err := prefs.validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	if prefs.RuntimeChannel != "dev" {
		t.Fatalf("fresh installs must default to dev while stable is empty, got %q", prefs.RuntimeChannel)
	}
}

func TestNormalizeRuntimeMetadataAcceptsBothSchemas(t *testing.T) {
	target, targetErr := runtimeTarget()
	if targetErr != nil {
		t.Fatalf("runtime target: %v", targetErr)
	}
	binary := "rsrs-runtime-" + target + binaryExtension()
	archive := runtimeArchive(target)
	v2 := `{"schema_version":2,"runtime_version":"1.0.21-dev.2","tag":"v1.0.21-dev.2",` +
		`"target_triple":"` + target + `","archive_file":"` + archive + `",` +
		`"archive_sha256":"` + strings.Repeat("1", 64) + `","binary_file":"` + binary + `",` +
		`"binary_sha256":"` + strings.Repeat("2", 64) + `",` +
		`"compat":{"min_cli":"1.0.21-dev.5","max_cli":"*"}}`
	meta, err := NormalizeRuntimeMetadata([]byte(v2))
	if err != nil {
		t.Fatalf("schema 2 must normalize: %v", err)
	}
	if meta.Target != target || meta.ArchiveFile != archive || meta.Binary != binary ||
		meta.BinaryHash != strings.Repeat("2", 64) || meta.ArchiveHash != strings.Repeat("1", 64) {
		t.Fatalf("schema 2 normalization drifted: %+v", meta)
	}
	if err = CheckRuntimeMetadata(meta, "v1.0.21-dev.2", target); err != nil {
		t.Fatalf("canonical schema 2 must pass identity checks: %v", err)
	}
	// The published v1 shape carries the legacy aliases; per the runtime
	// control contract duplicated equal aliases are accepted.
	v1 := `{"schema_version":1,"runtime_version":"1.0.21-dev.1","tag":"v1.0.21-dev.1",` +
		`"triple":"` + target + `","target_triple":"` + target + `",` +
		`"sha256":"` + strings.Repeat("3", 64) + `","archive_sha256":"` + strings.Repeat("3", 64) + `",` +
		`"binary_file":"` + binary + `","binary_sha256":"` + strings.Repeat("2", 64) + `",` +
		`"compat":{"min_cli":"1.0.20","max_cli":"*"}}`
	meta, err = NormalizeRuntimeMetadata([]byte(v1))
	if err != nil {
		t.Fatalf("schema 1 must normalize: %v", err)
	}
	if meta.Target != target || meta.ArchiveFile != archive || meta.ArchiveHash != strings.Repeat("3", 64) {
		t.Fatalf("schema 1 normalization drifted: %+v", meta)
	}
	if err = CheckRuntimeMetadata(meta, "v1.0.21-dev.1", target); err != nil {
		t.Fatalf("legacy schema 1 must pass identity checks: %v", err)
	}
}

func TestNormalizeRuntimeMetadataRejectsUnknownSchema(t *testing.T) {
	for _, body := range []string{
		`{"schema_version":3,"runtime_version":"1.0.0","tag":"v1.0.0"}`,
		`{"runtime_version":"1.0.0","tag":"v1.0.0"}`,
	} {
		if _, err := NormalizeRuntimeMetadata([]byte(body)); err == nil {
			t.Fatalf("schema %q must be rejected", body)
		}
	}
}

func TestNormalizeRuntimeMetadataRejectsConflictingAliases(t *testing.T) {
	target, targetErr := runtimeTarget()
	if targetErr != nil {
		t.Fatalf("runtime target: %v", targetErr)
	}
	other := "x86_64-unknown-linux-gnu"
	if other == target {
		other = "aarch64-unknown-linux-gnu"
	}
	cases := []string{
		// schema 1 with canonical aliases that disagree
		`{"schema_version":1,"runtime_version":"1.0.0","tag":"v1.0.0","triple":"` + target + `","target_triple":"` + other + `","sha256":"` + strings.Repeat("a", 64) + `","binary_file":"b","binary_sha256":"` + strings.Repeat("2", 64) + `"}`,
		`{"schema_version":1,"runtime_version":"1.0.0","tag":"v1.0.0","triple":"` + target + `","sha256":"` + strings.Repeat("a", 64) + `","archive_sha256":"` + strings.Repeat("b", 64) + `","binary_file":"b","binary_sha256":"` + strings.Repeat("2", 64) + `"}`,
		`{"schema_version":1,"runtime_version":"1.0.0","tag":"v1.0.0","triple":"` + target + `","sha256":"` + strings.Repeat("a", 64) + `","archive_file":"weird.tar.gz","binary_file":"b","binary_sha256":"` + strings.Repeat("2", 64) + `"}`,
		// schema 1 missing required legacy fields
		`{"schema_version":1,"runtime_version":"1.0.0","tag":"v1.0.0","triple":"` + target + `","binary_file":"b","binary_sha256":"` + strings.Repeat("2", 64) + `"}`,
		// schema 2 with disagreeing legacy aliases
		`{"schema_version":2,"runtime_version":"1.0.0","tag":"v1.0.0","target_triple":"` + target + `","archive_file":"` + runtimeArchive(target) + `","archive_sha256":"` + strings.Repeat("1", 64) + `","binary_file":"b","binary_sha256":"` + strings.Repeat("2", 64) + `","sha256":"` + strings.Repeat("9", 64) + `"}`,
		// schema 2 missing canonical identity
		`{"schema_version":2,"runtime_version":"1.0.0","tag":"v1.0.0","binary_file":"b","binary_sha256":"` + strings.Repeat("2", 64) + `"}`,
	}
	for _, body := range cases {
		if _, err := NormalizeRuntimeMetadata([]byte(body)); err == nil {
			t.Fatalf("conflicting metadata must be rejected: %s", body)
		}
	}
}

func TestCheckRuntimeMetadataRejectsIdentityAndHashDrift(t *testing.T) {
	target, targetErr := runtimeTarget()
	if targetErr != nil {
		t.Fatalf("runtime target: %v", targetErr)
	}
	base := RuntimeMetadata{
		Schema: 2, RuntimeVersion: "1.2.3", Tag: "v1.2.3", Target: target,
		ArchiveFile: runtimeArchive(target), ArchiveHash: strings.Repeat("1", 64),
		Binary: "rsrs-runtime-" + target + binaryExtension(), BinaryHash: strings.Repeat("2", 64),
		Compat: Compat{MinCLI: "1.0.0", MaxCLI: "*"},
	}
	if err := CheckRuntimeMetadata(base, "v1.2.3", target); err != nil {
		t.Fatalf("baseline must pass: %v", err)
	}
	wrong := base
	wrong.Tag = "v1.2.4"
	if err := CheckRuntimeMetadata(wrong, "v1.2.3", target); err == nil {
		t.Fatal("tag drift must be rejected")
	}
	wrong = base
	wrong.Target = "x86_64-pc-windows-msvc"
	if wrong.Target != target && CheckRuntimeMetadata(wrong, "v1.2.3", target) == nil {
		t.Fatal("wrong triple must be rejected")
	}
	wrong = base
	wrong.BinaryHash = "zz"
	if err := CheckRuntimeMetadata(wrong, "v1.2.3", target); err == nil {
		t.Fatal("invalid hash must be rejected")
	}
	wrong = base
	wrong.Compat.MaxCLI = "not-a-version"
	if err := CheckRuntimeMetadata(wrong, "v1.2.3", target); err == nil {
		t.Fatal("invalid compat.max_cli must be rejected")
	}
	wrong = base
	wrong.RuntimeVersion = "1.2.4"
	if err := CheckRuntimeMetadata(wrong, "v1.2.3", target); err == nil {
		t.Fatal("runtime_version/tag divergence must be rejected")
	}
}

func TestSelectReleaseByChannel(t *testing.T) {
	list := []ReleaseInfo{
		{Tag: "v1.0.0", Assets: []ReleaseAsset{{Name: "a", URL: "https://example.com/a"}}},
		{Tag: "v1.1.0-dev.1", Prerelease: true},
		{Tag: "v1.0.5"},
		{Tag: "v9.9.9", Draft: true},
		{Tag: "not-semver"},
	}
	stable, err := SelectRelease(list, "stable")
	if err != nil || stable.Tag != "v1.0.5" {
		t.Fatalf("stable must pick the newest full release, got %+v err=%v", stable, err)
	}
	dev, err := SelectRelease(list, "dev")
	if err != nil || dev.Tag != "v1.1.0-dev.1" {
		t.Fatalf("dev must pick the newest prerelease, got %+v err=%v", dev, err)
	}
	if _, err = SelectRelease([]ReleaseInfo{list[1], list[3], list[4]}, "stable"); err == nil {
		t.Fatal("stable with only prereleases and drafts must fail")
	}
}

func TestCheckCLICompat(t *testing.T) {
	compat := Compat{MinCLI: "1.0.20", MaxCLI: "2.0.0"}
	if err := CheckCLICompat("1.5.0", compat); err != nil {
		t.Fatalf("in-range CLI must pass: %v", err)
	}
	if err := CheckCLICompat("1.0.19", compat); err == nil {
		t.Fatal("CLI below min must fail")
	}
	if err := CheckCLICompat("2.0.1", compat); err == nil {
		t.Fatal("CLI above max must fail")
	}
	if err := CheckCLICompat("99.0.0", Compat{MinCLI: "1.0.0", MaxCLI: "*"}); err != nil {
		t.Fatalf("max_cli * must be unbounded: %v", err)
	}
	if err := CheckCLICompat("garbage", compat); err == nil {
		t.Fatal("unparseable CLI version must fail")
	}
}

func TestNormalizeCLIMetadataValidatesIdentity(t *testing.T) {
	target, targetErr := runtimeTarget()
	if targetErr != nil {
		t.Fatalf("runtime target: %v", targetErr)
	}
	body := `{"schema_version":1,"version":"0.4.1","target":"` + target + `","git_sha":"` + strings.Repeat("ab", 20) + `",` +
		`"binary_file":"rsrs-` + target + binaryExtension() + `","runtime_file":"rsrs-` + target + `-runtime.tar.gz",` +
		`"binary_sha256":"` + strings.Repeat("1", 64) + `","runtime_sha256":"` + strings.Repeat("2", 64) + `"}`
	if _, err := NormalizeCLIMetadata([]byte(body), "v0.4.1", target); err != nil {
		t.Fatalf("valid CLI metadata must pass: %v", err)
	}
	bad := strings.Replace(body, `"git_sha":"`, `"git_sha":"xyz`, 1)
	if _, err := NormalizeCLIMetadata([]byte(bad), "v0.4.1", target); err == nil {
		t.Fatal("invalid commit must be rejected")
	}
	bad = strings.Replace(body, `"version":"0.4.1"`, `"version":"0.4.2"`, 1)
	if _, err := NormalizeCLIMetadata([]byte(bad), "v0.4.1", target); err == nil {
		t.Fatal("version/tag drift must be rejected")
	}
}

func TestReleaseAssetURLRejectsForeignURLs(t *testing.T) {
	release := ReleaseInfo{Tag: "v1.0.0", Assets: []ReleaseAsset{
		{Name: "runtime-build-a.json", URL: "https://evil.example/runtime-build-a.json"},
	}}
	if _, err := ReleaseAssetURL(release, runtimeRepo, "runtime-build-a.json"); err == nil {
		t.Fatal("non-canonical download URLs must be rejected")
	}
	if _, err := ReleaseAssetURL(release, runtimeRepo, "missing.json"); err == nil {
		t.Fatal("missing assets must be rejected")
	}
}
