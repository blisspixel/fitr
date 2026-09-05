package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"
	"github.com/blisspixel/fitr/internal/score"
)

func evidenceLibrary(t *testing.T, root string) role.Library {
	t.Helper()
	spec := role.Spec{Schema: role.SpecSchema, Name: "coding", Description: "private role instruction", MaxAgeDays: 30,
		Decision: decision.DecisionSpec{Schema: decision.SpecSchema, Name: "private decision", Evidence: decision.EvidenceDecide,
			Requirements: []decision.Requirement{
				{ID: "private-quality", Behavior: &decision.BehaviorRequirement{Need: "coding", RequiredState: score.Pass}},
				{ID: "context", Context: &decision.ContextRequirement{MinimumEffectiveTokens: 4096}},
				{ID: "memory", Capacity: &decision.CapacityRequirement{MaximumResidentBytes: 16 << 30}},
			}}, Preferences: []role.Preference{{Requirement: "memory", Weight: 1, Worst: 32 << 30, Best: 0}}}
	library, err := (role.Store{Dir: filepath.Join(root, ".roles")}).Define(spec)
	if err != nil {
		t.Fatal(err)
	}
	return library
}

func TestLocalEvidenceListsAndReviewsWithoutPrivateText(t *testing.T) {
	root := t.TempDir()
	library := evidenceLibrary(t, root)
	source, err := newLocalEvidence(root)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := source.list(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(listed)
	if !bytes.Contains(data, []byte(`"name":"coding"`)) || bytes.Contains(data, []byte("private")) || bytes.Contains(data, []byte(root)) {
		t.Fatalf("list=%s", data)
	}
	reviewed, err := source.review(t.Context(), "coding")
	if err != nil {
		t.Fatal(err)
	}
	result, ok := reviewed.(reviewSummary)
	if !ok || result.State != "empty" || result.Revision != library.CurrentRevision || result.AdoptionAuthorized {
		t.Fatalf("review=%+v", reviewed)
	}
	if _, err := source.review(t.Context(), "../outside"); err == nil {
		t.Fatal("path role accepted")
	}
	if _, err := source.review(t.Context(), "missing"); err == nil {
		t.Fatal("missing role accepted")
	}
}

func TestSealedCanonicalEvidenceUsesExistingReviewContract(t *testing.T) {
	root := t.TempDir()
	evidenceLibrary(t, root)
	data, err := os.ReadFile("../record/testdata/schema5-signed-v0.9.8.json")
	if err != nil {
		t.Fatal(err)
	}
	var result record.Record
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	store := record.Store{Dir: root}
	saved, err := store.Save(&result)
	if err != nil {
		t.Fatal(err)
	}
	attachment := role.Attachment{Path: saved.CanonicalPath, RunID: saved.RunID, EvidenceSHA256: result.Completion.EvidenceSHA256}
	library, err := (role.Store{Dir: filepath.Join(root, ".roles")}).Attach("coding", attachment)
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
		t.Fatalf("MCP changed evidence semantics: %+v", got)
	}
	if err := os.Remove(saved.CanonicalPath); err != nil {
		t.Fatal(err)
	}
	again, err := source.review(t.Context(), "coding")
	if err != nil {
		t.Fatal(err)
	}
	missing, ok := again.(reviewSummary)
	if !ok || missing.State != "unresolved" || missing.AdoptionAuthorized {
		t.Fatalf("missing evidence was accepted: %+v", again)
	}
}

func TestReviewSummaryOmitsRawIdentityAndErrors(t *testing.T) {
	report := role.ReviewReport{Role: "coding", State: "unresolved", Candidates: []role.Candidate{{
		ID: "sha256:opaque", RunID: "secret-run", Model: "C:/secret/model", State: "unresolved", Reasons: []string{"token=secret"},
		Preference: &role.PreferenceResult{Estimate: .5, Low: .3, High: .7},
	}}, Gaps: []string{"secret diagnostic"}, Next: "secret command"}
	data, err := json.Marshal(summarizeReview(report))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("secret")) || !bytes.Contains(data, []byte(`"reason_count":1`)) || !bytes.Contains(data, []byte(`"low":0.3`)) {
		t.Fatalf("summary=%s", data)
	}
}

func TestEvidencePreflightRejectsExternalAndOversizedFiles(t *testing.T) {
	root := t.TempDir()
	source, err := newLocalEvidence(root)
	if err != nil {
		t.Fatal(err)
	}
	library := role.Library{Candidates: []role.Attachment{{Path: filepath.Join(t.TempDir(), "secret.json")}}}
	if source.checkEvidence(library) == nil {
		t.Fatal("external path accepted")
	}
	path := filepath.Join(root, "oversized.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxEvidenceFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	library.Candidates[0].Path = path
	if source.checkEvidence(library) == nil {
		t.Fatal("oversized evidence accepted")
	}
	if err := os.Mkdir(filepath.Join(root, "directory.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := checkedFile(filepath.Join(root, "directory.json"), maxEvidenceFileBytes); err == nil {
		t.Fatal("nonregular evidence accepted")
	}
}

func TestEvidencePreflightRejectsSymlinkRedirection(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".history")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	source, err := newLocalEvidence(root)
	if err != nil {
		t.Fatal(err)
	}
	if source.checkEvidence(role.Library{}) == nil {
		t.Fatal("redirected history accepted")
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.json"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.json"), filepath.Join(root, "link.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := checkedFile(filepath.Join(root, "link.json"), maxEvidenceFileBytes); err == nil {
		t.Fatal("redirected evidence accepted")
	}
}

func TestLocalEvidenceDoesNotCreateMissingDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	source, err := newLocalEvidence(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := source.list(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), root) {
		t.Fatal("path leaked")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("read-only tool created directory: %v", err)
	}
}

func TestPreflightBoundsDirectoryTotalsAndDisplayRoundoff(t *testing.T) {
	root := t.TempDir()
	for i := range 5 {
		file, err := os.Create(filepath.Join(root, strings.Repeat("a", i+1)+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxEvidenceFileBytes); err != nil {
			t.Fatal(err)
		}
		file.Close()
	}
	if _, err := boundedDirectory(root, maxEvidenceFileBytes); err == nil {
		t.Fatal("aggregate limit not enforced")
	}
	for _, path := range []string{"", `\\server\share`, "//server/share"} {
		if _, err := newLocalEvidence(path); err == nil {
			t.Fatalf("invalid local root accepted: %q", path)
		}
	}
	if clampUtility(1.0000000000000002) != 1 || clampUtility(-0.0000000000000001) != 0 {
		t.Fatal("output schema roundoff not clamped")
	}
}
