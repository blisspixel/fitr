package render

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/role"

	"github.com/clipperhouse/displaywidth"
)

func TestRoleReviewKeepsStatesAndWidth(t *testing.T) {
	for _, width := range []int{40, 60, 80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			t.Setenv("FITR_WIDTH", strconv.Itoa(width))
			t.Setenv("NO_COLOR", "1")
			var output bytes.Buffer
			WriteRoleReview(&output, role.ReviewReport{
				Role: strings.Repeat("模型", 30), State: "exploration-lead", Lead: "a",
				Candidates: []role.Candidate{
					{ID: "a", Model: "good model", State: "eligible", Preference: &role.PreferenceResult{Estimate: 0.9, Low: 0.8, High: 1},
						Evaluation: &decision.Evaluation{Requirements: []decision.RequirementResult{{ID: "quality", State: decision.RequirementEstablished}}}},
					{ID: "b", Model: "fast weak model", State: "ineligible", Reasons: []string{"bad \x1b[2J text and " + strings.Repeat("long", 30)}},
				},
				Gaps: []string{"not generally qualified for agentic work"}, Next: "confirm the role before adopting",
			}, "plain")
			text := output.String()
			for _, want := range []string{"INELIGIBLE", "Fresh confirmation", "Battery screening only", "not a joint confidence interval"} {
				if !strings.Contains(strings.Join(strings.Fields(text), " "), want) {
					t.Fatalf("missing state %q: %s", want, text)
				}
			}
			if strings.Contains(text, "\x1b") {
				t.Fatal("terminal control survived")
			}
			for _, line := range strings.Split(text, "\n") {
				if displaywidth.String(line) > width {
					t.Fatalf("line exceeds %d: %s", width, line)
				}
			}
		})
	}
}

func TestRoleIndexEmptyAndRich(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	for _, libraries := range [][]role.Library{nil, {{Name: "coding"}}} {
		var output bytes.Buffer
		WriteRoleLibraries(&output, libraries, "rich")
		if !strings.Contains(output.String(), "\x1b[") || !strings.Contains(output.String(), "fitr / roles") {
			t.Fatal("role index lost rich hierarchy")
		}
	}
}

func TestRoleReviewPrintsOneUnresolvedBehaviorCommand(t *testing.T) {
	behavior := decision.Action{
		Code: "collect_behavior_evidence", Experiment: "behavior",
		Argv:   []string{"fitr", "run", analysis.CurrentModelPlaceholder},
		Reason: "collect planned observations for coding under the same configuration",
	}
	performance := decision.Action{
		Code: "measure_performance_state", Experiment: "performance",
		Argv:   []string{"fitr", "run", analysis.CurrentModelPlaceholder, "--level", "standard"},
		Reason: "measure the exact performance state required by latency",
	}
	for _, behaviorFirst := range []bool{false, true} {
		t.Run(strconv.FormatBool(behaviorFirst), func(t *testing.T) {
			slow := unresolvedCandidate("slow", "latency", &performance)
			coder := unresolvedCandidate("coder", "coding", &behavior)
			candidates := []role.Candidate{slow, coder}
			if behaviorFirst {
				candidates = []role.Candidate{coder, slow}
			}
			report := role.ReviewReport{
				Role: "coding", State: "unresolved", Candidates: candidates,
				Next: "Resolve missing, stale or uncertain evidence before choosing a model.",
			}
			text := renderRoleReview(t, report)
			flat := strings.Join(strings.Fields(text), " ")
			if !strings.Contains(flat, "fitr run coder") || !strings.Contains(flat, behavior.Reason) || !strings.Contains(flat, "candidate coder") {
				t.Fatalf("behavior command missing:\n%s", text)
			}
			if strings.Contains(flat, "--level") || strings.Contains(flat, "fitr role confirm") || strings.Count(flat, "fitr run") != 1 {
				t.Fatalf("review printed a second command:\n%s", text)
			}
		})
	}
}

