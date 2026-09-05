package contextquality

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func answeredObservations(t *testing.T, plan Plan) []Observation {
	t.Helper()
	observations := make([]Observation, 0, len(plan.Cells))
	for index, cell := range plan.Cells {
		task, err := plannedTask(plan, index+1)
		if err != nil {
			t.Fatal(err)
		}
		observations = append(observations, Observation{CellID: cell.ID, PayloadSHA256: cell.PayloadSHA256,
			PromptSHA256: cell.PromptSHA256, Disposition: Answered, Answer: answerForTask(t, task)})
	}
	return observations
}

func TestAnalysisAllPassIsOnlyFiniteUnboundTaskSetEvidence(t *testing.T) {
	plan := testPlan(t, 2048, 4096, 8192)
	observations := answeredObservations(t, plan)
	report, err := Analyze(plan, observations)
	if err != nil {
		t.Fatal(err)
	}
	if report.Schema != ReportSchema || report.PlanSHA256 != plan.PlanSHA256 || report.Outcome != Pass || !report.Complete || !report.AtLeastLargestTested {
		t.Fatal("complete fixed task-set outcomes lost", report)
	}
	if report.Scope != "finite_context_task_set" || report.BoundKind != "finite_task_set" || report.Runtime != "unbound" || report.NativeTokenAccounting != "unknown" {
		t.Fatal("task-set report claimed runtime, token or population authority")
	}
	if report.Counts != (Counts{Planned: 27, Pass: 27}) || len(report.Cells) != 27 || len(report.Tiers) != 3 {
		t.Fatal("denominator changed", report.Counts)
	}
	assertPrefix(t, report.KnownPrefixUTF8Bytes, 8192)
	assertPrefix(t, report.PossiblePrefixUTF8Bytes, 8192)
	assertPrefix(t, report.VerifiedPrefixUTF8Bytes, 8192)
	slices.Reverse(observations)
	shuffled, err := Analyze(plan, observations)
	if err != nil || !reflect.DeepEqual(report, shuffled) {
		t.Fatal("arrival order changed paired task analysis", err)
	}
	*report.KnownPrefixUTF8Bytes = 1
	assertPrefix(t, report.VerifiedPrefixUTF8Bytes, 8192)
	assertPrefix(t, report.PossiblePrefixUTF8Bytes, 8192)
	plan.Policy.PayloadUTF8Bytes[2] = 65536
	if report.Tiers[2].PayloadUTF8Bytes != 8192 {
		t.Fatal("report retained mutable input tier slice")
	}
}

func TestAnalysisNeverBridgesFailedTierOrPromotesIncompleteEvidence(t *testing.T) {
	plan := testPlan(t, 2048, 4096, 8192)
	complete := answeredObservations(t, plan)
	for _, item := range []struct {
		name                      string
		failed, missing           []int
		known, possible, verified int
		outcome                   Outcome
	}{
		{"middle fails upper passes", []int{9}, nil, 2048, 2048, 2048, Fail},
		{"first fails", []int{0}, nil, 0, 0, 0, Fail},
		{"last fails", []int{26}, nil, 4096, 4096, 4096, Fail},
		{"missing above failed middle", []int{9}, []int{26}, 2048, 2048, 0, Unavailable},
		{"missing middle upper passes", nil, []int{9}, 2048, 8192, 0, Unavailable},
		{"first missing middle fails", []int{9}, []int{0}, 0, 2048, 0, Unavailable},
	} {
		t.Run(item.name, func(t *testing.T) {
			observations := slices.Clone(complete)
			for _, index := range item.failed {
				observations[index].Answer = `{}`
			}
			for _, index := range item.missing {
				observations = append(observations[:index], observations[index+1:]...)
			}
			report, err := Analyze(plan, observations)
			if err != nil || report.Outcome != item.outcome || report.Complete != (len(item.missing) == 0) || report.AtLeastLargestTested {
				t.Fatal("failed/incomplete tier promoted", report, err)
			}
			assertPrefix(t, report.KnownPrefixUTF8Bytes, item.known)
			assertPrefix(t, report.PossiblePrefixUTF8Bytes, item.possible)
			assertPrefix(t, report.VerifiedPrefixUTF8Bytes, item.verified)
			if report.Counts.Planned != 27 || report.Counts.Fail != len(item.failed) || report.Counts.Unavailable != len(item.missing) {
				t.Fatal("missing data shrank denominator", report.Counts)
			}
		})
	}
	report, err := Analyze(plan, nil)
	if err != nil || report.Complete || report.Counts.Unavailable != 27 {
		t.Fatal("absent phase became completed", report, err)
	}
	assertPrefix(t, report.KnownPrefixUTF8Bytes, 0)
	assertPrefix(t, report.VerifiedPrefixUTF8Bytes, 0)
	assertPrefix(t, report.PossiblePrefixUTF8Bytes, 8192)
}

