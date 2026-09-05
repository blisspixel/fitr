package role

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/eval"
)

func resealLifecycle(t *testing.T, life Lifecycle) Lifecycle {
	t.Helper()
	result := emptyLifecycle(life.Name)
	for index, event := range life.Events {
		event.Sequence, event.PreviousDigest, event.Digest = index+1, result.Digest, ""
		var err error
		event.Digest, err = lifecycleDigest(LifecycleSchema+".event", event)
		if err != nil {
			t.Fatal(err)
		}
		result.appendEvent(event)
	}
	return result
}

func TestLifecycleRevalidatesTransitionsBeyondHashConsistency(t *testing.T) {
	f := newLifecycleFixture(t)
	f.issue(t)
	f.start(t)
	f.finish(t)
	f.adopt(t)
	mutations := map[string]func(*Lifecycle){
		"missing issued plan": func(l *Lifecycle) { l.Events[0].Plan = nil },
		"bad plan":            func(l *Lifecycle) { l.Events[0].Plan.Schema = "invalid" },
		"false incumbent":     func(l *Lifecycle) { l.Events[0].IncumbentSHA256 = "sha256:" + strings.Repeat("1", 64) },
		"unknown action":      func(l *Lifecycle) { l.Events[1].Action = "resumed" },
		"clock reversal":      func(l *Lifecycle) { l.Events[1].At = f.created.Add(-time.Second).Format(time.RFC3339Nano) },
		"extended expiry": func(l *Lifecycle) {
			l.Events[2].Completion.ExpiresAt = f.timeNow.Add(365 * 24 * time.Hour).Format(time.RFC3339Nano)
		},
		"retroactive points": func(l *Lifecycle) {
			l.Events[2].Completion.Points[0].StartedAt = f.created.Add(-time.Hour).Format(time.RFC3339Nano)
		},
		"substituted original choice": func(l *Lifecycle) { l.Events[3].Selection.Selected = l.Events[3].Selection.Points[1] },
		"altered original digest":     func(l *Lifecycle) { l.Events[3].Selection.ChosenEvidenceSHA256 = "sha256:" + strings.Repeat("2", 64) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			broken := roleConfirmationClone(t, f.life)
			mutate(&broken)
			broken = resealLifecycle(t, broken)
			if broken.Validate() == nil {
				t.Fatal("invalid lifecycle passed after hash rewrite")
			}
		})
	}
	broken := f.life
	broken.IncumbentSHA256 = ""
	if broken.Validate() == nil {
		t.Fatal("false incumbent summary accepted")
	}
	broken = f.life
	broken.Schema = "unknown"
	if broken.Validate() == nil {
		t.Fatal("unknown lifecycle schema accepted")
	}
	broken = f.life
	broken.Events = make([]LifecycleEvent, maximumLifecycleEvents+1)
	if broken.Validate() == nil {
		t.Fatal("event bound ignored")
	}
}

