package discovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testIdea(t *testing.T) Idea {
	t.Helper()
	idea, err := New("https://www.youtube.com/watch?v=fixture&t=30", "candidate:q4", "coding", "pi pinned-version", "source reports strong tool use", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	return idea
}

func TestInboxDeduplicatesAndFiltersWithoutPromotingClaims(t *testing.T) {
	directory := t.TempDir()
	idea := testIdea(t)
	if _, err := Save(directory, idea); err != nil {
		t.Fatal(err)
	}
	later := idea
	later.CapturedAt = time.Unix(100, 0).UTC().Format(time.RFC3339)
	saved, err := Save(directory, later)
	if err != nil || saved != idea {
		t.Fatalf("duplicate changed capture: %+v, %v", saved, err)
	}
	for _, role := range []string{"", "coding", "classifier"} {
		ideas, err := List(directory, role)
		want := 1
		if role == "classifier" {
			want = 0
		}
		if err != nil || len(ideas) != want {
			t.Fatalf("role %q: %v, %v", role, ideas, err)
		}
		if want == 1 && ideas[0].State != "unmeasured" {
			t.Fatal("source claim became measured evidence")
		}
	}
	if ideas, err := List(filepath.Join(directory, "missing"), ""); err != nil || len(ideas) != 0 {
		t.Fatalf("empty inbox: %v, %v", ideas, err)
	}
}

func TestInboxRejectsUnsafeAndAmbiguousInputs(t *testing.T) {
	for _, mutate := range []func(*Idea){
		func(i *Idea) { i.Source = "" }, func(i *Idea) { i.Role = "" },
		func(i *Idea) { i.Source = "file:///private" }, func(i *Idea) { i.Source = "https://user:secret@example.com/x" },
		func(i *Idea) { i.Claim = "hidden\x1b[2J" }, func(i *Idea) { i.Model = "--pull" },
		func(i *Idea) { i.Claim = strings.Repeat("x", 8193) }, func(i *Idea) { i.CapturedAt = "yesterday" },
		func(i *Idea) { i.State = "recommended" }, func(i *Idea) { i.Schema = "unknown" },
	} {
		idea := testIdea(t)
		mutate(&idea)
		idea.ID = idea.digest()
		if _, err := Save(t.TempDir(), idea); err == nil {
			t.Fatalf("accepted invalid idea: %+v", idea)
		}
	}
	for _, data := range []string{`{}`, `{"id":"a","id":"b"}`, `{} {}`, `{"unexpected":true}`, strings.Repeat("x", maximumIdeaBytes+1)} {
		if _, err := decode([]byte(data)); err == nil {
			t.Fatal("accepted malformed input")
		}
	}
}

func TestInboxRejectsTamperingAndStorageErrors(t *testing.T) {
	directory := t.TempDir()
	idea := testIdea(t)
	path := filepath.Join(directory, idea.ID+".json")
	if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Save(directory, idea); err == nil {
		t.Fatal("overwrote a broken existing entry")
	}
	if _, err := List(directory, ""); err == nil {
		t.Fatal("silently ignored a broken entry")
	}
	if _, err := Save(path, idea); err == nil {
		t.Fatal("accepted a file as the inbox directory")
	}
	if _, err := List(path, ""); err == nil {
		t.Fatal("listed a file as a directory")
	}
	data, _ := json.Marshal(idea)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(directory, "wrong.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := List(directory, ""); err == nil {
		t.Fatal("accepted a renamed entry")
	}
}

func FuzzDecodeIdea(f *testing.F) {
	f.Add([]byte(`{}`))
	idea, _ := New("source", "model", "coding", "", "", time.Unix(1, 0))
	data, _ := json.Marshal(idea)
	f.Add(data)
	f.Fuzz(func(t *testing.T, input []byte) {
		idea, err := decode(input)
		if err == nil && idea.Validate() != nil {
			t.Fatal("decoded an invalid candidate")
		}
	})
}
