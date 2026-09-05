package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/capacity"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"
)

func TestRoleLifecycleCommandsRejectInvalidInputBeforeBackend(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	t.Setenv("FITR_BACKEND", "invalid-must-not-be-used")
	for _, args := range [][]string{
		{"confirm"}, {"adopt", "coding"}, {"status", "coding", "extra"},
		{"rollback"}, {"status", "coding", "--backend", "auto"},
		{"confirm", "coding", "--ctx", "4096"}, {"confirm", "coding", "--display", "unknown"},
		{"confirm", "saved.json", "--backend", "ollama"},
	} {
		_, code := captureTopStderr(t, func() int { return cmdRole(context.Background(), args) })
		if code != exitUsage {
			t.Fatalf("%v returned %d", args, code)
		}
	}
	for _, action := range []string{"confirm", "adopt", "rollback", "status"} {
		_, code := captureTopStderr(t, func() int { return cmdRole(context.Background(), []string{action, "--help"}) })
		if code != exitOK {
			t.Fatalf("%s help returned %d", action, code)
		}
	}
}

func TestRoleConfirmationCLIAdoptsOnlyCurrentConfirmedEvidence(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	path, bundle := cliRoleConfirmationFixture(t)
	for _, mode := range []string{"json", "plain", "none"} {
		output, code := captureTopStdout(t, func() int {
			return cmdRole(context.Background(), []string{"confirm", path, "--display", mode})
		})
		if code != exitOK || (mode == "plain" && !strings.Contains(output, "fitr role adopt")) {
			t.Fatalf("saved confirmation %s returned %d: %s", mode, code, output)
		}
	}
	_, code := captureTopStdout(t, func() int {
		return cmdRole(context.Background(), []string{"adopt", "coding", path, "--display", "json"})
	})
	if code != exitOK {
		t.Fatalf("valid confirmation adoption returned %d", code)
	}
	output, code := captureTopStdout(t, func() int {
		return cmdRole(context.Background(), []string{"status", "coding"})
	})
	if code != exitOK || !strings.Contains(output, "fast") || !strings.Contains(output, "QUALIFIED") {
		t.Fatalf("selection lost its model or qualification: %d %s", code, output)
	}
	store := role.Store{Dir: filepath.Join(resultsDir(), ".roles")}
	spec := bundle.Plan.Spec
	spec.Description = "edited after confirmation"
	if _, err := store.Define(spec); err != nil {
		t.Fatal(err)
	}
	output, code = captureTopStdout(t, func() int {
		return cmdRole(context.Background(), []string{"status", "coding", "--display", "json"})
	})
	if code != exitUnresolved || !strings.Contains(output, `"state": "stale"`) {
		t.Fatalf("role edit retained stale qualification: %d %s", code, output)
	}
}

func cliRoleConfirmationRecord(t *testing.T, model string, speed float64, started time.Time) *record.Record {
	t.Helper()
	result := mockResult(model, speed, 0.1, 100, 0.1, 0, 0, 0, 0)
	result.StartedAt = started.Format(time.RFC3339)
	result.Memory.ResidentGB, result.Memory.PctOnGPU, result.Memory.RequestedCtx = 8, 100, result.ContextSize()
	for round := range result.Repeats {
		for index := range 8 {
			id := fmt.Sprintf("check-%d", index)
			result.Checks = append(result.Checks, eval.CheckOutcome{
				TaskID: id, Family: id, Need: "structured_output", Origin: "builtin",
				Seed: eval.InstanceSeed(result.SeedSet, id, round), Pass: true, Outcome: eval.OutcomePass,
			})
		}
	}
	if err := prepareMockEvidence(result); err != nil {
		t.Fatal(err)
	}
	return result
}

