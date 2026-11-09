package desktop

// Direct prompt injection: the client writes each host's memory prompt itself
// (a port of respire-cli's crates/app/src/inject.rs), so the install page no
// longer rounds-trip through the Runtime CLI. The embedded docs are byte
// identical to the CLI's docs/respire*.md, and the workbuddy reference block
// template is extracted verbatim from the CLI source — a file written here
// must read as fresh for the CLI and the other way around.

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode"
)

//go:embed injectdocs/respire.md
var instructionsFullMD string

//go:embed injectdocs/respire-lite.md
var instructionsLiteMD string

//go:embed injectdocs/respire-readonly.md
var instructionsReadonlyMD string

//go:embed injectdocs/respire-off.md
var instructionsOffMD string

//go:embed injectdocs/respire-workbuddy-ref.md
var workbuddyRefTemplate string

// instructionsMD picks the inject source from workspace mode: off uses the off
// edition, readonly the read-only edition, otherwise the lite edition (same
// mapping as the CLI's instructions_md()).
func instructionsMD(mode string) string {
	switch mode {
	case "off":
		return instructionsOffMD
	case "readonly":
		return instructionsReadonlyMD
	default:
		return instructionsLiteMD
	}
}

// Inject-block markers (block mode: bound user content vs injected content).
const (
	blockBegin       = "<!-- respire:begin -->"
	blockEnd         = "<!-- respire:end -->"
	legacyBlockBegin = "<!-- 1memory:begin -->"
	legacyBlockEnd   = "<!-- 1memory:end -->"
)

// injectHome carries the paths the inject code needs; tests point it at a
// temporary directory instead of the real user profile.
type injectHome struct {
	home      string // os.UserHomeDir()
	localData string // %LOCALAPPDATA% (Windows entity file root)
	mode      string // workspace mode: normal | readonly | off
}

func (e injectHome) instructions() string { return instructionsMD(e.mode) }

// entityPath is the inject entity file: Windows `%LOCALAPPDATA%\respire\docs\respire.md`,
// Unix `~/.local/share/respire/docs/respire.md` (do not force an XDG path).
func (e injectHome) entityPath() string {
	if runtime.GOOS == "windows" {
		base := e.localData
		if base == "" {
			base = filepath.Join(e.home, "AppData", "Local")
		}
		return filepath.Join(base, "respire", "docs", "respire.md")
	}
	return filepath.Join(e.home, ".local", "share", "respire", "docs", "respire.md")
}

