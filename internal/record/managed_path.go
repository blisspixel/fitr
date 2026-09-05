package record

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func managedRoot(records Store, create bool) (string, error) {
	raw := records.dir()
	for _, part := range strings.FieldsFunc(strings.TrimPrefix(raw, filepath.VolumeName(raw)), func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." || strings.EqualFold(part, managedDirectory) {
			return "", errors.New("managed evidence requires the fixed results root without parent traversal")
		}
	}
	if create {
		if err := storeDir(raw); err != nil {
			return "", err
		}
	}
	absolute, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	// A user-supplied root alias is resolved once. Managed descendants never
	// follow links, so aliases such as macOS /var do not weaken confinement.
	physical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	if managedNamespace(physical) {
		return "", errors.New("managed evidence requires the enclosing fixed results root")
	}
	info, err := os.Stat(physical)
	if err != nil || !info.IsDir() {
		return "", errors.New("managed evidence results root is not a directory")
	}
	return physical, nil
}

func (store ManagedStore) directory() string {
	return filepath.Join(store.root, managedDirectory, store.id)
}

func managedCheckedDirectory(path string, create bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && create {
		if err = os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed evidence directory must not be a link or non-directory")
	}
	return nil
}

func (store ManagedStore) checkPath() error {
	if !managedIDPattern.MatchString(store.id) || !filepath.IsAbs(store.root) {
		return errors.New("invalid managed evidence store handle")
	}
	if err := managedNoLinks(store.root); err != nil {
		return err
	}
	for _, path := range []string{store.root, filepath.Join(store.root, managedDirectory), store.directory(), filepath.Join(store.directory(), historyDirName)} {
		if err := managedCheckedDirectory(path, false); err != nil {
			return err
		}
	}
	return nil
}

func managedNoLinks(path string) error {
	for {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed evidence path contains a symbolic link")
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

func managedEntries(path string, limit int) ([]os.DirEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > limit {
		return nil, errors.New("managed evidence directory exceeds its entry limit")
	}
	return entries, nil
}

func managedNamespace(path string) bool {
	if physical, err := filepath.EvalSymlinks(path); err == nil {
		path = physical
	}
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if strings.EqualFold(part, managedDirectory) {
			return true
		}
	}
	for {
		if _, err := os.Lstat(filepath.Join(path, managedMetadata)); err == nil {
			return true
		}
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	return false
}
