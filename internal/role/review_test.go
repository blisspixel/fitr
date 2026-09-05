package role

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

var roleReviewNow = time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)

func TestReviewKeepsFastFailedQualityOutOfPreferenceSelection(t *testing.T) {
	store := record.Store{Dir: t.TempDir()}
	fast := roleReviewSave(t, store, roleReviewRecord(t, "fast", 100, 0, 8192, "runtime-1", roleReviewNow.Add(-time.Hour)))
	slow := roleReviewSave(t, store, roleReviewRecord(t, "slow", 20, 8, 8192, "runtime-1", roleReviewNow.Add(-time.Hour)))
	report := roleReviewReport(t, store, roleReviewSpec(), fast, slow)
	if report.Candidates[0].State != "ineligible" || report.Candidates[0].Preference != nil ||
		report.Candidates[1].State != "eligible" || report.Candidates[1].Preference == nil ||
		report.State != "single-qualified" || report.Lead != "" {
		t.Fatalf("speed bypassed quality or manufactured a winner: %+v", report)
	}
}

func TestReviewUncertainCandidateBlocksOtherwiseClearLead(t *testing.T) {
	store := record.Store{Dir: t.TempDir()}
	fast := roleReviewSave(t, store, roleReviewRecord(t, "fast", 80, 8, 8192, "runtime-1", roleReviewNow.Add(-time.Hour)))
	slow := roleReviewSave(t, store, roleReviewRecord(t, "slow", 20, 8, 8192, "runtime-1", roleReviewNow.Add(-time.Hour)))
	spec := roleReviewSpec()
	initial := roleReviewReport(t, store, spec, fast, slow)
	if initial.State != "exploration-lead" || initial.Lead != fast.EvidenceSHA256 {
		t.Fatalf("comparable bounded observations did not establish an exploration lead: %+v", initial)
	}
	uncertain := roleReviewSave(t, store, roleReviewRecord(t, "uncertain", 100, 4, 8192, "runtime-1", roleReviewNow.Add(-time.Hour)))
	report := roleReviewReport(t, store, spec, fast, slow, uncertain)
	if report.State != "unresolved" || report.Lead != "" || report.Candidates[2].State != "unresolved" {
		t.Fatalf("uncertain declared candidate was omitted from selection: %+v", report)
	}
}

