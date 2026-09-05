package discovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSourceManagedByteBudgetAndCountBound(t *testing.T) {
	store, idea := sourceInboxStore(t)
	attachment, err := store.Attach(idea.ID, sourceInboxReceipt(t, "one.gguf"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	paths, guard, err := store.acquire(idea.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = guard.Release() }()
	filename, err := sourceDigestFilename(attachment.ResolutionSHA256)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(paths.root, idea.ID, filename))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := paths.readIdea(idea.ID, info.Size()); err != nil {
		t.Fatalf("exact byte budget rejected: %v", err)
	}
	if _, _, err := paths.readIdea(idea.ID, info.Size()-1); err == nil {
		t.Fatal("aggregate remaining byte budget ignored")
	}
	for _, name := range []string{"a", "b", "c", "d"} {
		if err := os.WriteFile(filepath.Join(paths.root, idea.ID, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := paths.readAll(); err == nil {
		t.Fatal("fifth entry bypassed bounded enumeration")
	}
}

func TestSourceManagedStoreRejectsUnsafeEntries(t *testing.T) {
	for _, kind := range []string{"root_file", "bad_directory", "oversized_file", "root_link", "receipt_link"} {
		t.Run(kind, func(t *testing.T) {
			store, idea := sourceInboxStore(t)
			root := filepath.Join(filepath.Dir(store.Directory), ".discovery-sources")
			if kind == "root_file" {
				if err := os.WriteFile(root, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				sourceCreateUnsafeStore(t, store, idea, root, kind)
			}
			if _, err := store.List(idea.ID); err == nil {
				t.Fatal("unsafe managed store accepted")
			}
		})
	}
}

func sourceCreateUnsafeStore(t *testing.T, store SourceStore, idea Idea, root, kind string) {
	t.Helper()
	if kind == "root_link" {
		if err := os.Symlink(store.Directory, root); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		return
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if kind == "bad_directory" {
		if err := os.Mkdir(filepath.Join(root, "unexpected"), 0o700); err != nil {
			t.Fatal(err)
		}
		return
	}
	directory := filepath.Join(root, idea.ID)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, strings.Repeat("0", 64)+".json")
	if kind == "receipt_link" {
		if err := os.Symlink(filepath.Join(store.Directory, idea.ID+".json"), path); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		return
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := file.Truncate(maxSourceAttachmentBytes + 1); err != nil {
		t.Fatal(err)
	}
}

func TestSourceAssociationBindingValidation(t *testing.T) {
	store, idea := sourceInboxStore(t)
	attachment, err := store.Attach(idea.ID, sourceInboxReceipt(t, "model.gguf"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, alter := range []func(*SourceAttachment){
		func(a *SourceAttachment) { a.Schema = "wrong" }, func(a *SourceAttachment) { a.IdeaID = "short" },
		func(a *SourceAttachment) { a.ResolutionSHA256 = "sha256:" + strings.Repeat("0", 64) },
		func(a *SourceAttachment) { a.AttachedAt = "yesterday" }, func(a *SourceAttachment) { a.AttachmentSHA256 = "wrong" },
		func(a *SourceAttachment) { a.Resolution.State = "qualified" },
	} {
		copyAttachment := attachment
		alter(&copyAttachment)
		if err := copyAttachment.Validate(); err == nil {
			t.Fatal("invalid source binding accepted")
		}
	}
	if _, err := store.Attach(idea.ID, sourceInboxReceipt(t, "second.gguf"), time.Time{}); err == nil {
		t.Fatal("zero attach time accepted")
	}
	data, err := json.Marshal(attachment)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSourceAttachment(data); err != nil {
		t.Fatal(err)
	}
}

func TestSourcePlanKeepsUnavailableMetadataUnmeasured(t *testing.T) {
	store, idea := sourceInboxStore(t)
	receipt := sourceInboxReceipt(t, "one.gguf")
	receipt.State = "unavailable"
	receipt.ResolvedRepo, receipt.ResolvedCommit = "", ""
	receipt.Files, receipt.InventoryPaths, receipt.Dependencies = nil, nil, nil
	receipt.Queries = receipt.Queries[:1]
	receipt.Queries[0].HTTPStatus = 403
	receipt.Queries[0].Outcome = "access_denied"
	receipt.Queries[0].ResponseSHA256 = ""
	receipt.Gaps = []string{"access_denied"}
	var err error
	receipt.ResolutionSHA256, err = receipt.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Attach(idea.ID, receipt, time.Now()); err != nil {
		t.Fatal(err)
	}
	plan, err := store.Plan(idea.ID, "")
	if err != nil || plan.Facets[0].State != "unavailable" || plan.Facets[1].State != "incomplete" || plan.Facets[4].State != "unmeasured" {
		t.Fatalf("unavailable source promoted: %+v %v", plan, err)
	}
	if plan.Sources[0].RepoID != "owner/model" || plan.Sources[0].Commit != "" || plan.Sources[0].MetadataState != "unavailable" {
		t.Fatalf("unavailable requested identity lost or promoted: %+v", plan.Sources)
	}
}
