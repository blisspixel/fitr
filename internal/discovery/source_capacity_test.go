package discovery

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSourceAttachmentsPreserveFullLegacyInbox(t *testing.T) {
	store, idea := sourceInboxStore(t)
	seedInboxIdeas(t, store.Directory, maximumIdeas-1)
	before, err := os.ReadFile(filepath.Join(store.Directory, idea.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	assertFullSourceInbox(t, store, idea, before)
	receipt := sourceInboxReceipt(t, "model.gguf")
	if _, err := store.Attach(idea.ID, receipt, time.Now()); err != nil {
		t.Fatalf("attachment failed with 1000 existing ideas: %v", err)
	}
	assertFullSourceInbox(t, store, idea, before)
	summaries, err := store.List(idea.ID)
	if err != nil || len(summaries) != 1 || summaries[0].ResolutionSHA256 != receipt.ResolutionSHA256 {
		t.Fatalf("source listing failed at inbox capacity: %+v, %v", summaries, err)
	}
	ids := assertFullSourceInbox(t, store, idea, before)
	plans, err := store.Plans(ids, "")
	if err != nil || len(plans) != maximumIdeas {
		t.Fatalf("full inbox planning failed: %d, %v", len(plans), err)
	}
	for index, plan := range plans {
		if plan.Idea.ID != ids[index] || plan.Idea.State != "unmeasured" {
			t.Fatalf("full inbox plan changed identity or evidence state: %+v", plan)
		}
		if plan.Idea.ID == idea.ID {
			if plan.State != "source_selected" || plan.SelectedResolutionSHA256 != receipt.ResolutionSHA256 {
				t.Fatalf("attached source was lost at inbox capacity: %+v", plan)
			}
		} else if plan.State != "source_missing" || plan.Selected != nil {
			t.Fatalf("attachment crossed into an unrelated idea: %+v", plan)
		}
	}
	later := idea
	later.CapturedAt = time.Now().UTC().Format(time.RFC3339)
	if saved, err := Save(store.Directory, later); err != nil || saved != idea {
		t.Fatalf("duplicate capture changed after attachment at capacity: %+v, %v", saved, err)
	}
	assertFullSourceInbox(t, store, idea, before)
	if err := store.Detach(idea.ID, receipt.ResolutionSHA256); err != nil {
		t.Fatalf("detach failed at inbox capacity: %v", err)
	}
	if summaries, err := store.List(idea.ID); err != nil || len(summaries) != 0 {
		t.Fatalf("detached source remains listed: %+v, %v", summaries, err)
	}
	if plan, err := store.Plan(idea.ID, ""); err != nil || plan.State != "source_missing" || plan.Selected != nil {
		t.Fatalf("detached source remains selected: %+v, %v", plan, err)
	}
	assertFullSourceInbox(t, store, idea, before)
}

func assertFullSourceInbox(t *testing.T, store SourceStore, original Idea, originalBytes []byte) []string {
	t.Helper()
	entries, err := os.ReadDir(store.Directory)
	if err != nil || len(entries) != maximumIdeas {
		t.Fatalf("source operation changed legacy inbox entry count: %d, %v", len(entries), err)
	}
	ideas, err := List(store.Directory, "")
	if err != nil || len(ideas) != maximumIdeas {
		t.Fatalf("1000-entry legacy inbox became unreadable: %d, %v", len(ideas), err)
	}
	ids := make([]string, 0, len(ideas))
	for _, idea := range ideas {
		ids = append(ids, idea.ID)
		if idea.ID == original.ID && idea != original {
			t.Fatalf("source operation changed the original capture: %+v", idea)
		}
	}
	current, err := os.ReadFile(filepath.Join(store.Directory, original.ID+".json"))
	if err != nil || !bytes.Equal(current, originalBytes) {
		t.Fatalf("source operation changed original Idea v1 bytes: %v", err)
	}
	return ids
}
