package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/render"
)

func TestViewedAndBoardJSONKeepDecidingWithdrawalEvidence(t *testing.T) {
	result := golden(t)
	result.Withdrawal = &eval.ToolLoopResult{
		Outcome: eval.OutcomeFail, Looped: true, Ended: "turn_cap", Calls: 10,
		Repeats: 6, Sequence: "RKWRWWWWWW",
	}
	sealCurrentResult(t, result)
	card, _ := viewedPresentation(result)
	want := analysis.PresentVerdict("tool_restraint", card.Needs["tool_restraint"])
	if want.State != "FAIL" || !strings.HasPrefix(want.Why, "withdrawal;") {
		t.Fatalf("fixture does not reproduce the failed withdrawal: %+v", want)
	}
	brief, code := captureTopStdout(t, func() int { return writeViewedJSON(result, false) })
	if code != exitOK {
		t.Fatalf("view JSON exit=%d: %s", code, brief)
	}
	var view struct {
		Verdicts map[string]analysis.VerdictPresentation `json:"verdicts"`
	}
	if err := json.Unmarshal([]byte(brief), &view); err != nil {
		t.Fatal(err)
	}
	if view.Verdicts["tool_restraint"].Why != want.Why {
		t.Errorf("view JSON hides the deciding evidence: %s", brief)
	}
	row := makeBoardRow(result, &boardExclusions{})
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &view); err != nil {
		t.Fatal(err)
	}
	if view.Verdicts["tool_restraint"].Why != want.Why {
		t.Errorf("board JSON hides the deciding evidence: %s", encoded)
	}
	var board strings.Builder
	render.WriteBoard(&board, render.Board{Results: 1, Groups: []render.BoardGroup{{Rows: []render.BoardRow{row}}}}, "plain")
	if !strings.Contains(board.String(), "[FAIL] leaves unused tools alone: withdrawal;") {
		t.Errorf("board hides failed quality behind performance: %s", board.String())
	}
	full, code := captureTopStdout(t, func() int { return writeViewedJSON(result, true) })
	var fullValue, sealedValue any
	if err := json.Unmarshal([]byte(full), &fullValue); err != nil {
		t.Fatal(err)
	}
	sealed, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(sealed, &sealedValue); err != nil {
		t.Fatal(err)
	}
	fullBytes, _ := json.Marshal(fullValue)
	sealedBytes, _ := json.Marshal(sealedValue)
	if code != exitOK || string(fullBytes) != string(sealedBytes) {
		t.Fatal("full export changed the sealed receipt")
	}
}
