package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"
	"github.com/blisspixel/fitr/internal/score"
)

func evidenceDirectoryAlias(t *testing.T) (string, string) {
	t.Helper()
	physical, alias := t.TempDir(), filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Skipf("directory alias unavailable: %v", err)
	}
	return physical, alias
}

func aliasedEvidenceLibrary(t *testing.T, root string) (role.Library, record.Store, string) {
	t.Helper()
	evidenceLibrary(t, root)
	result := currentMCPFixture(t)
	store := record.Store{Dir: root}
	saved, err := store.Save(result)
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := role.AttachRecord(saved.CanonicalPath, store)
	if err != nil {
		t.Fatal(err)
	}
	library, err := (role.Store{Dir: filepath.Join(root, ".roles")}).Attach("coding", attachment)
	if err != nil {
		t.Fatal(err)
	}
	return library, store, saved.CanonicalPath
}

func currentMCPFixture(t *testing.T) *record.Record {
	t.Helper()
	data, err := os.ReadFile("../record/testdata/schema5-signed-v0.9.8.json")
	if err != nil {
		t.Fatal(err)
	}
	var result record.Record
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	profile, identity, previous := result.Completion.Profile, result.Manifest.Model, *result.Manifest.Provenance
	result.Manifest, result.Completion, result.RunID = nil, nil, ""
	result.SchemaVersion, result.StartedAt = record.EvidenceSchemaVersion, time.Now().UTC().Format(time.RFC3339)
	result.Checks[0].Origin = "builtin"
	result.TaskPlan.CheckPlanSHA256, err = record.ObservedCheckPlanSHA256(result.Checks)
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := record.NewRunProvenance(previous.TaskSetSHA256, previous.SpecSHA256, profile,
		record.CurrentScoringPolicy(), record.SoftwareReceipt{FitrVersion: previous.FitrVersion,
			SoftwareBuildSHA256: previous.SoftwareBuildSHA256, BackendProtocol: previous.BackendProtocol})
	if err != nil {
		t.Fatal(err)
	}
	result.Scorecard = score.Score(result.Measured(), profile)
	if err := result.AttachManifest(identity, provenance); err != nil {
		t.Fatal(err)
	}
	if err := result.CompleteEvidence(profile); err != nil {
		t.Fatal(err)
	}
	return &result
}

func TestLocalEvidenceReviewsCanonicalRecordsThroughDirectoryAlias(t *testing.T) {
	_, alias := evidenceDirectoryAlias(t)
	root := filepath.Join(alias, "results")
	library, store, canonical := aliasedEvidenceLibrary(t, root)
	libraryPath := filepath.Join(root, ".roles", "coding.json")
	before, err := os.ReadFile(libraryPath)
	if err != nil {
		t.Fatal(err)
	}
	source, err := newLocalEvidence(root)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := source.review(t.Context(), "coding")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := role.Review(library, store, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got, ok := actual.(reviewSummary)
	if !ok {
		t.Fatalf("review type=%T", actual)
	}
	got.EvaluatedAt = expected.EvaluatedAt
	if !reflect.DeepEqual(got, summarizeReview(expected)) {
		t.Fatalf("aliased evidence changed semantics: %+v", got)
	}
	after, err := os.ReadFile(libraryPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("read-only review changed the saved attachment: %v", err)
	}
	if err := os.Remove(canonical); err != nil {
		t.Fatal(err)
	}
	missing, err := source.review(t.Context(), "coding")
	if err != nil {
		t.Fatalf("missing aliased evidence should remain reviewable: %v", err)
	}
	if summary, ok := missing.(reviewSummary); !ok || summary.State != "unresolved" || summary.AdoptionAuthorized {
		t.Fatalf("missing aliased evidence was resurrected: %+v", missing)
	}
}

func TestLocalEvidenceRejectsRetargetedAttachmentDirectoryAlias(t *testing.T) {
	physical, alias := evidenceDirectoryAlias(t)
	root := filepath.Join(alias, "results")
	aliasedEvidenceLibrary(t, root)
	source, err := newLocalEvidence(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(alias, "results"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := source.review(t.Context(), "coding"); err == nil {
		t.Fatal("attachment alias escaped the pinned physical evidence root")
	}
	if _, err := os.Stat(filepath.Join(physical, "results", ".roles", "coding.json")); err != nil {
		t.Fatal(err)
	}
}

func TestLocalEvidencePinsMissingRootThroughExistingDirectoryAlias(t *testing.T) {
	_, alias := evidenceDirectoryAlias(t)
	root := filepath.Join(alias, "future", "results")
	source, err := newLocalEvidence(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.list(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("server created the result root: %v", err)
	}
	aliasedEvidenceLibrary(t, root)
	if _, err := source.review(t.Context(), "coding"); err != nil {
		t.Fatalf("physical root changed when evidence appeared: %v", err)
	}
}

func TestLocalEvidenceRejectsAttachmentLeafSymlinkAfterParentResolution(t *testing.T) {
	_, alias := evidenceDirectoryAlias(t)
	library, _, path := aliasedEvidenceLibrary(t, filepath.Join(alias, "results"))
	source, err := newLocalEvidence(filepath.Join(alias, "results"))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "external.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := source.review(t.Context(), library.Name); err == nil {
		t.Fatal("external leaf symlink accepted after canonicalizing its parent")
	}
}
