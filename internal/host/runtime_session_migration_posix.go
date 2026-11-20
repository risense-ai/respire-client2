//go:build linux || darwin

package host

import (
	"os"
	"path/filepath"
)

func canonicalSessionDirectory(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

func publishFreshRuntimeFile(from, to string) error {
	directory, err := os.Open(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := os.Link(from, to); err != nil {
		return err
	}
	return directory.Sync()
}

func publishSessionFile(from, to string) error {
	directory, err := os.Open(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := os.Rename(from, to); err != nil {
		return err
	}
	return directory.Sync()
}
