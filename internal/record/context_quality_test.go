package record

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/contextquality"
	"github.com/blisspixel/fitr/internal/eval"
)

const contextFixtureSeed = "0123456789abcdef0123456789abcdef"

func contextFixturePlan(t *testing.T) contextquality.Plan {
	t.Helper()
	policy, err := contextquality.NewPolicy(8192, []int{2048, 4096})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := contextquality.NewPlan(policy, contextFixtureSeed)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// contextFixtureObservations answers every cell wrongly but completely, which
// exercises a complete phase without needing an expected answer here.
func contextFixtureObservations(plan contextquality.Plan) []contextquality.Observation {
	observations := make([]contextquality.Observation, 0, len(plan.Cells))
	for _, cell := range plan.Cells {
		observations = append(observations, contextquality.Observation{
			CellID: cell.ID, PayloadSHA256: cell.PayloadSHA256, PromptSHA256: cell.PromptSHA256,
			Disposition: contextquality.Answered, Answer: `{"answer":"none"}`,
		})
	}
	return observations
}

func contextSealedRecord(t *testing.T, plan contextquality.Plan, observations []contextquality.Observation) *Record {
	t.Helper()
	return completedEvidenceRecord(t, nil, nil, func(r *Record) {
		if err := r.PlanContextQuality(plan); err != nil {
			t.Fatal(err)
		}
		if err := r.AttachContextQuality(plan, observations); err != nil {
			t.Fatal(err)
		}
	})
}

// A run without a context phase must sign exactly the bytes it signed before
// this field existed. Absence has to be absence on the wire, not a null.
func TestRecordWithoutAContextPhaseKeepsItsSignedBytes(t *testing.T) {
	r := completedEvidenceRecord(t, nil, []eval.CheckOutcome{{TaskID: "check", Pass: true, Outcome: eval.OutcomePass}})
	manifest, err := json.Marshal(r.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := r.completedEvidenceJSON(r.Completion.Profile)
	if err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"context_cells", "context_plan_sha256", "context_quality"} {
		for name, encoded := range map[string][]byte{"manifest": manifest, "payload": payload, "record": record} {
			if bytes.Contains(encoded, []byte(key)) {
				t.Fatalf("%s carries %q for a run that planned no context phase", name, key)
			}
		}
	}
	if err := r.ValidateEvidenceContract(); err != nil {
		t.Fatal(err)
	}
}

// The plan is sealed before inference and the observations are signed after,
// so neither the schedule nor the answers can be replaced afterwards.
func TestContextPhaseIsSealedBeforeInferenceAndSigned(t *testing.T) {
	plan := contextFixturePlan(t)
	r := contextSealedRecord(t, plan, contextFixtureObservations(plan))
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if r.Manifest.TaskPlan.ContextPlanSHA256 != digest || r.Manifest.TaskPlan.ContextCells != len(plan.Cells) {
		t.Fatalf("manifest sealed %+v", r.Manifest.TaskPlan)
	}
	payload, err := r.completedEvidenceJSON(r.Completion.Profile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(payload, []byte("context_quality")) {
		t.Fatal("a completed context phase was left out of the signed payload")
	}
	if r.ContextQuality.Report.Outcome != contextquality.Fail || r.ContextQuality.Report.VerifiedPrefixUTF8Bytes != nil {
		t.Fatalf("wrong answers produced %+v", r.ContextQuality.Report)
	}
	if err := r.ValidateEvidenceContract(); err != nil {
		t.Fatal(err)
	}
}

// The stored report is the analyzer's conclusion about the stored
// observations. A record cannot carry a verdict its own evidence denies.
//
// The expected messages record which layer answers first, because more than
// one does. Rewriting an answer into a differently wrong answer leaves the
// report genuinely identical, so the signature over the exact text is what
// rejects it. Editing the record's task plan contradicts the manifest copy
// before the phase itself is examined. Both are stronger than this phase's own
// check, and the point of naming them is that they must stay that way.
func TestContextEvidenceRejectsTamperingAndMissingHalves(t *testing.T) {
	cellCount := len(contextFixturePlan(t).Cells)
	for _, tc := range []struct {
		name   string
		mutate func(*Record)
		want   string
	}{
		{"claimed pass", func(r *Record) {
			r.ContextQuality.Report.Outcome = contextquality.Pass
		}, "does not match its observations"},
		{"claimed prefix", func(r *Record) {
			value := 4096
			r.ContextQuality.Report.VerifiedPrefixUTF8Bytes = &value
		}, "does not match its observations"},
		{"dropped observation", func(r *Record) {
			r.ContextQuality.Observations = r.ContextQuality.Observations[1:]
		}, "does not match its observations"},
		{"forged plan", func(r *Record) {
			r.ContextQuality.Plan.Cells[0].PayloadUTF8Bytes = 4096
		}, "deterministic policy"},
		{"phase discarded", func(r *Record) {
			r.ContextQuality = nil
		}, "no attached observations"},
		{"answer rewritten", func(r *Record) {
			r.ContextQuality.Observations[0].Answer = `{"answer":"other"}`
		}, "does not match its receipt"},
		{"unsealed observations", func(r *Record) {
			r.TaskPlan.ContextCells, r.TaskPlan.ContextPlanSHA256 = 0, ""
		}, "task plan differs from its manifest"},
		{"substituted plan digest", func(r *Record) {
			r.TaskPlan.ContextPlanSHA256 = "sha256:" + strings.Repeat("a", 64)
		}, "task plan differs from its manifest"},
		{"count disagrees", func(r *Record) {
			r.TaskPlan.ContextCells = cellCount - 1
		}, "task plan differs from its manifest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Each case gets its own plan: Plan carries nested slices, so a
			// shared one would let a forged case corrupt every later subtest.
			plan := contextFixturePlan(t)
			r := contextSealedRecord(t, plan, contextFixtureObservations(plan))
			tc.mutate(r)
			err := r.ValidateEvidenceContract()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("tamper error = %v, want %q", err, tc.want)
			}
		})
	}
}

