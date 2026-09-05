package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"
)

func TestSelectionStatusCanonicalRootAliasKeepsSealedIdentity(t *testing.T) {
	_, alias := evidenceDirectoryAlias(t)
	f := newSelectionFixture(t, filepath.Join(alias, "results"), false)
	value, err := f.source.statusAt(t.Context(), f.library.Name, f.now)
	if err != nil || value.(statusSummary).State != "qualified" {
		t.Fatal("verified root alias lost canonical bundle identity", value, err)
	}
}

func TestSelectionPreflightRejectsEscapingCopiedAndForgedPointPaths(t *testing.T) {
	f := newSelectionFixture(t, selectionRoot(t), true)
	original := f.life.Events[len(f.life.Events)-1].Selection.Selected
	for _, change := range []string{"outside", "parent", "relative", "copied", "forged-id", "forged-seal", "unknown", "conflicting-seal"} {
		t.Run(change, func(t *testing.T) {
			point := selectionClone(t, original)
			stores := map[string]string{}
			switch change {
			case "outside":
				point.StoreRef = nil
				point.Attachment.Path = filepath.Join(selectionRoot(t), "private.json")
			case "parent":
				point.Attachment.Path = filepath.Dir(point.Attachment.Path) + string(filepath.Separator) + "link" + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(point.Attachment.Path)
			case "relative":
				point.Attachment.Path = "relative.json"
			case "copied":
				point.Attachment.Path += ".copy"
			case "forged-id":
				point.StoreRef.ID = "../outside"
			case "forged-seal":
				point.StoreRef.SealSHA256 = "bad"
			case "unknown":
				point.StoreRef.ID = "unknown"
				point.Attachment.Path = record.Store{Dir: filepath.Join(f.source.root, ".evidence-stores", "unknown")}.CanonicalPath(point.Model.Resolved)
			case "conflicting-seal":
				stores[point.StoreRef.ID] = selectionDigest("other")
			}
			reads := newSelectionReads(t.Context())
			records := f.records
			if err := f.source.checkSelectionPoint(reads, point, stores, &records); err == nil {
				t.Fatal("invalid point accepted")
			}
		})
	}
}

func TestSelectionPreflightDeduplicatesAndBoundsReferencedStores(t *testing.T) {
	f := newSelectionFixture(t, selectionRoot(t), true)
	reads := newSelectionReads(t.Context())
	if _, err := f.source.checkSelectionEvidence(reads, &f.life); err != nil {
		t.Fatal(err)
	}
	// Two records plus their twins and one managed manifest are read once,
	// despite references in both completion and adoption and Selected.
	if len(reads.files) != 5 || len(reads.metadata) != 1 {
		t.Fatal("repeated lifecycle references multiplied input reads", len(reads.files), len(reads.metadata))
	}
	prior := reads.total
	if _, err := f.source.checkSelectionEvidence(reads, &f.life); err != nil || reads.total != prior {
		t.Fatal("repeated snapshot charged inputs again", err)
	}
	point := f.life.Events[len(f.life.Events)-1].Selection.Selected
	stores := map[string]string{}
	for index := range maxSelectionStores {
		stores[string(rune('a'+index))] = selectionDigest("sealed")
	}
	if err := f.source.checkManagedSelection(reads, point, stores); err == nil {
		t.Fatal("too many referenced stores accepted")
	}
	if _, err := f.source.checkSelectionEvidence(reads, &role.Lifecycle{Events: make([]role.LifecycleEvent, maxSelectionEvents+1)}); err == nil {
		t.Fatal("excess lifecycle events accepted")
	}
	tooMany := &role.Lifecycle{Events: []role.LifecycleEvent{{Selection: &role.SelectionReceipt{Points: make([]role.ConfirmationPoint, 9)}}}}
	if _, err := f.source.checkSelectionEvidence(reads, tooMany); err == nil {
		t.Fatal("excess event points accepted")
	}
}

func TestSelectionStatusRejectsLinkedMetadataAndEvidence(t *testing.T) {
	for _, target := range []string{"library", "lifecycle", "record", "history", "store", "namespace", "manifest"} {
		t.Run(target, func(t *testing.T) {
			f := newSelectionFixture(t, selectionRoot(t), true)
			selected := f.life.Events[len(f.life.Events)-1].Selection.Selected.Attachment.Path
			paths := map[string]string{
				"library": filepath.Join(f.roles.Dir, f.library.Name+".json"), "lifecycle": filepath.Join(f.roles.Dir, ".lifecycle", f.library.Name+".json"),
				"record": selected, "history": filepath.Join(filepath.Dir(selected), ".history"),
				"store": filepath.Dir(selected), "namespace": filepath.Join(f.source.root, ".evidence-stores"),
				"manifest": filepath.Join(filepath.Dir(selected), ".fitr-managed-store.json"),
			}
			path := paths[target]
			moved := filepath.Join(selectionRoot(t), "linked-target")
			if err := os.Rename(path, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, path); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			if _, err := f.source.statusAt(t.Context(), f.library.Name, f.now); err == nil {
				t.Fatal("linked evidence accepted")
			}
		})
	}
}

func TestSelectionStatusRejectsInvalidLifecycleBeforeDomainReplay(t *testing.T) {
	f := newSelectionFixture(t, selectionRoot(t), true)
	path := filepath.Join(f.roles.Dir, ".lifecycle", f.library.Name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{`{}`, `{"name":"structured-work","name":"other"}`, `{"unknown":true}`, string(data) + `{}`} {
		selectionWrite(t, path, []byte(invalid))
		if _, err := f.source.statusAt(t.Context(), f.library.Name, f.now); err == nil {
			t.Fatal("invalid lifecycle accepted")
		}
	}
	changed := selectionClone(t, f.life)
	changed.Events[len(changed.Events)-1].Selection.Selected.StoreRef.SealSHA256 = selectionDigest("forged")
	data, err = json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	selectionWrite(t, path, data)
	if _, err := f.source.statusAt(t.Context(), f.library.Name, f.now); err == nil {
		t.Fatal("lifecycle digest forgery accepted")
	}
}

func TestSelectionRawTraversalAndMissingAncestorCannotHideLink(t *testing.T) {
	root := selectionRoot(t)
	outside := selectionRoot(t)
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	for _, path := range []string{link + string(filepath.Separator) + ".." + string(filepath.Separator) + "receipt.json", filepath.Join(link, "missing", "receipt.json"), "relative.json"} {
		if err := selectionNoLinks(path); err == nil {
			t.Fatal("unsafe spelling passed", path)
		}
	}
	if err := selectionNoLinks(filepath.Join(root, "missing", "receipt.json")); err != nil {
		t.Fatal("ordinary missing ancestor rejected", err)
	}
	if err := selectionNoLinks(filepath.Join(root, strings.Repeat("x", 10))); err != nil {
		t.Fatal(err)
	}
}
