package discovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSourcePlansBatchBoundsAndOrder(t *testing.T) {
	store, first := sourceInboxStore(t)
	second, err := New("second source", "model-two", "coding", "", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Save(store.Directory, second); err != nil {
		t.Fatal(err)
	}
	receipt := sourceInboxReceipt(t, "model.gguf")
	if _, err := store.Attach(first.ID, receipt, time.Now()); err != nil {
		t.Fatal(err)
	}
	plans, err := store.Plans([]string{second.ID, first.ID}, "")
	if err != nil || len(plans) != 2 || plans[0].Idea.ID != second.ID || plans[0].State != "source_missing" || plans[1].State != "source_selected" {
		t.Fatalf("batch order or isolation failed: %+v %v", plans, err)
	}
	for _, ids := range [][]string{{first.ID, first.ID}, {"short"}, make([]string, maximumIdeas+1)} {
		if _, err := store.Plans(ids, ""); err == nil {
			t.Fatal("invalid batch accepted")
		}
	}
	for _, ids := range [][]string{nil, {first.ID, second.ID}} {
		if _, err := store.Plans(ids, receipt.ResolutionSHA256); err == nil {
			t.Fatal("source selection accepted without one idea")
		}
	}
	if empty, err := store.Plans(nil, ""); err != nil || len(empty) != 0 {
		t.Fatalf("empty batch=%v %v", empty, err)
	}
}

func TestSourcePlansBatchFailsWithoutPartialSnapshot(t *testing.T) {
	store, first := sourceInboxStore(t)
	second, err := New("second source", "second-model", "coding", "", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Save(store.Directory, second); err != nil {
		t.Fatal(err)
	}
	attachment, err := store.Attach(first.ID, sourceInboxReceipt(t, "one.gguf"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Attach(second.ID, sourceInboxReceipt(t, "two.gguf"), time.Now()); err != nil {
		t.Fatal(err)
	}
	filename, err := sourceDigestFilename(attachment.ResolutionSHA256)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(store.Directory), ".discovery-sources", first.ID, filename)
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]string{{second.ID}, {second.ID, first.ID}, {first.ID, second.ID}} {
		plans, err := store.Plans(ids, "")
		if err == nil || plans != nil {
			t.Fatalf("corrupt store produced partial snapshot: %+v %v", plans, err)
		}
	}
	if _, err := store.Plans([]string{strings.Repeat("0", 64)}, ""); err == nil {
		t.Fatal("missing idea accepted")
	}
}

func TestSourceCustomInboxesHaveIndependentNamespaces(t *testing.T) {
	first, idea := sourceInboxStore(t)
	second := SourceStore{Directory: filepath.Join(filepath.Dir(first.Directory), "other-inbox")}
	if _, err := Save(second.Directory, idea); err != nil {
		t.Fatal(err)
	}
	firstReceipt := sourceInboxReceipt(t, "first.gguf")
	secondReceipt := sourceInboxReceipt(t, "second.gguf")
	if _, err := first.Attach(idea.ID, firstReceipt, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Attach(idea.ID, secondReceipt, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		store  SourceStore
		digest string
	}{{first, firstReceipt.ResolutionSHA256}, {second, secondReceipt.ResolutionSHA256}} {
		plan, err := test.store.Plan(idea.ID, "")
		if err != nil || len(plan.Sources) != 1 || plan.SelectedResolutionSHA256 != test.digest {
			t.Fatalf("same-ID custom inboxes collided: %+v %v", plan, err)
		}
	}
	otherIdea, err := New("other idea", "", "coding", "", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Save(second.Directory, otherIdea); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Attach(otherIdea.ID, secondReceipt, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Plan(idea.ID, ""); err != nil {
		t.Fatalf("other inbox idea contaminated this inbox: %v", err)
	}
	if _, err := first.Plan(otherIdea.ID, ""); err == nil {
		t.Fatal("other inbox idea became a member")
	}
	if err := first.Detach(idea.ID, firstReceipt.ResolutionSHA256); err != nil {
		t.Fatal(err)
	}
	if summaries, err := second.List(idea.ID); err != nil || len(summaries) != 1 {
		t.Fatalf("detach crossed inbox boundary: %v", err)
	}
}
