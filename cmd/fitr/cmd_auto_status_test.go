package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/automation"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/role"
)

func TestAutoStatusSeparatesOutcomeAllowanceAndExpiry(t *testing.T) {
	now := time.Now()
	plan := automation.Plan{ID: "auto-fixture", Spec: role.Spec{Name: "daily"}, ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano),
		Candidates:    []automation.Candidate{{Model: "first"}, {Model: "second"}},
		PointRequests: 10, PointRequestedOutputTokens: 200,
		Limits: automation.Limits{MaxRequests: 40, MaxRequestedOutputTokens: 800, ConfirmationWallSeconds: 1800}}
	state := automation.State{Phase: "exploration", LastObservedAt: now, ActivePoint: 2, Requests: 7, RequestedOutputTokens: 100,
		CompletedExploration: []automation.Event{{Action: "point_completed"}}}
	view, code := describeAuto(plan, state, now)
	if code != exitOK || view.Candidate != "second" || view.ExplorationPoints != 1 || view.Requests != 7 || view.ProtectedRequests != 20 || view.ProtectedOutputTokens != 400 {
		t.Fatalf("status confuses completed work and allowance: %+v code %d", view, code)
	}
	state.Phase = "awaiting_adoption"
	view, code = describeAuto(plan, state, now)
	if code != exitOK || view.Next != "fitr auto adopt auto-fixture" || view.ProtectedRequests != 0 {
		t.Fatal(view, code)
	}
	view, code = describeAuto(plan, state, now.Add(time.Hour))
	if code != exitUnresolved || view.State != "expired" || strings.Contains(view.Next, "adopt") {
		t.Fatalf("expired session offered first adoption: %+v %d", view, code)
	}
	state.Outcome = "incumbent_retained"
	view, code = describeAuto(plan, state, now.Add(2*time.Hour))
	if code != exitUnresolved || view.State != "incumbent_retained" {
		t.Fatal("terminal outcome changed with time", view, code)
	}
	state.Outcome = "adopted"
	view, code = describeAuto(plan, state, now.Add(2*time.Hour))
	if code != exitOK || view.State != "adopted" {
		t.Fatal("historical adoption was expired", view, code)
	}
}

func TestAutoProgressRespectsNoneAndJSONModes(t *testing.T) {
	for _, mode := range []string{"none", "json", "plain"} {
		stdout, stderr, code := captureCommandOutput(t, func() int {
			display := render.New(mode)
			defer display.Close()
			display.Phase("exploration", "1/2  model")
			return exitOK
		})
		if code != exitOK || (mode == "none" && stdout+stderr != "") ||
			(mode == "json" && (stderr != "" || !strings.Contains(stdout, `"event":"phase"`))) ||
			(mode == "plain" && (stdout != "" || !strings.Contains(stderr, "exploration"))) {
			t.Fatalf("%s stream discipline: %q %q %d", mode, stdout, stderr, code)
		}
	}
}

func TestAutoStatusShowsThePreselectedChoiceBeforeAdoption(t *testing.T) {
	now := time.Now()
	plan := automation.Plan{ID: "auto-fixture", Spec: role.Spec{Name: "daily"}, ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano),
		Candidates: []automation.Candidate{{Model: "first"}, {Model: "second"}}}
	state := automation.State{Phase: "awaiting_adoption", LastObservedAt: now,
		Confirmation: &role.ConfirmationPlan{ChosenEvidenceSHA256: "second-evidence",
			Candidates: []role.ConfirmationCandidate{
				{Model: record.ModelIdentity{Resolved: "first"}, EvidenceSHA256: "first-evidence"},
				{Model: record.ModelIdentity{Resolved: "second"}, EvidenceSHA256: "second-evidence"},
			}}}
	view, code := describeAuto(plan, state, now)
	if code != exitOK || view.Candidate != "" || view.Choice != "second" || !strings.Contains(view.Next, "adopt") {
		t.Fatalf("adoption omitted or substituted the fixed choice: %+v %d", view, code)
	}
	state.Phase, state.ActivePoint = "confirmation", 1
	view, _ = describeAuto(plan, state, now)
	if view.Candidate != "first" || view.Choice != "second" {
		t.Fatalf("current collection was confused with the preselected choice: %+v", view)
	}
}

func TestAutoInvalidArgumentsNeverReachRuntime(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	t.Setenv("FITR_BACKEND", "invalid")
	for _, args := range [][]string{
		{"unknown"}, {"start"}, {"start", "coding", "--runtime", "missing"},
		{"status"}, {"status", "session", "extra"}, {"resume", "session", "--display", "bad"},
		{"runtime", "missing.exe", "--models", "missing"},
	} {
		_, _, code := captureCommandOutput(t, func() int { return cmdAuto(t.Context(), args) })
		if code != exitUsage {
			t.Fatalf("%v: code %d", args, code)
		}
	}
}

