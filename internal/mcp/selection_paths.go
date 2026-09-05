package mcp

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"
)

func (source *localEvidence) checkSelectionPoint(reads *selectionReads, point role.ConfirmationPoint, stores map[string]string, records *record.Store) error {
	path := point.Attachment.Path
	// Reject raw traversal before Dir or Clean can erase a linked component.
	if err := localEvidencePath(path); err != nil {
		return err
	}
	if !filepath.IsAbs(path) || strings.ContainsAny(filepath.Base(path), "/\\") {
		return errors.New("invalid selection evidence path")
	}
	if point.StoreRef != nil {
		return source.checkManagedSelection(reads, point, stores)
	}
	parent, err := resolveLocalEvidence(filepath.Dir(path))
	if err != nil || !samePath(parent, source.root) {
		return errors.New("selection evidence is outside the canonical results root")
	}
	canonical := record.Store{Dir: source.root}.CanonicalPath(point.Model.Resolved)
	if !samePath(filepath.Join(parent, filepath.Base(path)), canonical) {
		return errors.New("selection path is not canonical for its model")
	}
	// Ordinary receipts preserve the caller's root spelling in their sealed
	// bundle. Keep that spelling for the shared exact-path check after proving
	// its parent resolves to the trusted physical root. Managed stores resolve
	// the same root physically themselves.
	if !samePath(records.Dir, source.root) && !samePath(records.Dir, filepath.Dir(path)) {
		return errors.New("ordinary selection receipts use different root spellings")
	}
	records.Dir = filepath.Dir(path)
	reads.rootAliases[records.Dir] = source.root
	if _, err := reads.file(canonical, maxEvidenceFileBytes); err != nil {
		return err
	}
	return selectionHistory(reads, filepath.Join(source.root, ".history"), maxDirectoryEntries)
}

func selectionHistory(reads *selectionReads, directory string, limit int) error {
	names, err := reads.directory(directory, limit)
	if err != nil {
		return err
	}
	for _, name := range names {
		if strings.HasSuffix(name, ".json") {
			if _, err := reads.file(filepath.Join(directory, name), maxEvidenceFileBytes); err != nil {
				return err
			}
		}
	}
	return nil
}

func (source *localEvidence) checkManagedSelection(reads *selectionReads, point role.ConfirmationPoint, stores map[string]string) error {
	ref := *point.StoreRef
	if err := ref.Validate(); err != nil {
		return err
	}
	namespace := filepath.Join(source.root, ".evidence-stores")
	directory := filepath.Join(namespace, ref.ID)
	canonical := record.Store{Dir: directory}.CanonicalPath(point.Model.Resolved)
	if point.Attachment.Path != canonical {
		return errors.New("managed selection path differs from its fixed-root identity")
	}
	if digest, exists := stores[ref.ID]; exists {
		if digest != ref.SealSHA256 {
			return errors.New("managed selection repeats a store with another seal")
		}
		return nil
	}
	if len(stores) >= maxSelectionStores {
		return errors.New("selection exceeds its referenced store limit")
	}
	stores[ref.ID] = ref.SealSHA256
	if _, err := reads.directory(namespace, maxDirectoryEntries); err != nil {
		return err
	}
	names, err := reads.directory(directory, 18)
	if err != nil {
		return err
	}
	if names == nil {
		return errors.New("selection references an unknown managed store")
	}
	for _, name := range names {
		if name == ".history" {
			continue
		}
		limit := int64(maxEvidenceFileBytes)
		if name == ".fitr-managed-store.json" {
			limit = maxManagedMetadataBytes
		}
		if _, err := reads.file(filepath.Join(directory, name), limit); err != nil {
			return err
		}
	}
	if _, err := reads.captureMetadata(filepath.Join(directory, ".fitr-managed-store.json"), maxManagedMetadataBytes); err != nil {
		return errors.New("selection managed manifest is unavailable")
	}
	return selectionHistory(reads, filepath.Join(directory, ".history"), 16)
}
