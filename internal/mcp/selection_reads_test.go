package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/blisspixel/fitr/internal/role"
)

func selectionSizedFile(t *testing.T, path string, size int64) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(size); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSelectionReadsEnforcePerFileAggregateAndCountLimits(t *testing.T) {
	root := selectionRoot(t)
	reads := newSelectionReads(t.Context())
	for index := range 4 {
		path := filepath.Join(root, fmt.Sprintf("%d.json", index))
		selectionSizedFile(t, path, maxEvidenceFileBytes)
		if _, err := reads.file(path, maxEvidenceFileBytes); err != nil {
			t.Fatal("exact aggregate limit should pass", err)
		}
	}
	path := filepath.Join(root, "extra.json")
	selectionSizedFile(t, path, 1)
	if _, err := reads.file(path, maxEvidenceFileBytes); err == nil {
		t.Fatal("aggregate overflow accepted")
	}
	if _, err := reads.file(filepath.Join(root, "0.json"), maxEvidenceFileBytes-1); err == nil {
		t.Fatal("deduplicated file bypassed tighter specific limit")
	}
	reads = newSelectionReads(t.Context())
	if _, err := reads.file(root, maxEvidenceFileBytes); err == nil {
		t.Fatal("directory accepted as evidence")
	}
	selectionSizedFile(t, path, maxEvidenceFileBytes+1)
	if _, err := reads.file(path, maxEvidenceFileBytes); err == nil {
		t.Fatal("individual overflow accepted")
	}
	for index := range maxDirectoryEntries {
		if _, err := reads.file(filepath.Join(root, fmt.Sprintf("missing-%d", index)), 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := reads.file(filepath.Join(root, "missing-overflow"), 1); err == nil {
		t.Fatal("missing path count was unbounded")
	}
}

func TestSelectionReadsBoundDirectoryEnumerationAndCancellation(t *testing.T) {
	root := selectionRoot(t)
	for index := range 3 {
		selectionWrite(t, filepath.Join(root, strconv.Itoa(index)), nil)
	}
	reads := newSelectionReads(t.Context())
	if _, err := reads.directory(root, 2); err == nil {
		t.Fatal("oversized directory accepted")
	}
	if names, err := reads.directory(root, 3); err != nil || len(names) != 3 {
		t.Fatal("exact directory bound rejected", names, err)
	}
	if _, err := reads.directory(root, 2); err == nil {
		t.Fatal("cached directory bypassed tighter limit")
	}
	ctx, cancel := context.WithCancel(t.Context())
	reads = newSelectionReads(ctx)
	cancel()
	if _, err := reads.file(filepath.Join(root, "0"), 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := reads.directory(root, 3); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := reads.recheck(); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSelectionStatusPreflightsSpecificByteLimits(t *testing.T) {
	f := newSelectionFixture(t, selectionRoot(t), true)
	selected := f.life.Events[len(f.life.Events)-1].Selection.Selected.Attachment.Path
	for _, entry := range []struct {
		path  string
		limit int64
	}{
		{filepath.Join(f.roles.Dir, ".lifecycle", f.library.Name+".json"), maxSelectionMetadataBytes},
		{filepath.Join(filepath.Dir(selected), ".fitr-managed-store.json"), maxManagedMetadataBytes},
		{selected, maxEvidenceFileBytes},
	} {
		original, err := os.ReadFile(entry.path)
		if err != nil {
			t.Fatal(err)
		}
		selectionSizedFile(t, entry.path, entry.limit+1)
		if _, err := f.source.statusAt(t.Context(), f.library.Name, f.now); err == nil {
			t.Fatal("oversized selection file reached replay", entry.path)
		}
		selectionWrite(t, entry.path, original)
	}
}

func TestSelectionRecheckDetectsFileAndMetadataChanges(t *testing.T) {
	for _, change := range []string{"removed", "replaced", "modified", "metadata-restored-mtime", "appeared", "directory-member"} {
		t.Run(change, func(t *testing.T) {
			root := selectionRoot(t)
			path := filepath.Join(root, "metadata.json")
			selectionWrite(t, path, []byte(`{"name":"first"}`))
			reads := newSelectionReads(t.Context())
			var value struct {
				Name string `json:"name"`
			}
			if err := reads.decode(path, 100, &value); err != nil {
				t.Fatal(err)
			}
			if _, err := reads.directory(root, 3); err != nil {
				t.Fatal(err)
			}
			missing := filepath.Join(root, "missing")
			if _, err := reads.file(missing, 100); err != nil || reads.recheck() != nil {
				t.Fatal("unchanged snapshot failed", err)
			}
			selectionChangeSnapshot(t, reads, path, missing, change)
			if err := reads.recheck(); err == nil {
				t.Fatal("concurrent change accepted", change)
			}
		})
	}
}

func selectionChangeSnapshot(t *testing.T, reads *selectionReads, path, missing, change string) {
	t.Helper()
	switch change {
	case "removed":
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	case "replaced":
		if err := os.Rename(path, path+".old"); err != nil {
			t.Fatal(err)
		}
		selectionWrite(t, path, []byte(`{"name":"first"}`))
		if err := os.Remove(path + ".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, reads.files[path].ModTime(), reads.files[path].ModTime()); err != nil {
			t.Fatal(err)
		}
	case "modified":
		selectionWrite(t, path, []byte(`{"name":"longer"}`))
	case "metadata-restored-mtime":
		selectionWrite(t, path, []byte(`{"name":"other"}`))
		if err := os.Chtimes(path, reads.files[path].ModTime(), reads.files[path].ModTime()); err != nil {
			t.Fatal(err)
		}
	case "appeared":
		selectionWrite(t, missing, nil)
	case "directory-member":
		selectionWrite(t, filepath.Join(filepath.Dir(path), "new-member"), nil)
	}
}

func TestSelectionRecheckRejectsRetargetedRootAlias(t *testing.T) {
	physical, alias := evidenceDirectoryAlias(t)
	resolved, err := filepath.EvalSymlinks(physical)
	if err != nil {
		t.Fatal(err)
	}
	reads := newSelectionReads(t.Context())
	reads.rootAliases[alias] = resolved
	if err := reads.recheck(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(selectionRoot(t), alias); err != nil {
		t.Fatal(err)
	}
	if err := reads.recheck(); err == nil {
		t.Fatal("retargeted configured root passed recheck")
	}
}

func TestSelectionMetadataDecodeRejectsAmbiguousAndUnknownFields(t *testing.T) {
	root := selectionRoot(t)
	path := filepath.Join(root, "library.json")
	for _, data := range []string{`{"name":"a","name":"b"}`, `{"unknown":1}`, `{} {}`, `{`} {
		selectionWrite(t, path, []byte(data))
		var library role.Library
		if err := newSelectionReads(t.Context()).decode(path, 100, &library); err == nil {
			t.Fatal("ambiguous or unknown metadata accepted", data)
		}
	}
	if _, err := newSelectionReads(t.Context()).captureMetadata(filepath.Join(root, "missing"), 100); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
