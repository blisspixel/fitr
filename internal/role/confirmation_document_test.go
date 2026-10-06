package role

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/contextquality"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/score"
)

func documentRole(floor int) Spec {
	spec := roleReviewSpec()
	spec.Decision.Requirements[1].Context.MinimumUsableContextBytes = &floor
	return spec
}

func TestBatteryAloneCannotConfirmAUsableContextFloor(t *testing.T) {
	batteries := []*record.Record{
		roleConfirmationRecord(t, "fast", 80, 8, roleReviewNow.Add(-time.Hour)),
		roleConfirmationRecord(t, "slow", 20, 8, roleReviewNow.Add(-time.Hour)),
	}
	_, err := NewConfirmationPlan(documentRole(2048), batteries, batteries[0].Completion.EvidenceSHA256, roleReviewNow)
	if err == nil || !strings.Contains(err.Error(), "usable-context confirmation requires exploration context evidence from the owned fitting; a battery record alone cannot confirm the floor") {
		t.Fatalf("battery-only floor: %v", err)
	}
	if strings.Contains(err.Error(), "not a conclusive exploration lead") {
		t.Fatal("the floor check ran after the lead check")
	}
}

func TestExplorationBatteryCannotCarryTheContextSchedule(t *testing.T) {
	batteries := []*record.Record{
		roleConfirmationRecord(t, "fast", 80, 8, roleReviewNow.Add(-time.Hour)),
		roleConfirmationRecord(t, "slow", 20, 8, roleReviewNow.Add(-time.Hour)),
	}
	plan := documentContextPlan(t)
	answers := documentAnswers(t, plan)
	carried := make([]*record.Record, len(batteries))
	for index, battery := range batteries {
		carried[index] = roleResealPrepared(t, battery, func(point *record.Record) {
			point.Level = "full"
			if err := point.PlanContextQuality(plan); err != nil {
				t.Fatal(err)
			}
			if err := point.AttachContextQuality(plan, answers); err != nil {
				t.Fatal(err)
			}
		})
	}
	_, err := NewConfirmationPlan(roleReviewSpec(), carried, carried[0].Completion.EvidenceSHA256, roleReviewNow)
	if err == nil || !strings.Contains(err.Error(), "exploration battery carries a context schedule") {
		t.Fatalf("carried schedule: %v", err)
	}
}

func TestFreshContextPlanDoesNotReuseExploration(t *testing.T) {
	batteries := []*record.Record{
		roleConfirmationRecord(t, "fast", 80, 8, roleReviewNow.Add(-time.Hour)),
		roleConfirmationRecord(t, "slow", 20, 8, roleReviewNow.Add(-time.Hour)),
	}
	plan := documentContextPlan(t)
	contextPoints := documentContextRecords(t, batteries, plan, documentAnswers(t, plan))
	confirmation, err := NewConfirmationPlanWithContext(documentRole(2048), batteries, contextPoints, batteries[0].Completion.EvidenceSHA256, roleReviewNow)
	if err != nil {
		t.Fatal(err)
	}
	if confirmation.ContextPlanSHA256 == "" || confirmation.ContextPlanSHA256 == contextPoints[0].TaskPlan.ContextPlanSHA256 {
		t.Fatalf("confirmation context plan = %s, exploration = %s", confirmation.ContextPlanSHA256, contextPoints[0].TaskPlan.ContextPlanSHA256)
	}
	seed, err := record.ContextTaskSeedSet(confirmation.SeedSet)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := contextquality.NewPlan(*confirmation.ContextPolicy, seed)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := fresh.Digest()
	if err != nil || digest != confirmation.ContextPlanSHA256 {
		t.Fatalf("fresh digest = %s, plan = %s, err = %v", digest, confirmation.ContextPlanSHA256, err)
	}
	for index, candidate := range confirmation.Candidates {
		if candidate.ContextEvidenceSHA256 != contextPoints[index].Completion.EvidenceSHA256 {
			t.Fatalf("candidate %d did not pin exploration context evidence", index+1)
		}
	}
}

