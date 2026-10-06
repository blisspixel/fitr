package main

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/automation"
	"github.com/blisspixel/fitr/internal/autoruntime"
)

func TestAutoContextScheduleRefusesMLXBeforeThePlanIsSealed(t *testing.T) {
	plan := automation.Plan{Runtime: autoruntime.Spec{NumCtx: 4096}, SeedSet: "auto-exploration"}
	err := bindAutoContextSchedule(&plan, []int{2048, 4096}, true)
	if err == nil || !strings.Contains(err.Error(), "MLX") || plan.ContextPlanSHA256 != "" {
		t.Fatalf("MLX schedule sealed a digest anyway: %v %q", err, plan.ContextPlanSHA256)
	}
	if err := bindAutoContextSchedule(&plan, nil, true); err != nil {
		t.Fatal(err)
	}
	if err := bindAutoContextSchedule(&plan, []int{2048, 4096}, false); err != nil || plan.ContextPlanSHA256 == "" {
		t.Fatalf("schedule was not sealed: %v %q", err, plan.ContextPlanSHA256)
	}
}
