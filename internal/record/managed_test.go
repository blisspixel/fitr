package record

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/lock"
)

func managedFixture(t *testing.T) (Store, ManagedStore, *Record) {
	t.Helper()
	root := Store{Dir: t.TempDir()}
	store, err := CreateManagedStore(root, ManagedStoreSpec{Schema: ManagedStoreSpecSchema, ID: "session-explore", SessionID: "session", Purpose: "exploration"})
	if err != nil {
		t.Fatal(err)
	}
	return root, store, derivedEvidenceBase(t)
}

func TestManagedStoreClosedTwinsAndLegacyIsolation(t *testing.T) {
	root, store, result := managedFixture(t)
	if _, err := store.Ref(); err == nil {
		t.Fatal("open store resolved")
	}
	paths, err := store.Save(result)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := store.Save(result); err != nil || again != paths {
		t.Fatal("identical retry failed", err)
	}
	ref, err := store.Close()
	if err != nil {
		t.Fatal(err)
	}
	if again, err := store.Close(); err != nil || again != ref {
		t.Fatal("close not idempotent", err)
	}
	if _, err := store.Save(result); err == nil {
		t.Fatal("closed store accepted a write")
	}
	resolved, err := ResolveManagedStore(root, ref)
	if err != nil {
		t.Fatal(err)
	}
	read, err := resolved.Read(result.Model)
	if err != nil || read.EvidenceIntegrityIssue() != "" || read.Completion.Signature != result.Completion.Signature {
		t.Fatal("signed evidence lost", err)
	}
	read.Checks[0].Pass = false
	again, err := resolved.Read(result.Model)
	if err != nil || !again.Checks[0].Pass {
		t.Fatal("read alias mutated managed evidence", err)
	}
	legacy := Store{Dir: store.directory()}
	if _, err := legacy.Save(result); err == nil {
		t.Fatal("ordinary save modified managed evidence")
	}
	if _, err := legacy.ClearHistory(); err == nil {
		t.Fatal("ordinary cleanup modified managed history")
	}
	for _, load := range []func() (LoadResult, error){legacy.LoadCurrent, legacy.LoadAll} {
		loaded, err := load()
		if err != nil || len(loaded.Records) != 1 || loaded.Records[0].EvidenceIntegrityIssue() == "" {
			t.Fatal("ordinary loader promoted managed evidence", err)
		}
	}
	plain, err := legacy.Read(paths.CanonicalPath)
	if err != nil || plain.EvidenceIntegrityIssue() == "" {
		t.Fatal("ordinary read promoted managed evidence", err)
	}
	if _, err := os.Stat(root.CanonicalPath(result.Model)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ordinary canonical created", err)
	}
}

