package discovery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/lock"
)

type inboxSaveResult struct {
	idea Idea
	err  error
}

func concurrentInboxSaves(directory string, ideas []Idea) []inboxSaveResult {
	results := make([]inboxSaveResult, len(ideas))
	start := make(chan struct{})
	var workers sync.WaitGroup
	for index, idea := range ideas {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results[index].idea, results[index].err = Save(directory, idea)
		}()
	}
	close(start)
	workers.Wait()
	return results
}

func TestInboxConcurrentDuplicateCapturesPreserveFirstPublication(t *testing.T) {
	directory := t.TempDir()
	ideas := make([]Idea, 12)
	for index := range ideas {
		ideas[index] = testIdea(t)
		ideas[index].CapturedAt = time.Unix(int64(index+1), 0).UTC().Format(time.RFC3339)
	}
	results := concurrentInboxSaves(directory, ideas)
	var first Idea
	for _, result := range results {
		if result.err != nil {
			var busy *lock.BusyError
			if !errors.As(result.err, &busy) {
				t.Fatalf("unexpected concurrent save error: %v", result.err)
			}
			continue
		}
		if first.ID == "" {
			first = result.idea
		} else if result.idea != first {
			t.Fatalf("concurrent duplicate replaced capture: %+v versus %+v", result.idea, first)
		}
	}
	if first.ID == "" {
		t.Fatal("no concurrent save succeeded")
	}
	for _, idea := range ideas {
		if saved, err := Save(directory, idea); err != nil || saved != first {
			t.Fatalf("retry changed the first capture: %+v, %v", saved, err)
		}
	}
	stored, err := List(directory, "")
	if err != nil || len(stored) != 1 || stored[0] != first {
		t.Fatalf("invalid final inbox: %+v, %v", stored, err)
	}
}

