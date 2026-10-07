package fitting

import (
	"math"
	"strings"
	"testing"
)

func TestDraftRejectsOverflowingContextReserves(t *testing.T) {
	req := ceilingRequest(t)
	req.Workload = Workload{
		ContextMeaning: ContextUsableInput, DesiredContextTokens: 1000,
		ReservedSystemTokens: math.MaxInt, ReservedOutputTokens: math.MaxInt, ReservedReasoningTokens: math.MaxInt,
	}
	if plan, err := Draft(req, testTasks(t)); err == nil {
		t.Fatalf("overflowing reserves accepted, blocked=%t", plan.Blocked)
	}
}

func workloadRequest(t *testing.T) Request {
	t.Helper()
	req := ceilingRequest(t)
	req.Workload.DesiredContextTokens = 250000
	req.Workload.ContextMeaning = ContextTotalWindow
	return req
}

func TestDesiredContextIsNotMetByASmallerWindow(t *testing.T) {
	if SatisfiesContextGoal(32768, 250000) {
		t.Fatal("32768 satisfied a 250000 token goal")
	}
	plan, err := Draft(workloadRequest(t), testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Blocked || plan.ContextTokens != 32768 || plan.Workload.DesiredContextTokens != 250000 {
		t.Fatalf("blocked=%t ctx=%d desired=%d", plan.Blocked, plan.ContextTokens, plan.Workload.DesiredContextTokens)
	}
	text := strings.Join(plan.Lines(PhaseBlocked), "\n")
	for _, want := range []string{"desired context unmet", "does not satisfy that workload", "not lowered", "250000", "32768"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "workload satisfied") || strings.Contains(text, "this device fits") {
		t.Fatal(text)
	}
}

func TestHarnessFloorRejects59392(t *testing.T) {
	ok, why := EligibleContext(59392, HermesOllamaMinContext)
	if ok || !strings.Contains(why, "59392") || !strings.Contains(why, "64000") || !strings.Contains(why, "not an eligible recommendation") {
		t.Fatal(ok, why)
	}
	req := ceilingRequest(t)
	req.ContextTokens = 59392
	req.Workload.Harness = "hermes"
	plan, err := Draft(req, testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Blocked || plan.Workload.HarnessMinContextTokens != 64000 {
		t.Fatalf("blocked=%t floor=%d reason=%s", plan.Blocked, plan.Workload.HarnessMinContextTokens, plan.BlockReason)
	}
	if !strings.Contains(plan.BlockReason, "not an eligible recommendation") {
		t.Fatal(plan.BlockReason)
	}
}

func TestHarnessFloorDoesNotSatisfyDesiredContext(t *testing.T) {
	req := workloadRequest(t)
	req.ContextTokens = 64000
	req.Workload.Harness = "hermes"
	plan, err := Draft(req, testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(plan.Lines(PhaseBlocked), "\n")
	if !plan.Blocked || !strings.Contains(text, "Meeting the harness floor does not establish the desired context.") {
		t.Fatalf("blocked=%t\n%s", plan.Blocked, text)
	}
	if SatisfiesContextGoal(64000, 250000) {
		t.Fatal("harness floor was treated as the desired workload")
	}
}

func TestWeightQuantIsNotKVCache(t *testing.T) {
	req := ceilingRequest(t)
	req.KVExplicit = true
	req.KVCacheType = "q8_0"
	req.FlashAttention = true
	req.Workload.WeightQuant = "Q4_K_M"
	req.Workload.WeightPreference = "Q4-or-better"
	plan, err := Draft(req, testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	if plan.KVCacheType != "q8_0" || plan.Workload.WeightQuant != "Q4_K_M" || plan.Workload.WeightQuant == plan.KVCacheType {
		t.Fatalf("quant=%s kv=%s", plan.Workload.WeightQuant, plan.KVCacheType)
	}
	text := strings.Join(plan.Lines(PhasePreviewed), "\n")
	if !plan.FlashAttention {
		t.Fatal("quantized KV was sealed without the plan requesting flash attention")
	}
	for _, want := range []string{"model weights", "cache precision", "They are separate.", "does not halve total model and runtime memory", "not a universal quality threshold", "does not set or prove the server"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q\n%s", want, text)
		}
	}
}

func TestClientEnvDoesNotCertifyKV(t *testing.T) {
	verified, text := KVVerification("q8_0", "", "q8_0")
	if verified || !strings.Contains(text, "does not prove") {
		t.Fatal(verified, text)
	}
	req := ceilingRequest(t)
	req.KVExplicit = true
	req.KVCacheType = "q8_0"
	req.FlashAttention = true
	req.KVClientEnv = "q8_0"
	plan, err := Draft(req, testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Workload.KVServerVerified || !strings.Contains(plan.Workload.KVVerification, "does not prove") {
		t.Fatalf("%+v", plan.Workload)
	}
	plan.Workload.KVServerVerified = true
	plan.Workload.KVVerification = "client said q8_0"
	if err := plan.seal(); err == nil {
		t.Fatal("a client KV claim was sealed")
	}
}

func TestShortPromptIsNotPopulatedContext(t *testing.T) {
	ok, text := PopulatedQualification(262144, 512)
	if ok || !strings.Contains(text, "not populated-context qualification") {
		t.Fatal(ok, text)
	}
	req := ceilingRequest(t)
	req.ContextTokens = 262144
	req.Workload.RequirePopulatedContext = true
	plan, err := Draft(req, testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Blocked || plan.Workload.PopulatedContextQualified {
		t.Fatalf("blocked=%t qualified=%t", plan.Blocked, plan.Workload.PopulatedContextQualified)
	}
	if !strings.Contains(plan.BlockReason, "not populated-context qualification") {
		t.Fatal(plan.BlockReason)
	}
}

func TestFasterFailureCannotWin(t *testing.T) {
	winner, ok, reason := SelectEligible([]CandidateObservation{
		{Model: "devstral:24b", DecodePerSecond: 54.86, FailedMandatory: []string{"tool_restraint"}},
		{Model: "qwen3:30b", DecodePerSecond: 45.84},
	})
	if !ok || winner != "qwen3:30b" || strings.Contains(reason, "faster") && strings.Contains(reason, "won") {
		t.Fatal(winner, ok, reason)
	}
	if _, ok, reason = SelectEligible([]CandidateObservation{
		{Model: "devstral:24b", DecodePerSecond: 54.86, FailedMandatory: []string{"tool_restraint"}},
		{Model: "qwen3:30b", DecodePerSecond: 45.84, FailedMandatory: []string{"coding"}},
	}); ok || !strings.Contains(reason, "no eligible configuration") {
		t.Fatal(ok, reason)
	}
}

func TestAllowanceDoesNotGrow(t *testing.T) {
	approved, err := ParseGiB(20, true)
	if err != nil {
		t.Fatal(err)
	}
	free, err := ParseGiB(32, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ResolveAllowance(CapacityCeiling, approved, free)
	if err != nil || got.ApprovedBytes != approved || got.RemainderBytes != approved {
		t.Fatalf("%+v %v", got, err)
	}
	short, err := ParseGiB(16, true)
	if err != nil {
		t.Fatal(err)
	}
	shrunk, err := ResolveAllowance(CapacityCeiling, approved, short)
	if err != nil || shrunk.ApprovedBytes != approved || shrunk.RemainderBytes != short {
		t.Fatalf("%+v %v", shrunk, err)
	}
}

func TestSearchDoesNotLowerTheGoal(t *testing.T) {
	workload := Workload{DesiredContextTokens: 250000, ContextMeaning: ContextTotalWindow, HarnessMinContextTokens: 64000}
	items := ReviewSearch(workload, []SearchOption{{ContextTokens: 32768}, {ContextTokens: 59392}, {ContextTokens: 65536}})
	if AdoptedContext(32768, items) != 32768 {
		t.Fatal("search replaced the requested window")
	}
	for _, item := range items {
		if item.Eligible || item.Option.ContextTokens >= 250000 {
			t.Fatalf("row became eligible: %+v", item)
		}
		if item.Option.ContextTokens == 59392 && !strings.Contains(item.Reason, "not an eligible recommendation") {
			t.Fatal(item.Reason)
		}
	}
}

func TestResumeKeepsTheGoalAndSkipsCompletedWork(t *testing.T) {
	req := workloadRequest(t)
	req.Scope = ScopeScreen
	plan, err := Draft(req, testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	store := Store{Results: root}
	session := Session{Schema: SessionSchema, Phase: PhaseMeasuring, Plan: plan}
	session.Points = []Point{{Model: plan.Candidates[0], RunID: "run-00001", EvidenceSHA256: "sha256:" + strings.Repeat("a", 64)}}
	if err := store.Create(session); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	action, err := loaded.ResumeAction()
	if err != nil || action != "measure" || !loaded.Completed(plan.Candidates[0]) || loaded.ConfirmationSeed != "" {
		t.Fatal(action, err, loaded.ConfirmationSeed)
	}
	if loaded.Plan.Workload.DesiredContextTokens != 250000 || loaded.Plan.ContextTokens != 32768 {
		t.Fatalf("goal changed: %+v", loaded.Plan.Workload)
	}
	if err := FreshConfirmation(loaded.Plan, loaded.Plan.SeedSet); err == nil {
		t.Fatal("resume minted confirmation")
	}
	decision, err := ConsiderHistory(loaded.Plan, loaded.Plan.Candidates[0], loaded.Plan.SeedSet, loaded.Plan.ContextTokens, loaded.Plan.CapacityKind, loaded.Plan.CapacityBytes, loaded.Plan.Endpoint)
	if err != nil || !decision.Reuse || decision.Fresh || !strings.Contains(decision.Reason, "does not satisfy") {
		t.Fatal(decision, err)
	}
}

func TestQwenCardIsNotLocalProof(t *testing.T) {
	req := ceilingRequest(t)
	req.Scope = ScopeScreen
	req.Candidates = []string{"qwen3.6:27b"}
	req.ModelCard = "qwen3.6-27b"
	plan, err := Draft(req, testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(plan.Lines(PhasePreviewed), "\n")
	for _, want := range []string{"262144", "131072", "not a measurement", "vendor recommendation"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q\n%s", want, text)
		}
	}
}

func TestPresetDoesNotHideTheContextGoal(t *testing.T) {
	req := ceilingRequest(t)
	req.Outcomes = nil
	req.Workload.Preset = "repository-planning"
	plan, err := Draft(req, testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(plan.Lines(PhaseBlocked), "\n")
	if !plan.Blocked || !strings.Contains(text, "mandatory:") || !strings.Contains(text, "does not substitute 32768") {
		t.Fatalf("blocked=%t\n%s", plan.Blocked, text)
	}
	if strings.Contains(text, "workload satisfied") {
		t.Fatal(text)
	}
}

func TestWeightMinimumStopsInsteadOfLowering(t *testing.T) {
	req := ceilingRequest(t)
	req.Workload.WeightQuant = "Q3_K_M"
	req.Workload.WeightMinimum = "Q4"
	plan, err := Draft(req, testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Blocked || !strings.Contains(plan.BlockReason, "no eligible configuration") {
		t.Fatal(plan.Blocked, plan.BlockReason)
	}
}

func TestUsableInputPlusReservesExceedsWindow(t *testing.T) {
	req := ceilingRequest(t)
	req.ContextTokens = 8192
	req.Workload.ContextMeaning = ContextUsableInput
	req.Workload.DesiredContextTokens = 8000
	req.Workload.ReservedOutputTokens = 1024
	plan, err := Draft(req, testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Blocked || !strings.Contains(plan.BlockReason, "desired input plus reserves exceed") {
		t.Fatal(plan.Blocked, plan.BlockReason)
	}
}