func TestAutoPublicCommandDispatch(t *testing.T) {
	handler := commandHandler("auto")
	if handler == nil {
		t.Fatal("auto was dispatched as a model name")
	}
	stdout, stderr, code := captureCommandOutput(t, func() int { return handler(t.Context(), []string{"--help"}) })
	if code != exitOK || stderr != "" || !strings.Contains(stdout, "fitr auto start") || !strings.Contains(usageText, "fitr auto runtime") {
		t.Fatalf("public auto dispatch failed: %d %q %q", code, stdout, stderr)
	}
}

func TestAutoDocumentedArgumentsKeepValuesWithFlags(t *testing.T) {
	args := []string{"start", "daily", "--mode", "establish", "--runtime", "runtime.json", "--candidate", "first:tag", "--candidate", "second:tag",
		"--adoption", "confirmed-only", "--max-wall", "3h", "--confirmation-wall", "2h", "--max-requests", "700", "--max-requested-output-tokens", "300000", "--max-points", "8", "-k", "4"}
	command, code, ok := parseAutoCommand(args)
	if !ok || code != exitOK || command.role != "daily" || command.runtimePath != "runtime.json" || len(command.candidates) != 2 ||
		command.candidates[1] != "second:tag" || command.limits.MaxRequests != 700 || command.wall != 3*time.Hour || command.repeats != 4 {
		t.Fatalf("documented invocation misparsed: %+v %d %v", command, code, ok)
	}
}

func TestAutoAdmissionRejectsChangedSoftwareBeforeCollecting(t *testing.T) {
	f := newAutoLifecycleFixture(t, nil, "manual", [2]float64{80, 20})
	point := &runExecution{result: f.points[0], provenance: f.run.plan.Provenance}
	if err := validateAutoDefinition(f.run.plan, point); err != nil {
		t.Fatal(err)
	}
	point.provenance.SoftwareBuildSHA256 = autoLifecycleHash("different executable")
	if err := f.run.plan.Provenance.CompatibilityError(point.provenance); err != nil {
		t.Fatal("fixture should remain ordinarily comparable", err)
	}
	if err := validateAutoDefinition(f.run.plan, point); err == nil {
		t.Fatal("auto admitted a different software build")
	}
}

func TestAutoStatusJSONAndSilentKeepTheTerminalExitCode(t *testing.T) {
	f := newAutoLifecycleFixture(t, nil, "manual", [2]float64{80, 20})
	autoLifecycleMust(t, f.run.session.Append(automation.Event{Action: "finished", Outcome: "cancelled"}, time.Now()))
	for _, mode := range []string{"plain", "json", "none"} {
		stdout, stderr, code := captureCommandOutput(t, func() int { return showAuto(f.run.plan.ID, mode) })
		if code != exitUnresolved || stderr != "" || (mode == "none" && stdout != "") {
			t.Fatalf("%s terminal status changed contract: %d %q %q", mode, code, stdout, stderr)
		}
		if mode == "json" && (!json.Valid([]byte(stdout)) || !strings.Contains(stdout, `"outcome": "cancelled"`)) {
			t.Fatal("invalid structured status", stdout)
		}
	}
}

func autoTerminalStatusFixture(t *testing.T, outcome string) *autoLifecycleFixture {
	t.Helper()
	f := newAutoLifecycleFixture(t, nil, "manual", [2]float64{40, 40})
	for index, point := range f.points {
		for check := range point.Checks {
			if outcome == "no_qualified" || (outcome == "unresolved" && check%2 == 0) {
				point.Checks[check].Pass, point.Checks[check].Outcome = false, eval.OutcomeFail
			}
		}
		autoLifecycleMust(t, prepareMockEvidence(point))
		f.savePoint(t, index, point, true)
	}
	group, err := f.run.group("exploration")
	autoLifecycleMust(t, err)
	ref, err := group.Close()
	autoLifecycleMust(t, err)
	autoLifecycleMust(t, f.run.session.Append(automation.Event{Action: "exploration_closed", StoreRef: &ref}, time.Now()))
	autoLifecycleMust(t, f.run.compareExploration(f.state(t)))
	if state := f.state(t); state.Outcome != outcome {
		t.Fatalf("fixture ended %q, wanted %q", state.Outcome, outcome)
	}
	return f
}