func TestManagedStoreConflictLeavesNoOrphanAndRecoversMissingTwin(t *testing.T) {
	_, store, result := managedFixture(t)
	paths, err := store.Save(result)
	if err != nil {
		t.Fatal(err)
	}
	conflict := derivedEvidenceBase(t)
	if _, err := store.Save(conflict); err == nil {
		t.Fatal("different completion overwrote model")
	}
	entries, err := os.ReadDir(filepath.Dir(paths.HistoryPath))
	if err != nil || len(entries) != 1 {
		t.Fatal("conflict left history orphan", err)
	}
	if err := os.Remove(paths.CanonicalPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Close(); err == nil {
		t.Fatal("missing canonical closed")
	}
	if _, err := store.Save(result); err != nil {
		t.Fatal("identical crash retry failed", err)
	}
	if _, err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedReadSavedRecoversOnlyExactSignedOpenTwins(t *testing.T) {
	root, store, result := managedFixture(t)
	paths, err := store.Save(result)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.ReadSaved(result.Model)
	if err != nil || saved.Completion.Signature != result.Completion.Signature || saved.StableRunID() != result.StableRunID() {
		t.Fatal("saved point did not reconcile", err)
	}
	if _, err := store.Read(result.Model); err == nil {
		t.Fatal("open group became closed evidence")
	}
	if _, err := ResolveManagedStore(root, ManagedStoreRef{Schema: ManagedStoreRefSchema, ID: store.id, SealSHA256: managedHash([]byte("fake"))}); err == nil {
		t.Fatal("open store resolved")
	}
	if err := os.Remove(paths.HistoryPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadSaved(result.Model); err == nil {
		t.Fatal("missing twin reconciled")
	}
	if _, err := store.Save(result); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadSaved(result.Model); err == nil {
		t.Fatal("closed store used as pending recovery")
	}
}

func TestManagedStoreRejectsCorruptCopiedAndEscapingEvidence(t *testing.T) {
	for _, mutation := range []string{"canonical", "history", "copied-history", "manifest-id", "unknown", "duplicate", "case", "trailing", "seal"} {
		t.Run(mutation, func(t *testing.T) {
			root, store, result := managedFixture(t)
			paths, err := store.Save(result)
			if err != nil {
				t.Fatal(err)
			}
			ref, err := store.Close()
			if err != nil {
				t.Fatal(err)
			}
			mutateManagedStore(t, store, paths, mutation)
			if _, err := ResolveManagedStore(root, ref); err == nil {
				t.Fatal("modified group resolved")
			}
		})
	}
	root, _, _ := managedFixture(t)
	for _, id := range []string{"../outside", "UPPER", "", "a/b", strings.Repeat("a", 65)} {
		if _, err := OpenManagedStore(root, id); err == nil {
			t.Fatal("invalid ID accepted", id)
		}
	}
	if _, err := OpenManagedStore(Store{Dir: root.Dir + string(filepath.Separator) + ".."}, "session-explore"); err == nil {
		t.Fatal("parent path accepted")
	}
}

func mutateManagedStore(t *testing.T, store ManagedStore, paths SavedPaths, mutation string) {
	t.Helper()
	path := filepath.Join(store.directory(), managedMetadata)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	switch mutation {
	case "canonical", "history":
		path = paths.CanonicalPath
		if mutation == "history" {
			path = paths.HistoryPath
		}
		data = []byte(`{"model":"forged"}`)
	case "copied-history":
		data, err = os.ReadFile(paths.HistoryPath)
		if err != nil {
			t.Fatal(err)
		}
		path = filepath.Join(filepath.Dir(paths.HistoryPath), "copied.json")
	case "manifest-id":
		var manifest managedManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest.Spec.ID = "other"
		manifest.SealSHA256, err = managedSeal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		data, err = json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
	case "unknown":
		data = bytes.Replace(data, []byte(`"state":`), []byte(`"extra":1,"state":`), 1)
	case "duplicate":
		data = bytes.Replace(data, []byte(`"state":`), []byte(`"state":"closed","state":`), 1)
	case "case":
		data = bytes.Replace(data, []byte(`"schema":`), []byte(`"SCHEMA":`), 1)
	case "trailing":
		data = append(data, []byte(`{}`)...)
	case "seal":
		data = bytes.Replace(data, []byte(`"seal_sha256": "sha256:`), []byte(`"seal_sha256": "sha256:f`), 1)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestManagedStoreConcurrentIdenticalPublication(t *testing.T) {
	_, store, result := managedFixture(t)
	var wait sync.WaitGroup
	errorsOut := make(chan error, 8)
	for range 8 {
		wait.Go(func() {
			deadline := time.Now().Add(10 * time.Second)
			for {
				_, err := store.Save(result)
				var busy *lock.BusyError
				if !errors.As(err, &busy) || time.Now().After(deadline) {
					errorsOut <- err
					return
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
	wait.Wait()
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(store.directory(), historyDirName))
	if err != nil || len(entries) != 1 {
		t.Fatal("concurrent publication lost identity", err)
	}
}