func TestSiblingContextPromotesTheByteFloorOnly(t *testing.T) {
	batteries := []*record.Record{
		roleConfirmationRecord(t, "fast", 80, 8, roleReviewNow.Add(-time.Hour)),
		roleConfirmationRecord(t, "slow", 20, 8, roleReviewNow.Add(-time.Hour)),
	}
	spec := documentRole(2048)
	records := record.Store{Dir: t.TempDir()}
	batteryRef := closeRoleStore(t, records, "doc-explore", "exploration", batteries)
	report, err := ReviewManaged(spec, records, batteryRef, []string{"fast", "slow"}, roleReviewNow)
	if err != nil {
		t.Fatal(err)
	}
	if report.Candidates[0].State == "eligible" || report.Candidates[1].State == "eligible" {
		t.Fatalf("battery alone cleared the byte floor: %+v", report.Candidates)
	}
	plan := documentContextPlan(t)
	contextPoints := documentContextRecords(t, batteries, plan, documentAnswers(t, plan))
	contextRef := closeRoleStore(t, records, "doc-context", "exploration", contextPoints)
	report, err = ReviewManagedWithContext(spec, records, batteryRef, &contextRef, []string{"fast", "slow"}, roleReviewNow)
	if err != nil {
		t.Fatal(err)
	}
	if report.Candidates[0].State != "eligible" || report.Candidates[1].State != "eligible" {
		t.Fatalf("passing sibling left the floor unresolved: %+v", report.Candidates)
	}

	failed := []*record.Record{
		roleConfirmationRecord(t, "fast", 80, 0, roleReviewNow.Add(-time.Hour)),
		batteries[1],
	}
	failedRef := closeRoleStore(t, records, "doc-failed", "exploration", failed)
	failedContext := documentContextRecords(t, failed, plan, documentAnswers(t, plan))
	failedContextRef := closeRoleStore(t, records, "doc-failed-context", "exploration", failedContext)
	report, err = ReviewManagedWithContext(spec, records, failedRef, &failedContextRef, []string{"fast", "slow"}, roleReviewNow)
	if err != nil {
		t.Fatal(err)
	}
	if report.Candidates[0].State != "ineligible" {
		t.Fatalf("context prefix repaired a failed behavior floor: %+v", report.Candidates[0])
	}
}

func TestOverlayDoesNotRepairATokenDisproofOrARogueLength(t *testing.T) {
	battery := roleConfirmationRecord(t, "fast", 80, 8, roleReviewNow.Add(-time.Hour))
	plan := documentContextPlan(t)
	contextPoint := documentContextRecords(t, []*record.Record{battery}, plan, documentAnswers(t, plan))[0]
	spec := roleReviewSpec()
	floor := 2048
	spec.Decision.Requirements[1].Context.MinimumEffectiveTokens = 16384
	spec.Decision.Requirements[1].Context.MinimumUsableContextBytes = &floor
	candidate, err := confirmationEvaluation(spec, battery)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := overlayUsableContext(candidate, spec, battery, contextPoint, plan.PlanSHA256)
	if err != nil {
		t.Fatal(err)
	}
	got := requirementByID(t, updated, "context")
	if got.State != decision.RequirementDisproven || got.Unit != analysis.UnitTokens {
		t.Fatalf("token disproof was replaced: %+v", got)
	}
	if updated.State == "eligible" {
		t.Fatal("token disproof became eligible")
	}

	unchanged, err := overlayUsableContext(candidate, documentRole(2048), battery, nil, plan.PlanSHA256)
	if err != nil || unchanged.State == "eligible" {
		t.Fatalf("missing context record cleared the floor: %+v %v", unchanged, err)
	}

	observed := 9999.0
	if declaredContextLength(decision.RequirementResult{Observed: &observed}, contextPoint) {
		t.Fatal("a length outside the declared tiers was treated as measured")
	}
	zero := 0.0
	if !declaredContextLength(decision.RequirementResult{Observed: &zero}, contextPoint) {
		t.Fatal("a failed smallest tier was not kept as a disproof")
	}
}

func TestOneFailedInstructionCellStopsThePrefix(t *testing.T) {
	battery := roleConfirmationRecord(t, "fast", 80, 8, roleReviewNow.Add(-time.Hour))
	plan := documentContextPlan(t)
	answers := documentAnswers(t, plan)
	tier := plan.Policy.PayloadUTF8Bytes[1]
	failed := -1
	for index, cell := range plan.Cells {
		if cell.PayloadUTF8Bytes == tier && cell.Family == contextquality.InstructionRetention {
			failed = index
			break
		}
	}
	if failed < 0 {
		t.Fatal("second tier has no instruction cell")
	}
	answers[failed].Answer = `{"answer":"none"}`
	contextPoint := documentContextRecords(t, []*record.Record{battery}, plan, answers)[0]
	spec := documentRole(plan.Policy.PayloadUTF8Bytes[1])
	candidate, err := confirmationEvaluation(spec, battery)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := overlayUsableContext(candidate, spec, battery, contextPoint, plan.PlanSHA256)
	if err != nil {
		t.Fatal(err)
	}
	got := requirementByID(t, updated, "context")
	if got.State != decision.RequirementDisproven || got.Observed == nil || int(*got.Observed) != plan.Policy.PayloadUTF8Bytes[0] {
		t.Fatalf("instruction failure prefix = %+v", got)
	}
	lower := documentRole(plan.Policy.PayloadUTF8Bytes[0])
	candidate, err = confirmationEvaluation(lower, battery)
	if err != nil {
		t.Fatal(err)
	}
	held, err := overlayUsableContext(candidate, lower, battery, contextPoint, plan.PlanSHA256)
	if err != nil {
		t.Fatal(err)
	}
	got = requirementByID(t, held, "context")
	if got.State != decision.RequirementEstablished || got.Observed == nil || int(*got.Observed) != plan.Policy.PayloadUTF8Bytes[0] {
		t.Fatalf("lower floor after a later instruction failure = %+v", got)
	}
}

