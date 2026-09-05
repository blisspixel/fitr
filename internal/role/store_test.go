package role

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/lock"
)

func persistenceStore(t *testing.T) Store {
	t.Helper()
	return Store{Dir: filepath.Join(t.TempDir(), "roles")}
}

func persistenceAttachment(t *testing.T, index int) Attachment {
	t.Helper()
	return Attachment{Path: filepath.Join(t.TempDir(), "evidence.json"), EvidenceSHA256: fmt.Sprintf("sha256:%064x", index), RunID: fmt.Sprintf("run-%d", index)}
}

func persistenceDefine(t *testing.T, store Store) Library {
	t.Helper()
	library, err := store.Define(persistenceSpec())
	if err != nil {
		t.Fatal(err)
	}
	return library
}

func TestRoleStoreRevisionAndAttachmentLifecycle(t *testing.T) {
	store := persistenceStore(t)
	if libraries, err := store.List(); err != nil || len(libraries) != 0 {
		t.Fatalf("empty list=%v err=%v", libraries, err)
	}
	original := persistenceDefine(t, store)
	attachment := persistenceAttachment(t, 1)
	library, err := store.Attach("coding", attachment)
	if err != nil || len(library.Candidates) != 1 {
		t.Fatalf("attach=%+v err=%v", library, err)
	}
	attachment.Path = filepath.Join(t.TempDir(), "copy.json")
	library, err = store.Attach("coding", attachment)
	if err != nil || len(library.Candidates) != 1 {
		t.Fatalf("dedupe=%+v err=%v", library, err)
	}
	spec := persistenceSpec()
	spec.Description = "Revised role"
	library, err = store.Define(spec)
	if err != nil || len(library.Revisions) != 2 || len(library.Candidates) != 1 {
		t.Fatalf("revision=%+v err=%v", library, err)
	}
	if library.Revisions[0].ID != original.CurrentRevision {
		t.Fatal("old revision changed")
	}
	current, err := library.CurrentSpec()
	if err != nil || current.Description != spec.Description {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	library, err = store.Define(persistenceSpec())
	if err != nil || len(library.Revisions) != 2 || library.CurrentRevision != original.CurrentRevision {
		t.Fatalf("reselect=%+v err=%v", library, err)
	}
	library, err = store.Detach("coding", attachment.EvidenceSHA256)
	if err != nil || len(library.Candidates) != 0 {
		t.Fatalf("detach=%+v err=%v", library, err)
	}
	if _, err := store.Detach("coding", attachment.EvidenceSHA256); err != nil {
		t.Fatal(err)
	}
	if libraries, err := store.List(); err != nil || len(libraries) != 1 {
		t.Fatalf("list=%v err=%v", libraries, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(store.Dir, "coding.json"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("file permissions=%v err=%v", info, err)
		}
	}
}

func TestLibraryValidatesEveryRevisionAndAttachment(t *testing.T) {
	library := persistenceDefine(t, persistenceStore(t))
	tests := map[string]func(*Library){
		"schema":              func(l *Library) { l.Schema = "other" },
		"name":                func(l *Library) { l.Name = "../other" },
		"missing revision":    func(l *Library) { l.CurrentRevision = "missing" },
		"empty revisions":     func(l *Library) { l.Revisions = nil },
		"revision digest":     func(l *Library) { l.Revisions[0].ID = "wrong" },
		"revision spec":       func(l *Library) { l.Revisions[0].Spec.Schema = "wrong" },
		"revision name":       func(l *Library) { l.Name = "other" },
		"repeated revision":   func(l *Library) { l.Revisions = append(l.Revisions, l.Revisions[0]) },
		"invalid attachment":  func(l *Library) { l.Candidates = []Attachment{{Path: "relative"}} },
		"repeated attachment": func(l *Library) { a := persistenceAttachment(t, 1); l.Candidates = []Attachment{a, a} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			copyOf := library
			copyOf.Revisions = append([]Revision(nil), library.Revisions...)
			mutate(&copyOf)
			if _, err := copyOf.CurrentSpec(); err == nil {
				t.Fatal("invalid library accepted")
			}
		})
	}
}

func TestRoleStoreRejectsInvalidMutationAndNames(t *testing.T) {
	store := persistenceStore(t)
	persistenceDefine(t, store)
	attachment := persistenceAttachment(t, 1)
	for _, name := range []string{"", "../escape", "UPPER", "a/b", strings.Repeat("x", 65)} {
		if _, err := store.Load(name); err == nil {
			t.Fatalf("unsafe name %q accepted", name)
		}
		if _, err := store.Attach(name, attachment); err == nil {
			t.Fatalf("unsafe mutation %q accepted", name)
		}
	}
	if _, err := store.Load("missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing err=%v", err)
	}
	if _, err := store.Attach("missing", attachment); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("attach missing err=%v", err)
	}
	if _, err := store.Attach("coding", Attachment{}); err == nil {
		t.Fatal("invalid attachment accepted")
	}
	if _, err := store.Detach("coding", "invalid"); err == nil {
		t.Fatal("invalid detach accepted")
	}
	if _, err := store.Attach("coding", attachment); err != nil {
		t.Fatal(err)
	}
	attachment.RunID = "other"
	if _, err := store.Attach("coding", attachment); err == nil {
		t.Fatal("conflicting attachment accepted")
	}
	if _, err := (Store{}).Define(persistenceSpec()); err == nil {
		t.Fatal("empty store accepted")
	}
	if _, err := (Store{}).List(); err == nil {
		t.Fatal("empty list store accepted")
	}
	if _, err := store.Define(Spec{}); err == nil {
		t.Fatal("invalid define accepted")
	}
}

