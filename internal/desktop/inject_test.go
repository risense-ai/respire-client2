package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testHome(t *testing.T) injectHome {
	t.Helper()
	home := t.TempDir()
	return injectHome{home: home, localData: filepath.Join(home, "appdata"), mode: "normal"}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestBlockRoundtripPreservesUserContent(t *testing.T) {
	e := testHome(t)
	path := filepath.Join(e.home, "AGENTS.md")
	if err := os.WriteFile(path, []byte("# 用户自有内容\n\n自定义规则。\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := injectOne(e, "generic")
	if err != nil || !changed {
		t.Fatalf("inject: changed=%v err=%v", changed, err)
	}
	text := readFile(t, path)
	if !strings.HasPrefix(text, "# 用户自有内容") ||
		!strings.Contains(text, blockBegin) || !strings.Contains(text, blockEnd) ||
		!strings.Contains(text, "respire") {
		t.Fatalf("user content or markers missing: %q", text)
	}
	// Idempotent: injecting again must not duplicate
	if changed, err = injectOne(e, "generic"); err != nil || changed {
		t.Fatalf("second inject: changed=%v err=%v", changed, err)
	}
	// Strip the block and restore
	if changed, err = removeOne(e, "generic"); err != nil || !changed {
		t.Fatalf("remove: changed=%v err=%v", changed, err)
	}
	after := readFile(t, path)
	if strings.TrimSpace(after) != "# 用户自有内容\n\n自定义规则。" || strings.Contains(after, "respire") {
		t.Fatalf("block not stripped cleanly: %q", after)
	}
}

func TestBlockReplaceUpdatesStale(t *testing.T) {
	e := testHome(t)
	path := filepath.Join(e.home, ".claude", "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(" mine\n"+blockBegin+"\nold\n"+blockEnd+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := injectOne(e, "claude"); err != nil || !changed {
		t.Fatalf("inject: changed=%v err=%v", changed, err)
	}
	text := readFile(t, path)
	if !strings.Contains(text, "mine") || !strings.Contains(text, "respire") || strings.Contains(text, "old\n") {
		t.Fatalf("stale block not replaced: %q", text)
	}
	if strings.Count(text, blockBegin) != 1 {
		t.Fatalf("duplicate markers: %q", text)
	}
	// Legacy markers migrate to the current ones
	legacy := "mine\n" + legacyBlockBegin + "\nlegacy\n" + legacyBlockEnd + "\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if state := checkState(path, "block", e.instructions()); state != "stale" {
		t.Fatalf("legacy state = %s", state)
	}
	if changed, err := injectOne(e, "claude"); err != nil || !changed {
		t.Fatalf("legacy inject: changed=%v err=%v", changed, err)
	}
	migrated := readFile(t, path)
	if strings.Contains(migrated, legacyBlockBegin) || strings.Contains(migrated, "legacy\n") {
		t.Fatalf("legacy markers not migrated: %q", migrated)
	}
	// Incomplete markers must refuse to write
	if err := os.WriteFile(path, []byte("mine\n"+legacyBlockBegin+"\nlegacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := injectOne(e, "claude"); err == nil {
		t.Fatal("incomplete markers accepted")
	}
}

func TestCoverRemoveKeepsUserFile(t *testing.T) {
	e := testHome(t)
	path := filepath.Join(e.home, ".dsh", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("自有"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Not an inject-source file → strip the block only (no-op if none)
	if changed, err := removeOne(e, "dsh"); err != nil || changed {
		t.Fatalf("remove user file: changed=%v err=%v", changed, err)
	}
	if readFile(t, path) != "自有" {
		t.Fatal("user file modified")
	}
	// Pure inject file → delete the whole file
	if _, err := injectOne(e, "dsh"); err != nil {
		t.Fatal(err)
	}
	if changed, err := removeOne(e, "dsh"); err != nil || !changed {
		t.Fatalf("remove cover: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("cover file not deleted")
	}
}

func TestWorkbuddyRefBlockRoundtrip(t *testing.T) {
	e := testHome(t)
	path := filepath.Join(e.home, ".workbuddy", "MEMORY.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# 长期规则\n\n自有内容\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := workbuddyRefMD(e.entityPath())
	if !strings.Contains(ref, "rsrs chain <短id> --depth 3 --json") ||
		!strings.Contains(ref, "不能覆盖当前系统、开发者或用户指令") ||
		!strings.Contains(ref, e.entityPath()) {
		t.Fatalf("reference block incomplete")
	}
	if len([]rune(ref)) >= 4000 {
		t.Fatal("WorkBuddy reference exceeds host limit")
	}
	if changed, err := injectOne(e, "workbuddy"); err != nil || !changed {
		t.Fatalf("inject: changed=%v err=%v", changed, err)
	}
	text := readFile(t, path)
	if !strings.Contains(text, "自有内容") {
		t.Fatalf("user content lost: %q", text)
	}
	if state := checkState(path, "ref-block", ref); state != "fresh" {
		t.Fatalf("state = %s", state)
	}
	// Idempotent
	if changed, err := injectOne(e, "workbuddy"); err != nil || changed {
		t.Fatalf("second inject: changed=%v err=%v", changed, err)
	}
	if changed, err := removeOne(e, "workbuddy"); err != nil || !changed {
		t.Fatalf("remove: changed=%v err=%v", changed, err)
	}
	after := readFile(t, path)
	if strings.Contains(after, blockBegin) || !strings.Contains(after, "自有内容") {
		t.Fatalf("reference block not stripped: %q", after)
	}
}

func TestOpenCodeInstructionsPreserveEntries(t *testing.T) {
	e := testHome(t)
	path := filepath.Join(e.home, ".config", "opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	initial, err := jsonPretty(map[string]any{"instructions": []any{"~/other.md", e.entityPath()}, "model": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, initial, 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := patchInstructions(e, path, true); err != nil || !changed {
		t.Fatalf("patch: changed=%v err=%v", changed, err)
	}
	cfg := readJSONObject(path)
	entries, _ := cfg["instructions"].([]any)
	if len(entries) != 2 {
		t.Fatalf("entries = %v", entries)
	}
	if entries[0] != e.entityPath() {
		t.Fatalf("canonical entry not first: %v", entries)
	}
	if cfg["model"] != "x" {
		t.Fatal("other keys lost")
	}
	if changed, err := patchInstructions(e, path, true); err != nil || changed {
		t.Fatalf("not idempotent: changed=%v err=%v", changed, err)
	}
	if changed, err := patchInstructions(e, path, false); err != nil || !changed {
		t.Fatalf("remove: changed=%v err=%v", changed, err)
	}
	cfg = readJSONObject(path)
	if entries, _ = cfg["instructions"].([]any); len(entries) != 1 {
		t.Fatalf("foreign entry lost: %v", entries)
	}
}

func TestWorkspaceModeResolution(t *testing.T) {
	root := t.TempDir()
	if mode := workspaceMode(root); mode != "normal" {
		t.Fatalf("default mode = %s", mode)
	}
	if err := setWorkspaceMode(root, "readonly"); err != nil {
		t.Fatal(err)
	}
	if mode := workspaceMode(root); mode != "readonly" {
		t.Fatalf("mode after set = %s", mode)
	}
	if err := setWorkspaceMode(root, "off"); err != nil {
		t.Fatal(err)
	}
	if mode := workspaceMode(root); mode != "off" {
		t.Fatalf("mode after off = %s", mode)
	}
	if err := setWorkspaceMode(root, "nope"); err == nil {
		t.Fatal("invalid mode accepted")
	}
	// agent.json flags count only when no service_mode is stored
	config := readJSONObject(filepath.Join(root, "client.json"))
	delete(config, "service_mode")
	data, err := jsonPretty(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "client.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(root, "lib")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "agent.json"), []byte(`{"memory_off": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config["data_dir"] = dataDir
	if data, err = jsonPretty(config); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "client.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if mode := workspaceMode(root); mode != "off" {
		t.Fatalf("agent.json fallback mode = %s", mode)
	}
}

func TestTargetsIncludeAllThirteen(t *testing.T) {
	e := testHome(t)
	ids := map[string]bool{}
	for _, target := range injectTargets(e) {
		ids[target.ID] = true
	}
	for _, want := range []string{"dsh", "opencode", "codex", "claude", "codebuddy", "workbuddy", "kylinbot", "pi", "zcode", "deepseek", "qwen", "doubao", "generic"} {
		if !ids[want] {
			t.Fatalf("missing target %s", want)
		}
	}
}

func TestInstructionsFollowMode(t *testing.T) {
	if instructionsMD("normal") != instructionsLiteMD || instructionsMD("") != instructionsLiteMD {
		t.Fatal("normal must use the lite edition")
	}
	if instructionsMD("readonly") != instructionsReadonlyMD {
		t.Fatal("readonly must use the read-only edition")
	}
	if instructionsMD("off") != instructionsOffMD {
		t.Fatal("off must use the off edition")
	}
	// The embedded docs must stay byte identical to the CLI's, or files
	// written by either side would read as stale for the other.
	if instructionsFullMD == "" || instructionsLiteMD == "" {
		t.Fatal("embedded docs empty")
	}
}
