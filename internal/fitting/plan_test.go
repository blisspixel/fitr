package fitting

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/eval"
)

func TestParseGiBRejectsRoundedOverflowAndSubByteCeiling(t *testing.T) {
	for _, value := range []float64{float64(math.MaxInt64) / (1 << 30), 1e-20} {
		if got, err := ParseGiB(value, true); err == nil {
			t.Fatalf("accepted %g GiB as %d bytes", value, got)
		}
	}
}

func TestPlanValidationRejectsResealedInvalidSettings(t *testing.T) {
	plan, err := Draft(ceilingRequest(t), testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Plan){
		func(p *Plan) { p.ContextTokens = -1 },
		func(p *Plan) { p.Repeats = 0 },
		func(p *Plan) { p.CapacityKind = "unknown" },
		func(p *Plan) { p.Role.Decision.Requirements = nil },
		func(p *Plan) { p.Endpoint = "http://user:secret@localhost:11434" },
	} {
		bad := plan
		mutate(&bad)
		if err := bad.seal(); err == nil {
			t.Fatal("valid digest accepted invalid fitting settings")
		}
	}
}

func testTasks(t *testing.T) *eval.Spec {
	t.Helper()
	tasks, err := eval.LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	return tasks
}

func ceilingRequest(t *testing.T) Request {
	t.Helper()
	bytes, err := ParseGiB(20, true)
	if err != nil {
		t.Fatal(err)
	}
	return Request{
		RoleName: "daily", Outcomes: []string{"structured_output"}, Scope: ScopeQualify,
		Candidates: []string{"qwen3:30b"}, ContextTokens: 32768,
		CapacityKind: CapacityCeiling, CapacityBytes: bytes, ResidentLimitBytes: bytes,
		Endpoint: "http://127.0.0.1:11434", EndpointSource: "explicit", Locality: "loopback-unproven",
		BuildVersion: "0.11.1", Now: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC),
	}
}

func TestDraftKeepsTheCeilingOutOfAReserve(t *testing.T) {
	plan, err := Draft(ceilingRequest(t), testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Blocked || plan.CapacityKind != CapacityCeiling || plan.CapacityBytes != plan.ResidentLimitBytes {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.ContextTokens != 32768 || plan.SeedSet != plan.ID || plan.Comparison != ComparisonAssessment {
		t.Fatalf("identity = %+v", plan)
	}
	if plan.Repeats != 3 || !plan.RepeatsDefaulted || plan.KVCacheType != "f16" || plan.KVExplicit || !plan.FlashAttention {
		t.Fatalf("defaults replaced the request: %+v", plan)
	}
	text := strings.Join(plan.Lines(PhasePreviewed), "\n")
	for _, want := range []string{"32768", "20.00 GiB", "absolute ceiling", "not written to OLLAMA_GPU_OVERHEAD", "non-comparative", "not percent complete"} {
		if !strings.Contains(text, want) {
			t.Fatalf("preview missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "8192") {
		t.Fatalf("preview installed the owned default window:\n%s", text)
	}
}

func TestDraftRejectsWritingACeilingIntoTheOwnedReserve(t *testing.T) {
	req := ceilingRequest(t)
	req.OwnedRuntime = true
	req.Candidates = []string{"qwen3:30b", "devstral:24b"}
	if _, err := Draft(req, testTasks(t)); err == nil || !strings.Contains(err.Error(), "OLLAMA_GPU_OVERHEAD") {
		t.Fatal(err)
	}
}

func TestDraftBlocksCodingUntilExecutionExists(t *testing.T) {
	req := ceilingRequest(t)
	req.Outcomes = []string{"coding"}
	plan, err := Draft(req, testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Blocked || !strings.Contains(plan.BlockReason, "independently checked code") || !strings.Contains(plan.BlockReason, "--scope screen") {
		t.Fatal(plan.BlockReason)
	}
}

func TestDraftScreenDoesNotPromiseACoderOrCopyTheRate(t *testing.T) {
	req := ceilingRequest(t)
	req.Scope = ScopeScreen
	req.Outcomes = []string{"coding"}
	rate := 1.0
	req.MinimumRate = &rate
	plan, err := Draft(req, testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Blocked {
		t.Fatal(plan.BlockReason)
	}
	if !strings.Contains(plan.ScreeningNote, "does not qualify a coder") || !strings.Contains(plan.ScreeningNote, "not copied") {
		t.Fatal(plan.ScreeningNote)
	}
	for _, requirement := range plan.Role.Decision.Requirements {
		if requirement.Behavior != nil && requirement.Behavior.MinimumRate != nil {
			t.Fatalf("screening copied the rate onto %s", requirement.Behavior.Need)
		}
	}
}

func TestDraftDoesNotLowerAnImpossibleRate(t *testing.T) {
	req := ceilingRequest(t)
	rate := 1.0
	req.MinimumRate = &rate
	plan, err := Draft(req, testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Blocked || !strings.Contains(plan.BlockReason, "not lowered") {
		t.Fatal(plan.BlockReason)
	}
}

func TestDraftKeepsTwoCandidatesComparativeAndOneUnpadded(t *testing.T) {
	req := ceilingRequest(t)
	req.Candidates = []string{"qwen3:30b", "devstral:24b"}
	plan, err := Draft(req, testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Blocked || plan.Comparison != ComparisonComparative || len(plan.Candidates) != 2 {
		t.Fatalf("%s %v %s", plan.Comparison, plan.Candidates, plan.BlockReason)
	}
}

func TestExplicitF16CanDisableFlashAndQuantizedKVCannot(t *testing.T) {
	req := ceilingRequest(t)
	req.KVExplicit = true
	req.KVCacheType = "f16"
	req.FlashAttention = false
	plan, err := Draft(req, testTasks(t))
	if err != nil || plan.FlashAttention {
		t.Fatal(err, plan.FlashAttention)
	}
	req.KVCacheType = "q8_0"
	if _, err := Draft(req, testTasks(t)); err == nil || !strings.Contains(err.Error(), "flash attention") {
		t.Fatal(err)
	}
}

func TestOwnedReserveUsesTheRequestedContextAndAllowance(t *testing.T) {
	req := ceilingRequest(t)
	req.OwnedRuntime = true
	req.CapacityKind = CapacityReserve
	reserve, err := ParseGiB(4, true)
	if err != nil {
		t.Fatal(err)
	}
	limit, err := ParseGiB(24, true)
	if err != nil {
		t.Fatal(err)
	}
	req.CapacityBytes, req.ResidentLimitBytes = reserve, limit
	req.Candidates = []string{"qwen3:30b", "devstral:24b"}
	req.Endpoint, req.EndpointSource, req.Locality = "owned-process", "owned-runtime", "owned-process"
	plan, err := Draft(req, testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Blocked || plan.ContextTokens != 32768 || plan.CapacityBytes != reserve || plan.MaxRequests != defaultMaxRequests {
		t.Fatalf("blocked=%t reason=%s ctx=%d reserve=%d requests=%d", plan.Blocked, plan.BlockReason, plan.ContextTokens, plan.CapacityBytes, plan.MaxRequests)
	}
	text := strings.Join(plan.Lines(PhasePreviewed), "\n")
	if !strings.Contains(text, "600 requests") || !strings.Contains(text, "not percent complete") {
		t.Fatal(text)
	}
}
