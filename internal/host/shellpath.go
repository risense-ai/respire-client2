package host

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PATH setup for the embedded CLI. macOS default login shell is zsh and
// Terminal starts login shells, so ~/.zprofile is the supported location:
// one clearly owned block, prepended only when the bin dir is missing from
// PATH, written atomically, never touching system locations or other shells.
// Removing the app never removes this block; the docs carry the manual
// cleanup instructions.

const (
	shellPathBegin = "# >>> respire-client2 >>>"
	shellPathEnd   = "# <<< respire-client2 <<<"
	shellPathFile  = ".zprofile"
)

func shellPathBlock() string {
	return shellPathBegin + "\n" +
		"case \":$PATH:\" in *\":$HOME/.rsrs/bin:\"*) ;; *) export PATH=\"$HOME/.rsrs/bin:$PATH\";; esac\n" +
		shellPathEnd + "\n"
}

// shellPathManualGuidance is the fallback users see when the block cannot be
// written; it keeps the verified components usable without the write.
func shellPathManualGuidance(reason error) error {
	return fmt.Errorf("终端 PATH 配置失败（%v）。已安装的 rsrs 与 Runtime 不受影响；新终端要使用 rsrs，请手动把以下内容加入 ~/.zprofile：\n%s",
		reason, strings.TrimSuffix(shellPathBlock(), "\n"))
}

// configureUserShellPath writes the owned PATH block into the current user's
// ~/.zprofile. It is idempotent: an existing block short-circuits, existing
// content and permissions are preserved, and the write is atomic.
func configureUserShellPath() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return shellPathManualGuidance(err)
	}
	profile := filepath.Join(home, shellPathFile)
	existing, err := os.ReadFile(profile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return shellPathManualGuidance(err)
	}
	if strings.Contains(string(existing), shellPathBegin) {
		return nil
	}
	content := shellPathBlock()
	if len(existing) > 0 {
		if !strings.HasSuffix(string(existing), "\n") {
			content = "\n" + content
		}
		content = string(existing) + content
	}
	mode := os.FileMode(0644)
	if info, statErr := os.Stat(profile); statErr == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(home, "."+shellPathFile+".respire-*")
	if err != nil {
		return shellPathManualGuidance(err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err = tmp.WriteString(content); err != nil {
		tmp.Close()
		return shellPathManualGuidance(err)
	}
	if err = tmp.Chmod(mode); err != nil {
		tmp.Close()
		return shellPathManualGuidance(err)
	}
	if err = tmp.Close(); err != nil {
		return shellPathManualGuidance(err)
	}
	if err = os.Rename(tmpPath, profile); err != nil {
		return shellPathManualGuidance(err)
	}
	return nil
}
