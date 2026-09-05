package autoruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

func validatePath(path string) error {
	windowsAbsolute := len(path) >= 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1] == ':' && (path[2] == '/' || path[2] == '\\')
	if len(path) == 0 || len(path) > 4096 || (!filepath.IsAbs(path) && !windowsAbsolute) || strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		return errors.New("an absolute local physical path is required")
	}
	for _, c := range path {
		if unicode.IsControl(c) {
			return errors.New("path contains control characters")
		}
	}
	rest := strings.TrimPrefix(path, filepath.VolumeName(path))
	if windowsAbsolute {
		rest = path[2:]
	}
	if strings.Contains(rest, ":") {
		return errors.New("alternate data streams are not supported")
	}
	for _, part := range strings.FieldsFunc(rest, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return errors.New("parent traversal and trailing dot/space path components are not supported")
		}
	}
	return nil
}

func physicalPath(path string, directory bool) (string, error) {
	if err := validatePath(path); err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("owned runtime path is not native to this platform")
	}
	path = filepath.Clean(path)
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("owned runtime paths cannot contain symbolic links")
		}
		if current == path && ((directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular())) {
			return "", errors.New("owned runtime path has the wrong file type")
		}
		if current != path && !info.IsDir() {
			return "", errors.New("owned runtime parent is not a directory")
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return path, nil
}

// Ollama startup creates these directories even with pruning disabled. Refuse
// an uninitialized store so an owned experiment cannot initialize user storage.
func validateModelStore(path string) error {
	if _, err := physicalPath(path, true); err != nil {
		return err
	}
	for _, name := range []string{"blobs", "manifests"} {
		if _, err := physicalPath(filepath.Join(path, name), true); err != nil {
			return fmt.Errorf("owned runtime needs an existing physical model-store %s directory: %w", name, err)
		}
	}
	return nil
}

type libraryFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func installationDigests(ctx context.Context, executable string) (string, string, error) {
	var used int64
	exeHash, _, err := hashFile(ctx, executable, &used)
	if err != nil {
		return "", "", err
	}
	base := filepath.Dir(executable)
	root, err := physicalPath(filepath.Join(base, "lib", "ollama"), true)
	if err != nil {
		return "", "", fmt.Errorf("owned runtime requires its physical adjacent lib/ollama tree: %w", err)
	}
	paths, err := installationFiles(ctx, base, root)
	if err != nil {
		return "", "", err
	}
	files := make([]libraryFile, 0, len(paths))
	for _, path := range paths {
		hash, size, hashErr := hashFile(ctx, path, &used)
		if hashErr != nil {
			return "", "", hashErr
		}
		rel, relErr := filepath.Rel(base, path)
		if relErr != nil {
			return "", "", relErr
		}
		files = append(files, libraryFile{filepath.ToSlash(rel), size, hash})
	}
	// Catch added/removed dependency paths as well as per-file identity changes.
	after, err := installationFiles(ctx, base, root)
	if err != nil || !samePaths(paths, after) {
		return "", "", errors.New("runtime dependency inventory changed during hashing")
	}
	return exeHash, sealJSON("fitr.autoruntime.libraries.v1", files), nil
}

func installationFiles(ctx context.Context, base, root string) ([]string, error) {
	inventory := runtimeInventory{ctx: ctx}
	if err := inventory.visit(base, true); err != nil {
		return nil, err
	}
	if err := inventory.visit(root, false); err != nil {
		return nil, err
	}
	if len(inventory.files) == 0 {
		return nil, errors.New("runtime dependency tree is empty")
	}
	sort.Strings(inventory.files)
	return inventory.files, nil
}

type runtimeInventory struct {
	ctx   context.Context
	files []string
	count int
}

func (v *runtimeInventory) visit(dir string, top bool) error {
	if err := v.ctx.Err(); err != nil {
		return err
	}
	if _, err := physicalPath(dir, true); err != nil {
		return err
	}
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		entries, readErr := f.ReadDir(128)
		for _, entry := range entries {
			if err := v.entry(dir, entry, top); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (v *runtimeInventory) entry(dir string, entry os.DirEntry, top bool) error {
	v.count++
	if v.count > MaxRuntimeFiles {
		return errors.New("runtime dependency inventory exceeds 4096 entries")
	}
	path := filepath.Join(dir, entry.Name())
	if top {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(path), ".dll") {
			v.files = append(v.files, path)
		}
		return nil
	}
	if entry.Type()&os.ModeSymlink != 0 {
		return errors.New("runtime dependencies cannot contain symbolic links")
	}
	if entry.IsDir() {
		return v.visit(path, false)
	}
	v.files = append(v.files, path)
	return nil
}

func samePaths(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hashFile(ctx context.Context, path string, used *int64) (string, int64, error) {
	if _, err := physicalPath(path, false); err != nil {
		return "", 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > MaxFileBytes || before.Size() > MaxRuntimeBytes-*used {
		return "", 0, errors.New("runtime dependency exceeds the regular-file byte budget")
	}
	_ = os.SameFile(before, before) // materialize native Windows file identity before hashing
	*used += before.Size()
	h := sha256.New()
	buffer := make([]byte, 256<<10)
	var read int64
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		n, readErr := f.Read(buffer)
		read += int64(n)
		if read > before.Size() {
			return "", 0, errors.New("runtime file grew during hashing")
		}
		_, _ = h.Write(buffer[:n])
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", 0, readErr
		}
	}
	after, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return "", 0, err
	}
	if read != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) ||
		!os.SameFile(before, after) || !os.SameFile(before, pathInfo) || pathInfo.Mode()&os.ModeSymlink != 0 {
		return "", 0, errors.New("runtime file changed during hashing")
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), read, nil
}