func TestContextConfirmationCannotFinishFromTheBatteryStore(t *testing.T) {
	batteries := []*record.Record{
		roleConfirmationRecord(t, "fast", 80, 8, roleReviewNow.Add(-time.Hour)),
		roleConfirmationRecord(t, "slow", 20, 8, roleReviewNow.Add(-time.Hour)),
	}
	explored := documentContextPlan(t)
	exploredPoints := documentContextRecords(t, batteries, explored, documentAnswers(t, explored))
	plan, err := NewConfirmationPlanWithContext(documentRole(2048), batteries, exploredPoints, batteries[0].Completion.EvidenceSHA256, roleReviewNow)
	if err != nil {
		t.Fatal(err)
	}
	now := roleReviewNow.Add(2 * time.Minute)
	fresh := freshConfirmationPoints(t, plan, batteries, now.Add(-time.Minute))
	seed, err := record.ContextTaskSeedSet(plan.SeedSet)
	if err != nil {
		t.Fatal(err)
	}
	freshPlan, err := contextquality.NewPlan(*plan.ContextPolicy, seed)
	if err != nil {
		t.Fatal(err)
	}
	freshContext := documentContextRecords(t, fresh, freshPlan, documentAnswers(t, freshPlan))
	bundle, err := NewConfirmationBundleWithContext(plan, fresh, freshContext, now)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Report.State != "confirmed" {
		t.Fatalf("fresh context confirmation = %s", bundle.Report.State)
	}
	records := record.Store{Dir: t.TempDir()}
	batteryRef := closeRoleStore(t, records, "doc-confirm", "confirmation", fresh)
	if _, err := confirmationAttemptWithStores(bundle, records, &batteryRef, nil, now); err == nil || !strings.Contains(err.Error(), "usable-context confirmation requires its sealed context evidence") {
		t.Fatalf("battery store stood in for context evidence: %v", err)
	}
	contextRef := closeRoleStore(t, records, "doc-confirm-context", "confirmation", freshContext)
	if _, err := confirmationAttemptWithStores(bundle, records, &batteryRef, &contextRef, now); err != nil {
		t.Fatal(err)
	}
}

