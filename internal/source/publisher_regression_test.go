package source

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirstPartyRequiresEveryBaseAuthor(t *testing.T) {
	for _, bases := range []string{
		`["google/first","other/second"]`,
		`["other/second","google/first"]`,
	} {
		metadata := meta("google", "", "gemma")
		metadata.CardData.BaseModel = json.RawMessage(bases)
		publisher := publisherFrom(metadata)
		if publisher.FirstPartyConversion || publisher.BaseAuthor != "" {
			t.Fatalf("mixed authors claimed a common author: %+v", publisher)
		}
	}
	metadata := meta("google", "", "gemma")
	metadata.CardData.BaseModel = json.RawMessage(`["google/first","google/second"]`)
	if publisher := publisherFrom(metadata); !publisher.FirstPartyConversion || publisher.BaseAuthor != "google" {
		t.Fatalf("common author was not recognized: %+v", publisher)
	}
}

func TestConflictingPublisherOwnerCannotClaimFirstParty(t *testing.T) {
	metadata := meta("google", "google/model", "gemma")
	metadata.ID = "thirdparty/repository"
	publisher := publisherFrom(metadata)
	if publisher == nil || publisher.Author != "" || publisher.FirstPartyConversion {
		t.Fatalf("conflicting publisher author became first-party: %+v", publisher)
	}
}

func TestStoredPublisherOwnerMustMatchPinnedRepository(t *testing.T) {
	result := sourceFixture(t)
	result.Publisher = &Publisher{Author: "google", BaseModels: []string{"google/model"}, BaseAuthor: "google", FirstPartyConversion: true}
	if _, err := result.Digest(); err == nil {
		t.Fatal("receipt accepted an author outside its pinned repository namespace")
	}
}

func TestMalformedBaseModelsStayUnmeasured(t *testing.T) {
	for _, bases := range []string{
		`"google/"`, `"google/../model"`, `"https://huggingface.co/google/model"`,
		`["google/model",null]`, `["google/model",""]`, `["google/model","../bad"]`,
	} {
		t.Run(bases, func(t *testing.T) {
			metadata := meta("google", "", "gemma")
			metadata.CardData.BaseModel = json.RawMessage(bases)
			publisher := publisherFrom(metadata)
			if publisher.Lineage() || publisher.FirstPartyConversion || publisher.BaseAuthor != "" {
				t.Fatalf("malformed base list became lineage: %+v", publisher)
			}
		})
	}
}

func TestPublisherReceiptRejectsContradictoryFacts(t *testing.T) {
	fixture := sourceFixture(t)
	for name, publisher := range map[string]*Publisher{
		"invented_first_party": {Author: "owner", FirstPartyConversion: true},
		"wrong_base_author":    {Author: "owner", BaseModels: []string{"other/model"}, BaseAuthor: "owner"},
		"mixed_first_party":    {Author: "owner", BaseModels: []string{"owner/model", "other/model"}, BaseAuthor: "owner", FirstPartyConversion: true},
		"malformed_base":       {Author: "owner", BaseModels: []string{"owner/"}, BaseAuthor: "owner", FirstPartyConversion: true},
		"terminal_control":     {Author: "owner", License: "mit\nallowed"},
	} {
		t.Run(name, func(t *testing.T) {
			result := sourceClone(t, fixture)
			result.Publisher = publisher
			// Recomputing an unkeyed seal must not legitimize facts that the
			// same receipt's own observations contradict.
			result.ResolutionSHA256 = ""
			data, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			result.ResolutionSHA256 = hashBytes(append([]byte(ResolutionSchema+"\x00"), data...))
			if err := result.Validate(); err == nil {
				t.Fatal("resealed contradictory publisher was accepted")
			}
		})
	}
}

func TestDeclaredBaseRelationIsRecognized(t *testing.T) {
	body := publisherWireBody(t, `{"base_model_relation":"quantized"}`)
	resolver, _ := sourceResolver(t, body, body)
	result, err := resolver.ResolveHF(t.Context(), sourceRequest())
	if err != nil || result.Publisher == nil || !result.Publisher.DeclaredQuantization {
		t.Fatalf("explicit quantization relation was lost: %+v, %v", result.Publisher, err)
	}
}

