package mcp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/blisspixel/fitr/internal/role"
)

// Pin the physical root even when the result directory has not been created
// yet. Platform aliases in existing ancestors must not change its spelling
// when another fitr process later creates the directory.
func resolveEvidenceRoot(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return resolved, err
	}
	if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err = resolveEvidenceRoot(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}

// Saved attachment paths can use the host's original spelling, including
// /var on macOS or Windows directory aliases. Resolve only their parents and
// require the pinned physical root. The leaf is still checked without following
// symlinks, and the shared role reader still verifies its canonical model name.
func (source *localEvidence) loadLibrary(name string) (role.Library, error) {
	library, err := (role.Store{Dir: filepath.Join(source.root, ".roles")}).Load(name)
	if err != nil {
		return role.Library{}, err
	}
	for index := range library.Candidates {
		path := library.Candidates[index].Path
		parent, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil || !samePath(parent, source.root) || !strings.HasSuffix(path, ".json") {
			return role.Library{}, errors.New("attachment is outside the configured canonical result directory")
		}
		library.Candidates[index].Path = filepath.Join(source.root, filepath.Base(path))
	}
	return library, nil
}
