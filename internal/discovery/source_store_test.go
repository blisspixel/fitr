package discovery

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/lock"
	"github.com/blisspixel/fitr/internal/source"
)

type discoverySourceTransport func(*http.Request) (*http.Response, error)

func (transport discoverySourceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func sourceInboxReceipt(t *testing.T, filename string) source.Resolution {
	t.Helper()
	body := fmt.Sprintf(`{"id":"owner/model","sha":"%s","siblings":[{"rfilename":"%s","size":100,"blobId":"%s","lfs":{"size":100,"sha256":"%s","pointerSize":130}}]}`, strings.Repeat("a", 40), filename, strings.Repeat("b", 40), strings.Repeat("c", 64))
	resolver := source.NewResolver(discoverySourceTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}, nil
	}))
	receipt, err := resolver.ResolveHF(t.Context(), source.HFRequest{RepoID: "owner/model", Revision: "main", Files: []string{filename}})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func sourceInboxStore(t *testing.T) (SourceStore, Idea) {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := SourceStore{Directory: filepath.Join(directory, ".discovery")}
	idea := testIdea(t)
	if _, err := Save(store.Directory, idea); err != nil {
		t.Fatal(err)
	}
	return store, idea
}

func TestSourceAttachmentsPreserveIdeaAndOriginalIndependence(t *testing.T) {
	store, idea := sourceInboxStore(t)
	ideaPath := filepath.Join(store.Directory, idea.ID+".json")
	before, err := os.ReadFile(ideaPath)
	if err != nil {
		t.Fatal(err)
	}
	receipt := sourceInboxReceipt(t, "model.gguf")
	original := filepath.Join(filepath.Dir(store.Directory), "original.json")
	if err := source.WriteResolution(original, receipt); err != nil {
		t.Fatal(err)
	}
	attachment, err := store.Attach(idea.ID, receipt, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.Attach(idea.ID, receipt, time.Now().Add(time.Hour))
	if err != nil || again.AttachmentSHA256 != attachment.AttachmentSHA256 {
		t.Fatalf("idempotent attach changed association: %v", err)
	}
	renamed := filepath.Join(filepath.Dir(store.Directory), "moved.json")
	if err := os.Rename(original, renamed); err != nil {
		t.Fatal(err)
	}
	*receipt.Files[0].SizeBytes = 999
	*attachment.Resolution.Files[0].SizeBytes = 888
	summaries, err := store.List(idea.ID)
	if err != nil || len(summaries) != 1 || *summaries[0].Files[0].SizeBytes != 100 {
		t.Fatalf("managed source changed: %+v %v", summaries, err)
	}
	if err := store.Detach(idea.ID, again.ResolutionSHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := source.LoadResolution(renamed); err != nil {
		t.Fatalf("detach changed original: %v", err)
	}
	after, err := os.ReadFile(ideaPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("source association mutated Idea v1 bytes")
	}
	ideas, err := List(store.Directory, "coding")
	if err != nil || len(ideas) != 1 || ideas[0] != idea {
		t.Fatalf("legacy inbox changed: %v", err)
	}
	if err := store.Detach(idea.ID, again.ResolutionSHA256); err == nil {
		t.Fatal("detached nonexistent source")
	}
}

func TestSourcePlansRequireExplicitSelection(t *testing.T) {
	store, idea := sourceInboxStore(t)
	plan, err := store.Plan(idea.ID, "")
	if err != nil || plan.State != "source_missing" {
		t.Fatalf("empty plan=%+v %v", plan, err)
	}
	first := sourceInboxReceipt(t, "one.gguf")
	if _, err := store.Attach(idea.ID, first, time.Now()); err != nil {
		t.Fatal(err)
	}
	plan, err = store.Plan(idea.ID, "")
	if err != nil || plan.State != "source_selected" || plan.SelectedResolutionSHA256 != first.ResolutionSHA256 {
		t.Fatalf("sole source plan=%+v %v", plan, err)
	}
	second := sourceInboxReceipt(t, "two.gguf")
	if _, err := store.Attach(idea.ID, second, time.Now()); err != nil {
		t.Fatal(err)
	}
	plan, err = store.Plan(idea.ID, "")
	if err != nil || plan.State != "selection_required" || plan.Selected != nil {
		t.Fatalf("multiple sources implicitly selected: %+v %v", plan, err)
	}
	plan, err = store.Plan(idea.ID, first.ResolutionSHA256)
	if err != nil || plan.Selected.Files[0].Path != "one.gguf" || len(plan.Selected.Files) != 1 {
		t.Fatalf("selected files blended: %+v %v", plan, err)
	}
	for _, step := range plan.Steps {
		if len(step.Argv) != 0 {
			t.Fatal("unbound source received executable recipe")
		}
	}
	if plan.Schema != SourcePlanSchema || plan.Idea.State != "unmeasured" || len(plan.Facets) != 5 {
		t.Fatalf("invalid plan facets: %+v", plan)
	}
	for _, digest := range []string{"short", "sha256:" + strings.Repeat("0", 64)} {
		if _, err := store.Plan(idea.ID, digest); err == nil {
			t.Fatal("invalid or unattached selection accepted")
		}
	}
}

func TestSourceAttachmentConcurrencyAndPerIdeaBound(t *testing.T) {
	store, idea := sourceInboxStore(t)
	receipts := make([]source.Resolution, 8)
	for index := range receipts {
		receipts[index] = sourceInboxReceipt(t, fmt.Sprintf("model%d.gguf", index))
	}
	var succeeded atomic.Int32
	var workers sync.WaitGroup
	for _, receipt := range receipts {
		workers.Go(func() {
			for range 1000 {
				_, err := store.Attach(idea.ID, receipt, time.Now())
				if err == nil {
					succeeded.Add(1)
					return
				}
				var busy *lock.BusyError
				if !errors.As(err, &busy) {
					return
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
	workers.Wait()
	summaries, err := store.List(idea.ID)
	if err != nil || len(summaries) != MaxSourcesPerIdea || succeeded.Load() != MaxSourcesPerIdea {
		t.Fatalf("bound failed: %d %+v %v", succeeded.Load(), summaries, err)
	}
}

func TestSourceAttachmentTamperAndStrictEnvelope(t *testing.T) {
	store, idea := sourceInboxStore(t)
	attachment, err := store.Attach(idea.ID, sourceInboxReceipt(t, "model.gguf"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	filename, err := sourceDigestFilename(attachment.ResolutionSHA256)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(store.Directory), ".discovery-sources", idea.ID, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{
		strings.Replace(string(data), `"relation":`, `"unknown":true,"relation":`, 1),
		strings.Replace(string(data), `"relation":`, `"Relation":`, 1),
		strings.Replace(string(data), `"relation":`, `"relation":"bad","relation":`, 1),
		strings.Replace(string(data), `operator_association`, `source_verified`, 1),
		strings.Replace(string(data), `"repo_id":`, `"Repo_ID":`, 1),
		strings.Replace(string(data), idea.ID, strings.Repeat("0", 64), 1),
		string(data) + " {}", "null", strings.Repeat("x", maxSourceAttachmentBytes+1),
	}
	for _, body := range cases {
		if _, err := decodeSourceAttachment([]byte(body)); err == nil {
			t.Fatal("invalid envelope accepted")
		}
	}
	if err := os.Rename(path, filepath.Join(filepath.Dir(path), strings.Repeat("0", 64)+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Plan(idea.ID, ""); err == nil {
		t.Fatal("renamed association accepted")
	}
	if _, err := store.List(idea.ID); err == nil {
		t.Fatal("listing ignored corrupt association")
	}
}

func TestSourceAttachmentPathsAndIdeaMembership(t *testing.T) {
	store, idea := sourceInboxStore(t)
	for _, id := range []string{"short", "../outside", strings.Repeat("0", 64)} {
		if _, err := store.List(id); err == nil {
			t.Fatalf("invalid idea membership accepted: %s", id)
		}
	}
	if err := store.Detach(idea.ID, "short"); err == nil {
		t.Fatal("abbreviated digest accepted")
	}
	if _, err := store.Attach(idea.ID, source.Resolution{}, time.Now()); err == nil {
		t.Fatal("invalid receipt accepted")
	}
	rawStore := SourceStore{Directory: store.Directory + "/child/../"}
	if _, err := rawStore.List(idea.ID); err == nil {
		t.Fatal("raw parent traversal accepted")
	}
	alias := filepath.Join(filepath.Dir(store.Directory), "inbox-alias")
	if err := os.Symlink(store.Directory, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	aliasStore := SourceStore{Directory: alias}
	if _, err := aliasStore.Attach(idea.ID, sourceInboxReceipt(t, "one.gguf"), time.Now()); err != nil {
		t.Fatalf("configured physical alias failed: %v", err)
	}
	summaries, err := store.List(idea.ID)
	if err != nil || len(summaries) != 1 {
		t.Fatal("alias used a different attachment namespace")
	}
	ideaPath := filepath.Join(store.Directory, idea.ID+".json")
	moved := filepath.Join(filepath.Dir(store.Directory), "moved-idea.json")
	if err := os.Rename(ideaPath, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, ideaPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(idea.ID); err == nil {
		t.Fatal("managed idea symlink accepted")
	}
}