func publisherWireBody(t *testing.T, cardData string) string {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(sourceBody(t, sourceSibling("model.gguf"))), &fields); err != nil {
		t.Fatal(err)
	}
	fields["cardData"] = json.RawMessage(cardData)
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMalformedDeclaredLicenseStaysUnmeasured(t *testing.T) {
	for _, value := range []string{`7`, `["mit"]`, `{"name":"mit"}`, `"mit\nallowed"`} {
		body := publisherWireBody(t, `{"license":`+value+`}`)
		resolver, _ := sourceResolver(t, body, body)
		result, err := resolver.ResolveHF(t.Context(), sourceRequest())
		if err != nil || result.State != "resolved" || result.Publisher == nil || result.Publisher.License != "" {
			t.Fatalf("wrong-shaped license contaminated file metadata: %+v, %v", result, err)
		}
	}
}

func TestUnavailableReceiptCannotCarryPublisher(t *testing.T) {
	body := sourceBody(t, sourceSibling("model.gguf"))
	resolver, _ := sourceResolver(t, strings.Replace(body, "owner/model", "owner/other", 1))
	result, err := resolver.ResolveHF(t.Context(), sourceRequest())
	if err != nil || result.State != "unavailable" {
		t.Fatalf("unavailable fixture = %+v, %v", result, err)
	}
	result.Publisher = &Publisher{Author: "owner", License: "mit"}
	if _, err := result.Digest(); err == nil {
		t.Fatal("unavailable metadata carried resolved publisher facts")
	}
}

func TestPublisherKeepsFrozenLegacyReceiptBytes(t *testing.T) {
	path := filepath.Join("testdata", "resolution-without-publisher.json")
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := LoadResolution(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := result.JSON()
	if err != nil || !bytes.Equal(want, got) || result.Publisher != nil {
		t.Fatalf("legacy receipt bytes changed: %v", err)
	}
}

func TestPublisherFromRecordedOfficialModelMetadata(t *testing.T) {
	// Recorded anonymously from the pinned public Hub API on 2026-09-13.
	// These upstream spellings can disagree with our decoder, unlike a
	// fixture marshaled from hfMetadata itself.
	body, err := os.ReadFile(filepath.Join("testdata", "qwen3-0.6b-metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := LoadResolution(filepath.Join("testdata", "qwen3-0.6b-resolution.json"))
	if err != nil {
		t.Fatal(err)
	}
	resolver, _ := sourceResolver(t, string(body), string(body))
	result, err := resolver.ResolveHF(t.Context(), recorded.Request)
	if err != nil || result.State != "resolved" || result.Publisher == nil {
		t.Fatalf("recorded upstream metadata rejected: %+v, %v", result, err)
	}
	publisher := result.Publisher
	if publisher.Author != "Qwen" || publisher.BaseAuthor != "Qwen" ||
		!publisher.FirstPartyConversion || !publisher.DeclaredQuantization ||
		publisher.License != "apache-2.0" || result.ResolvedCommit != recorded.ResolvedCommit {
		t.Fatalf("recorded upstream publisher lost: %+v", publisher)
	}
	if len(publisher.BaseModels) != 1 || publisher.BaseModels[0] != "Qwen/Qwen3-0.6B" ||
		result.Files[0].DeclaredSHA256 != recorded.Files[0].DeclaredSHA256 {
		t.Fatal("recorded upstream lineage or file identity changed")
	}
}

func FuzzPublisherMetadata(f *testing.F) {
	for _, bases := range []string{`"owner/model"`, `["owner/model","other/model"]`, `["owner/model",null]`, `{}`} {
		f.Add(bases, `"mit"`, `false`, `"quantized"`, "owner", "2026-09-13T00:00:00Z")
	}
	f.Fuzz(func(t *testing.T, bases, license, gated, relation, author, created string) {
		metadata := hfMetadata{ID: "owner/model", Author: author, CreatedAt: created, Gated: json.RawMessage(gated)}
		metadata.CardData.BaseModel = json.RawMessage(bases)
		metadata.CardData.License = json.RawMessage(license)
		metadata.CardData.BaseModelRelation = json.RawMessage(relation)
		publisher := publisherFrom(metadata)
		if err := publisher.validate(); err != nil {
			t.Fatalf("provider decoder produced invalid publisher: %+v: %v", publisher, err)
		}
		if publisher == nil {
			return
		}
		if publisher.FirstPartyConversion {
			if len(publisher.BaseModels) == 0 {
				t.Fatal("missing lineage became first-party")
			}
			for _, base := range publisher.BaseModels {
				owner, _, _ := strings.Cut(base, "/")
				if !strings.EqualFold(owner, publisher.Author) {
					t.Fatal("mixed lineage became first-party")
				}
			}
		}
	})
}