func TestRoleStoreLockPreventsConcurrentMutation(t *testing.T) {
	store := persistenceStore(t)
	persistenceDefine(t, store)
	guard, err := store.acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Release()
	_, err = store.Attach("coding", persistenceAttachment(t, 1))
	var busy *lock.BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("concurrent mutation err=%v", err)
	}
	library, err := store.Load("coding")
	if err != nil || len(library.Candidates) != 0 {
		t.Fatalf("library changed=%+v err=%v", library, err)
	}
}

func TestRoleStoreRejectsCorruptFiles(t *testing.T) {
	store := persistenceStore(t)
	library := persistenceDefine(t, store)
	data, err := json.Marshal(library)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Dir, "coding.json")
	tests := [][]byte{
		[]byte(`null`), []byte(`{"schema":null,"schema":null}`), []byte(`[]`),
		[]byte(strings.Replace(string(data), `"current_revision":`, `"unknown":true,"current_revision":`, 1)),
		append(append([]byte{}, data...), []byte(` true`)...),
		[]byte(strings.Repeat(" ", maximumLibraryBytes+1)),
	}
	for _, invalid := range tests {
		if err := os.WriteFile(path, invalid, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load("coding"); err == nil {
			t.Fatal("corrupt load accepted")
		}
		if _, err := store.Define(persistenceSpec()); err == nil {
			t.Fatal("corrupt define accepted")
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir, "other.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil {
		t.Fatal("mismatched filename accepted")
	}
}

func TestRoleStoreRejectsSymlinks(t *testing.T) {
	store := persistenceStore(t)
	persistenceDefine(t, store)
	link := filepath.Join(t.TempDir(), "roles-link")
	if err := os.Symlink(store.Dir, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := (Store{Dir: link}).List(); err == nil {
		t.Fatal("symlink root accepted")
	}
	entry := filepath.Join(store.Dir, "alias.json")
	if err := os.Symlink(filepath.Join(store.Dir, "coding.json"), entry); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("alias"); err == nil {
		t.Fatal("symlink entry accepted")
	}
	if _, err := store.List(); err == nil {
		t.Fatal("symlink enumeration accepted")
	}
}

func TestRoleStoreRevisionLimit(t *testing.T) {
	store := persistenceStore(t)
	for index := range maximumRoleRevisions {
		spec := persistenceSpec()
		spec.Description = fmt.Sprintf("revision %d", index)
		if _, err := store.Define(spec); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Define(persistenceSpec()); err == nil {
		t.Fatal("revision limit exceeded")
	}
	library, err := store.Load("coding")
	if err != nil || len(library.Revisions) != maximumRoleRevisions {
		t.Fatalf("library=%+v err=%v", library, err)
	}
}

func TestRoleStoreCandidateLimit(t *testing.T) {
	store := persistenceStore(t)
	persistenceDefine(t, store)
	attachment := persistenceAttachment(t, 1)
	for index := range maximumRoleCandidates {
		attachment.EvidenceSHA256 = fmt.Sprintf("sha256:%064x", index)
		if _, err := store.Attach("coding", attachment); err != nil {
			t.Fatal(err)
		}
	}
	attachment.EvidenceSHA256 = fmt.Sprintf("sha256:%064x", maximumRoleCandidates)
	if _, err := store.Attach("coding", attachment); err == nil {
		t.Fatal("candidate limit exceeded")
	}
}

func TestRoleStoreRoleLimit(t *testing.T) {
	store := persistenceStore(t)
	library := persistenceDefine(t, store)
	for index := range maximumRoles {
		library.Name = fmt.Sprintf("role-%02d", index)
		library.Revisions[0].Spec.Name = library.Name
		library.Revisions[0].ID, _ = library.Revisions[0].Spec.Digest()
		library.CurrentRevision = library.Revisions[0].ID
		data, err := json.Marshal(library)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(store.Dir, library.Name+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.List(); err == nil {
		t.Fatal("oversized directory accepted")
	}
	if err := os.Remove(filepath.Join(store.Dir, "coding.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Define(persistenceSpec()); err == nil {
		t.Fatal("role count limit exceeded")
	}
}

func TestRoleStoreSizeAndDirectoryErrors(t *testing.T) {
	store := persistenceStore(t)
	persistenceDefine(t, store)
	spec := persistenceSpec()
	spec.Decision.Fallback.Unresolved = strings.Repeat("x", maximumLibraryBytes)
	if _, err := spec.Digest(); err == nil {
		t.Fatal("oversized spec accepted")
	}
	spec.Decision.Fallback.Unresolved = strings.Repeat("x", maximumLibraryBytes/2)
	if _, err := store.Define(spec); err != nil {
		t.Fatal(err)
	}
	spec.Description = "second large revision"
	if _, err := store.Define(spec); err == nil {
		t.Fatal("oversized library accepted")
	}
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Store{Dir: path}).List(); err == nil {
		t.Fatal("file directory accepted")
	}
	if _, err := (Store{Dir: filepath.Join(path, "child")}).Define(persistenceSpec()); err == nil {
		t.Fatal("impossible directory accepted")
	}
}
