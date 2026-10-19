package reference

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDifferentManifestDoesNotCompare(t *testing.T) {
	left := sealedPlan(t, "2024-01-01", fakeSHA("a"))
	draft := left
	draft.Instances = append([]Instance(nil), left.Instances...)
	draft.Instances[0].ImageDigest = fakeSHA("other-image")
	draft.PlanSHA256 = ""
	draft.ManifestSHA256 = ""
	draft.HarnessBindingSHA256 = ""
	right, err := NewPlan(draft)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := Compare(mustAnalyze(t, left, rowsLike(left, OutcomeResolved)), mustAnalyze(t, right, rowsLike(right, OutcomeResolved)))
	if err != nil {
		t.Fatal(err)
	}
	if pair.Comparable || !strings.Contains(pair.Reason, "manifest") {
		t.Fatalf("pair = %+v", pair)
	}
	raw, err := json.Marshal(pair)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"feeds_fit":false`) || strings.Contains(string(raw), `"feeds_fit":true`) {
		t.Fatalf("pair claimed a fit feed: %s", raw)
	}
}

func TestSameScheduleReportsDiscordantCounts(t *testing.T) {
	leftPlan := sealedPlan(t, "2024-01-01", fakeSHA("a"))
	rightPlan := sealedPlan(t, "2024-01-01", fakeSHA("b"))
	leftRows := rowsLike(leftPlan, OutcomeResolved)
	rightRows := rowsLike(rightPlan, OutcomeResolved)
	rightRows[0].Verdict = OutcomeUnresolved
	rightRows[1].Verdict = OutcomeTimeout
	pair, err := Compare(mustAnalyze(t, leftPlan, leftRows), mustAnalyze(t, rightPlan, rightRows))
	if err != nil {
		t.Fatal(err)
	}
	if !pair.Comparable || pair.LeftOnly+pair.RightOnly != 1 || pair.Discordant != 1 || pair.Excluded != 1 || pair.Shared != 23 {
		t.Fatalf("pair = %+v", pair)
	}
	if pair.Separable || pair.PExact != nil || pair.FeedsPerformance || pair.FeedsRoleFloor || pair.FeedsGlobalScore {
		t.Fatalf("pair overclaimed: %+v", pair)
	}
	note := FormatPair(pair)
	if !strings.Contains(note, "1 discordant") || !strings.Contains(note, "not a fit or performance claim") {
		t.Fatalf("note = %s", note)
	}
	leftWant, rightWant := fakeSHA("a"), fakeSHA("b")
	if leftWant > rightWant {
		leftWant, rightWant = rightWant, leftWant
	}
	if pair.LeftArtifact != leftWant || pair.RightArtifact != rightWant {
		t.Fatalf("left/right = %s %s", pair.LeftArtifact, pair.RightArtifact)
	}
}

func TestSeedSetMismatchStaysUncompared(t *testing.T) {
	left := sealedPlan(t, "2024-01-01", fakeSHA("a"))
	draft := left
	draft.SeedSet = "fedcba9876543210fedcba9876543210"
	draft.PlanSHA256 = ""
	draft.ManifestSHA256 = ""
	draft.HarnessBindingSHA256 = ""
	right, err := NewPlan(draft)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := Compare(mustAnalyze(t, left, rowsLike(left, OutcomeResolved)), mustAnalyze(t, right, rowsLike(right, OutcomeResolved)))
	if err != nil {
		t.Fatal(err)
	}
	if pair.Comparable || !strings.Contains(pair.Reason, "seed set") || pair.Discordant != 0 {
		t.Fatalf("pair = %+v", pair)
	}
}

func TestThreeRunsAreNotReducedToAWinner(t *testing.T) {
	one := analyzedArtifact(t, "a")
	two := analyzedArtifact(t, "b")
	three := analyzedArtifact(t, "c")
	notes := SeriesNotes([]Result{one, two, three})
	if len(notes) != 1 || !strings.Contains(notes[0], "name two files") {
		t.Fatalf("notes = %v", notes)
	}
}

func analyzedArtifact(t *testing.T, name string) Result {
	t.Helper()
	plan := sealedPlan(t, "2024-01-01", fakeSHA(name))
	return mustAnalyze(t, plan, rowsLike(plan, OutcomeResolved))
}