func seedInboxIdeas(t *testing.T, directory string, count int) {
	t.Helper()
	for index := range count {
		idea, err := New(fmt.Sprintf("seed-%d", index), "", "coding", "", "", time.Unix(1, 0))
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.MarshalIndent(idea, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, idea.ID+".json"), append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInboxConcurrentInsertsCannotExceedLastAvailableSlot(t *testing.T) {
	directory := t.TempDir()
	seedInboxIdeas(t, directory, maximumIdeas-1)
	ideas := make([]Idea, 12)
	for index := range ideas {
		ideas[index] = testIdea(t)
		ideas[index].Claim = fmt.Sprintf("contender-%d", index)
		ideas[index].ID = ideas[index].digest()
	}
	results := concurrentInboxSaves(directory, ideas)
	var winner Idea
	for _, result := range results {
		if result.err == nil {
			if winner.ID != "" {
				t.Fatal("multiple distinct contenders entered the last slot")
			}
			winner = result.idea
			continue
		}
		var busy *lock.BusyError
		if !errors.As(result.err, &busy) && !strings.Contains(result.err.Error(), "1000-entry limit") {
			t.Fatalf("unexpected capacity error: %v", result.err)
		}
	}
	if winner.ID == "" {
		t.Fatal("no contender entered the available slot")
	}
	stored, err := List(directory, "")
	if err != nil || len(stored) != maximumIdeas {
		t.Fatalf("inbox no longer readable at capacity: %d, %v", len(stored), err)
	}
	for _, idea := range ideas {
		saved, err := Save(directory, idea)
		if idea.ID == winner.ID {
			if err != nil || saved != winner {
				t.Fatalf("duplicate failed at capacity: %+v, %v", saved, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "1000-entry limit") {
			t.Fatalf("retry entered a full inbox: %+v, %v", saved, err)
		}
	}
}

func TestInboxLockPreventsMutationAndTemporaryInventoryReads(t *testing.T) {
	directory, err := canonicalInboxDirectory(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	seedInboxIdeas(t, directory, 2)
	guard, err := acquireInbox(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Release()
	_, saveErr := Save(directory, testIdea(t))
	_, listErr := List(directory, "")
	for _, operationErr := range []error{saveErr, listErr} {
		var busy *lock.BusyError
		if !errors.As(operationErr, &busy) || !strings.Contains(operationErr.Error(), "retry") || strings.Contains(operationErr.Error(), "inference") {
			t.Fatalf("wrong busy diagnostic: %v", operationErr)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 2 {
		t.Fatalf("busy attempt mutated inventory or stored its lock there: %d, %v", len(entries), err)
	}
}

func TestInboxPublicationIsExclusiveAndPreservesPrivateCanonicalBytes(t *testing.T) {
	directory := t.TempDir()
	idea := testIdea(t)
	// The v1 parser permits this time spelling, and timestamps do not affect IDs.
	idea.CapturedAt = "1970-01-01T01:00:01+01:00"
	if _, err := Save(directory, idea); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, idea.ID+".json")
	want, err := json.MarshalIndent(idea, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if err := publishIdea(path, []byte("replacement")); !errors.Is(err, os.ErrExist) {
		t.Fatalf("publication was not exclusive: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("publication changed v1 bytes: %q, %v", got, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary publication entry leaked: %d, %v", len(entries), err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("idea is not private: %v", info.Mode())
	}
	if err := publishIdea(filepath.Join(directory, "missing", "entry.json"), want); err == nil {
		t.Fatal("publication succeeded without a parent directory")
	}
}

func TestInboxBoundedInventoryCountsIgnoredEntries(t *testing.T) {
	directory := t.TempDir()
	for index := range maximumIdeas {
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("ignored-%04d", index)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if entries, err := inboxEntries(directory); err != nil || len(entries) != maximumIdeas {
		t.Fatalf("rejected exact inventory bound: %d, %v", len(entries), err)
	}
	if ideas, err := List(directory, ""); err != nil || len(ideas) != 0 {
		t.Fatalf("ignored entries became ideas: %+v, %v", ideas, err)
	}
	if _, err := Save(directory, testIdea(t)); err == nil {
		t.Fatal("ignored entries stopped counting toward the existing limit")
	}
	if err := os.Mkdir(filepath.Join(directory, "extra-directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if entries, err := inboxEntries(directory); err == nil || len(entries) != 0 {
		t.Fatalf("enumerator returned an oversized inventory: %d, %v", len(entries), err)
	}
	if _, err := List(directory, ""); err == nil {
		t.Fatal("list accepted an oversized inventory")
	}
	if _, err := Save(directory, testIdea(t)); err == nil {
		t.Fatal("save accepted an oversized inventory")
	}
}

func TestInboxRejectsRawParentTraversalBeforeAnyCleaning(t *testing.T) {
	directory := t.TempDir()
	idea := testIdea(t)
	if _, err := Save(directory, idea); err != nil {
		t.Fatal(err)
	}
	rawDirectory := directory + string(filepath.Separator) + "child" + string(filepath.Separator) + ".."
	for _, path := range []string{"", "bad\npath", rawDirectory, "safe\\child\\..", "C:child\\.."} {
		if _, err := Save(path, idea); err == nil {
			t.Fatalf("save accepted raw unsafe directory %q", path)
		}
		if _, err := List(path, ""); err == nil {
			t.Fatalf("list accepted raw unsafe directory %q", path)
		}
		loadPath := path
		if path != "" {
			loadPath += string(filepath.Separator) + idea.ID + ".json"
		}
		if _, err := Load(loadPath); err == nil {
			t.Fatalf("load accepted raw unsafe path %q", path)
		}
	}
	if _, err := Load(""); err == nil {
		t.Fatal("load accepted an empty path")
	}
	if _, err := Load(filepath.Join(directory, "missing", "idea.json")); err == nil {
		t.Fatal("load accepted a missing parent")
	}
	if _, err := Load(filepath.Join(directory, "missing.json")); err == nil {
		t.Fatal("load accepted a missing entry")
	}
}

func TestInboxDirectoryAliasesShareLockAndRemainReadable(t *testing.T) {
	parent := t.TempDir()
	directory := filepath.Join(parent, "physical")
	idea := testIdea(t)
	if _, err := Save(directory, idea); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(directory, alias); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	physical, err := canonicalInboxDirectory(directory, false)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := acquireInbox(physical)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Release()
	aliases := []string{alias}
	if runtime.GOOS == "windows" {
		aliases = append(aliases, strings.ToUpper(alias))
	}
	for _, path := range aliases {
		var busy *lock.BusyError
		if _, err := Save(path, idea); !errors.As(err, &busy) {
			t.Fatalf("alias bypassed physical directory lock: %v", err)
		}
	}
	if err := guard.Release(); err != nil {
		t.Fatal(err)
	}
	if ideas, err := List(alias, ""); err != nil || len(ideas) != 1 || ideas[0] != idea {
		t.Fatalf("safe root alias became unreadable: %+v, %v", ideas, err)
	}
	if loaded, err := Load(filepath.Join(alias, idea.ID+".json")); err != nil || loaded != idea {
		t.Fatalf("safe parent alias became unreadable: %+v, %v", loaded, err)
	}
}

func TestInboxRejectsSymlinkEntryOnDirectLoadSaveAndList(t *testing.T) {
	parent := t.TempDir()
	idea := testIdea(t)
	if _, err := Save(parent, idea); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(parent, "inbox")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, idea.ID+".json")
	link := filepath.Join(directory, idea.ID+".json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("file symlinks unavailable: %v", err)
	}
	if _, err := Load(link); err == nil {
		t.Fatal("direct load followed a symlink entry")
	}
	if _, err := Save(directory, idea); err == nil {
		t.Fatal("save followed a symlink entry")
	}
	if _, err := List(directory, ""); err == nil {
		t.Fatal("list followed a symlink entry")
	}
	if loaded, err := Load(target); err != nil || loaded != idea {
		t.Fatalf("symlink refusal changed the target: %+v, %v", loaded, err)
	}
}