func expandHome(home, p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(home, rest)
	}
	if rest, ok := strings.CutPrefix(p, `~\`); ok {
		return filepath.Join(home, rest)
	}
	return p
}

func joinHome(home string, elems ...string) string {
	return filepath.Join(append([]string{home}, elems...)...)
}

// workbuddyRefMD renders the workbuddy reference block: MEMORY.md is injected
// each turn as <user_memory> (4000-char cap), so it carries the mandatory
// gates only and points at the entity file for the full policy.
func workbuddyRefMD(entity string) string {
	return strings.ReplaceAll(workbuddyRefTemplate, "{entity}", entity)
}

// displayPath abbreviates the home prefix as `~` for display.
func displayPath(e injectHome, path string) string {
	if rest, err := filepath.Rel(e.home, path); err == nil && rest != ".." && !strings.HasPrefix(rest, ".."+string(filepath.Separator)) {
		return "~/" + filepath.ToSlash(rest)
	}
	return path
}

// injectTarget is one row of the install page's agent list.
type injectTarget struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Path            string        `json:"path"`
	RealPath        string        `json:"realPath"`
	Mode            string        `json:"mode"` // inject | block | reference | ref-block | plugin
	LikelyInstalled bool          `json:"likelyInstalled"`
	State           string        `json:"state"` // fresh | stale | none | absent
	Plugin          *pluginStatus `json:"plugin,omitempty"`
}

// injectTargets probes every target (shown in the UI before inject).
func injectTargets(e injectHome) []injectTarget {
	dirAt := func(elem ...string) bool {
		info, err := os.Stat(joinHome(e.home, elem...))
		return err == nil && info.IsDir()
	}
	probe := []struct {
		id    string
		known bool
	}{
		{"generic", true},
		{"dsh", dirAt(".dsh")},
		{"opencode", dirAt(".config", "opencode")},
		{"codex", dirAt(".codex")},
		{"claude", dirAt(".claude")},
		{"codebuddy", dirAt(".codebuddy")},
		{"workbuddy", dirAt(".workbuddy")},
		{"kylinbot", dirAt(".kylinbot")},
		{"pi", dirAt(".pi")},
		{"zcode", dirAt(".zcode") || dirAt(".zigcode")},
		{"deepseek", dirAt(".deepseek")},
		{"qwen", dirAt(".qwen") || dirAt(".config", "qwen")},
		{"doubao", dirAt(".doubao") || dirAt(".config", "doubao")},
		{"grok", dirAt(".grok")},
	}
	out := make([]injectTarget, 0, len(probe))
	for _, item := range probe {
		target := injectTarget{ID: item.id, LikelyInstalled: item.known}
		switch item.id {
		case "dsh":
			target.Name, target.Path, target.Mode = "dsh", "~/.dsh/AGENTS.md", "inject"
		case "opencode":
			target.Name, target.Path, target.Mode = "opencode", displayPath(e, e.entityPath()), "reference"
		case "codex":
			target.Name, target.Path, target.Mode = "codex", "~/.codex/AGENTS.md", "block"
		case "claude":
			target.Name, target.Path, target.Mode = "claude code", "~/.claude/CLAUDE.md", "block"
		case "codebuddy":
			target.Name, target.Path, target.Mode = "codebuddy", "~/.codebuddy/AGENTS.md", "block"
		case "workbuddy":
			target.Name, target.Path, target.Mode = "workbuddy", "~/.workbuddy/MEMORY.md", "ref-block"
		case "pi":
			target.Name, target.Path, target.Mode = "pi agent", "~/.pi/AGENTS.md", "block"
		case "zcode":
			target.Name, target.Path, target.Mode = "ZCode", "~/.zcode/AGENTS.md", "block"
		case "deepseek":
			target.Name, target.Path, target.Mode = "deepseek harness", "~/.deepseek/AGENTS.md", "block"
		case "qwen":
			target.Name, target.Path, target.Mode = "qwen assistant", "~/.qwen/AGENTS.md", "block"
		case "doubao":
			target.Name, target.Path, target.Mode = "doubao office", "~/.doubao/AGENTS.md", "block"
		case "grok":
			target.Name, target.Path, target.Mode = "grok build", "~/.grok/AGENTS.md", "block"
		case "kylinbot":
			target.Name, target.Path, target.Mode = "kylinbot", "~/.kylinbot/workspace/AGENTS.md", "block"
		default:
			target.Name, target.Path, target.Mode = "generic AGENTS.md", "~/AGENTS.md", "block"
		}
		if target.Mode != "reference" {
			target.RealPath = expandHome(e.home, target.Path)
		} else {
			target.RealPath = e.entityPath()
		}
		target.State = e.targetState(&target)
		out = append(out, target)
	}
	return out
}

// targetState computes one target's state; the workbuddy reference block also
// requires the entity file to be current.
func (e injectHome) targetState(target *injectTarget) string {
	if target.ID == "opencode" {
		return e.opencodeState()
	}
	expected := e.instructions()
	if target.Mode == "ref-block" {
		expected = workbuddyRefMD(e.entityPath())
	}
	state := checkState(target.RealPath, target.Mode, expected)
	if target.Mode == "ref-block" && state == "fresh" &&
		checkState(e.entityPath(), "reference", e.instructions()) != "fresh" {
		return "stale"
	}
	return state
}

// checkState: absent (no file) / fresh (up to date) / stale (injected but not
// current) / none (file exists, not injected).
func checkState(path, mode, expected string) string {
	text, err := os.ReadFile(path)
	if err != nil {
		return "absent"
	}
	s := string(text)
	switch mode {
	// Cover/reference: the target file IS the inject source; compare the whole text
	case "inject", "reference":
		if trimSpaceRight(s) == trimSpaceRight(expected) {
			return "fresh"
		}
		return "stale"
	// Block: no markers → not injected; markers present → compare inner content
	default:
		if strings.Contains(s, legacyBlockBegin) || strings.Contains(s, legacyBlockEnd) {
			return "stale"
		}
		inner, ok := extractBlock(s)
		if !ok {
			return "none"
		}
		if inner == strings.ReplaceAll(trimSpaceRight(expected), "\r\n", "\n") {
			return "fresh"
		}
		return "stale"
	}
}

func trimSpaceRight(s string) string { return strings.TrimRightFunc(s, unicode.IsSpace) }

func extractBlock(text string) (string, bool) {
	start := strings.Index(text, blockBegin)
	if start < 0 {
		return "", false
	}
	start += len(blockBegin)
	end := strings.Index(text, blockEnd)
	if end < start {
		return "", false
	}
	return strings.TrimFunc(text[start:end], unicode.IsSpace), true
}

// injectOne writes one target. Returns whether a write actually happened.
func injectOne(e injectHome, id string) (bool, error) {
	switch id {
	case "dsh":
		return writeCover(e, joinHome(e.home, ".dsh", "AGENTS.md"))
	case "opencode":
		return injectOpencode(e)
	case "codex":
		return writeBlock(e, joinHome(e.home, ".codex", "AGENTS.md"))
	case "claude":
		return writeBlock(e, joinHome(e.home, ".claude", "CLAUDE.md"))
	case "codebuddy":
		return writeBlock(e, joinHome(e.home, ".codebuddy", "AGENTS.md"))
	case "workbuddy":
		return injectWorkbuddy(e)
	case "pi":
		return writeBlock(e, joinHome(e.home, ".pi", "AGENTS.md"))
	case "zcode":
		return writeBlock(e, joinHome(e.home, ".zcode", "AGENTS.md"))
	case "deepseek":
		return writeBlock(e, joinHome(e.home, ".deepseek", "AGENTS.md"))
	case "qwen":
		return writeBlock(e, joinHome(e.home, ".qwen", "AGENTS.md"))
	case "doubao":
		return writeBlock(e, joinHome(e.home, ".doubao", "AGENTS.md"))
	case "grok":
		return writeBlock(e, joinHome(e.home, ".grok", "AGENTS.md"))
	case "kylinbot":
		return writeBlock(e, joinHome(e.home, ".kylinbot", "workspace", "AGENTS.md"))
	case "generic":
		return writeBlock(e, joinHome(e.home, "AGENTS.md"))
	default:
		return false, fmt.Errorf("unknown inject target: %s", id)
	}
}

// removeOne uninstalls one target. Returns whether a write actually happened.
func removeOne(e injectHome, id string) (bool, error) {
	switch id {
	case "dsh":
		return removeCover(e, joinHome(e.home, ".dsh", "AGENTS.md"))
	case "opencode":
		return removeOpencode(e)
	case "codex":
		return removeBlock(e, joinHome(e.home, ".codex", "AGENTS.md"))
	case "claude":
		return removeBlock(e, joinHome(e.home, ".claude", "CLAUDE.md"))
	case "codebuddy":
		return removeBlock(e, joinHome(e.home, ".codebuddy", "AGENTS.md"))
	case "workbuddy":
		return removeBlock(e, joinHome(e.home, ".workbuddy", "MEMORY.md"))
	case "pi":
		return removeBlock(e, joinHome(e.home, ".pi", "AGENTS.md"))
	case "zcode":
		return removeBlock(e, joinHome(e.home, ".zcode", "AGENTS.md"))
	case "deepseek":
		return removeBlock(e, joinHome(e.home, ".deepseek", "AGENTS.md"))
	case "qwen":
		return removeBlock(e, joinHome(e.home, ".qwen", "AGENTS.md"))
	case "doubao":
		return removeBlock(e, joinHome(e.home, ".doubao", "AGENTS.md"))
	case "grok":
		return removeBlock(e, joinHome(e.home, ".grok", "AGENTS.md"))
	case "kylinbot":
		return removeBlock(e, joinHome(e.home, ".kylinbot", "workspace", "AGENTS.md"))
	case "generic":
		return removeBlock(e, joinHome(e.home, "AGENTS.md"))
	default:
		return false, fmt.Errorf("unknown inject target: %s", id)
	}
}

// ── cover mode (dsh): target file = inject source ──

func writeCover(e injectHome, path string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	// Drop a symlink first — os.WriteFile would follow it and overwrite the repo source
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(path); err != nil {
			return false, err
		}
	}
	source := e.instructions()
	if old, err := os.ReadFile(path); err == nil && string(old) == source {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func removeCover(e injectHome, path string) (bool, error) {
	text, err := os.ReadFile(path)
	if err != nil {
		// No file (or unreadable) → nothing of ours to remove
		return false, nil
	}
	// Identical to the inject source → treat as a pure inject file and delete it
	if string(text) == e.instructions() || string(text) == instructionsFullMD {
		return true, os.Remove(path)
	}
	// User-owned content → strip the block only
	return removeBlock(e, path)
}

// ── reference mode (opencode): entity file + opencode.json instructions ──

func (e injectHome) opencodeState() string {
	dir := joinHome(e.home, ".config", "opencode")
	referenced := false
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		text, err := os.ReadFile(joinPath(dir, name))
		if err != nil {
			continue // missing config is fine; unreadable ones just don't count
		}
		cfg, err := parseJSONObject(text)
		if err != nil {
			return "manual"
		}
		// A later jsonc overrides the former only when it declares instructions.
		if entries, ok := cfg["instructions"].([]any); ok {
			for _, entry := range entries {
				if raw, ok := entry.(string); ok && e.sameEntry(raw) {
					referenced = true
				}
			}
		}
	}
	if !referenced {
		return "none"
	}
	return checkState(e.entityPath(), "reference", e.instructions())
}

func (e injectHome) sameEntry(raw string) bool {
	return filepath.Clean(expandHome(e.home, raw)) == filepath.Clean(e.entityPath())
}

func injectOpencode(e injectHome) (bool, error) {
	changed, err := writeCover(e, e.entityPath())
	if err != nil {
		return false, err
	}
	// Both opencode.json and opencode.jsonc are loaded (jsonc later can override
	// instructions) — write both if present
	dir := joinHome(e.home, ".config", "opencode")
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		path := joinPath(dir, name)
		if name == "opencode.jsonc" && !fileExists(path) {
			continue
		}
		item, err := patchInstructions(e, path, true)
		if err != nil {
			return false, err
		}
		changed = changed || item
	}
	// oh-my-openagent prompt_append (touch only if the field exists; idempotent)
	oma := joinPath(dir, "oh-my-openagent.json")
	if fileExists(oma) {
		item, err := patchPromptAppend(e, oma, true)
		if err != nil {
			return false, err
		}
		changed = changed || item
	}
	return changed, nil
}

func removeOpencode(e injectHome) (bool, error) {
	// The entity file is shared by OpenCode and WorkBuddy; uninstall only drops
	// this target's reference.
	changed := false
	dir := joinHome(e.home, ".config", "opencode")
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		path := joinPath(dir, name)
		if fileExists(path) {
			item, err := patchInstructions(e, path, false)
			if err != nil {
				return false, err
			}
			changed = changed || item
		}
	}
	oma := joinPath(dir, "oh-my-openagent.json")
	if fileExists(oma) {
		item, err := patchPromptAppend(e, oma, false)
		if err != nil {
			return false, err
		}
		changed = changed || item
	}
	return changed, nil
}

// patchInstructions adds/removes the inject path in the instructions array;
// keeps other entries (the old impl replaced the whole list and deleted
// extras; fixed 2026-09-09 in the CLI).
func patchInstructions(e injectHome, path string, add bool) (bool, error) {
	entry := e.entityPath()
	textBytes, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, err
		}
		if !add {
			return false, nil
		}
		cfg := map[string]any{
			"$schema":      "https://opencode.ai/config.json",
			"instructions": []any{entry},
		}
		data, err := jsonPretty(cfg)
		if err != nil {
			return false, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, err
		}
		return true, os.WriteFile(path, data, 0o644)
	}
	text := string(textBytes)
	cfg, err := parseJSONObject(textBytes)
	if err != nil {
		return false, fmt.Errorf("%s 解析失败（带注释的 jsonc 请手工编辑：在 instructions 数组中添加/移除 %s）: %w", path, entry, err)
	}
	switch existing := cfg["instructions"].(type) {
	case []any:
		// Normalize: `~` form and absolute form are the same item; drop every
		// form then add the canonical one back (no duplicates)
		kept := existing[:0:0]
		for _, item := range existing {
			if raw, ok := item.(string); ok && e.sameEntry(raw) {
				continue
			}
			kept = append(kept, item)
		}
		if add {
			kept = append([]any{entry}, kept...)
		}
		cfg["instructions"] = kept
	case nil:
		if add {
			cfg["instructions"] = []any{entry}
		}
	default:
		return false, fmt.Errorf("%s instructions 不是数组；请手工检查", path)
	}
	data, err := jsonPretty(cfg)
	if err != nil {
		return false, err
	}
	if string(data) == text {
		return false, nil
	}
	return true, os.WriteFile(path, data, 0o644)
}

// patchPromptAppend adds/removes the inject path in each agent's prompt_append
// in oh-my-openagent.json (no-op if the field is missing).
func patchPromptAppend(e injectHome, path string, add bool) (bool, error) {
	textBytes, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	cfg, err := parseJSONObject(textBytes)
	if err != nil {
		return false, fmt.Errorf("%s 解析失败: %w", path, err)
	}
	abs := e.entityPath()
	line := "- 记忆铁律（respire）：" + abs
	var walk func(any) any
	walk = func(value any) any {
		switch item := value.(type) {
		case map[string]any:
			if s, ok := item["prompt_append"].(string); ok {
				if add && !strings.Contains(s, abs) {
					if s != "" && !strings.HasSuffix(s, "\n") {
						s += "\n"
					}
					item["prompt_append"] = s + line
				} else if !add {
					kept := make([]string, 0, 4)
					for _, l := range strings.Split(s, "\n") {
						if l != line {
							kept = append(kept, l)
						}
					}
					item["prompt_append"] = strings.Join(kept, "\n")
				}
			}
			for key, child := range item {
				item[key] = walk(child)
			}
		case []any:
			for i, child := range item {
				item[i] = walk(child)
			}
		}
		return value
	}
	updated := walk(cfg).(map[string]any)
	data, err := jsonPretty(updated)
	if err != nil {
		return false, err
	}
	if string(data) == string(textBytes) {
		return false, nil
	}
	return true, os.WriteFile(path, data, 0o644)
}

// ── block mode (codex/claude/codebuddy/…): markers wrap the insert ──

func writeBlock(e injectHome, path string) (bool, error) {
	return applyBlockWith(path, false, e.instructions())
}

func removeBlock(e injectHome, path string) (bool, error) {
	return applyBlockWith(path, true, e.instructions())
}

// workbuddy reference mode: entity file (shared with opencode) + MEMORY.md
// reference block.
func injectWorkbuddy(e injectHome) (bool, error) {
	changed, err := writeCover(e, e.entityPath())
	if err != nil {
		return false, err
	}
	item, err := applyBlockWith(joinHome(e.home, ".workbuddy", "MEMORY.md"), false, workbuddyRefMD(e.entityPath()))
	if err != nil {
		return false, err
	}
	return changed || item, nil
}

type blockPreview struct {
	path    string
	before  string
	after   string
	changed bool
}

func blockPreviewWith(path string, remove bool, content string) (blockPreview, error) {
	beforeBytes, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return blockPreview{}, err
	}
	before := string(beforeBytes)
	type marker struct{ start, end int }
	var bounds []marker
	for _, pair := range [][2]string{{blockBegin, blockEnd}, {legacyBlockBegin, legacyBlockEnd}} {
		starts := indicesOf(before, pair[0])
		ends := indicesOf(before, pair[1])
		switch {
		case len(starts) == 0 && len(ends) == 0:
		case len(starts) == 1 && len(ends) == 1 && starts[0] < ends[0]:
			bounds = append(bounds, marker{starts[0], ends[0] + len(pair[1])})
		default:
			return blockPreview{}, fmt.Errorf("inject 标记不完整或重复，请先修复 %s", path)
		}
	}
	slices.SortFunc(bounds, func(a, b marker) int { return a.start - b.start })
	for i := 1; i < len(bounds); i++ {
		if bounds[i-1].end > bounds[i].start {
			return blockPreview{}, fmt.Errorf("inject 标记重叠，请先修复 %s", path)
		}
	}
	newline := "\n"
	if i := strings.IndexByte(before, '\n'); i > 0 && before[i-1] == '\r' {
		newline = "\r\n"
	}
	block := blockBegin + newline + trimSpaceRight(content) + newline + blockEnd
	var after string
	if len(bounds) == 0 {
		if remove {
			after = before
		} else {
			var sep string
			switch {
			case before == "":
			case strings.HasSuffix(before, "\n\n"):
			case strings.HasSuffix(before, "\n"):
				sep = newline
			default:
				sep = newline + newline
			}
			after = before + sep + block + newline
		}
	} else {
		var b strings.Builder
		cursor := 0
		for index, m := range bounds {
			b.WriteString(before[cursor:m.start])
			if index == 0 && !remove {
				b.WriteString(block)
			}
			cursor = m.end
		}
		b.WriteString(before[cursor:])
		after = b.String()
	}
	return blockPreview{path: path, before: before, after: after, changed: before != after}, nil
}

func applyBlockWith(path string, remove bool, content string) (bool, error) {
	preview, err := blockPreviewWith(path, remove, content)
	if err != nil {
		return false, err
	}
	if !preview.changed {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(preview.after), 0o644)
}

// ── workspace mode (client.json service_mode, same store as the CLI) ──

func clientConfigPath(dataRoot string) string {
	return joinPath(dataRoot, "client.json")
}

// resolveDataDir mirrors the CLI: client.json data_dir when set, else the root
// (RSRS_DATA_DIR or ~/.rsrs, resolved by the manager).
func resolveDataDir(dataRoot string, config map[string]any) string {
	if raw, _ := config["data_dir"].(string); raw != "" {
		if home, err := os.UserHomeDir(); err == nil {
			return expandHome(home, raw)
		}
	}
	return dataRoot
}

// workspaceMode resolves the effective three-state mode the same way the
// CLI's workspace_mode() does: client.json service_mode first, then the
// agent.json flags in the data dir.
func workspaceMode(dataRoot string) string {
	config := readJSONObject(clientConfigPath(dataRoot))
	if mode, _ := config["service_mode"].(string); mode == "normal" || mode == "readonly" || mode == "off" {
		return mode
	}
	agent := readJSONObject(joinPath(resolveDataDir(dataRoot, config), "agent.json"))
	if agent["memory_off"] == true {
		return "off"
	}
	if agent["readonly"] == true || agent["readonly_team"] == true {
		return "readonly"
	}
	return "normal"
}

func setWorkspaceMode(dataRoot, mode string) error {
	if mode != "normal" && mode != "readonly" && mode != "off" {
		return fmt.Errorf("未知模式 %s — 可选 normal | readonly | off", mode)
	}
	path := clientConfigPath(dataRoot)
	config := map[string]any{}
	if text, err := os.ReadFile(path); err == nil {
		parsed, parseErr := parseJSONObject(text)
		if parseErr != nil {
			return fmt.Errorf("%s 解析失败，请先在 CLI 侧修复后再切换模式: %w", path, parseErr)
		}
		config = parsed
	} else if !os.IsNotExist(err) {
		return err
	}
	config["service_mode"] = mode
	data, err := jsonPretty(config)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// ── page-level operations behind the /api routes ──

// hostPluginRepo maps a prompt-injection host to its Respire plugin repo (both
// are released together when the agent is enabled).
var hostPluginRepo = map[string]string{
	"dsh":       "respire-dsh",
	"codex":     "respire-codex",
	"workbuddy": "respire-workbuddy",
	"pi":        "respire-pi",
	"zcode":     "respire-zcode",
}

// pluginOnlyHosts have a plugin but no prompt target of their own.
var pluginOnlyHosts = []struct{ id, name, repo string }{
	{"magpie", "magpie", "respire-magpie"},
	{"qoder", "qoder", "respire-qoder"},
	{"kimi", "kimi code", "respire-kimi"},
	{"dimcode", "dim code", "respire-dimcode"},
	{"hermes", "hermes", "respire-hermes"},
}

// InjectTargetsView lists every agent row: prompt targets (with their plugin,
// if any) plus the plugin-only hosts.
func (d *desktop) InjectTargetsView(ctx context.Context) (map[string]any, error) {
	e, err := d.injectHome()
	if err != nil {
		return nil, err
	}
	targets := injectTargets(e)
	plugins := map[string]*pluginStatus{}
	for _, item := range d.PluginStatuses(ctx) {
		plugin := item
		plugins[plugin.ID] = &plugin
	}
	for i := range targets {
		if repo, ok := hostPluginRepo[targets[i].ID]; ok {
			targets[i].Plugin = plugins[repo]
		}
	}
	for _, host := range pluginOnlyHosts {
		plugin := plugins[host.repo]
		row := injectTarget{
			ID:     host.id,
			Name:   host.name,
			Mode:   "plugin",
			Path:   d.pluginPath(host.repo),
			Plugin: plugin,
		}
		row.RealPath = row.Path
		row.LikelyInstalled = plugin != nil && (plugin.Source || plugin.Command)
		if plugin != nil && plugin.Source {
			row.State = "fresh"
		} else {
			row.State = "absent"
		}
		targets = append(targets, row)
	}
	return map[string]any{"mode": e.mode, "targets": targets}, nil
}

// InjectSet enables or disables one agent. Scope selects what is released:
// "all" (the row switch: prompt + plugin), "prompt" (write/remove the prompt
// only) or "plugin" (install/update the plugin only). Disable always means
// removing the injected prompt — plugins stay and must be removed in the host
// UI, so scope=plugin only supports enable.
func (d *desktop) InjectSet(ctx context.Context, id string, enable, mirror bool, scope string) (map[string]any, error) {
	if scope == "" {
		scope = "all"
	}
	if scope != "all" && scope != "prompt" && scope != "plugin" {
		return nil, fmt.Errorf("未知范围 %s — 可选 all | prompt | plugin", scope)
	}
	for _, host := range pluginOnlyHosts {
		if host.id == id {
			if scope == "prompt" {
				return nil, errors.New("该宿主没有独立提示词文件")
			}
			if !enable {
				return nil, errors.New("该插件需要在宿主的插件界面移除，Client2 不自动卸载")
			}
			message, _, err := d.ensurePlugin(ctx, host.repo, mirror)
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": id, "enabled": true, "message": message}, nil
		}
	}
	e, err := d.injectHome()
	if err != nil {
		return nil, err
	}
	if scope == "plugin" {
		repo, ok := hostPluginRepo[id]
		if !ok {
			return nil, errors.New("该宿主没有对应的 Respire 插件")
		}
		message, _, pluginErr := d.ensurePlugin(ctx, repo, mirror)
		if pluginErr != nil {
			return nil, pluginErr
		}
		return map[string]any{"id": id, "enabled": true, "plugin": map[string]any{"ok": true, "message": message}}, nil
	}
	var changed bool
	if enable {
		changed, err = injectOne(e, id)
	} else {
		changed, err = removeOne(e, id)
	}
	if err != nil {
		return nil, err
	}
	result := map[string]any{"id": id, "enabled": enable, "changed": changed}
	if repo, ok := hostPluginRepo[id]; ok && enable && scope == "all" {
		message, _, pluginErr := d.ensurePlugin(ctx, repo, mirror)
		pluginResult := map[string]any{"ok": pluginErr == nil, "message": message}
		if pluginErr != nil {
			pluginResult["message"] = pluginErr.Error()
		}
		result["plugin"] = pluginResult
	}
	return result, nil
}

// InjectSetMode saves the workspace mode and rewrites the prompts of every
// currently injected target so the injected edition follows the mode.
func (d *desktop) InjectSetMode(ctx context.Context, mode string) (map[string]any, error) {
	if err := setWorkspaceMode(d.m.DataRoot, mode); err != nil {
		return nil, err
	}
	e, err := d.injectHome()
	if err != nil {
		return nil, err
	}
	results := []map[string]any{}
	for _, target := range injectTargets(e) {
		switch target.State {
		case "fresh", "stale":
			changed, err := injectOne(e, target.ID)
			item := map[string]any{"id": target.ID, "ok": err == nil, "changed": changed}
			if err != nil {
				item["error"] = err.Error()
			}
			results = append(results, item)
		}
	}
	return map[string]any{"mode": mode, "updated": len(results), "results": results}, nil
}

func (d *desktop) injectHome() (injectHome, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return injectHome{}, err
	}
	return injectHome{home: home, localData: localDataDir(), mode: workspaceMode(d.m.DataRoot)}, nil
}

func localDataDir() string {
	if runtime.GOOS == "windows" {
		return os.Getenv("LOCALAPPDATA")
	}
	return ""
}

// ── small helpers ──

func joinPath(elem ...string) string { return filepath.Join(elem...) }

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func indicesOf(text, sub string) []int {
	var out []int
	for i := 0; ; {
		j := strings.Index(text[i:], sub)
		if j < 0 {
			return out
		}
		i += j
		out = append(out, i)
		i += len(sub)
	}
}

// parseJSONObject reads a JSON object; anything else (or a parse failure) is
// an error.
func parseJSONObject(data []byte) (map[string]any, error) {
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, errors.New("JSON 顶层值不是对象")
	}
	return value, nil
}

func readJSONObject(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	value, err := parseJSONObject(data)
	if err != nil {
		return map[string]any{}
	}
	return value
}

// jsonPretty renders compact-stable pretty JSON (2-space indent, no HTML
// escaping) to match what the CLI writes for the same files.
func jsonPretty(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
