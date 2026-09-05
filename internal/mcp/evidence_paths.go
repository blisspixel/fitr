package mcp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/blisspixel/fitr/internal/role"
)

// Reject remote/device spellings and parent traversal before any path
// resolution or metadata access. Drive letters are the only supported colon.
func localEvidencePath(path string) error {
	return localEvidenceSpelling(path, false)
}

func localEvidenceSpelling(path string, linkTarget bool) error {
	if strings.TrimSpace(path) == "" || len(path) > 4096 || strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return errors.New("invalid local evidence path")
	}
	normalized := strings.ReplaceAll(path, `\`, "/")
	if strings.HasPrefix(normalized, "//") {
		return errors.New("network and device evidence paths are unsupported")
	}
	if len(normalized) >= 2 && normalized[1] == ':' && ((normalized[0] >= 'A' && normalized[0] <= 'Z') || (normalized[0] >= 'a' && normalized[0] <= 'z')) {
		if len(normalized) < 3 || normalized[2] != '/' {
			return errors.New("drive-relative evidence paths are unsupported")
		}
		normalized = normalized[2:]
	}
	if strings.Contains(normalized, ":") {
		return errors.New("alternate data streams are unsupported")
	}
	for _, part := range strings.Split(normalized, "/") {
		if part == ".." && !linkTarget {
			return errors.New("evidence paths cannot contain parent traversal")
		}
	}
	return nil
}

// Pin the physical root even when the result directory has not been created
// yet. Platform aliases in existing ancestors must not change its spelling
// when another fitr process later creates the directory.
func resolveEvidenceRoot(path string) (string, error) {
	if err := localEvidencePath(path); err != nil {
		return "", err
	}
	resolved, err := resolveLocalEvidence(path)
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
		if err := localEvidencePath(path); err != nil {
			return role.Library{}, err
		}
		if !filepath.IsAbs(path) {
			return role.Library{}, errors.New("attachment path must be native absolute")
		}
		parent, err := resolveLocalEvidence(filepath.Dir(path))
		if err != nil || !samePath(parent, source.root) || !strings.HasSuffix(path, ".json") {
			return role.Library{}, errors.New("attachment is outside the configured canonical result directory")
		}
		library.Candidates[index].Path = filepath.Join(source.root, filepath.Base(path))
	}
	return library, nil
}
