package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

func TestBoardDoesNotLeadWithTheFasterModel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	slow, fast := golden(t), golden(t)
	slow.Model, slow.Scorecard.Model = "aa", "aa"
	fast.Model, fast.Scorecard.Model = "zz", "zz"
	for i := range slow.Speed {
		slow.Speed[i].DecodeTPS = 1
	}
	for i := range fast.Speed {
		fast.Speed[i].DecodeTPS = 100
	}
	sealCurrentResult(t, slow, fast)
	saveCurrentResults(t, slow, fast)

	oldOut := os.Stdout
	reader, writer, _ := os.Pipe()
	os.Stdout = writer
	code := cmdBoard(context.Background(), []string{"--display", "plain"})
	writer.Close()
	os.Stdout = oldOut
	out, _ := io.ReadAll(reader)
	got := string(out)
	if code != exitOK {
		t.Fatalf("code = %d\n%s", code, got)
	}
	aa := strings.Index(got, "aa")
	zz := strings.Index(got, "zz")
	if aa < 0 || zz < 0 || aa > zz {
		t.Fatalf("faster model led the block:\n%s", got)
	}
}