func TestRoleReviewLeavesExplorationCommandUnmeasured(t *testing.T) {
	borrowed := decision.Action{
		Code: "collect_behavior_evidence", Experiment: "behavior",
		Argv:   []string{"fitr", "run", analysis.CurrentModelPlaceholder},
		Reason: "collect planned observations for coding under the same configuration",
	}
	report := role.ReviewReport{
		Role: "coding", State: "exploration-lead", Lead: "lead",
		Candidates: []role.Candidate{
			{ID: "lead", Model: "leader", State: "eligible", Evaluation: &decision.Evaluation{
				State: decision.DecisionEligible,
				NextAction: &decision.Action{Code: "confirm_candidate", Experiment: "confirmation",
					Reason: "collect fresh evidence after sealing the selected candidate"},
			}},
			unresolvedCandidate("other", "coding", &borrowed),
		},
		Next: "Collect fresh confirmation under a sealed role preference policy before adoption. Exploration does not authorize a model switch.",
	}
	text := renderRoleReview(t, report)
	flat := strings.Join(strings.Fields(text), " ")
	if !strings.Contains(flat, "next command is unmeasured") || !strings.Contains(flat, "does not authorize a model switch") {
		t.Fatalf("exploration limit missing:\n%s", text)
	}
	for _, banned := range []string{"fitr role confirm", "fitr run", "ollama pull", "download"} {
		if strings.Contains(flat, banned) {
			t.Fatalf("invented %s:\n%s", banned, text)
		}
	}
}

func unresolvedCandidate(model, requirement string, action *decision.Action) role.Candidate {
	return role.Candidate{Model: model, State: "unresolved", Evaluation: &decision.Evaluation{
		State: decision.DecisionUnresolved, NextAction: action,
		Requirements: []decision.RequirementResult{{ID: requirement, State: decision.RequirementUnresolved}},
	}}
}

func reviewKeepsModelPlaceholder(report role.ReviewReport) bool {
	for _, candidate := range report.Candidates {
		if candidate.Evaluation == nil || candidate.Evaluation.NextAction == nil {
			continue
		}
		for _, arg := range candidate.Evaluation.NextAction.Argv {
			if arg == analysis.CurrentModelPlaceholder {
				return true
			}
		}
	}
	return false
}

func renderRoleReview(t *testing.T, report role.ReviewReport) string {
	t.Helper()
	before, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("NO_COLOR", "1")
	t.Setenv("FITR_WIDTH", "40")
	var output bytes.Buffer
	WriteRoleReview(&output, report, "plain")
	after, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("text render changed the review JSON")
	}
	if !reviewKeepsModelPlaceholder(report) {
		t.Fatal("evaluation argv lost its placeholder")
	}
	text := output.String()
	for _, line := range strings.Split(text, "\n") {
		if displaywidth.String(line) > 40 {
			t.Fatalf("line exceeds 40: %s", line)
		}
	}
	return text
}

func TestRoleReviewDoesNotInventLeadFromMissingIdentity(t *testing.T) {
	for _, state := range []string{"single-qualified", "unresolved", "exploration-lead"} {
		var output bytes.Buffer
		WriteRoleReview(&output, role.ReviewReport{
			Role: "coding", State: state,
			Candidates: []role.Candidate{{Model: "failed", State: "ineligible"}, {Model: "screened", State: "eligible"}},
		}, "plain")
		if strings.Contains(output.String(), "Survives metric bounds") {
			t.Fatalf("empty lead became a recommendation in %s: %s", state, output.String())
		}
	}
}

func TestRoleSelectionKeepsIncumbentAndSanitizesOutput(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("FITR_WIDTH", "40")
	for _, state := range []string{"qualified", "stale", "unselected"} {
		var output bytes.Buffer
		WriteRoleSelection(&output, role.SelectionStatus{
			Role: "coding\x1b[2J", State: state, Reason: "changed " + strings.Repeat("模型", 30),
			LastAttempt: &role.LifecycleAttemptStatus{Action: "failed", At: "2026-09-05T12:00:00Z"},
		}, "rich")
		text := output.String()
		if strings.Contains(text, "\x1b") || !strings.Contains(text, strings.ToUpper(state)) || !strings.Contains(text, "failed") {
			t.Fatalf("selection state or terminal safety lost: %q", text)
		}
		if strings.Contains(text, "fitr role review") != (state != "qualified") {
			t.Fatalf("wrong next action: %s", text)
		}
		for _, line := range strings.Split(text, "\n") {
			if displaywidth.String(line) > 40 {
				t.Fatalf("selection overflows: %q", line)
			}
		}
	}
}
