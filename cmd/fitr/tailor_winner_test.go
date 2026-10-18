package main

import (
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/automation"
	"github.com/blisspixel/fitr/internal/contextquality"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"
)

func winnerHash(fill byte) string {
	return "sha256:" + strings.Repeat(string(fill), 64)
}

func TestWinnerRecordsCertifyOnlyThePreselectedCandidate(t *testing.T) {
	hash := winnerHash('a')
	other := winnerHash('b')
	plan := role.ConfirmationPlan{ChosenEvidenceSHA256: hash, Candidates: []role.ConfirmationCandidate{{EvidenceSHA256: hash}}}
	for _, state := range []string{"", "incumbent_retained", "confirmed"} {
		report := role.ConfirmationReport{State: state, WinnerEvidenceSHA256: other}
		if state == "" {
			report.WinnerEvidenceSHA256 = ""
		}
		_, note := winnerRecords(plan, report, nil, nil)
		want := state
		if want == "" {
			want = "unresolved"
		}
		if !strings.Contains(note, want) || !strings.Contains(note, "nothing was certified") {
			t.Fatalf("state %q note %q", state, note)
		}
	}
	plan.Candidates = []role.ConfirmationCandidate{{EvidenceSHA256: hash}, {EvidenceSHA256: hash}}
	_, note := winnerRecords(plan, role.ConfirmationReport{State: "confirmed", WinnerEvidenceSHA256: hash}, nil, nil)
	if !strings.Contains(note, "not a single") {
		t.Fatal(note)
	}
	plan.Candidates = []role.ConfirmationCandidate{{EvidenceSHA256: other}, {EvidenceSHA256: hash}}
	_, note = winnerRecords(plan, role.ConfirmationReport{State: "confirmed", WinnerEvidenceSHA256: hash}, nil, nil)
	if !strings.Contains(note, "not in the sealed") {
		t.Fatal(note)
	}
	battery := &record.Record{Completion: &record.CompletionReceipt{}}
	_, note = winnerRecords(plan, role.ConfirmationReport{State: "confirmed", WinnerEvidenceSHA256: hash}, []*record.Record{nil, {}}, nil)
	if !strings.Contains(note, "not in the sealed") {
		t.Fatal(note)
	}
	plan.ContextPolicy = &contextquality.Policy{}
	_, note = winnerRecords(plan, role.ConfirmationReport{State: "confirmed", WinnerEvidenceSHA256: hash}, []*record.Record{nil, battery}, nil)
	if !strings.Contains(note, "no document") {
		t.Fatal(note)
	}
	document := &record.Record{Completion: &record.CompletionReceipt{}}
	selected, note := winnerRecords(plan, role.ConfirmationReport{State: "confirmed", WinnerEvidenceSHA256: hash}, []*record.Record{nil, battery}, []*record.Record{nil, document})
	if note != "" || len(selected) != 2 || selected[0] != battery || selected[1] != document {
		t.Fatalf("note %q selected %d", note, len(selected))
	}
}

func TestConfirmedDelegationRefusesAnUnresolvedStore(t *testing.T) {
	records := record.Store{Dir: t.TempDir()}
	sources, notes := confirmedDelegationSources(automation.Plan{}, automation.State{Outcome: "failed"}, records, time.Now())
	if sources != nil || len(notes) != 1 || notes[0] != "nothing was certified" {
		t.Fatalf("%v %v", sources, notes)
	}
	sources, notes = confirmedDelegationSources(automation.Plan{}, automation.State{Outcome: "adopted"}, records, time.Now())
	if sources != nil || !strings.Contains(notes[0], "no sealed plan") {
		t.Fatal(notes)
	}
	state := automation.State{Confirmation: &role.ConfirmationPlan{}, ConfirmationStore: &record.ManagedStoreRef{}}
	sources, notes = confirmedDelegationSources(automation.Plan{}, state, records, time.Now())
	if sources != nil || !strings.Contains(notes[0], "could not be resolved") {
		t.Fatal(notes)
	}
	if got := sourcesFromRecords([]*record.Record{{}}, true); len(got) != 0 {
		t.Fatalf("an incomplete record became a source: %+v", got)
	}
}

func TestConfirmationContextPointsFailWithoutASchedule(t *testing.T) {
	points, err := confirmationContextPoints(automation.Plan{}, automation.State{Confirmation: &role.ConfirmationPlan{}}, record.Store{})
	if err != nil || points != nil {
		t.Fatalf("battery-only %v %v", points, err)
	}
	state := automation.State{Confirmation: &role.ConfirmationPlan{ContextPolicy: &contextquality.Policy{}}}
	_, err = confirmationContextPoints(automation.Plan{ID: "fit"}, state, record.Store{Dir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "context schedule") {
		t.Fatal(err)
	}
	_, err = confirmationContextPoints(automation.Plan{ID: "fit", ContextPolicy: &contextquality.Policy{}}, state, record.Store{Dir: t.TempDir()})
	if err == nil {
		t.Fatal("opened a context store that was not created")
	}
}
