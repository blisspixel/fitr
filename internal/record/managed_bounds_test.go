package record

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedStoreRecordBoundAndCreationIdentity(t *testing.T) {
	root, store, _ := managedFixture(t)
	spec, err := store.Spec()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateManagedStore(root, spec); err != nil {
		t.Fatal("same creation failed", err)
	}
	spec.SessionID = "another"
	if _, err := CreateManagedStore(root, spec); err == nil {
		t.Fatal("store identity changed")
	}
	for index := range maximumManagedRecords + 1 {
		r := managedNamedRecord(t, fmt.Sprintf("model-%02d", index))
		_, err := store.Save(r)
		if index < maximumManagedRecords && err != nil {
			t.Fatal(index, err)
		}
		if index == maximumManagedRecords && err == nil {
			t.Fatal("record bound exceeded")
		}
	}
	if _, err := store.Close(); err != nil {
		t.Fatal("overflow attempt damaged existing store", err)
	}
}

func managedNamedRecord(t *testing.T, name string) *Record {
	t.Helper()
	r := derivedEvidenceBase(t)
	profile, provenance := r.Completion.Profile, *r.Manifest.Provenance
	r.Model, r.Scorecard.Model = name, name
	r.Manifest, r.Completion, r.RunID = nil, nil, ""
	if err := r.AttachManifest(digestIdentity(t, name, name), provenance); err != nil {
		t.Fatal(err)
	}
	if err := r.CompleteEvidence(profile); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestManagedStoreNativeLinksAndRootAlias(t *testing.T) {
	root, store, r := managedFixture(t)
	paths, err := store.Save(r)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Close()
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "root-alias")
	if err := os.Symlink(root.Dir, alias); err != nil {
		t.Skip("native symlink unavailable", err)
	}
	if _, err := ResolveManagedStore(Store{Dir: alias}, ref); err != nil {
		t.Fatal("physical root alias failed", err)
	}
	if err := os.Rename(paths.CanonicalPath, paths.CanonicalPath+".outside"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(paths.CanonicalPath+".outside", paths.CanonicalPath); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveManagedStore(root, ref); err == nil {
		t.Fatal("managed leaf symlink accepted")
	}
}

func TestManagedStoreRejectsNamespaceSymlinksAndTraversal(t *testing.T) {
	root := Store{Dir: t.TempDir()}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root.Dir, managedDirectory)); err != nil {
		t.Skip("native symlink unavailable", err)
	}
	spec := ManagedStoreSpec{Schema: ManagedStoreSpecSchema, ID: "a", SessionID: "a", Purpose: "confirmation"}
	if _, err := CreateManagedStore(root, spec); err == nil {
		t.Fatal("namespace symlink accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("outside path modified", err)
	}
	bad := Store{Dir: root.Dir + string(filepath.Separator) + managedDirectory + string(filepath.Separator) + ".."}
	if _, err := CreateManagedStore(bad, spec); err == nil {
		t.Fatal("raw symlink parent traversal accepted")
	}
}

func TestManagedStoreBoundedReadersAndInvalidHandles(t *testing.T) {
	_, store, r := managedFixture(t)
	if _, err := store.Save(nil); err == nil {
		t.Fatal("nil evidence accepted")
	}
	r.Checks[0].Pass = false
	if _, err := store.Save(r); err == nil {
		t.Fatal("tampered signature accepted")
	}
	if _, err := (ManagedStore{}).Ref(); err == nil {
		t.Fatal("zero handle resolved")
	}
	for index := range maximumManagedRecords + 3 {
		if err := os.WriteFile(filepath.Join(store.directory(), fmt.Sprintf("unexpected-%d", index)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Close(); err == nil {
		t.Fatal("unbounded directory closed")
	}
	if _, _, err := store.usage(); err == nil {
		t.Fatal("unbounded usage traversal accepted")
	}
	path := filepath.Join(t.TempDir(), "bounded")
	if err := os.WriteFile(path, []byte("1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := managedReadFile(path, 3); err == nil {
		t.Fatal("byte bound ignored")
	}
	if err := managedPublish(path, []byte("different")); err == nil {
		t.Fatal("exclusive publication overwritten")
	}
}
