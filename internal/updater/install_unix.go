//go:build !windows

package updater

import (
	"fmt"
	"os"
	"path/filepath"
)

// Install replaces target atomically. Download stages the file in the same
// directory, so rename cannot cross filesystems.
func Install(stagedPath, targetPath, expectedCurrentDigest string) (deferred bool, err error) {
	if filepath.Dir(stagedPath) != filepath.Dir(targetPath) {
		return false, fmt.Errorf("staged update is not beside the target executable")
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		return false, fmt.Errorf("inspect target executable: %w", err)
	}
	if info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return false, fmt.Errorf("refusing to replace a setuid or setgid executable")
	}
	actual, err := HashFile(targetPath)
	if err != nil {
		return false, fmt.Errorf("rehash target executable: %w", err)
	}
	if actual != expectedCurrentDigest {
		return false, fmt.Errorf("target executable changed during update")
	}
	if err := os.Chmod(stagedPath, info.Mode().Perm()); err != nil {
		return false, fmt.Errorf("preserve executable permissions: %w", err)
	}
	if err := os.Rename(stagedPath, targetPath); err != nil {
		return false, fmt.Errorf("replace executable: %w", err)
	}
	if dir, err := os.Open(filepath.Dir(targetPath)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return false, nil
}