func assertPrefix(t *testing.T, prefix *int, want int) {
	t.Helper()
	if want == 0 {
		if prefix != nil {
			t.Fatal("no established tier must be absent, not zero ability", *prefix)
		}
		return
	}
	if prefix == nil || *prefix != want {
		t.Fatal("unexpected task-set prefix", prefix, want)
	}
}

func TestAnalysisDistinguishesTaskFailuresFromUnavailableObservations(t *testing.T) {
	plan := testPlan(t)
	complete := answeredObservations(t, plan)
	for _, item := range []struct {
		disposition                  Disposition
		answer, reason, resultReason string
		outcome                      Outcome
	}{
		{OutputLimit, complete[0].Answer, "", "output_limit", Fail},
		{ContextLimit, "", "", "context_limit", Fail},
		{Answered, strings.Repeat("x", MaxAnswerBytes+1), "", "answer_too_large", Fail},
		{NotAvailable, "", "cancelled", "cancelled", Unavailable},
		{NotAvailable, "", "transport_error", "transport_error", Unavailable},
		{NotAvailable, "", "accounting_unknown", "accounting_unknown", Unavailable},
		{NotAvailable, "", "runtime_unverified", "runtime_unverified", Unavailable},
		{NotAvailable, "", "not_attempted", "not_attempted", Unavailable},
	} {
		observations := slices.Clone(complete)
		observations[0].Disposition, observations[0].Answer, observations[0].UnavailableReason = item.disposition, item.answer, item.reason
		report, err := Analyze(plan, observations)
		if err != nil || report.Outcome != item.outcome || report.Cells[0].Reason != item.resultReason || report.Complete != (item.outcome == Fail) {
			t.Fatal("failure/unavailability distinction changed", item, report, err)
		}
	}
}

func TestAnalysisRefusesForgedOrContradictoryObservationInputs(t *testing.T) {
	plan := testPlan(t)
	valid := answeredObservations(t, plan)
	for _, mutate := range []func([]Observation) []Observation{
		func(o []Observation) []Observation { return append(o, o[0]) },
		func(o []Observation) []Observation { o[0] = o[1]; return o },
		func(o []Observation) []Observation { o[0].CellID = "sha256:" + strings.Repeat("0", 64); return o },
		func(o []Observation) []Observation { o[0].CellID = strings.Repeat("x", 10000); return o },
		func(o []Observation) []Observation { o[0].PayloadSHA256 = o[1].PayloadSHA256; return o },
		func(o []Observation) []Observation { o[0].PromptSHA256 = o[1].PromptSHA256; return o },
		func(o []Observation) []Observation { o[0].Disposition = "pass"; return o },
		func(o []Observation) []Observation { o[0].UnavailableReason = "cancelled"; return o },
		func(o []Observation) []Observation { o[0].Disposition = ContextLimit; return o },
		func(o []Observation) []Observation { o[0].Disposition = NotAvailable; return o },
		func(o []Observation) []Observation {
			o[0].Disposition = NotAvailable
			o[0].Answer = ""
			o[0].UnavailableReason = "other"
			return o
		},
		func(o []Observation) []Observation {
			o[0].Disposition = OutputLimit
			o[0].Answer = strings.Repeat("x", MaxAnswerBytes+1)
			return o
		},
	} {
		if _, err := Analyze(plan, mutate(slices.Clone(valid))); err == nil {
			t.Fatal("forged or contradictory task evidence accepted")
		}
	}
	plan.Cells[0].Seed++
	if _, err := Analyze(plan, valid); err == nil {
		t.Fatal("analysis trusted observations under an invalid plan")
	}
}
