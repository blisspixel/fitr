package automation

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/autoruntime"
	"github.com/blisspixel/fitr/internal/contextquality"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"
)

func documentFloorRole(floor int) role.Spec {
	spec := feasibleRole()
	spec.Decision.Requirements[1].Context.MinimumUsableContextBytes = &floor
	return spec
}

func TestFeasibilityScheduleFundsContextOnlyWhenAFloorExists(t *testing.T) {
	tasks := feasibilityTasks(t)
	if err := ValidateFeasibilitySchedule(feasibleRole(), tasks, 3, 4096, FeasibilitySchedule{
		Tiers: []int{2048, 4096}, Candidates: 2,
	}); err == nil || !strings.Contains(err.Error(), "context tiers require a usable-context floor") {
		t.Fatalf("tiers without a floor: %v", err)
	}
	if err := ValidateFeasibilitySchedule(feasibleRole(), tasks, 3, 4096, FeasibilitySchedule{Limits: Limits{MaxRequests: 1}}); err != nil {
		t.Fatalf("battery-only schedule consulted context funding: %v", err)
	}

	spec := documentFloorRole(4096)
	if err := ValidateFeasibilitySchedule(spec, tasks, 3, 4096, FeasibilitySchedule{}); err == nil || !strings.Contains(err.Error(), "usable-context floor requires declared context tiers") {
		t.Fatalf("missing tiers: %v", err)
	}
	below := FeasibilitySchedule{Tiers: []int{2048, 4096}, Candidates: 2}
	if err := ValidateFeasibilitySchedule(documentFloorRole(8192), tasks, 3, 4096, below); err == nil || !strings.Contains(err.Error(), "largest context tier is below the usable-context floor") {
		t.Fatalf("tier below the floor: %v", err)
	}
	starved := FeasibilitySchedule{Tiers: []int{2048, 4096}, Candidates: 2, Limits: Limits{MaxRequests: 1, MaxRequestedOutputTokens: 1}}
	if err := ValidateFeasibilitySchedule(spec, tasks, 3, 4096, starved); err == nil || !strings.Contains(err.Error(), "auto limits must fund the battery and context schedule for exploration and confirmation") {
		t.Fatalf("unfunded context schedule: %v", err)
	}

	requests, tokens := fundedContextAllowance(t, tasks, []int{2048, 4096})
	funded := FeasibilitySchedule{Tiers: []int{2048, 4096}, Candidates: 2, Limits: Limits{MaxRequests: requests, MaxRequestedOutputTokens: tokens}}
	if err := ValidateFeasibilitySchedule(spec, tasks, 3, 4096, funded); err != nil {
		t.Fatal(err)
	}
	funded.Limits.MaxRequests--
	if err := ValidateFeasibilitySchedule(spec, tasks, 3, 4096, funded); err == nil {
		t.Fatal("one request short of both phases was accepted")
	}
}

func fundedContextAllowance(t *testing.T, tasks *eval.Spec, tiers []int) (int64, int64) {
	t.Helper()
	envelope, err := eval.PlanRequestEnvelope(tasks, eval.RequestEnvelopeOptions{
		Backend: "ollama", Level: "full", Repeats: 3, CheckRepeats: 3, ContextProbe: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	requests, tokens, err := ContextPointBudget(tiers)
	if err != nil {
		t.Fatal(err)
	}
	return 4 * (envelope.MaxRequests + requests), 4 * (envelope.MaxRequestedOutputTokens + tokens)
}

func TestContextScheduleIsSealedOnceFromTheSessionSeed(t *testing.T) {
	tiers := []int{2048, 4096}
	plan := documentSessionPlan(t, tiers, 2048)
	seed, err := record.ContextTaskSeedSet(plan.SeedSet)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := contextquality.NewPolicy(plan.Runtime.NumCtx, tiers)
	if err != nil {
		t.Fatal(err)
	}
	built, err := contextquality.NewPlan(policy, seed)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := built.Digest()
	if err != nil || digest != plan.ContextPlanSHA256 || plan.ContextCells != len(built.Cells) {
		t.Fatalf("sealed context plan = %s (%d cells)", plan.ContextPlanSHA256, plan.ContextCells)
	}
	again := documentSessionPlan(t, tiers, 2048)
	again.SeedSet = plan.SeedSet
	again.SHA256 = ""
	if err := again.BindContextSchedule(tiers); err != nil {
		t.Fatal(err)
	}
	if again.ContextPlanSHA256 != plan.ContextPlanSHA256 {
		t.Fatal("candidates would not share the exploration context plan")
	}
	again.SeedSet = plan.SeedSet + "-other"
	again.ContextPlanSHA256 = ""
	if err := again.BindContextSchedule(tiers); err != nil {
		t.Fatal(err)
	}
	if again.ContextPlanSHA256 == plan.ContextPlanSHA256 {
		t.Fatal("a different seed reused the exploration context plan")
	}
}

func TestBatteryOnlyPlanAndEventsOmitTheContextSchedule(t *testing.T) {
	plan := sessionPlan(t)
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"context_policy", "context_plan_sha256", "context_cells", "context_evidence_sha256"} {
		if bytes.Contains(data, []byte(key)) {
			t.Fatalf("battery-only plan carries %q", key)
		}
	}
	confirmation := syntheticConfirmation(t, plan)
	encoded, err := json.Marshal(confirmation)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"context_policy", "context_plan_sha256", "context_cells", "context_evidence_sha256"} {
		if bytes.Contains(encoded, []byte(key)) {
			t.Fatalf("battery-only confirmation carries %q", key)
		}
	}
	event, err := json.Marshal(sessionPointEvents("exploration", 1)[2])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(event, []byte("context_evidence_sha256")) {
		t.Fatal("battery-only completion carries context evidence")
	}
}