func TestReviewRechecksAttachmentIdentityAndFreshness(t *testing.T) {
	for _, scenario := range []string{"expired", "replaced", "missing", "invalid", "future"} {
		t.Run(scenario, func(t *testing.T) {
			store := record.Store{Dir: t.TempDir()}
			started := roleReviewNow.Add(-time.Hour)
			if scenario == "expired" {
				started = roleReviewNow.Add(-31 * 24 * time.Hour)
			}
			if scenario == "future" {
				started = roleReviewNow.Add(time.Hour)
			}
			attachment := roleReviewSave(t, store, roleReviewRecord(t, "model", 50, 8, 8192, "runtime-1", started))
			switch scenario {
			case "replaced":
				roleReviewSave(t, store, roleReviewRecord(t, "model", 80, 8, 8192, "runtime-1", roleReviewNow.Add(-time.Minute)))
			case "missing":
				if err := os.Remove(attachment.Path); err != nil {
					t.Fatal(err)
				}
			case "invalid":
				if err := os.WriteFile(attachment.Path, []byte(`{`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			report := roleReviewReport(t, store, roleReviewSpec(), attachment)
			want := "unresolved"
			if scenario == "expired" || scenario == "replaced" {
				want = "stale"
			}
			if report.Candidates[0].State != want || report.Candidates[0].Preference != nil || report.Lead != "" || report.State != "unresolved" {
				t.Fatalf("unusable attachment qualified: %+v", report)
			}
		})
	}
}

func TestReviewRejectsCrossContextAndRuntimeComparisons(t *testing.T) {
	for _, scenario := range []string{"context", "runtime"} {
		t.Run(scenario, func(t *testing.T) {
			store := record.Store{Dir: t.TempDir()}
			first := roleReviewSave(t, store, roleReviewRecord(t, "first", 20, 8, 8192, "runtime-1", roleReviewNow.Add(-time.Hour)))
			context, runtime := 8192, "runtime-1"
			if scenario == "context" {
				context = 16384
			} else {
				runtime = "runtime-2"
			}
			second := roleReviewSave(t, store, roleReviewRecord(t, "second", 80, 8, context, runtime, roleReviewNow.Add(-time.Hour)))
			report := roleReviewReport(t, store, roleReviewSpec(), first, second)
			if report.Candidates[0].State != "eligible" || report.Candidates[1].State != "eligible" ||
				report.State != "unresolved" || report.Lead != "" || report.Comparison == nil || report.Comparison.Ready {
				t.Fatalf("incomparable configurations acquired a preference winner: %+v", report)
			}
		})
	}
}

func TestReviewExternalCopyCannotQualifyEvenWithValidSignature(t *testing.T) {
	store := record.Store{Dir: t.TempDir()}
	canonical := roleReviewSave(t, store, roleReviewRecord(t, "model", 50, 8, 8192, "runtime-1", roleReviewNow.Add(-time.Hour)))
	data, err := os.ReadFile(canonical.Path)
	if err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "copied.json")
	if err := os.WriteFile(external, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AttachRecord(external, store); err == nil {
		t.Fatal("external copy became canonical evidence")
	}
	// A hand-edited library must not bypass AttachRecord's reconciliation.
	canonical.Path = external
	report := roleReviewReport(t, store, roleReviewSpec(), canonical)
	if report.State != "unresolved" || report.Candidates[0].State != "unresolved" || report.Candidates[0].Preference != nil {
		t.Fatalf("external evidence was promoted during review: %+v", report)
	}
}

func TestReviewRenamedArchiveCannotRestoreSupersededEvidence(t *testing.T) {
	store := record.Store{Dir: t.TempDir()}
	old := roleReviewSave(t, store, roleReviewRecord(t, "model", 90, 8, 8192, "runtime-1", roleReviewNow.Add(-time.Hour)))
	data, err := os.ReadFile(old.Path)
	if err != nil {
		t.Fatal(err)
	}
	roleReviewSave(t, store, roleReviewRecord(t, "model", 50, 0, 8192, "runtime-1", roleReviewNow.Add(-time.Minute)))
	old.Path = filepath.Join(store.Dir, "renamed-old-run.json")
	if err := os.WriteFile(old.Path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	// The record store accepts this history twin for read-only inspection;
	// role qualification must still require the actual current model path.
	if result, err := store.Read(old.Path); err != nil || result.EvidenceIntegrityIssue() != "" {
		t.Fatalf("fixture did not reach the canonical-path boundary: result=%+v err=%v", result, err)
	}
	if _, err := AttachRecord(old.Path, store); err == nil {
		t.Fatal("renamed archived success became current role evidence")
	}
	report := roleReviewReport(t, store, roleReviewSpec(), old)
	if report.Candidates[0].State != "unresolved" || report.Candidates[0].Preference != nil || report.Lead != "" {
		t.Fatalf("hand-edited attachment restored superseded evidence: %+v", report)
	}
}

func TestReviewCanonicalSymlinkCannotRestoreArchivedSuccess(t *testing.T) {
	store := record.Store{Dir: t.TempDir()}
	previous := roleReviewRecord(t, "model", 90, 8, 8192, "runtime-1", roleReviewNow.Add(-time.Hour))
	paths, err := store.Save(previous)
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := AttachRecord(paths.CanonicalPath, store)
	if err != nil {
		t.Fatal(err)
	}
	roleReviewSave(t, store, roleReviewRecord(t, "model", 50, 0, 8192, "runtime-1", roleReviewNow.Add(-time.Minute)))
	if err := os.Remove(paths.CanonicalPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(paths.HistoryPath, paths.CanonicalPath); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symbolic links require Windows developer mode or privilege: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := AttachRecord(paths.CanonicalPath, store); err == nil {
		t.Fatal("symbolic link restored archived evidence during attachment")
	}
	report := roleReviewReport(t, store, roleReviewSpec(), attachment)
	if report.State != "unresolved" || report.Candidates[0].State != "unresolved" || report.Candidates[0].Preference != nil {
		t.Fatalf("symbolic link restored archived evidence during review: %+v", report)
	}
}

func TestReviewFailedQualityWithUnknownContextRemainsUnresolved(t *testing.T) {
	store := record.Store{Dir: t.TempDir()}
	result := roleReviewRecord(t, "unknown-context", 100, 0, 8192, "runtime-1", roleReviewNow.Add(-time.Hour))
	profile, identity, provenance := result.Completion.Profile, result.Manifest.Model, *result.Manifest.Provenance
	result.Manifest, result.Completion, result.RunID = nil, nil, ""
	result.DeviceV2.Context.EffectiveTokens, result.DeviceV2.Context.EffectiveSource = nil, ""
	result.DeviceKey = result.Device.Key()
	result.Memory.EffectiveCtx = nil
	result.Scorecard = score.Score(result.Measured(), profile)
	if err := result.AttachManifest(identity, provenance); err != nil {
		t.Fatal(err)
	}
	if err := result.CompleteEvidence(profile); err != nil {
		t.Fatal(err)
	}
	attachment := roleReviewSave(t, store, result)
	report := roleReviewReport(t, store, roleReviewSpec(), attachment)
	if report.Candidates[0].Evaluation == nil || report.Candidates[0].Evaluation.Eligibility != decision.DecisionIneligible {
		t.Fatalf("fixture did not expose failed quality with unresolved configuration: %+v", report)
	}
	if report.State != "unresolved" || report.Candidates[0].State != "unresolved" || report.Candidates[0].Preference != nil {
		t.Fatalf("unknown serving context became a definite role rejection: %+v", report)
	}
	qualified := roleReviewSave(t, store, roleReviewRecord(t, "qualified", 20, 8, 8192, "runtime-1", roleReviewNow.Add(-time.Hour)))
	report = roleReviewReport(t, store, roleReviewSpec(), attachment, qualified)
	if report.State != "unresolved" || report.Lead != "" {
		t.Fatalf("unknown configuration was discarded from the candidate set: %+v", report)
	}
}

func TestReattachmentRelocatesSameCanonicalEvidence(t *testing.T) {
	roles := Store{Dir: filepath.Join(t.TempDir(), "roles")}
	originalStore, relocatedStore := record.Store{Dir: t.TempDir()}, record.Store{Dir: t.TempDir()}
	result := roleReviewRecord(t, "model", 50, 8, 8192, "runtime-1", roleReviewNow.Add(-time.Hour))
	original, relocated := roleReviewSave(t, originalStore, result), roleReviewSave(t, relocatedStore, result)
	if _, err := roles.Define(roleReviewSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := roles.Attach("structured-work", original); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(original.Path); err != nil {
		t.Fatal(err)
	}
	library, err := roles.Attach("structured-work", relocated)
	if err != nil {
		t.Fatal(err)
	}
	if len(library.Candidates) != 1 || library.Candidates[0] != relocated {
		t.Fatalf("reattachment retained a stale location or duplicated evidence: %+v", library.Candidates)
	}
	reloaded, err := roles.Load("structured-work")
	if err != nil {
		t.Fatal(err)
	}
	report, err := Review(reloaded, relocatedStore, roleReviewNow)
	if err != nil || report.State != "single-qualified" {
		t.Fatalf("relocated evidence did not survive persistence and review: report=%+v err=%v", report, err)
	}
}

func TestReviewEmptyAndNoQualifiedCandidates(t *testing.T) {
	store := record.Store{Dir: t.TempDir()}
	if report := roleReviewReport(t, store, roleReviewSpec()); report.State != "empty" || report.Lead != "" {
		t.Fatalf("empty role = %+v", report)
	}
	failed := roleReviewSave(t, store, roleReviewRecord(t, "failed", 50, 0, 8192, "runtime-1", roleReviewNow.Add(-time.Hour)))
	if report := roleReviewReport(t, store, roleReviewSpec(), failed); report.State != "no-qualified-candidate" || report.Lead != "" {
		t.Fatalf("failed role = %+v", report)
	}
}

func TestReviewMissingPreferenceBoundsAndTiesDoNotProduceLead(t *testing.T) {
	for _, scenario := range []string{"single speed sample", "equal speed"} {
		t.Run(scenario, func(t *testing.T) {
			store := record.Store{Dir: t.TempDir()}
			samples := 3
			if scenario == "single speed sample" {
				samples = 1
			}
			first := roleReviewSave(t, store, roleReviewRecord(t, "first", 50, 8, 8192, "runtime-1", roleReviewNow.Add(-time.Hour), samples))
			second := roleReviewSave(t, store, roleReviewRecord(t, "second", 50, 8, 8192, "runtime-1", roleReviewNow.Add(-time.Hour), samples))
			report := roleReviewReport(t, store, roleReviewSpec(), first, second)
			if report.Lead != "" || report.Candidates[0].State != "eligible" || report.Candidates[1].State != "eligible" {
				t.Fatalf("missing bounds or tie manufactured a winner: %+v", report)
			}
			if scenario == "equal speed" && report.State != "tradeoff" {
				t.Fatalf("equal observations did not remain a tradeoff: %+v", report)
			}
			if scenario == "single speed sample" && (report.State != "unresolved" || report.Candidates[1].Preference != nil ||
				report.Comparison == nil || !report.Comparison.Ready) {
				t.Fatalf("one speed sample acquired preference certainty: %+v", report)
			}
		})
	}
}

func roleReviewSpec() Spec {
	rate, speed := 0.5, 1.0
	return Spec{
		Schema: SpecSchema, Name: "structured-work", MaxAgeDays: 30,
		Decision: decision.DecisionSpec{
			Schema: decision.SpecSchema, Name: "structured work", Evidence: decision.EvidenceDecide,
			Requirements: []decision.Requirement{
				{ID: "quality", Behavior: &decision.BehaviorRequirement{Need: "structured_output", MinimumRate: &rate}},
				{ID: "context", Context: &decision.ContextRequirement{MinimumEffectiveTokens: 4096}},
				{ID: "memory", Capacity: &decision.CapacityRequirement{MaximumResidentBytes: 8 * device.GB}},
				{ID: "speed", Performance: &decision.PerformanceRequirement{Metric: decision.MetricDecodeTPS, AtLeast: &speed}},
			},
		},
		Preferences: []Preference{{Requirement: "speed", Weight: 1, Worst: 0, Best: 100}},
	}
}

func roleReviewReport(t *testing.T, records record.Store, spec Spec, attachments ...Attachment) ReviewReport {
	t.Helper()
	digest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	library := Library{Schema: LibrarySchema, Name: spec.Name, CurrentRevision: digest,
		Revisions: []Revision{{ID: digest, Spec: spec}}, Candidates: attachments}
	report, err := Review(library, records, roleReviewNow)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func roleReviewSave(t *testing.T, store record.Store, result *record.Record) Attachment {
	t.Helper()
	paths, err := store.Save(result)
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := AttachRecord(paths.CanonicalPath, store)
	if err != nil {
		t.Fatal(err)
	}
	return attachment
}

func roleReviewRecord(t *testing.T, model string, speed float64, passes, context int, runtime string, started time.Time, speedSamples ...int) *record.Record {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "record", "testdata", "schema5-signed-v0.9.8.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result record.Record
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	profile, identity, previous := result.Completion.Profile, result.Manifest.Model, *result.Manifest.Provenance
	result.Manifest, result.Completion, result.RunID = nil, nil, ""
	result.SchemaVersion, result.Model, result.StartedAt = record.EvidenceSchemaVersion, model, started.Format(time.RFC3339)
	result.NumCtx, result.Device.Runtime = context, runtime
	result.ModelMeta = ollama.ModelInfo{Name: model}
	result.Checks = nil
	for i := range 8 {
		outcome := eval.OutcomeFail
		if i < passes {
			outcome = eval.OutcomePass
		}
		result.Checks = append(result.Checks, eval.CheckOutcome{TaskID: fmt.Sprintf("check-%d", i),
			Family: fmt.Sprintf("family-%d", i), Need: "structured_output", Origin: "builtin", Seed: uint64(i),
			Pass: i < passes, Outcome: outcome})
	}
	result.TaskPlan.CheckTrialsLimit = len(result.Checks)
	result.TaskPlan.CheckPlanSHA256, err = record.ObservedCheckPlanSHA256(result.Checks)
	if err != nil {
		t.Fatal(err)
	}
	prepareRoleReviewMeasurements(t, &result, speed, context, speedSamples)
	result.EvidenceCounts, err = result.DeriveEvidenceCounts()
	if err != nil {
		t.Fatal(err)
	}
	result.Scorecard = score.Score(result.Measured(), profile)
	provenance, err := record.NewRunProvenance(previous.TaskSetSHA256, previous.SpecSHA256, profile,
		record.CurrentScoringPolicy(), record.SoftwareReceipt{FitrVersion: previous.FitrVersion,
			SoftwareBuildSHA256: previous.SoftwareBuildSHA256, BackendProtocol: previous.BackendProtocol})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(model))
	identity.Requested, identity.Resolved, identity.Runtime, identity.Value = model, model, runtime, "sha256:"+hex.EncodeToString(sum[:])
	if err := result.AttachManifest(identity, provenance); err != nil {
		t.Fatal(err)
	}
	if err := result.CompleteEvidence(profile); err != nil {
		t.Fatal(err)
	}
	if issue := result.EvidenceIntegrityIssue(); strings.TrimSpace(issue) != "" {
		t.Fatal(issue)
	}
	return &result
}

func prepareRoleReviewMeasurements(t *testing.T, result *record.Record, speed float64, context int, speedSamples []int) {
	t.Helper()
	fingerprint, err := device.NewFingerprintV2(result.Device, device.ContextVerification{
		RequestedTokens: context, EffectiveTokens: &context, EffectiveSource: device.ContextSourceRuntimeReport})
	if err != nil {
		t.Fatal(err)
	}
	result.DeviceV2 = &fingerprint
	result.DeviceKey, err = fingerprint.ComparabilityKey()
	if err != nil {
		t.Fatal(err)
	}
	sample := eval.SpeedResult{DecodeTPS: speed, TTFT: 0.5, PrefillTPS: 200, FirstOutputObserved: true,
		GatedCacheKnown: true, GatedPromptTok: 100, PrefillCacheKnown: true, PromptTok: 100,
		GatedLoadKnown: true, GatedResidencyKnown: true, GatedResident: true}
	sampleCount := 3
	if len(speedSamples) > 0 {
		sampleCount = speedSamples[0]
	}
	var speeds, latencies, prefills []float64
	for range sampleCount {
		result.Speed = append(result.Speed, sample)
		speeds, latencies, prefills = append(speeds, speed), append(latencies, 0.5), append(prefills, 200)
	}
	result.DecodeSum, result.TTFTSum, result.PrefillSum = stats.MeanSD(speeds), stats.MeanSD(latencies), stats.MeanSD(prefills)
	result.TaskPlan.SpeedSamples, result.TaskPlan.Memory = sampleCount, true
	result.Memory = eval.MemoryResult{Outcome: eval.OutcomePass, ResidentGB: 6, PctOnGPU: 100,
		RequestedCtx: context, EffectiveCtx: &context, ResidentBytes: 6 * device.GB, AcceleratorBytes: 6 * device.GB}
}
