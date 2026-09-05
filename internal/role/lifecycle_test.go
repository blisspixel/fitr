package role

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/record"
)

type lifecycleFixture struct {
	roles            Store
	records          record.Store
	plan             ConfirmationPlan
	points           []*record.Record
	bundle           ConfirmationBundle
	created, timeNow time.Time
	life             Lifecycle
}

func newLifecycleFixture(t *testing.T) lifecycleFixture {
	t.Helper()
	plan, points, now := roleConfirmationFixture(t)
	roles := Store{Dir: filepath.Join(t.TempDir(), "roles")}
	if _, err := roles.Define(plan.Spec); err != nil {
		t.Fatal(err)
	}
	life, err := roles.LoadLifecycle(plan.Spec.Name)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := NewConfirmationBundle(plan, points, now)
	if err != nil {
		t.Fatal(err)
	}
	created, _ := time.Parse(time.RFC3339Nano, plan.CreatedAt)
	return lifecycleFixture{roles: roles, records: record.Store{Dir: t.TempDir()}, plan: plan, points: points, bundle: bundle, created: created, timeNow: now, life: life}
}

func (f *lifecycleFixture) issue(t *testing.T) {
	t.Helper()
	var err error
	f.life, err = f.roles.IssueConfirmation(f.plan, f.life.Digest, f.created)
	if err != nil {
		t.Fatal(err)
	}
}

func (f *lifecycleFixture) start(t *testing.T) {
	t.Helper()
	var err error
	f.life, err = f.roles.BeginConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, f.life.Digest, f.created.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, point := range f.points {
		roleReviewSave(t, f.records, point)
	}
}

func (f *lifecycleFixture) finish(t *testing.T) {
	t.Helper()
	var err error
	f.life, err = f.roles.FinishConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, "completed", &f.bundle, f.records, f.life.Digest, f.timeNow)
	if err != nil {
		t.Fatal(err)
	}
}

func (f *lifecycleFixture) adopt(t *testing.T) {
	t.Helper()
	var err error
	f.life, err = f.roles.AdoptConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, f.bundle, f.records, f.life.Digest, f.timeNow.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleIssuesBeforeAttemptAndAdoptsOnlyConfirmedCurrentEvidence(t *testing.T) {
	f := newLifecycleFixture(t)
	initial := f.life.Digest
	status, err := f.roles.ReviewSelection(f.plan.Spec.Name, f.records, f.created)
	if err != nil || status.State != "unselected" {
		t.Fatalf("initial status=%+v err=%v", status, err)
	}
	f.issue(t)
	repeated, err := f.roles.IssueConfirmation(f.plan, initial, f.created.Add(time.Second))
	if err != nil || repeated.Digest != f.life.Digest || len(repeated.Events) != 1 {
		t.Fatal("issuance not idempotent", err)
	}
	issued := f.life.Digest
	f.start(t)
	repeated, err = f.roles.BeginConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, issued, f.created.Add(2*time.Second))
	if err != nil || repeated.Digest != f.life.Digest {
		t.Fatal("start not idempotent", err)
	}
	started := f.life.Digest
	f.finish(t)
	repeated, err = f.roles.FinishConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, "completed", &f.bundle, f.records, started, f.timeNow)
	if err != nil || repeated.Digest != f.life.Digest {
		t.Fatal("completion not idempotent", err)
	}
	completed := f.life.Digest
	f.adopt(t)
	repeated, err = f.roles.AdoptConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, f.bundle, f.records, completed, f.timeNow.Add(time.Second))
	if err != nil || repeated.Digest != f.life.Digest {
		t.Fatal("adoption not idempotent", err)
	}
	status, err = f.roles.ReviewSelection(f.plan.Spec.Name, f.records, f.timeNow.Add(48*time.Hour))
	if err != nil || status.State != "qualified" || status.Scope != "battery_screening" || status.Selection.Selected.Attachment.EvidenceSHA256 != f.points[0].Completion.EvidenceSHA256 {
		t.Fatalf("qualified status=%+v err=%v", status, err)
	}
	loaded, err := f.roles.LoadLifecycle(f.plan.Spec.Name)
	if err != nil || loaded.Validate() != nil || loaded.Digest != f.life.Digest || len(loaded.Events) != 4 {
		t.Fatal("lifecycle failed round trip", err)
	}
}