func TestAutoStatusRechecksTerminalExplorationWithoutChangingHistory(t *testing.T) {
	for _, outcome := range []string{"unresolved", "no_qualified", "overlap"} {
		t.Run(outcome, func(t *testing.T) {
			f := autoTerminalStatusFixture(t, outcome)
			before := f.state(t)
			now := time.Now()
			view, code := describeAuto(f.run.plan, before, now)
			autoExplorationStatus(&view, f.run.plan, before, f.run.records, now)
			if code != exitUnresolved || view.State != outcome || view.Next != "" || view.Gap != "" || view.Review == nil ||
				view.Review.EvaluatedAt != now.UTC().Format(time.RFC3339) || len(view.Review.Candidates) != 2 {
				t.Fatalf("terminal exploration review missing or changed history: %+v", view)
			}
			for index, candidate := range view.Review.Candidates {
				if candidate.Model != f.run.plan.Candidates[index].Model || candidate.ID != before.CompletedExploration[index].EvidenceSHA256 || candidate.Evaluation == nil ||
					(outcome != "overlap" && len(candidate.Reasons) == 0) || (outcome == "overlap" && candidate.Preference == nil) {
					t.Fatalf("candidate lost identity, gaps or preference: %+v", candidate)
				}
			}
			future := now.Add(31 * 24 * time.Hour)
			view, code = describeAuto(f.run.plan, before, future)
			autoExplorationStatus(&view, f.run.plan, before, f.run.records, future)
			if code != exitUnresolved || view.State != outcome || view.Next != "" || view.Review == nil || view.Review.Candidates[0].State != "stale" || !reflect.DeepEqual(before, f.state(t)) {
				t.Fatal("fresh aging review rewrote history or reused old qualification", view)
			}
		})
	}
}

func TestAutoStatusJSONContainsFullFixedPlanReview(t *testing.T) {
	f := autoTerminalStatusFixture(t, "unresolved")
	spec := f.run.plan.Spec
	spec.Description = "role edited after the session"
	_, err := f.run.roles.Define(spec)
	autoLifecycleMust(t, err)
	stdout, stderr, code := captureCommandOutput(t, func() int { return showAuto(f.run.plan.ID, "json") })
	var payload struct {
		State  automation.State  `json:"state"`
		Status render.AutoStatus `json:"status"`
	}
	autoLifecycleMust(t, json.Unmarshal([]byte(stdout), &payload))
	if code != exitUnresolved || stderr != "" || payload.State.Outcome != "unresolved" || payload.Status.Review == nil || payload.Status.Next != "" {
		t.Fatalf("JSON lost terminal review: %d %s %s", code, stdout, stderr)
	}
	now, err := time.Parse(time.RFC3339, payload.Status.Review.EvaluatedAt)
	autoLifecycleMust(t, err)
	expected, err := role.ReviewManaged(f.run.plan.Spec, f.run.records, *payload.State.ExplorationStore, []string{"first", "second"}, now)
	autoLifecycleMust(t, err)
	if !reflect.DeepEqual(*payload.Status.Review, expected) || payload.Status.Review.Revision != f.run.plan.RoleRevision {
		t.Fatal("JSON projected or changed the fixed role review", stdout)
	}
}

func TestAutoStatusRejectsMissingOrCorruptedExploration(t *testing.T) {
	for _, mutation := range []string{"missing", "corrupt"} {
		t.Run(mutation, func(t *testing.T) {
			f := autoTerminalStatusFixture(t, "unresolved")
			before := f.state(t)
			group, err := record.ResolveManagedStore(f.run.records, *before.ExplorationStore)
			autoLifecycleMust(t, err)
			path := group.CanonicalPath("first")
			if mutation == "missing" {
				autoLifecycleMust(t, os.Remove(path))
			} else {
				autoLifecycleMust(t, os.WriteFile(path, []byte(`{"tampered":"private-canary"}`), 0o600))
			}
			for _, mode := range []string{"json", "plain", "none"} {
				stdout, stderr, code := captureCommandOutput(t, func() int { return showAuto(f.run.plan.ID, mode) })
				if code != exitUnresolved || stderr != "" || strings.Contains(stdout, "private-canary") || strings.Contains(stdout, "fitr auto adopt") || strings.Contains(stdout, "fitr auto resume") ||
					(mode == "none" && stdout != "") || (mode != "none" && !strings.Contains(stdout, "Exploration review unavailable")) {
					t.Fatalf("%s evidence error changed terminal status: %d %s %s", mode, code, stdout, stderr)
				}
				if mode == "json" && (!json.Valid([]byte(stdout)) || strings.Contains(stdout, `"exploration_review"`)) {
					t.Fatal("invalid evidence produced a fabricated review", stdout)
				}
			}
			if !reflect.DeepEqual(before, f.state(t)) {
				t.Fatal("status changed the historical journal")
			}
		})
	}
}

func TestAutoStatusRequiresTheOriginalExplorationIdentities(t *testing.T) {
	f := autoTerminalStatusFixture(t, "unresolved")
	state := f.state(t)
	state.CompletedExploration[0].EvidenceSHA256 = autoLifecycleHash("another signed result")
	view, _ := describeAuto(f.run.plan, state, time.Now())
	autoExplorationStatus(&view, f.run.plan, state, f.run.records, time.Now())
	if view.Review != nil || !strings.Contains(view.Gap, "Exploration review unavailable") || view.State != "unresolved" {
		t.Fatal("fresh review bypassed the journal's original evidence identities", view)
	}
}
