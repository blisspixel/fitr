package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/strictjson"
)

const (
	maxSelectionMetadataBytes = 1 << 20
	maxSelectionEvents        = 64
	maxSelectionStores        = 16
	maxManagedMetadataBytes   = 64 << 10
)

// Snapshot checks detect ordinary concurrent atomic publications. They do not
// turn subsequent path-based record reads into a filesystem sandbox or attest
// against an authorized writer changing and restoring files during the call.
type selectionReads struct {
	ctx         context.Context
	files       map[string]os.FileInfo
	directories map[string][]string
	metadata    map[string][]byte
	rootAliases map[string]string
	total       int64
}

func newSelectionReads(ctx context.Context) *selectionReads {
	return &selectionReads{ctx: ctx, files: map[string]os.FileInfo{}, directories: map[string][]string{}, metadata: map[string][]byte{}, rootAliases: map[string]string{}}
}

func selectionNoLinks(path string) error {
	if err := localEvidencePath(path); err != nil {
		return err
	}
	if !filepath.IsAbs(path) {
		return errors.New("selection evidence requires an absolute local path")
	}
	current := filepath.VolumeName(path) + string(filepath.Separator)
	for _, part := range evidencePathParts(strings.TrimPrefix(path, filepath.VolumeName(path))) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info != nil && info.Mode()&os.ModeSymlink != 0 {
			return errors.New("selection evidence paths cannot contain symbolic links")
		}
	}
	return nil
}

func (reads *selectionReads) file(path string, limit int64) (os.FileInfo, error) {
	if err := reads.ctx.Err(); err != nil {
		return nil, err
	}
	if err := selectionNoLinks(path); err != nil {
		return nil, err
	}
	if prior, ok := reads.files[path]; ok {
		if prior != nil && prior.Size() > limit {
			return nil, errors.New("selection file exceeds its specific limit")
		}
		return prior, nil
	}
	if len(reads.files) >= maxDirectoryEntries {
		return nil, errors.New("selection exceeds its inspected file limit")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		reads.files[path] = nil
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit || info.Size() > maxEvidenceTotalBytes-reads.total {
		return nil, errors.New("selection evidence is nonregular or exceeds its byte limits")
	}
	_ = os.SameFile(info, info) // Capture lazy Windows identity before any reopen.
	reads.total += info.Size()
	reads.files[path] = info
	return info, nil
}

func (reads *selectionReads) directory(path string, limit int) ([]string, error) {
	if err := reads.ctx.Err(); err != nil {
		return nil, err
	}
	if err := selectionNoLinks(path); err != nil {
		return nil, err
	}
	if names, ok := reads.directories[path]; ok {
		if len(names) > limit {
			return nil, errors.New("selection directory exceeds its specific entry limit")
		}
		return names, nil
	}
	names, err := selectionDirectoryNames(path, limit)
	if err == nil {
		reads.directories[path] = names
	}
	return names, err
}

func selectionDirectoryNames(path string, limit int) ([]string, error) {
	if err := checkedDirectory(path); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries, err := file.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > limit {
		return nil, errors.New("selection directory exceeds its entry limit")
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, errors.New("selection directory contains a symbolic link")
		}
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names, nil
}

func (reads *selectionReads) captureMetadata(path string, limit int64) ([]byte, error) {
	info, err := reads.file(path, limit)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, os.ErrNotExist
	}
	data, err := boundedio.ReadFile(path, limit)
	if err != nil {
		return nil, err
	}
	reads.metadata[path] = data
	return data, nil
}

func (reads *selectionReads) decode(path string, limit int64, into any) error {
	data, err := reads.captureMetadata(path, limit)
	if err != nil {
		return err
	}
	if err := strictjson.Validate(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return err
	}
	return nil
}

func (reads *selectionReads) recheck() error {
	for alias, physical := range reads.rootAliases {
		resolved, err := resolveLocalEvidence(alias)
		if err != nil || !samePath(resolved, physical) {
			return errors.New("selection results root alias changed during review")
		}
	}
	for path, before := range reads.files {
		if err := reads.ctx.Err(); err != nil {
			return err
		}
		if err := selectionNoLinks(path); err != nil {
			return err
		}
		after, err := os.Lstat(path)
		if before == nil && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || before == nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			return errors.New("selection evidence changed during review")
		}
		if original, ok := reads.metadata[path]; ok {
			data, err := boundedio.ReadFile(path, maxSelectionMetadataBytes)
			if err != nil || !bytes.Equal(original, data) {
				return errors.New("selection metadata changed during review")
			}
		}
	}
	for path, names := range reads.directories {
		if err := reads.ctx.Err(); err != nil {
			return err
		}
		if err := selectionNoLinks(path); err != nil {
			return err
		}
		current, err := selectionDirectoryNames(path, maxDirectoryEntries)
		if err != nil || !reflect.DeepEqual(names, current) {
			return errors.New("selection directory changed during review")
		}
	}
	return reads.ctx.Err()
}
