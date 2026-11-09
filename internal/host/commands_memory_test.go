package host

import (
	"encoding/json"
	"reflect"
	"testing"
)

func buildArgs(t *testing.T, name string, raw string) []string {
	t.Helper()
	spec, ok := operationSpec(name)
	if !ok {
		t.Fatalf("operation %q is not allowlisted", name)
	}
	args, err := spec.build(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("build(%s, %s) failed: %v", name, raw, err)
	}
	return args
}

func buildArgsErr(t *testing.T, name string, raw string) error {
	t.Helper()
	spec, ok := operationSpec(name)
	if !ok {
		t.Fatalf("operation %q is not allowlisted", name)
	}
	args, err := spec.build(json.RawMessage(raw))
	if err == nil {
		t.Fatalf("build(%s, %s) must fail, got argv %v", name, raw, args)
	}
	return err
}

func TestSessionImportTargetsBuildsDiscoveryArgv(t *testing.T) {
	args := buildArgs(t, "session_import_targets", `{}`)
	if want := []string{"extract", "--targets"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("targets argv = %v, want %v", args, want)
	}
	if spec, _ := operationSpec("session_import_targets"); spec.write {
		t.Fatal("session_import_targets must stay read-only")
	}
}

func TestSessionImportPreviewOmitsDefaultsAndPassesFilters(t *testing.T) {
	args := buildArgs(t, "session_import_preview", `{}`)
	if want := []string{"extract", "--dry-run"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("bare preview argv = %v, want %v", args, want)
	}
	args = buildArgs(t, "session_import_preview",
		`{"id":"codex","since":"7d","cwd":"risense","model":"gpt","limit":50}`)
	want := []string{
		"extract", "--dry-run",
		"--id=codex", "--since=7d", "--cwd=risense", "--model=gpt", "--limit=50",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("filtered preview argv = %v, want %v", args, want)
	}
	// Preview bakes --dry-run into the command words; a caller-supplied flag
	// cannot add or remove it.
	args = buildArgs(t, "session_import_preview", `{"id":"codex","limit":200}`)
	want = []string{"extract", "--dry-run", "--id=codex", "--limit=200"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("preview argv = %v, want %v", args, want)
	}
	if spec, _ := operationSpec("session_import_preview"); spec.write {
		t.Fatal("session_import_preview must stay read-only")
	}
}

func TestSessionImportApplyIsWriteWithoutDryRun(t *testing.T) {
	args := buildArgs(t, "session_import_apply", `{"id":"claude"}`)
	if want := []string{"extract", "--id=claude"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("apply argv = %v, want %v", args, want)
	}
	for _, arg := range args {
		if arg == "--dry-run" {
			t.Fatal("apply argv must never contain --dry-run")
		}
	}
	if spec, _ := operationSpec("session_import_apply"); !spec.write {
		t.Fatal("session_import_apply must be marked as a write operation")
	}
}

func TestSessionImportPreviewRejectsUnknownAndForbiddenInputs(t *testing.T) {
	cases := []string{
		`{"tui":true}`,
		`{"force":true}`,
		`{"include_subagents":true}`,
		`{"parent":"tree-root"}`,
		`{"kind":"diary"}`,
		`{"importance":"trivial"}`,
		`{"session":"abc123"}`,
		`{"dry_run":true}`,
		`{"all":true}`,
		`{"id":123}`,
		`{"cwd":42}`,
		`{"since":7}`,
		`{"limit":"50"}`,
		`{"limit":2001}`,
		`{"limit":10.5}`,
	}
	for _, raw := range cases {
		if err := buildArgsErr(t, "session_import_preview", raw); err == nil {
			t.Errorf("preview accepted %s", raw)
		}
	}
}

func TestSessionImportApplyRejectsUnknownAndForbiddenInputs(t *testing.T) {
	cases := []string{
		`{"tui":true}`,
		`{"force":true}`,
		`{"include_subagents":true}`,
		`{"parent":"tree-root"}`,
		`{"kind":"diary"}`,
		`{"importance":"trivial"}`,
		`{"session":"abc123"}`,
		`{"dry_run":true}`,
		`{"targets":true}`,
		`{"id":7}`,
		`{"model":true}`,
		`{"limit":5000}`,
		`{"limit":-1}`,
	}
	for _, raw := range cases {
		if err := buildArgsErr(t, "session_import_apply", raw); err == nil {
			t.Errorf("apply accepted %s", raw)
		}
	}
}

func TestSessionImportTargetsRejectsAnyArgument(t *testing.T) {
	for _, raw := range []string{`{"id":"codex"}`, `{"since":"7d"}`, `{"limit":5}`} {
		if err := buildArgsErr(t, "session_import_targets", raw); err == nil {
			t.Errorf("targets accepted %s", raw)
		}
	}
}
