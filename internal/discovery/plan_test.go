package discovery

import "testing"

func TestPlanPinsHarnessBeforeMeasurement(t *testing.T) {
	plan := Plan(Idea{ID: "idea", Model: "model", Harness: "pi"})
	want := []string{"resolve", "fit", "requirements", "harness", "measure", "confirm", "review"}
	if len(plan.Steps) != len(want) {
		t.Fatalf("unexpected steps: %+v", plan.Steps)
	}
	for i, code := range want {
		if plan.Steps[i].Code != code {
			t.Fatalf("step %d = %s, want %s", i, plan.Steps[i].Code, code)
		}
	}
	plan = Plan(Idea{ID: "unresolved"})
	for _, step := range plan.Steps {
		if len(step.Argv) != 0 || step.Code == "fit" || step.Code == "harness" {
			t.Fatalf("unresolved idea received executable recipe: %+v", step)
		}
	}
}
