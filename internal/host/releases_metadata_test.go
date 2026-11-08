package host

import (
	"encoding/json"
	"testing"
)

// Fixture shape mirrors a published runtime-build-<triple>.json; the hashes
// are the real values from respire-runtime v1.0.21-dev.17 (windows-msvc).
const runtimeMetadataV2 = `{
	"schema_version": 2,
	"runtime_version": "1.0.21-dev.17",
	"tag": "v1.0.21-dev.17",
	"channel": "dev",
	"target_triple": "x86_64-pc-windows-msvc",
	"archive_file": "rsrs-runtime-x86_64-pc-windows-msvc.tar.gz",
	"archive_sha256": "142a0d345d5220d73e53e4adbb8942058af099942323dc29cd6305099b446d83",
	"binary_file": "rsrs-runtime-x86_64-pc-windows-msvc.exe",
	"binary_sha256": "ce42f64781e736759bcb23860979a3e8bcec997514c24e2907d3ddc206f13c8a",
	"compat": {"min_cli": "1.0.21-dev.5", "max_cli": "*"}
}`

const runtimeMetadataV1 = `{
	"runtime_version": "1.0.20",
	"tag": "v1.0.20",
	"triple": "x86_64-pc-windows-msvc",
	"sha256": "142a0d345d5220d73e53e4adbb8942058af099942323dc29cd6305099b446d83",
	"binary_file": "rsrs-runtime-x86_64-pc-windows-msvc.exe",
	"binary_sha256": "ce42f64781e736759bcb23860979a3e8bcec997514c24e2907d3ddc206f13c8a",
	"compat": {"min_cli": "1.0.18", "max_cli": "1.9.9"}
}`

func parseAndValidateRuntimeMetadata(t *testing.T, payload string) runtimeMetadata {
	t.Helper()
	var meta runtimeMetadata
	if err := json.Unmarshal([]byte(payload), &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	normalizeRuntimeMetadata(&meta)
	if meta.SchemaVersion != 0 && meta.SchemaVersion != 1 && meta.SchemaVersion != 2 {
		t.Fatalf("schema %d must fail the contract check", meta.SchemaVersion)
	}
	if meta.Target != "x86_64-pc-windows-msvc" {
		t.Fatalf("target = %q", meta.Target)
	}
	if meta.Binary != "rsrs-runtime-x86_64-pc-windows-msvc.exe" {
		t.Fatalf("binary = %q", meta.Binary)
	}
	if !validHash(meta.BinaryHash) || !validHash(meta.ArchiveHash) {
		t.Fatalf("hashes invalid: binary=%q archive=%q", meta.BinaryHash, meta.ArchiveHash)
	}
	return meta
}

func TestRuntimeMetadataSchemaV2(t *testing.T) {
	meta := parseAndValidateRuntimeMetadata(t, runtimeMetadataV2)
	if meta.RuntimeVersion != "1.0.21-dev.17" || meta.Tag != "v1.0.21-dev.17" {
		t.Fatalf("version fields: %q %q", meta.RuntimeVersion, meta.Tag)
	}
	if meta.Compat.MaxCLI != "*" {
		t.Fatalf("max_cli = %q", meta.Compat.MaxCLI)
	}
}

func TestRuntimeMetadataSchemaV1(t *testing.T) {
	meta := parseAndValidateRuntimeMetadata(t, runtimeMetadataV1)
	if meta.RuntimeVersion != "1.0.20" || meta.Tag != "v1.0.20" {
		t.Fatalf("version fields: %q %q", meta.RuntimeVersion, meta.Tag)
	}
	if meta.ArchiveHash != "142a0d345d5220d73e53e4adbb8942058af099942323dc29cd6305099b446d83" {
		t.Fatalf("archive hash = %q", meta.ArchiveHash)
	}
}
