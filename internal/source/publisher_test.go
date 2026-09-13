package source

import (
	"encoding/json"
	"strings"
	"testing"
)

func meta(author, base, license string, tags ...string) hfMetadata {
	m := hfMetadata{ID: author + "/repo", Author: author, Tags: tags}
	m.CardData.License, _ = json.Marshal(license)
	if base != "" {
		m.CardData.BaseModel = json.RawMessage(`["` + base + `"]`)
	}
	return m
}

// Lineage answers "who converted this, and from whose model" without asking
// how popular the repository is. Downloads rank by age and fame, which is
// backwards for the new releases a candidate search is about.
func TestConversionAuthorshipIsDerivedFromLineage(t *testing.T) {
	third := publisherFrom(meta("unsloth", "Qwen/Qwen3.8-27B", "apache-2.0",
		"base_model:quantized:Qwen/Qwen3.8-27B"))
	if third.FirstPartyConversion {
		t.Fatalf("a conversion by another author read as first party: %+v", third)
	}
	if third.BaseAuthor != "Qwen" || !third.DeclaredQuantization {
		t.Fatalf("lineage = %+v", third)
	}
	first := publisherFrom(meta("google", "google/gemma-4-12B-it", "gemma"))
	if !first.FirstPartyConversion || first.BaseAuthor != "google" {
		t.Fatalf("an author's own conversion was not recognized: %+v", first)
	}
}

// Absent lineage is evidence of nothing, and must never read as first party.
func TestAbsentLineageIsNotFirstParty(t *testing.T) {
	p := publisherFrom(meta("someone", "", "mit"))
	if p == nil {
		t.Fatal("an author and a license are worth recording")
	}
	if p.FirstPartyConversion {
		t.Fatal("a repository with no declared base model claimed first-party authorship")
	}
	if p.Lineage() {
		t.Fatal("no base model was declared, so there is no lineage")
	}
	if !strings.Contains(p.Summary(), "lineage is unknown") {
		t.Fatalf("summary hid the missing lineage: %q", p.Summary())
	}
}

// The provider writes base_model as one string or a list, and gated as a
// boolean or a name. Both shapes are real; anything else is no lineage rather
// than a guess.
func TestProviderFieldShapesAreBothAccepted(t *testing.T) {
	if got := decodeBaseModels(json.RawMessage(`"Qwen/Qwen3.8-27B"`)); len(got) != 1 || got[0] != "Qwen/Qwen3.8-27B" {
		t.Fatalf("string form = %v", got)
	}
	if got := decodeBaseModels(json.RawMessage(`["a/b","c/d"]`)); len(got) != 2 {
		t.Fatalf("list form = %v", got)
	}
	for _, bad := range []string{`{"a":1}`, `7`, ``, `""`, `[""]`} {
		if got := decodeBaseModels(json.RawMessage(bad)); len(got) != 0 {
			t.Fatalf("%q became lineage: %v", bad, got)
		}
	}
	if got := decodeGated(json.RawMessage(`"manual"`)); got != "manual" {
		t.Fatalf("named gate = %q", got)
	}
	if got := decodeGated(json.RawMessage(`true`)); got != "yes" {
		t.Fatalf("boolean gate = %q", got)
	}
	if got := decodeGated(json.RawMessage(`false`)); got != "" {
		t.Fatalf("an ungated repository reported %q", got)
	}
}

// A provider that said nothing produces nothing, so a receipt written before
// this existed keeps its exact bytes and therefore its digest.
func TestSilentMetadataProducesNoPublisher(t *testing.T) {
	if p := publisherFrom(hfMetadata{}); p != nil {
		t.Fatalf("empty metadata produced %+v", p)
	}
	var absent *Publisher
	if absent.Summary() != "" || absent.Lineage() {
		t.Fatal("a nil publisher answered as though it had data")
	}
}

// The owner falls back to the repository id, which always carries it.
func TestAuthorFallsBackToTheRepositoryOwner(t *testing.T) {
	m := hfMetadata{ID: "bartowski/Some-Model-GGUF"}
	m.CardData.License = json.RawMessage(`"mit"`)
	if p := publisherFrom(m); p == nil || p.Author != "bartowski" {
		t.Fatalf("owner was not recovered from the id: %+v", p)
	}
}