// The plan binding is checked on its own, because the outer manifest and
// signature comparisons answer first in a complete record and would otherwise
// leave this layer untested. A loader that reaches it must still refuse
// observations whose sealed plan is absent, different, or a different length.
func TestContextPlanBindingIsCheckedIndependently(t *testing.T) {
	plan := contextFixturePlan(t)
	observations := contextFixtureObservations(plan)
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	report, err := contextquality.Analyze(plan, observations)
	if err != nil {
		t.Fatal(err)
	}
	evidence := &ContextQuality{Plan: plan, Observations: observations, Report: report}
	for _, tc := range []struct {
		name string
		plan TaskPlan
		want string
	}{
		{"bound", TaskPlan{ContextCells: len(plan.Cells), ContextPlanSHA256: digest}, ""},
		{"no sealed plan", TaskPlan{}, "do not match the sealed plan"},
		{"digest absent", TaskPlan{ContextCells: len(plan.Cells)}, "do not match the sealed plan"},
		{"digest malformed", TaskPlan{ContextCells: len(plan.Cells), ContextPlanSHA256: "sha256:zz"},
			"do not match the sealed plan"},
		{"different plan", TaskPlan{ContextCells: len(plan.Cells), ContextPlanSHA256: "sha256:" + strings.Repeat("a", 64)},
			"do not match the sealed plan"},
		{"count disagrees", TaskPlan{ContextCells: len(plan.Cells) - 1, ContextPlanSHA256: digest},
			"do not match planned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Record{SchemaVersion: EvidenceSchemaVersion, TaskPlan: tc.plan, ContextQuality: evidence}
			err := r.validateContextQuality()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("a correctly bound phase was rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("binding error = %v, want %q", err, tc.want)
			}
		})
	}
}

// A phase cannot be sealed after the manifest exists, sealed twice, or
// attached twice, and a rejected attachment leaves nothing behind.
func TestContextPhaseSealingIsSingleUseAndOrdered(t *testing.T) {
	plan := contextFixturePlan(t)
	observations := contextFixtureObservations(plan)
	r := contextSealedRecord(t, plan, observations)
	if err := r.PlanContextQuality(plan); err == nil || !strings.Contains(err.Error(), "before the run manifest") {
		t.Fatalf("sealing after the manifest = %v", err)
	}
	if err := r.AttachContextQuality(plan, observations); err == nil || !strings.Contains(err.Error(), "already attached") {
		t.Fatalf("second attachment = %v", err)
	}
	fresh := &Record{SchemaVersion: EvidenceSchemaVersion}
	if err := fresh.PlanContextQuality(plan); err != nil {
		t.Fatal(err)
	}
	if err := fresh.PlanContextQuality(plan); err == nil || !strings.Contains(err.Error(), "already sealed") {
		t.Fatalf("second seal = %v", err)
	}
	// An attachment the analyzer refuses must not leave partial evidence.
	stray := []contextquality.Observation{{CellID: strings.Repeat("z", 71), Disposition: contextquality.Answered}}
	if err := fresh.AttachContextQuality(plan, stray); err == nil {
		t.Fatal("observations bound to no planned cell were accepted")
	}
	if fresh.ContextQuality != nil {
		t.Fatal("a refused attachment left evidence on the record")
	}
}

// An invalid plan cannot be sealed at all, so a manifest never commits to a
// schedule the generator would not reproduce.
func TestContextPlanMustBeValidBeforeItCanBeSealed(t *testing.T) {
	broken := contextFixturePlan(t)
	broken.SeedSet = strings.Repeat("f", 32)
	r := &Record{SchemaVersion: EvidenceSchemaVersion}
	if err := r.PlanContextQuality(broken); err == nil {
		t.Fatal("a plan that differs from its policy and seed was sealed")
	}
	if r.TaskPlan.ContextCells != 0 || r.TaskPlan.ContextPlanSHA256 != "" {
		t.Fatalf("a refused seal left %+v", r.TaskPlan)
	}
}

// A sealed phase must not change under the caller that supplied the plan.
// Plan carries nested slices, so storing the caller's value would leave the
// record's evidence editable from outside after it was attached.
func TestAttachedContextPlanIsIndependentOfTheCaller(t *testing.T) {
	plan := contextFixturePlan(t)
	r := &Record{SchemaVersion: EvidenceSchemaVersion}
	if err := r.PlanContextQuality(plan); err != nil {
		t.Fatal(err)
	}
	if err := r.AttachContextQuality(plan, contextFixtureObservations(plan)); err != nil {
		t.Fatal(err)
	}
	plan.Cells[0].PayloadUTF8Bytes = 4096
	plan.Cells[0].Spans = nil
	plan.Policy.PayloadUTF8Bytes[0] = 8192
	if err := r.validateContextQuality(); err != nil {
		t.Fatalf("the caller's later edit reached sealed evidence: %v", err)
	}
}
