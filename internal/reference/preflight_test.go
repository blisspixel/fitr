package reference

import (
	"math"
	"strings"
	"testing"
)

func TestBudgetPreflightRefusesBeforeAVerifier(t *testing.T) {
	plan := sealedPlan(t, "2024-01-01", fakeSHA("artifact"))
	plan.WallBudgetSeconds = 1
	plan.PlanSHA256 = ""
	plan.ManifestSHA256 = ""
	plan.HarnessBindingSHA256 = ""
	sealed, err := NewPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	err = Preflight(sealed, ready(sealed))
	if err == nil || !strings.Contains(err.Error(), "wall budget") {
		t.Fatalf("tight budget error = %v", err)
	}
}

func TestDiskAndContextAndLoadRefuse(t *testing.T) {
	plan := sealedPlan(t, "2024-01-01", fakeSHA("artifact"))
	overDisk := plan
	overDisk.RequiredDiskBytes = plan.DiskBudgetBytes + 1
	overDisk.PlanSHA256 = ""
	overDisk.ManifestSHA256 = ""
	overDisk.HarnessBindingSHA256 = ""
	sealed, err := NewPlan(overDisk)
	if err != nil {
		t.Fatal(err)
	}
	if err := Preflight(sealed, ready(sealed)); err == nil || !strings.Contains(err.Error(), "disk budget") {
		t.Fatalf("disk error = %v", err)
	}
	low := ready(plan)
	low.ServedContext = plan.ContextCap - 1
	if err := Preflight(plan, low); err == nil || !strings.Contains(err.Error(), "served context") {
		t.Fatalf("context error = %v", err)
	}
	busy := ready(plan)
	busy.GPUBusyDeclared = true
	if err := Preflight(plan, busy); err == nil || !strings.Contains(err.Error(), "GPU is busy") {
		t.Fatalf("gpu error = %v", err)
	}
	resident := ready(plan)
	resident.OtherModelDeclared = true
	if err := Preflight(plan, resident); err == nil || !strings.Contains(err.Error(), "another model") {
		t.Fatalf("residency error = %v", err)
	}
}

func TestUnavailableProbeRefuses(t *testing.T) {
	plan := sealedPlan(t, "2024-01-01", fakeSHA("artifact"))
	conditions := ready(plan)
	conditions.WorkerProbeAvailable = false
	conditions.WorkerProbeReason = "isolated execution needs bubblewrap on Linux"
	err := Preflight(plan, conditions)
	if err == nil || !strings.Contains(err.Error(), "bubblewrap") {
		t.Fatalf("probe error = %v", err)
	}
}

func TestTokenProductDoesNotWrapIntoTheBudget(t *testing.T) {
	if _, ok := mulInt64(math.MaxInt64, 2); ok {
		t.Fatal("an overflowing product became a number")
	}
	plan := sealedPlan(t, "2024-01-01", fakeSHA("artifact"))
	draft := plan
	draft.PromptTokens = maxTokens
	draft.K = maxK
	draft.PrefillTokensPerSecond = 1
	draft.DecodeTokensPerSecond = 1
	draft.CompletionTokens = maxTokens
	draft.WallBudgetSeconds = maxWallBudget
	draft.PlanSHA256 = ""
	draft.ManifestSHA256 = ""
	draft.HarnessBindingSHA256 = ""
	sealed, err := NewPlan(draft)
	if err != nil {
		t.Fatal(err)
	}
	if err := Preflight(sealed, ready(sealed)); err == nil {
		t.Fatal("a multi-hour estimate fit inside the wall budget")
	}
}
