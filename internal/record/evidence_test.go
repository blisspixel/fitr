package record

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/score"
)

func TestCompletionRejectsTamperedRawOutcomeCountsSummaryAndReceipt(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Record)
		want   string
	}{
		{"raw outcome", func(r *Record) { r.Checks[0].Outcome = eval.OutcomeFail }, "pass flag"},
		{"counts", func(r *Record) { c := r.EvidenceCounts["checks"]; c.Passed++; r.EvidenceCounts["checks"] = c }, "counts"},
		{"summary", func(r *Record) { r.DecodeSum.Mean = 999 }, "speed summaries"},
		{"scorecard", func(r *Record) { r.Scorecard.Passes++ }, "receipt"},
		{"completion signature", func(r *Record) { r.Completion.Signature = strings.Repeat("A", len(r.Completion.Signature)) }, "signature"},
		{"legacy device", func(r *Record) { r.Device.GPU = "forged-gpu" }, "legacy device"},
		{"resealed manifest", func(r *Record) {
			r.Manifest.Provenance.SpecSHA256 = "sha256:" + strings.Repeat("f", 64)
			if err := r.Manifest.Seal(); err != nil {
				t.Fatalf("reseal manifest: %v", err)
			}
		}, "receipt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := completedEvidenceRecord(t, []eval.ExecResult{{Outcome: eval.OutcomeInconclusive}},
				[]eval.CheckOutcome{{TaskID: "check", Pass: true, Outcome: eval.OutcomePass}})
			tc.mutate(r)
			if err := r.ValidateEvidenceContract(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("tamper error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestUnsafeEvidenceRejectsVerifierFromDifferentInterpreter(t *testing.T) {
	executor := eval.ExecutorReceipt{
		Kind: "python", Path: filepath.Join(t.TempDir(), "python"), Version: "Python 3.12.7",
		SHA256: "sha256:" + strings.Repeat("a", 64),
	}
	receipt := eval.VerificationReceipt{
		Protocol: "fitr.verifier.v2", InterpreterPath: executor.Path, InterpreterVer: executor.Version,
		InterpreterHash: "sha256:" + strings.Repeat("b", 64),
	}
	r := &Record{
		Manifest:  &RunManifest{Schema: RunManifestSchema, ExecutionPolicy: ExecutionUnsafe, Executor: &executor},
		CodeWrite: []eval.ExecResult{{Outcome: eval.OutcomeInconclusive, Verifier: &receipt}},
	}
	if err := r.validateExecutorEvidence(); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("mismatched verifier interpreter error = %v", err)
	}
}

func TestOriginalProfileAndProvenanceCompatibility(t *testing.T) {
	a := completedEvidenceRecord(t, []eval.ExecResult{{Outcome: eval.OutcomeInconclusive}},
		[]eval.CheckOutcome{{TaskID: "check", Pass: true, Outcome: eval.OutcomePass}})
	profile, err := a.OriginalProfile()
	if err != nil || profile.Name != "default" {
		t.Fatalf("original profile = %+v, err=%v", profile, err)
	}
	profile.Name = "mutated"
	again, err := a.OriginalProfile()
	if err != nil || again.Name != "default" {
		t.Fatalf("profile snapshot was mutable: %+v, err=%v", again, err)
	}
	b := completedEvidenceRecord(t, []eval.ExecResult{{Outcome: eval.OutcomeInconclusive}},
		[]eval.CheckOutcome{{TaskID: "check", Pass: true, Outcome: eval.OutcomePass}})
	if err := ProvenanceCompatibilityError(a, b); err != nil {
		t.Fatal(err)
	}
	changed := *b.Manifest.Provenance
	changed.SpecSHA256 = "sha256:" + strings.Repeat("f", 64)
	if err := a.Manifest.Provenance.CompatibilityError(changed); err == nil || !strings.Contains(err.Error(), "effective spec") {
		t.Fatalf("provenance mismatch = %v", err)
	}
}

func TestObservedLocalIdentityPersistsButIsNotRankable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(path, []byte("weights"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := NewModelIdentity("model", "model", "llama-server", "llama-server 1", "", path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Binding != IdentityBindingObserved || identity.RankingIssue() == "" {
		t.Fatalf("local identity = %+v", identity)
	}
	if err := identity.Validate(); err != nil {
		t.Fatalf("observed identity did not remain persistable: %v", err)
	}
}

func TestCurrentResultRequiresExactImmutableHistoryTwin(t *testing.T) {
	store := NewStore(t.TempDir())
	r := completedEvidenceRecord(t, []eval.ExecResult{{Outcome: eval.OutcomeInconclusive}},
		[]eval.CheckOutcome{{TaskID: "check", Pass: true, Outcome: eval.OutcomePass}})
	if _, err := store.Save(r); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadCurrent()
	if err != nil || len(loaded.Records) != 1 || loaded.Records[0].EvidenceIntegrityIssue() != "" {
		t.Fatalf("reconciled current = %+v, err=%v", loaded, err)
	}
	if err := os.RemoveAll(store.HistoryDir()); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.LoadCurrent()
	if err != nil || len(loaded.Records) != 1 || !strings.Contains(loaded.Records[0].EvidenceIntegrityIssue(), "immutable history") {
		t.Fatalf("unreconciled current = %+v, err=%v", loaded, err)
	}
}

func TestMultipleCurrentResultsReconcileWithTheirHistoryTwins(t *testing.T) {
	store := NewStore(t.TempDir())
	for range []string{"first", "second"} {
		r := completedEvidenceRecord(t, []eval.ExecResult{{Outcome: eval.OutcomeInconclusive}},
			[]eval.CheckOutcome{{TaskID: "check", Pass: true, Outcome: eval.OutcomePass}})
		if _, err := store.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := store.LoadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range loaded.Records {
		if issue := r.EvidenceIntegrityIssue(); issue != "" {
			t.Fatalf("result %s did not reconcile: %s", r.Model, issue)
		}
	}
}

func TestArtifactStemResistsSanitizationAndLongPrefixCollisions(t *testing.T) {
	if ArtifactStem("a/b") == ArtifactStem("a?b") {
		t.Fatal("sanitized names collided")
	}
	prefix := strings.Repeat("x", 100)
	if ArtifactStem(prefix+"a") == ArtifactStem(prefix+"b") {
		t.Fatal("long names collided after prefix truncation")
	}
	if strings.ContainsAny(ArtifactStem("../../ model"), `/\\`) {
		t.Fatal("artifact stem contains a path separator")
	}
}

func TestCompleteEvidenceRequiresExactProfileSnapshot(t *testing.T) {
	r := manifestRecord("model", "2026-08-21T12:00:00Z")
	r.SchemaVersion = EvidenceSchemaVersion
	r.TaskPlan = TaskPlan{CheckTrialsLimit: 1}
	r.Checks = []eval.CheckOutcome{{TaskID: "check", Pass: true, Outcome: eval.OutcomePass}}
	r.EvidenceCounts = map[string]eval.OutcomeCounts{
		"coding": eval.CountOutcomes(0), "checks": eval.CountOutcomes(1, eval.OutcomePass),
		"tools": eval.CountOutcomes(0), "refusal": eval.CountOutcomes(0), "plumbing": eval.CountOutcomes(0),
		"withdrawal": eval.CountOutcomes(0), "agentic": eval.CountOutcomes(0),
	}
	r.Rep = score.RepetitionMetrics("")
	r.Scorecard = score.Scorecard{Model: r.Model, Profile: r.Profile, Needs: map[string]score.Verdict{}}
	addTestFingerprintV2(t, r)
	profile := device.Profile{Name: "default", Description: "original", Gates: map[string]device.Gate{}}
	hashes, err := eval.BuiltinHashes()
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := NewRunProvenance(hashes.TaskSetSHA256, hashes.SpecSHA256, profile, CurrentScoringPolicy(), testSoftwareReceipt())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AttachManifest(digestIdentity(t, "model", "model"), provenance); err != nil {
		t.Fatal(err)
	}
	profile.Description = "forged"
	if err := r.CompleteEvidence(profile); err == nil || !strings.Contains(err.Error(), "differs from provenance") {
		t.Fatalf("forged profile completion error = %v", err)
	}
}