func TestLifecycleQualificationRecomputesEvidenceInsteadOfTrustingReceiptState(t *testing.T) {
	f := newLifecycleFixture(t)
	f.issue(t)
	f.start(t)
	f.finish(t)
	f.adopt(t)
	for index, point := range f.points {
		changed := roleConfirmationClone(t, point)
		for trial := range changed.Checks {
			changed.Checks[trial].Pass = false
			changed.Checks[trial].Outcome = eval.OutcomeFail
		}
		f.points[index] = roleConfirmationReseal(t, changed)
		roleReviewSave(t, f.records, f.points[index])
	}
	failed, err := NewConfirmationBundle(f.plan, f.points, f.timeNow)
	if err != nil || failed.Report.State == "confirmed" {
		t.Fatal("failed fixture unexpectedly confirmed", err)
	}
	attempt, err := confirmationAttempt(failed, f.records, f.timeNow)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate rewritten local derived receipt fields with consistent hashes.
	// Qualification must reconstruct the verifier-backed decision from twins.
	attempt.State = "confirmed"
	broken := roleConfirmationClone(t, f.life)
	broken.Events[2].Completion = &attempt
	selection := broken.Events[3].Selection
	selection.BundleSHA256, selection.Points = attempt.BundleSHA256, attempt.Points
	selection.Selected, err = selectedConfirmationPoint(f.plan, attempt.Points)
	if err != nil {
		t.Fatal(err)
	}
	broken = resealLifecycle(t, broken)
	if err := broken.Validate(); err != nil {
		t.Fatalf("fixture failed before evidence reconstruction: %v", err)
	}
	path, err := f.roles.lifecyclePath(f.plan.Spec.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(broken)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := f.roles.ReviewSelection(f.plan.Spec.Name, f.records, f.timeNow.Add(time.Second))
	if err != nil || status.State != "stale" || !strings.Contains(status.Reason, "reestablish") {
		t.Fatalf("receipt label overrode failed quality: %+v %v", status, err)
	}
}

func TestLifecycleRetryRequiresFreshSeedAndReportsAbandonedAttempt(t *testing.T) {
	f := newLifecycleFixture(t)
	f.issue(t)
	f.start(t)
	status, err := f.roles.ReviewSelection(f.plan.Spec.Name, f.records, f.timeNow)
	if err != nil || status.State != "unselected" || status.Attempts != 1 || status.LastAttempt.Action != "started" {
		t.Fatalf("abandoned attempt vanished: %+v %v", status, err)
	}
	reused := roleConfirmationClone(t, f.plan)
	reused.CreatedAt = f.created.Add(time.Second).Format(time.RFC3339Nano)
	reused.ExpiresAt = f.created.Add(time.Second + 24*time.Hour).Format(time.RFC3339Nano)
	reused.PlanSHA256 = ""
	reused.PlanSHA256, err = confirmationDigest(ConfirmationPlanSchema, reused)
	if err != nil || reused.Validate() != nil {
		t.Fatal("invalid retry fixture", err)
	}
	if _, err := f.roles.IssueConfirmation(reused, f.life.Digest, f.timeNow); err == nil || !strings.Contains(err.Error(), "seed set was reused") {
		t.Fatalf("seed reused across plans: %v", err)
	}
}

func TestLifecycleDerivedTimestampsCannotExtendQualifiedEvidenceLifetime(t *testing.T) {
	f := newLifecycleFixture(t)
	f.issue(t)
	f.start(t)
	f.finish(t)
	f.adopt(t)
	broken := roleConfirmationClone(t, f.life)
	attempt := broken.Events[2].Completion
	for index := range attempt.Points {
		started, _ := time.Parse(time.RFC3339Nano, attempt.Points[index].StartedAt)
		attempt.Points[index].StartedAt = started.Add(10 * time.Second).Format(time.RFC3339Nano)
	}
	originalExpiry, _ := time.Parse(time.RFC3339Nano, attempt.ExpiresAt)
	attempt.ExpiresAt = originalExpiry.Add(10 * time.Second).Format(time.RFC3339Nano)
	selection := broken.Events[3].Selection
	selection.Points, selection.ExpiresAt = attempt.Points, attempt.ExpiresAt
	selection.Selected, _ = selectedConfirmationPoint(f.plan, attempt.Points)
	broken = resealLifecycle(t, broken)
	if err := broken.Validate(); err != nil {
		t.Fatal("fixture rejected before canonical check", err)
	}
	path, err := f.roles.lifecyclePath(f.plan.Spec.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(broken)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := f.roles.ReviewSelection(f.plan.Spec.Name, f.records, originalExpiry.Add(5*time.Second))
	if err != nil || status.State != "stale" || !strings.Contains(status.Reason, "canonical current twins") {
		t.Fatalf("derived timestamps extended actual evidence: %+v %v", status, err)
	}
}
