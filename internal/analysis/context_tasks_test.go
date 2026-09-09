package analysis

import (
	"testing"

	"github.com/blisspixel/fitr/internal/contextquality"
	"github.com/blisspixel/fitr/internal/record"
)

// contextTasksFrom projects an already-derived report. Whether that report
// matches its observations is owned and tested by contextquality.Analyze and
// by record's re-derivation on load, so these fixtures state a report directly
// and assert only what the projection itself is responsible for: copying the
// sealed facts faithfully, and withholding a claim the record cannot make.
func contextTaskEvidence(t *testing.T, report contextquality.Report) *record.ContextQuality {
	t.Helper()
	policy, err := contextquality.NewPolicy(8192, []int{2048, 4096})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	plan, err := contextquality.NewPlan(policy, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	report.PlanSHA256 = plan.PlanSHA256
	return &record.ContextQuality{Plan: plan, Report: report}
}

func passingContextTaskReport() contextquality.Report {
	return contextquality.Report{
		Schema:  contextquality.ReportSchema,
		Outcome: contextquality.Pass,
		Counts:  contextquality.Counts{Planned: 18, Pass: 18},
		Tiers: []contextquality.Tier{
			{PayloadUTF8Bytes: 2048, Outcome: contextquality.Pass,
				Counts: contextquality.Counts{Planned: 9, Pass: 9}},
			{PayloadUTF8Bytes: 4096, Outcome: contextquality.Pass,
				Counts: contextquality.Counts{Planned: 9, Pass: 9}},
		},
		Complete:                true,
		VerifiedPrefixUTF8Bytes: intPointer(4096),
		AtLeastLargestTested:    true,
	}
}

func TestContextTasksProjectCountsAndTheVerifiedPrefix(t *testing.T) {
	evidence := contextTaskEvidence(t, passingContextTaskReport())
	projection := contextTasksFrom(&record.Record{ContextQuality: evidence}, false)
	if projection == nil {
		t.Fatal("an attached phase produced no projection")
	}
	if projection.Status != StatusAvailable {
		t.Fatalf("status = %q, want %q", projection.Status, StatusAvailable)
	}
	if projection.OperatingWindow != 8192 || projection.OutputReserve != contextquality.OutputReserveTokens {
		t.Fatalf("window %d reserve %d do not match the sealed policy",
			projection.OperatingWindow, projection.OutputReserve)
	}
	if projection.PlanSHA256 != evidence.Report.PlanSHA256 {
		t.Fatalf("plan digest = %q, want the sealed %q", projection.PlanSHA256, evidence.Report.PlanSHA256)
	}
	if projection.Planned != 18 || projection.Pass != 18 || projection.Fail != 0 || projection.Unavailable != 0 {
		t.Fatalf("counts planned %d pass %d fail %d unavailable %d do not match the report",
			projection.Planned, projection.Pass, projection.Fail, projection.Unavailable)
	}
	if len(projection.Tiers) != 2 {
		t.Fatalf("tiers = %d, want one per declared payload size", len(projection.Tiers))
	}
	if projection.Tiers[1].PayloadUTF8Bytes != 4096 || projection.Tiers[1].Pass != 9 {
		t.Fatalf("tier projection = %+v, want the sealed tier counts", projection.Tiers[1])
	}
	if projection.VerifiedPrefixBytes == nil || *projection.VerifiedPrefixBytes != 4096 {
		t.Fatalf("verified prefix = %v, want the largest passing tier", projection.VerifiedPrefixBytes)
	}
	if !projection.AtLeastLargestTested {
		t.Fatal("an all-pass phase should report that only the tested sizes are known")
	}
}

// A verified prefix is a claim about this exact artifact and runtime, which is
// what an integrity or identity issue removes. The observations stay visible so
// the operator can still read what happened.
func TestContextTasksStayDescriptiveWhenTheRecordCannotClaim(t *testing.T) {
	evidence := contextTaskEvidence(t, passingContextTaskReport())
	projection := contextTasksFrom(&record.Record{ContextQuality: evidence}, true)
	if projection.Status != StatusDescriptiveOnly {
		t.Fatalf("status = %q, want %q", projection.Status, StatusDescriptiveOnly)
	}
	if projection.VerifiedPrefixBytes != nil {
		t.Fatalf("unclaimable record projected a verified prefix of %d", *projection.VerifiedPrefixBytes)
	}
	if projection.Pass != 18 || len(projection.Tiers) != 2 {
		t.Fatal("descriptive evidence lost the observed counts it is still allowed to show")
	}
}

// An unavailable cell suppresses the prefix for the whole phase. The analyzer
// owns that rule; this proves the projection does not reintroduce a prefix from
// the tiers that did pass.
func TestContextTasksCarryAnUnavailablePhaseWithoutAPrefix(t *testing.T) {
	report := contextquality.Report{
		Schema:  contextquality.ReportSchema,
		Outcome: contextquality.Unavailable,
		Counts:  contextquality.Counts{Planned: 18, Pass: 8, Unavailable: 10},
		Tiers: []contextquality.Tier{
			{PayloadUTF8Bytes: 2048, Outcome: contextquality.Unavailable,
				Counts: contextquality.Counts{Planned: 9, Pass: 8, Unavailable: 1}},
			{PayloadUTF8Bytes: 4096, Outcome: contextquality.Unavailable,
				Counts: contextquality.Counts{Planned: 9, Unavailable: 9}},
		},
	}
	projection := contextTasksFrom(&record.Record{ContextQuality: contextTaskEvidence(t, report)}, false)
	if projection.VerifiedPrefixBytes != nil {
		t.Fatalf("verified prefix = %d, want none while a cell is unavailable",
			*projection.VerifiedPrefixBytes)
	}
	if projection.Unavailable != 10 || projection.Complete {
		t.Fatalf("unavailable %d complete %v do not describe an incomplete phase",
			projection.Unavailable, projection.Complete)
	}
}

// Every record written before the phase existed has no context evidence, and
// the projection must stay absent rather than becoming an empty section.
func TestContextTasksAreAbsentWithoutAPhase(t *testing.T) {
	if projection := contextTasksFrom(&record.Record{}, false); projection != nil {
		t.Fatalf("projection = %+v, want none for a run that planned no phase", projection)
	}
}
