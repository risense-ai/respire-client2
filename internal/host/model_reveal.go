package host

import (
	"errors"
	"os"
	"path/filepath"
)

// CheckModelRevealPath applies the same symlink/reparse policy as model
// maintenance before asking the OS file manager to reveal a local path.
func (m *Manager) CheckModelRevealPath(path string) (string, error) {
	root := filepath.Join(m.DataRoot, "models")
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if path == "" || err != nil || !filepath.IsLocal(rel) {
		return "", errors.New("路径不在模型目录内")
	}
	// The configured installation may live below OS-managed aliases such
	// as macOS /var. Resolve the trusted data root, but do not follow links
	// within models (including the models directory itself).
	dataRoot, err := filepath.EvalSymlinks(m.DataRoot)
	if err != nil {
		return "", err
	}
	resolvedRoot := filepath.Join(dataRoot, "models")
	if err := checkModelUninstallPath(filepath.Join(resolvedRoot, rel)); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(resolvedRoot, rel)); err != nil {
		return "", err
	}
	return abs, nil
}