func cliRoleConfirmationFixture(t *testing.T) (string, role.ConfirmationBundle) {
	t.Helper()
	now := time.Now()
	store := role.Store{Dir: filepath.Join(resultsDir(), ".roles")}
	records := record.Store{Dir: resultsDir()}
	spec := initialRoleSpec("coding", "structured_output", 0.5, 22, eval.NumCtx, 30)
	minimum := 1.0
	spec.Decision.Requirements = append(spec.Decision.Requirements, decision.Requirement{
		ID: "speed", Performance: &decision.PerformanceRequirement{Metric: decision.MetricDecodeTPS, AtLeast: &minimum},
	})
	spec.Preferences = []role.Preference{{Requirement: "speed", Weight: 1, Worst: 0, Best: 100}}
	library, err := store.Define(spec)
	if err != nil {
		t.Fatal(err)
	}
	points := []*record.Record{cliRoleConfirmationRecord(t, "fast", 80, now.Add(-time.Hour)), cliRoleConfirmationRecord(t, "slow", 20, now.Add(-time.Hour))}
	library = cliRoleSaveSources(t, store, records, library, points)
	plan, err := planRoleConfirmation(library, records, now.Add(-20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	life, err := store.LoadLifecycle(library.Name)
	if err != nil {
		t.Fatal(err)
	}
	life, err = store.IssueConfirmation(plan, life.Digest, now.Add(-20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	life, err = store.BeginConfirmation(library.Name, plan.PlanSHA256, life.Digest, now.Add(-10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for index, point := range points {
		cliRoleFreshPoint(t, point, plan, index, now.Add(-5*time.Second))
		if _, err := records.Save(point); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := role.NewConfirmationBundle(plan, points, now)
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.SaveConfirmationBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishConfirmation(library.Name, plan.PlanSHA256, "completed", &bundle, records, life.Digest, now); err != nil {
		t.Fatal(err)
	}
	return path, bundle
}

func cliRoleSaveSources(t *testing.T, store role.Store, records record.Store, library role.Library, points []*record.Record) role.Library {
	t.Helper()
	for _, point := range points {
		if _, err := records.Save(point); err != nil {
			t.Fatal(err)
		}
		attachment, err := role.AttachRecord(records.CanonicalPath(point.Model), records)
		if err != nil {
			t.Fatal(err)
		}
		library, err = store.Attach(library.Name, attachment)
		if err != nil {
			t.Fatal(err)
		}
	}
	return library
}

func cliRoleFreshPoint(t *testing.T, point *record.Record, plan role.ConfirmationPlan, index int, started time.Time) {
	t.Helper()
	point.StartedAt, point.SeedSet = started.Format(time.RFC3339), plan.SeedSet
	binding := role.ConfirmationPlanBinding(plan, index+1)
	point.Experiment, point.TaskPlan = &binding, plan.Protocol.TaskPlan
	for trial, check := range plan.Protocol.Checks {
		point.Checks[trial].Seed = check.Seed
	}
	control := plan.Candidates[index].Capacity
	policy, err := capacity.BuildPolicy(capacity.PolicyInput{ResourceDomain: control.ResourceDomain, OperatorBudgetBytes: control.OperatorBudgetBytes})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	point.CapacityPlan = &capacity.Plan{Schema: capacity.PlanSchema, Policy: policy, Prediction: capacity.Prediction{
		Schema: capacity.PredictionSchema, CreatedAt: point.StartedAt, ArtifactSHA256: point.Manifest.Model.Value,
		ResourceDomain: control.ResourceDomain, RequestedContext: point.ContextSize(), PlacementAssumption: "fixture",
		State: capacity.PredictionUnavailable, Missing: []string{"architecture"}, Excluded: []string{"runtime buffers"}, PolicySHA256: digest,
	}}
	point.RunID = ""
	if err := completeMockEvidence(point, false); err != nil {
		t.Fatal(err)
	}
}

func TestRoleLifecycleUnselectedStateAndMissingBundles(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	t.Setenv("FITR_BACKEND", "invalid-must-not-be-used")
	_, code := captureTopStdout(t, func() int {
		return cmdRole(context.Background(), []string{"init", "coding", "--quality", "user_tasks", "--memory-gb", "22", "--display", "none"})
	})
	if code != exitOK {
		t.Fatal(code)
	}
	for _, mode := range []string{"plain", "json", "none"} {
		output, code := captureTopStdout(t, func() int {
			return cmdRole(context.Background(), []string{"status", "coding", "--display", mode})
		})
		if code != exitUnresolved || (mode != "none" && !strings.Contains(strings.ToLower(output), "unselected")) {
			t.Fatalf("unselected role became qualified: %d %s", code, output)
		}
	}
	for _, args := range [][]string{
		{"confirm", "coding"}, {"adopt", "coding", "missing.json"},
		{"rollback", "coding"}, {"confirm", "missing.json"}, {"status", "missing"},
	} {
		_, code := captureTopStderr(t, func() int { return cmdRole(context.Background(), args) })
		if code != exitError {
			t.Fatalf("%v returned %d", args, code)
		}
	}
}
