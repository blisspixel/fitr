package main

import (
	"context"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
)

func TestExcludedRunDoesNotOfferTheBoard(t *testing.T) {
	res := &Result{
		SchemaVersion: record.EvidenceSchemaVersion,
		Model:         "tiny:1b", Level: "quick", Repeats: 1, NumCtx: 8192,
	}
	command := runCommand{mode: "plain"}
	command.level = "quick"
	command.reps = 1
	stderr, _ := captureTopStderr(t, func() int {
		writeRunNextSteps(context.Background(), nil, command, res, "saved.json", "")
		return 0
	})
	if nextBoard(stderr) {
		t.Fatalf("excluded run offered the board:\n%s", stderr)
	}
	if labeledField(stderr, "next") != 0 {
		t.Fatalf("excluded run printed a next command:\n%s", stderr)
	}
	if !strings.Contains(stderr, "fitr run tiny:1b -k 3") {
		t.Fatalf("excluded run hid the repeat command:\n%s", stderr)
	}
}

func TestCardAndTrailerShareOneNext(t *testing.T) {
	t.Setenv("FITR_WIDTH", "80")
	t.Setenv("NO_COLOR", "1")
	res := reportGateFixture(t, "clean")
	meta := resultMeta(res, res.Profile)
	if meta.Analysis == nil || len(meta.Analysis.NextActions) != 1 {
		t.Fatalf("fixture next actions = %+v", meta.Analysis)
	}
	want := analysis.FormatArgv(meta.Analysis.NextActions[0].Argv, res.Scorecard.Model)
	stdout, _ := captureTopStdout(t, func() int {
		disp := render.New("plain")
		render.MarkRunCard(disp)
		disp.Result(res.Scorecard, meta)
		return 0
	})
	command := runCommand{mode: "plain"}
	command.level = res.Level
	command.reps = res.Repeats
	stderr, _ := captureTopStderr(t, func() int {
		writeRunNextSteps(context.Background(), nil, command, res, "saved.json", "")
		return 0
	})
	if labeledField(stdout, "next") != 1 || !strings.Contains(stdout, want) {
		t.Fatalf("card next want %q:\n%s", want, stdout)
	}
	if labeledField(stderr, "next") != 0 {
		t.Fatalf("trailer added a second next:\n%s", stderr)
	}
}

func nextBoard(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "next" && fields[1] == "fitr" && strings.Contains(line, "board") {
			return true
		}
	}
	return false
}

func labeledField(text, label string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == label && (len(fields) < 2 || fields[1] != "experiment") {
			n++
		}
	}
	return n
}
