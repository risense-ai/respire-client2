package host

import (
	"errors"
	"os"
)

// An interruption before the rename or after cleanup may leave just an empty
// stage. Only that empty shell can be safely recovered without a Runtime
// transaction receipt. Never infer the identity of staged model contents.
func recoverEmptyM3Stage(models *os.Root, verify func() error) error {
	original, err := models.Lstat(m3UninstallStage)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !original.IsDir() || modelUninstallRedirect(original) {
		return errors.New("BGE-M3 暂存路径不是普通目录")
	}
	if err := verify(); err != nil {
		return err
	}
	current, err := models.Lstat(m3UninstallStage)
	if err != nil || !os.SameFile(original, current) || modelUninstallRedirect(current) {
		return errors.Join(errors.New("BGE-M3 暂存目录已改变"), err)
	}
	// Remove is deliberately non-recursive and fails atomically if files
	// are present or a concurrent writer has inserted data.
	return models.Remove(m3UninstallStage)
}
