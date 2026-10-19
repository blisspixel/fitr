package main

import (
	"testing"

	"github.com/blisspixel/fitr/internal/score"
)

func TestMeasuredRoleRowsStayInsideTheGroupAndDropSkips(t *testing.T) {
	rows := []*Result{
		{Scorecard: score.Scorecard{Needs: map[string]score.Verdict{"coding": {State: score.Pass}}}},
		{Scorecard: score.Scorecard{Needs: map[string]score.Verdict{"coding": {State: score.Skip}}}},
		{Scorecard: score.Scorecard{Needs: map[string]score.Verdict{"coding": {State: score.Inconclusive}}}},
		{Scorecard: score.Scorecard{Needs: map[string]score.Verdict{"unattended_agentic": {State: score.Fail}}}},
	}
	coding := measuredRoleRows(rows, "coding")
	if len(coding) != 2 || coding[0] != rows[0] || coding[1] != rows[2] {
		t.Fatalf("coding rows = %d", len(coding))
	}
	agent := measuredRoleRows(rows, "unattended_agentic")
	if len(agent) != 1 || agent[0] != rows[3] {
		t.Fatalf("agent rows = %d", len(agent))
	}
	if got := measuredRoleRows(rows, ""); len(got) != len(rows) {
		t.Fatalf("unfiltered rows = %d", len(got))
	}
	if need, ok := boardRoleNeed("agent"); !ok || need != "unattended_agentic" {
		t.Fatalf("agent role = %q %v", need, ok)
	}
	if _, ok := boardRoleNeed("librarian"); ok {
		t.Fatal("an unknown role was accepted")
	}
}
