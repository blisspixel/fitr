package artifact

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Reject parent components before Abs/Clean/Dir, then use only this checked
// spelling for operations. No network paths, device namespaces or ADS paths.
func checkedPath(path string) (string, error) {
	if path == "" || len(path) > 4096 || strings.ContainsAny(path, "\x00\r\n\t") {
		return "", errors.New("invalid artifact path")
	}
	relative := strings.TrimPrefix(path, filepath.VolumeName(path))
	for _, part := range strings.FieldsFunc(relative, func(char rune) bool { return char == '/' || char == '\\' }) {
		if part == ".." {
			return "", errors.New("artifact paths cannot contain parent traversal; use the canonical physical path")
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if !validAbsolutePath(absolute) {
		return "", errors.New("unsupported artifact path")
	}
	return absolute, nil
}

// ValidateBindingOutputPath additionally prevents publishing a receipt over a
// mapped input that is currently missing. It performs no creation or hashing.
func ValidateBindingOutputPath(path string, spec Spec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	absolute, err := checkedPath(path)
	if err != nil {
		return err
	}
	for _, mapping := range spec.Files {
		if localKey(absolute) == localKey(mapping.LocalPath) {
			return errors.New("artifact output overlaps a mapped local input")
		}
		if sameMissingLocation(absolute, mapping.LocalPath) {
			return errors.New("artifact output overlaps a mapped local input through a physical directory alias")
		}
	}
	return ValidateOutputPath(absolute)
}

func sameMissingLocation(output, input string) bool {
	if !filepath.IsAbs(input) {
		return false
	}
	left, right := filepath.Base(output), filepath.Base(input)
	if windowsAbsolute(strings.ReplaceAll(output, "\\", "/")) {
		left, right = strings.ToLower(left), strings.ToLower(right)
	}
	if left != right {
		return false
	}
	outputParent, err := os.Stat(filepath.Dir(output))
	if err != nil {
		return false
	}
	inputParent, err := os.Stat(filepath.Dir(input))
	return err == nil && os.SameFile(outputParent, inputParent)
}

func rejectLinks(path string, allowMissing bool) error {
	for {
		info, err := os.Lstat(path)
		if err != nil && (!allowMissing || !errors.Is(err, os.ErrNotExist)) {
			return err
		}
		if err == nil && ambiguousFile(info) {
			return errors.New("artifact paths cannot contain symbolic links; use the canonical physical directory path")
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

func ValidateOutputPath(path string) error {
	path, err := checkedPath(path)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		return errors.New("artifact output already exists")
	}
	parent := filepath.Dir(path)
	if err := rejectLinks(parent, false); err != nil {
		return err
	}
	info, err := os.Stat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("artifact output parent must be an existing directory")
	}
	return nil
}

func sameFacts(left, right os.FileInfo) bool {
	return right.Mode().IsRegular() && os.SameFile(left, right) && left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}
