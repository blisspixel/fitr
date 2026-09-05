package role

import (
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/record"
)

func TestConfirmationAliasSpellingPreservesRuntimeIdentity(t *testing.T) {
	plan, points, _ := roleConfirmationFixture(t)
	expected := plan.Candidates[0].Model
	actual := expected
	actual.Requested = "friendly-alias"
	if !sameConfirmationModel(actual, expected) {
		t.Fatal("request spelling changed the runtime-bound subject")
	}
	mutations := map[string]func(*record.ModelIdentity){
		"empty requested": func(identity *record.ModelIdentity) { identity.Requested = "" },
		"resolved":        func(identity *record.ModelIdentity) { identity.Resolved += "-other" },
		"backend":         func(identity *record.ModelIdentity) { identity.Backend += "-other" },
		"runtime":         func(identity *record.ModelIdentity) { identity.Runtime += "-other" },
		"artifact":        func(identity *record.ModelIdentity) { identity.Value = points[1].Manifest.Model.Value },
		"binding":         func(identity *record.ModelIdentity) { identity.Binding = record.IdentityBindingObserved },
		"size":            func(identity *record.ModelIdentity) { identity.SizeBytes++ },
		"addressing":      func(identity *record.ModelIdentity) { identity.ContentAddressed = false },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := actual
			mutate(&changed)
			if sameConfirmationModel(changed, expected) || sameConfirmationModel(expected, changed) {
				t.Fatal("alias tolerance accepted a different or invalid subject")
			}
		})
	}
}

func TestLifecycleConfirmsAliasUsingCanonicalRuntimeSpelling(t *testing.T) {
	f := newLifecycleFixture(t)
	f.plan.Candidates[0].Model.Requested = "friendly-alias"
	f.plan.PlanSHA256 = ""
	var err error
	f.plan.PlanSHA256, err = confirmationDigest(ConfirmationPlanSchema, f.plan)
	if err != nil {
		t.Fatal(err)
	}
	for index, point := range f.points {
		binding := ConfirmationPlanBinding(f.plan, index+1)
		point.Experiment = &binding
		f.points[index] = roleConfirmationReseal(t, point)
	}
	point := f.points[0]
	if err := ValidatePreparedConfirmationPoint(f.plan, 1, point, point.Manifest.Model, *point.Manifest.Provenance); err != nil {
		t.Fatalf("canonical spelling failed before inference: %v", err)
	}
	f.bundle, err = NewConfirmationBundle(f.plan, f.points, f.timeNow)
	if err != nil {
		t.Fatal(err)
	}
	f.issue(t)
	f.start(t)
	f.finish(t)
	f.adopt(t)
	status, err := f.roles.ReviewSelection(f.plan.Spec.Name, f.records, f.timeNow.Add(time.Second))
	if err != nil || status.State != "qualified" {
		t.Fatalf("alias confirmation did not remain qualified: %+v, %v", status, err)
	}
	if status.Selection.Selected.Model.Requested != "fast" || f.life.Events[0].Plan.Candidates[0].Model.Requested != "friendly-alias" {
		t.Fatal("confirmation rewrote either original or fresh request provenance")
	}
}
