package role

import (
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/record"
)

func roleRuntimeBinding(result *record.Record) *record.RuntimeBinding {
	return &record.RuntimeBinding{Schema: record.RuntimeBindingSchema, Kind: "owned_ollama", ProfileSHA256: "sha256:" + strings.Repeat("1", 64),
		ExecutableSHA256: "sha256:" + strings.Repeat("2", 64), LaunchConfigurationSHA256: "sha256:" + strings.Repeat("3", 64),
		ModelConfigurationSHA256: "sha256:" + strings.Repeat("4", 64), ArtifactDigest: result.Manifest.Model.RuntimeBoundDigest(),
		RuntimeVersion: result.Manifest.Model.Runtime, OwnershipSHA256: "sha256:" + strings.Repeat("5", 64)}
}

func roleRuntimeFixture(t *testing.T) (ConfirmationPlan, []*record.Record, time.Time) {
	t.Helper()
	plan, points, now := roleConfirmationFixture(t)
	for index := range plan.Candidates {
		plan.Candidates[index].RuntimeBinding = roleRuntimeBinding(points[index])
		points[index].RuntimeBinding = record.CloneRuntimeBinding(plan.Candidates[index].RuntimeBinding)
		points[index].RuntimeBinding.OwnershipSHA256 = "sha256:" + strings.Repeat("6", 64)
	}
	plan.PlanSHA256 = ""
	var err error
	plan.PlanSHA256, err = confirmationDigest(ConfirmationPlanSchema, plan)
	if err != nil {
		t.Fatal(err)
	}
	for index, point := range points {
		binding := ConfirmationPlanBinding(plan, index+1)
		point.Experiment = &binding
		points[index] = roleConfirmationReseal(t, point)
	}
	return plan, points, now
}

func TestConfirmationRuntimeAllowsNewOwnershipButRejectsConfigurationDrift(t *testing.T) {
	plan, points, now := roleRuntimeFixture(t)
	bundle, err := NewConfirmationBundle(plan, points, now)
	if err != nil || bundle.Report.State != "confirmed" {
		t.Fatal("new owned launch failed", err)
	}
	for _, mutate := range []func(*record.RuntimeBinding){
		func(b *record.RuntimeBinding) { b.LaunchConfigurationSHA256 = "sha256:" + strings.Repeat("7", 64) },
		func(b *record.RuntimeBinding) { b.ModelConfigurationSHA256 = "sha256:" + strings.Repeat("7", 64) },
		func(b *record.RuntimeBinding) { b.OwnershipSHA256 = "bad" },
	} {
		point := roleConfirmationClone(t, points[0])
		mutate(point.RuntimeBinding)
		if ValidatePreparedConfirmationPoint(plan, 1, point, point.Manifest.Model, *point.Manifest.Provenance) == nil {
			t.Fatal("runtime drift accepted before inference")
		}
	}
	points[0].RuntimeBinding = nil
	if ValidatePreparedConfirmationPoint(plan, 1, points[0], points[0].Manifest.Model, *points[0].Manifest.Provenance) == nil {
		t.Fatal("owned plan accepted unbound preparation")
	}
	plan.Candidates[1].RuntimeBinding.ProfileSHA256 = "sha256:" + strings.Repeat("8", 64)
	if plan.validateCandidates(now) == nil {
		t.Fatal("different runtime profiles accepted")
	}
}

func TestManagedSelectionPreservesRuntimeBindingAndIncumbentProfile(t *testing.T) {
	f := newLifecycleFixture(t)
	f.plan, f.points, f.timeNow = roleRuntimeFixture(t)
	var err error
	f.bundle, err = NewConfirmationBundle(f.plan, f.points, f.timeNow)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lifecycleManagedAdopt(t, &f, "runtime")
	status, err := f.roles.ReviewSelection(f.plan.Spec.Name, f.records, f.timeNow.Add(time.Second))
	if err != nil || status.State != "qualified" || *status.Selection.Selected.RuntimeBinding != *f.points[0].RuntimeBinding {
		t.Fatal("selected runtime binding lost", err)
	}
	changed := roleConfirmationClone(t, f.plan)
	changed.Candidates[0].RuntimeBinding.ModelConfigurationSHA256 = "sha256:" + strings.Repeat("9", 64)
	if planIncludesIncumbent(changed, f.life) == nil {
		t.Fatal("incumbent configuration silently replaced")
	}
	changed = roleConfirmationClone(t, f.plan)
	changed.Candidates[0].RuntimeBinding.OwnershipSHA256 = "sha256:" + strings.Repeat("a", 64)
	if err := planIncludesIncumbent(changed, f.life); err != nil {
		t.Fatal("fresh incumbent launch rejected", err)
	}
}

func TestNewConfirmationPlanCopiesBoundExplorationAndRejectsMixedProfiles(t *testing.T) {
	sources := []*record.Record{
		roleConfirmationRecord(t, "fast", 80, 8, roleReviewNow.Add(-time.Hour)),
		roleConfirmationRecord(t, "slow", 20, 8, roleReviewNow.Add(-time.Hour)),
	}
	for index, source := range sources {
		source.RuntimeBinding = roleRuntimeBinding(source)
		sources[index] = roleConfirmationReseal(t, source)
	}
	plan, err := NewConfirmationPlan(roleReviewSpec(), sources, sources[0].Completion.EvidenceSHA256, roleReviewNow)
	if err != nil {
		t.Fatal("bound exploration rejected", err)
	}
	sources[0].RuntimeBinding.OwnershipSHA256 = "sha256:" + strings.Repeat("c", 64)
	if err := plan.Validate(); err != nil {
		t.Fatal("plan retained mutable input binding", err)
	}
	sources[0] = roleConfirmationReseal(t, sources[0])
	sources[1].RuntimeBinding.LaunchConfigurationSHA256 = "sha256:" + strings.Repeat("d", 64)
	sources[1] = roleConfirmationReseal(t, sources[1])
	if _, err := NewConfirmationPlan(roleReviewSpec(), sources, sources[0].Completion.EvidenceSHA256, roleReviewNow); err == nil {
		t.Fatal("differing launch configuration produced a conclusive plan")
	}
}