func documentContextPlan(t *testing.T) contextquality.Plan {
	t.Helper()
	policy, err := contextquality.NewPolicy(8192, []int{2048, 4096})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := record.ContextTaskSeedSet("role-exploration")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := contextquality.NewPlan(policy, seed)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func documentContextRecords(t *testing.T, batteries []*record.Record, plan contextquality.Plan, answers []contextquality.Observation) []*record.Record {
	t.Helper()
	points := make([]*record.Record, len(batteries))
	for index, battery := range batteries {
		points[index] = roleResealPrepared(t, battery, func(point *record.Record) {
			point.Level = "context"
			point.Experiment = nil
			if plan.PlanSHA256 == "" {
				return
			}
			if err := point.PlanContextQuality(plan); err != nil {
				t.Fatal(err)
			}
			if err := point.AttachContextQuality(plan, answers); err != nil {
				t.Fatal(err)
			}
		})
	}
	return points
}

func roleResealPrepared(t *testing.T, result *record.Record, prepare func(*record.Record)) *record.Record {
	t.Helper()
	result = roleConfirmationClone(t, result)
	profile, identity, provenance := result.Completion.Profile, result.Manifest.Model, *result.Manifest.Provenance
	result.Manifest, result.Completion, result.RunID = nil, nil, ""
	if prepare != nil {
		prepare(result)
	}
	var err error
	result.EvidenceCounts, err = result.DeriveEvidenceCounts()
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
	if issue := result.EvidenceIntegrityIssue(); issue != "" {
		t.Fatal(issue)
	}
	return result
}

func closeRoleStore(t *testing.T, records record.Store, id, purpose string, points []*record.Record) record.ManagedStoreRef {
	t.Helper()
	store, err := record.CreateManagedStore(records, record.ManagedStoreSpec{Schema: record.ManagedStoreSpecSchema, ID: id, SessionID: "doc-session", Purpose: purpose})
	if err != nil {
		t.Fatal(err)
	}
	for _, point := range points {
		if _, err := store.Save(point); err != nil {
			t.Fatal(err)
		}
	}
	ref, err := store.Close()
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func freshConfirmationPoints(t *testing.T, plan ConfirmationPlan, sources []*record.Record, started time.Time) []*record.Record {
	t.Helper()
	points := make([]*record.Record, len(sources))
	for index, source := range sources {
		result := roleConfirmationClone(t, source)
		result.StartedAt, result.SeedSet = started.Format(time.RFC3339), plan.SeedSet
		binding := ConfirmationPlanBinding(plan, index+1)
		result.Experiment, result.TaskPlan = &binding, plan.Protocol.TaskPlan
		for trial, check := range plan.Protocol.Checks {
			result.Checks[trial].Seed = check.Seed
		}
		result.CapacityPlan = roleConfirmationCapacityPlan(t, result, plan.Candidates[index].Capacity)
		points[index] = roleConfirmationReseal(t, result)
	}
	return points
}

func requirementByID(t *testing.T, candidate Candidate, id string) decision.RequirementResult {
	t.Helper()
	if candidate.Evaluation == nil {
		t.Fatal("candidate has no evaluation")
	}
	for _, item := range candidate.Evaluation.Requirements {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("requirement %s missing", id)
	return decision.RequirementResult{}
}

func documentAnswers(t *testing.T, plan contextquality.Plan) []contextquality.Observation {
	t.Helper()
	observations := make([]contextquality.Observation, 0, len(plan.Cells))
	for index, cell := range plan.Cells {
		task, err := contextquality.Generate(plan, index+1)
		if err != nil {
			t.Fatal(err)
		}
		observations = append(observations, contextquality.Observation{
			CellID: cell.ID, PayloadSHA256: cell.PayloadSHA256, PromptSHA256: cell.PromptSHA256,
			Disposition: contextquality.Answered, Answer: documentAnswer(t, task),
		})
	}
	return observations
}

// documentAnswer reads only the text the model would see. It is the same
// visible-text oracle the context pack tests use, so a passing phase here is
// one the production verifier accepts.
func documentAnswer(t *testing.T, task contextquality.Task) string {
	t.Helper()
	var entity, value string
	switch task.Cell.Family {
	case contextquality.IndirectRetrieval:
		site := documentMatch(t, `At station (\S+), which`, task.Prompt)[1]
		person := documentMatch(t, `(?m)^PERSON (\S+) AT `+regexp.QuoteMeta(site)+` JOB clock_sync CODE (\S+) *$`, task.Payload)
		entity, value = person[1], person[2]
	case contextquality.DistantDependency:
		entity = documentMatch(t, `effective route code for (\S+)\.`, task.Prompt)[1]
		route := documentMatch(t, `(?m)^REQUEST `+regexp.QuoteMeta(entity)+` ROUTE (\S+) *$`, task.Payload)[1]
		kind := "OVERRIDE"
		if strings.Contains(task.Payload, "RULE DEFAULT remains authoritative") {
			kind = "DEFAULT"
		}
		value = documentMatch(t, `(?m)^`+kind+` `+regexp.QuoteMeta(route)+` CODE (\S+) *$`, task.Payload)[1]
	case contextquality.InstructionRetention:
		entity = documentMatch(t, `approval rule to (\S+) and retain`, task.Prompt)[1]
		pattern := `(?m)^TICKET ` + regexp.QuoteMeta(entity) + ` CODE (\S+) APPROVED yes *$`
		if strings.Contains(task.Payload, "RULE BASELINE remains authoritative") {
			pattern = `(?m)^BASELINE ` + regexp.QuoteMeta(entity) + ` CODE (\S+) *$`
		}
		value = documentMatch(t, pattern, task.Payload)[1]
	default:
		t.Fatal("unknown context family")
	}
	answer := map[string]string{"entity": entity, "value": value}
	if task.Cell.Family == contextquality.InstructionRetention {
		answer["action"] = "retain_audit"
		if strings.Contains(task.Payload, "action must be retain_backup") {
			answer["action"] = "retain_backup"
		}
	}
	data, err := json.Marshal(answer)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func documentMatch(t *testing.T, pattern, text string) []string {
	t.Helper()
	matches := regexp.MustCompile(pattern).FindAllStringSubmatch(text, -1)
	if len(matches) != 1 {
		t.Fatalf("expected one visible match for %q, got %d", pattern, len(matches))
	}
	return matches[0]
}