func TestLifecycleCancellationFailureAndCASCannotBecomeAdoption(t *testing.T) {
	for _, outcome := range []string{"cancelled", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			f := newLifecycleFixture(t)
			if _, err := f.roles.BeginConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, f.life.Digest, f.created); err == nil {
				t.Fatal("unissued plan started")
			}
			f.issue(t)
			old := f.life.Digest
			f.start(t)
			if _, err := f.roles.FinishConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, outcome, nil, f.records, old, f.timeNow); err == nil {
				t.Fatal("stale CAS accepted")
			}
			life, err := f.roles.FinishConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, outcome, nil, f.records, f.life.Digest, f.timeNow)
			if err != nil {
				t.Fatal(err)
			}
			status, err := f.roles.ReviewSelection(f.plan.Spec.Name, f.records, f.timeNow)
			if err != nil || status.Attempts != 1 || status.LastAttempt == nil || status.LastAttempt.Action != outcome || status.LastAttempt.PlanSHA256 != f.plan.PlanSHA256 {
				t.Fatalf("failed attempt disappeared: %+v %v", status, err)
			}
			if _, err := f.roles.BeginConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, life.Digest, f.timeNow); err == nil {
				t.Fatal("terminal plan started again")
			}
			if _, err := f.roles.FinishConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, "completed", &f.bundle, f.records, life.Digest, f.timeNow); err == nil {
				t.Fatal("terminal plan gained completion")
			}
			if _, err := f.roles.AdoptConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, f.bundle, f.records, life.Digest, f.timeNow); err == nil {
				t.Fatal("terminal failure adopted")
			}
			fresh, _, _ := roleConfirmationFixture(t)
			if fresh.SeedSet == f.plan.SeedSet {
				t.Fatal("fixture reused a seed")
			}
			if _, err := f.roles.IssueConfirmation(fresh, life.Digest, f.timeNow); err != nil {
				t.Fatal("fresh retry blocked", err)
			}
		})
	}
}

func TestLifecycleRechecksRoleRevisionExpiryAndCanonicalTwins(t *testing.T) {
	for _, change := range []string{"revision", "expired", "missing", "replaced", "history", "clock"} {
		t.Run(change, func(t *testing.T) {
			f := newLifecycleFixture(t)
			f.issue(t)
			f.start(t)
			f.finish(t)
			f.adopt(t)
			now := f.timeNow.Add(time.Second)
			switch change {
			case "clock":
				now = f.created
			case "revision":
				spec := f.plan.Spec
				spec.Description = "changed requirements revision"
				if _, err := f.roles.Define(spec); err != nil {
					t.Fatal(err)
				}
			case "expired":
				now = now.Add(time.Duration(f.plan.Spec.MaxAgeDays+1) * 24 * time.Hour)
			case "missing":
				if err := os.Remove(f.records.CanonicalPath(f.points[0].Model)); err != nil {
					t.Fatal(err)
				}
			case "replaced":
				roleReviewSave(t, f.records, roleReviewRecord(t, f.points[0].Model, 99, 8, 8192, "runtime-1", now))
			case "history":
				entries, err := os.ReadDir(f.records.HistoryDir())
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if err := os.Remove(filepath.Join(f.records.HistoryDir(), entry.Name())); err != nil {
						t.Fatal(err)
					}
				}
			}
			status, err := f.roles.ReviewSelection(f.plan.Spec.Name, f.records, now)
			if err != nil || status.State != "stale" || status.Selection == nil || status.Reason == "" {
				t.Fatalf("changed evidence qualified: %+v err=%v", status, err)
			}
		})
	}
}

