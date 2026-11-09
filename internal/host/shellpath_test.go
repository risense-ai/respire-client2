package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureUserShellPath(t *testing.T) {
	t.Run("creates the block in a fresh home", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		if err := configureUserShellPath(); err != nil {
			t.Fatalf("fresh config failed: %v", err)
		}
		b, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), shellPathFile))
		if err != nil {
			t.Fatal(err)
		}
		content := string(b)
		if !strings.Contains(content, shellPathBegin) || !strings.Contains(content, shellPathEnd) {
			t.Fatalf("block markers missing: %q", content)
		}
		if !strings.Contains(content, `case ":$PATH:" in *":$HOME/.rsrs/bin:"*)`) {
			t.Fatalf("the PATH guard clause is missing: %q", content)
		}
		info, err := os.Stat(filepath.Join(os.Getenv("HOME"), shellPathFile))
		if err != nil || info.Mode().Perm() != 0644 {
			t.Fatalf("new profile must be 0644: %v %v", err, info)
		}
	})
	t.Run("preserves existing content", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		profile := filepath.Join(home, shellPathFile)
		if err := os.WriteFile(profile, []byte("export EDITOR=vim"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := configureUserShellPath(); err != nil {
			t.Fatalf("append failed: %v", err)
		}
		b, err := os.ReadFile(profile)
		if err != nil {
			t.Fatal(err)
		}
		content := string(b)
		if !strings.HasPrefix(content, "export EDITOR=vim") {
			t.Fatalf("existing content must stay first: %q", content)
		}
		if !strings.Contains(content, shellPathBegin) {
			t.Fatalf("block missing: %q", content)
		}
		if info, _ := os.Stat(profile); info.Mode().Perm() != 0644 {
			t.Fatalf("existing permissions must be preserved: %v", info.Mode().Perm())
		}
	})
	t.Run("is idempotent", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		for i := 0; i < 3; i++ {
			if err := configureUserShellPath(); err != nil {
				t.Fatalf("run %d failed: %v", i+1, err)
			}
		}
		b, err := os.ReadFile(filepath.Join(home, shellPathFile))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(string(b), shellPathBegin); got != 1 {
			t.Fatalf("expected exactly one block, found %d", got)
		}
	})
	t.Run("unwritable home surfaces manual guidance", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("permission-based failure needs a non-root runner")
		}
		home := t.TempDir()
		t.Setenv("HOME", home)
		if err := os.Chmod(home, 0555); err != nil {
			t.Fatal(err)
		}
		err := configureUserShellPath()
		if err == nil {
			t.Fatal("unwritable home must fail")
		}
		if !strings.Contains(err.Error(), "~/.zprofile") {
			t.Fatalf("guidance must name the file: %v", err)
		}
		if !strings.Contains(err.Error(), strings.TrimSuffix(shellPathBlock(), "\n")) {
			t.Fatalf("guidance must carry the exact block to add: %v", err)
		}
	})
	t.Run("new entries are guarded inside the block only", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		profile := filepath.Join(home, shellPathFile)
		if err := os.WriteFile(profile, []byte("export PATH=\"/opt/tools/bin:$PATH\"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := configureUserShellPath(); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(profile)
		content := string(b)
		if !strings.HasPrefix(content, "export PATH=") {
			t.Fatal("pre-existing PATH entries must stay untouched")
		}
		if strings.Count(content, "export PATH=") != 2 {
			t.Fatalf("the block adds exactly one guarded export: %q", content)
		}
	})
}