func TestCompletionRequiresContextEvidenceOnlyForASealedSchedule(t *testing.T) {
	at := sessionTime().Add(time.Second).Format(time.RFC3339Nano)
	battery := sessionPlan(t)
	state := State{Phase: "exploration", LastObservedAt: sessionTime(), ActivePoint: 1, ActiveRunID: "test-run", PointRequests: 1}
	plain := Event{Action: "point_completed", Phase: "exploration", Point: 1, RunID: "test-run", EvidenceSHA256: sessionHash("evidence"), At: at}
	if err := state.apply(battery, plain); err != nil {
		t.Fatalf("battery completion: %v", err)
	}
	state.ActivePoint, state.ActiveRunID, state.PointRequests = 1, "test-run", 1
	withContext := plain
	withContext.ContextEvidenceSHA256 = sessionHash("context")
	if err := state.apply(battery, withContext); err == nil || !strings.Contains(err.Error(), "context evidence the plan did not seal") {
		t.Fatalf("unexpected context evidence: %v", err)
	}

	scheduled := documentSessionPlan(t, []int{2048, 4096}, 2048)
	state = State{Phase: "exploration", LastObservedAt: sessionTime(), ActivePoint: 1, ActiveRunID: "test-run", PointRequests: 1}
	if err := state.apply(scheduled, plain); err == nil || !strings.Contains(err.Error(), "missing the sealed context evidence") {
		t.Fatalf("missing context evidence: %v", err)
	}
	withContext.At = at
	if err := state.apply(scheduled, withContext); err != nil {
		t.Fatalf("paired completion: %v", err)
	}
}

func TestConfirmationCannotReuseTheExplorationContextPlan(t *testing.T) {
	plan := documentSessionPlan(t, []int{2048, 4096}, 2048)
	completed := []Event{{ContextEvidenceSHA256: sessionHash("first")}, {ContextEvidenceSHA256: sessionHash("second")}}
	var confirmation role.ConfirmationPlan
	confirmation.Candidates = make([]role.ConfirmationCandidate, len(completed))
	if err := plan.matchesConfirmationContext(confirmation, completed); err == nil {
		t.Fatal("confirmation without a context schedule matched a sealed one")
	}
	confirmation.ContextPolicy = plan.ContextPolicy
	confirmation.ContextPlanSHA256 = plan.ContextPlanSHA256
	confirmation.ContextCells = plan.ContextCells
	for index := range confirmation.Candidates {
		confirmation.Candidates[index].ContextEvidenceSHA256 = completed[index].ContextEvidenceSHA256
	}
	if err := plan.matchesConfirmationContext(confirmation, completed); err == nil || !strings.Contains(err.Error(), "reused the exploration plan") {
		t.Fatalf("reused exploration plan: %v", err)
	}
	confirmation.ContextPlanSHA256 = sessionHash("fresh-context-plan")
	if err := plan.matchesConfirmationContext(confirmation, completed); err != nil {
		t.Fatal(err)
	}
	confirmation.Candidates[0].ContextEvidenceSHA256 = sessionHash("replaced")
	if err := plan.matchesConfirmationContext(confirmation, completed); err == nil || !strings.Contains(err.Error(), "replaced exploration context evidence") {
		t.Fatalf("replaced evidence: %v", err)
	}
}

func documentSessionPlan(t *testing.T, tiers []int, floor int) Plan {
	t.Helper()
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	spec := documentFloorRole(floor)
	revision, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	requests, tokens, err := ContextPointBudget(tiers)
	if err != nil {
		t.Fatal(err)
	}
	pointRequests, pointTokens := int64(4)+requests, int64(40)+tokens
	plan := Plan{ID: id, Mode: "establish", Adoption: "manual", Spec: spec, RoleRevision: revision,
		LifecycleSHA256: sessionHash("lifecycle"), Runtime: autoruntime.Spec{Schema: autoruntime.SpecSchema,
			Executable: filepath.Join(directory, "ollama"), ModelStore: filepath.Join(directory, "models"),
			ExecutableSHA256: sessionHash("runtime"), LibrariesSHA256: sessionHash("libraries"), RuntimeVersion: "0.17.0", NumCtx: 4096, KVCacheType: "f16"},
		SoftwareSHA256: sessionHash("software"), TaskSetSHA256: sessionHash("tasks"), SpecSHA256: sessionHash("spec"),
		Profile: "test-profile", DeviceSHA256: sessionHash("device"), Repeats: 3, SeedSet: "auto-exploration", EnvelopeSHA256: sessionHash("envelope"),
		PointRequests: pointRequests, PointRequestedOutputTokens: pointTokens, Limits: Limits{MaxRequests: 4 * pointRequests, MaxRequestedOutputTokens: 4 * pointTokens,
			MaxPoints: 4, WallSeconds: 3600, ConfirmationWallSeconds: 1200}}
	plan.Provenance = record.RunProvenance{TaskSetSHA256: plan.TaskSetSHA256, SpecSHA256: plan.SpecSHA256,
		ProfileSHA256: sessionHash("profile"), ScoringPolicySHA256: sessionHash("scoring"), FitrVersion: "test",
		SoftwareBuildSHA256: plan.SoftwareSHA256, BackendProtocol: record.BackendProtocolOllama}
	for _, name := range []string{"first", "second"} {
		plan.Candidates = append(plan.Candidates, Candidate{ID: name, Model: name + ":latest", ArtifactDigest: sessionHash(name), ModelConfigurationSHA256: sessionHash(name + "-configuration")})
	}
	if err := plan.BindContextSchedule(tiers); err != nil {
		t.Fatal(err)
	}
	if err := plan.Seal(sessionTime()); err != nil {
		t.Fatal(err)
	}
	return plan
}