func TestLifecycleRejectsIncompleteWrongAndRetroactiveBundles(t *testing.T) {
	f := newLifecycleFixture(t)
	f.issue(t)
	for _, point := range f.points {
		roleReviewSave(t, f.records, point)
	}
	if _, err := f.roles.FinishConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, "completed", &f.bundle, f.records, f.life.Digest, f.timeNow); err == nil {
		t.Fatal("completion preceded issuance of attempt")
	}
	f.start(t)
	for _, status := range []string{"bogus", "cancelled", "failed"} {
		if _, err := f.roles.FinishConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, status, &f.bundle, f.records, f.life.Digest, f.timeNow); err == nil {
			t.Fatalf("invalid terminal bundle accepted for %s", status)
		}
	}
	incomplete, err := NewConfirmationBundle(f.plan, []*record.Record{nil, nil}, f.timeNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.roles.FinishConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, "completed", &incomplete, f.records, f.life.Digest, f.timeNow); err == nil {
		t.Fatal("incomplete bundle completed or panicked")
	}
	if _, err := f.roles.FinishConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, "completed", nil, f.records, f.life.Digest, f.timeNow); err == nil {
		t.Fatal("nil bundle completed")
	}
	if _, err := f.roles.AdoptConfirmation(f.plan.Spec.Name, "sha256:"+strings.Repeat("a", 64), f.bundle, f.records, f.life.Digest, f.timeNow); err == nil {
		t.Fatal("foreign plan adopted")
	}
	bad := roleConfirmationClone(t, f.bundle)
	bad.Report.State = "unresolved"
	if _, err := f.roles.FinishConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, "completed", &bad, f.records, f.life.Digest, f.timeNow); err == nil {
		t.Fatal("tampered report completed")
	}
	if _, err := f.roles.FinishConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, "completed", &f.bundle, f.records, f.life.Digest, f.timeNow.Add(48*time.Hour)); err == nil {
		t.Fatal("expired plan completed")
	}
}

func TestLifecycleStoreRejectsTamperSymlinksAndWrongNames(t *testing.T) {
	f := newLifecycleFixture(t)
	f.issue(t)
	path, err := f.roles.lifecyclePath(f.plan.Spec.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range [][]byte{[]byte(`{"schema":1,"schema":2}`), []byte(`{"unknown":true}`), []byte(strings.Repeat(" ", maximumLifecycleBytes+1))} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := f.roles.LoadLifecycle(f.plan.Spec.Name); err == nil {
			t.Fatal("invalid sidecar loaded")
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	broken := roleConfirmationClone(t, f.life)
	broken.Events[0].At = f.created.Add(time.Second).Format(time.RFC3339Nano)
	if broken.Validate() == nil {
		t.Fatal("tampered event retained validity")
	}
	broken = f.life
	broken.Name = "another-role"
	encoded, _ := json.Marshal(broken)
	os.WriteFile(path, encoded, 0o600)
	if _, err := f.roles.LoadLifecycle(f.plan.Spec.Name); err == nil {
		t.Fatal("renamed lifecycle accepted")
	}
	os.WriteFile(path, data, 0o600)
	if _, err := f.roles.LoadLifecycle("../escape"); err == nil {
		t.Fatal("unsafe role name accepted")
	}
	if _, err := f.roles.lifecycleDirectory("../escape", true); err == nil {
		t.Fatal("unsafe sidecar directory accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := f.roles.LoadLifecycle(f.plan.Spec.Name); err == nil {
		t.Fatal("symbolic lifecycle accepted")
	}
}

func TestLifecycleBundleStorageIsPrivateFullDigestAndImmutable(t *testing.T) {
	f := newLifecycleFixture(t)
	if _, err := f.roles.SaveConfirmationBundle(f.bundle); err == nil {
		t.Fatal("unissued bundle saved")
	}
	f.issue(t)
	f.start(t)
	path, err := f.roles.SaveConfirmationBundle(f.bundle)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != strings.TrimPrefix(f.plan.PlanSHA256, "sha256:")+".json" || filepath.Base(filepath.Dir(path)) != ".confirmations" {
		t.Fatal("short or unscoped bundle path", path)
	}
	again, err := f.roles.SaveConfirmationBundle(f.bundle)
	if err != nil || again != path {
		t.Fatal("same bundle did not reuse private receipt", err)
	}
	other, err := NewConfirmationBundle(f.plan, f.points, f.timeNow.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.roles.SaveConfirmationBundle(other); err == nil {
		t.Fatal("immutable bundle was overwritten")
	}
	loaded, err := LoadConfirmationBundle(path)
	if err != nil || !sameLifecycleValue(loaded, f.bundle) {
		t.Fatal("saved bundle does not validate", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := f.roles.SaveConfirmationBundle(f.bundle); err == nil {
		t.Fatal("symbolic bundle path accepted")
	}
}
