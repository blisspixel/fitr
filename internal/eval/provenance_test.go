package eval

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/blisspixel/fitr/internal/stats"
)

func TestBuiltinHashesAreStableAndCoverVersionSeparately(t *testing.T) {
	base := fstest.MapFS{
		"tasks/task.json":    &fstest.MapFile{Data: []byte(`{"id":"a"}`)},
		"tasks/version.json": &fstest.MapFile{Data: []byte(`{"spec_version":1}`)},
	}
	a, err := hashBuiltinCorpus(base)
	if err != nil {
		t.Fatal(err)
	}
	b, err := hashBuiltinCorpus(base)
	if err != nil {
		t.Fatal(err)
	}
	if a != b || !strings.HasPrefix(a.TaskSetSHA256, "sha256:") || len(a.TaskSetSHA256) != 71 {
		t.Fatalf("unstable or malformed hashes: %+v vs %+v", a, b)
	}

	versionChanged := fstest.MapFS{
		"tasks/task.json":    &fstest.MapFile{Data: []byte(`{"id":"a"}`)},
		"tasks/version.json": &fstest.MapFile{Data: []byte(`{"spec_version":2}`)},
	}
	v, err := hashBuiltinCorpus(versionChanged)
	if err != nil {
		t.Fatal(err)
	}
	if v.TaskSetSHA256 != a.TaskSetSHA256 || v.SpecSHA256 == a.SpecSHA256 {
		t.Fatalf("version change affected the wrong digests: base=%+v changed=%+v", a, v)
	}

	taskChanged := fstest.MapFS{
		"tasks/task.json":    &fstest.MapFile{Data: []byte(`{"id":"b"}`)},
		"tasks/version.json": &fstest.MapFile{Data: []byte(`{"spec_version":1}`)},
	}
	c, err := hashBuiltinCorpus(taskChanged)
	if err != nil {
		t.Fatal(err)
	}
	if c.TaskSetSHA256 == a.TaskSetSHA256 || c.SpecSHA256 == a.SpecSHA256 {
		t.Fatalf("task change did not affect both digests: base=%+v changed=%+v", a, c)
	}
}

func TestEmbeddedDefinitionHashesAreAvailable(t *testing.T) {
	hashes, err := BuiltinHashes()
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"task set": hashes.TaskSetSHA256,
		"spec":     hashes.SpecSHA256,
	} {
		if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
			t.Fatalf("%s hash = %q", name, value)
		}
	}
}

func TestEffectiveHashesCoverMergedUserChecks(t *testing.T) {
	spec, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	base, err := EffectiveHashes(spec)
	if err != nil {
		t.Fatal(err)
	}
	dup := *spec
	dup.Checks = append([]CheckSpec(nil), spec.Checks...)
	dup.Checks = append(dup.Checks, CheckSpec{ID: "local-receipt", Need: "user_tasks", Kind: "exact"})
	changed, err := EffectiveHashes(&dup)
	if err != nil {
		t.Fatal(err)
	}
	if changed.TaskSetSHA256 == base.TaskSetSHA256 || changed.SpecSHA256 == base.SpecSHA256 {
		t.Fatalf("merged user check did not alter effective hashes: base=%+v changed=%+v", base, changed)
	}
	if _, err := EffectiveHashes(nil); err == nil {
		t.Fatal("nil effective spec was accepted")
	}
}

func TestBuiltinJSONDecoderRejectsUnknownFieldsAndTrailingData(t *testing.T) {
	type definition struct {
		ID string `json:"id"`
	}
	for _, tc := range []struct {
		name string
		data string
	}{
		{"unknown", `{"id":"a","idd":"typo"}`},
		{"duplicate", `{"id":"shadow","id":"a"}`},
		{"trailing object", `{"id":"a"} {"id":"b"}`},
		{"trailing scalar", `{"id":"a"} true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got definition
			if err := decodeBuiltinJSON([]byte(tc.data), &got); err == nil {
				t.Fatalf("accepted %s", tc.data)
			}
		})
	}
	var got definition
	if err := decodeBuiltinJSON([]byte(" \n\t{\"id\":\"a\"}\n"), &got); err != nil || got.ID != "a" {
		t.Fatalf("strict valid decode = %+v, %v", got, err)
	}
}

func TestAdaptiveDecisionCapturesBoundaryAndCap(t *testing.T) {
	above, err := stats.GateSPRT(0.75)
	if err != nil {
		t.Fatal(err)
	}
	passes := 0
	for above.State() == stats.SPRTContinue && above.N < 100 {
		above.Add(true)
		passes++
	}
	receipt, err := CaptureAdaptiveDecision("structured_output", 0.75, 100, passes, above)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Decision != AdaptiveAboveGate || receipt.StopReason != "upper_boundary" || receipt.Trials != passes {
		t.Fatalf("boundary receipt = %+v", receipt)
	}

	capped, _ := stats.GateSPRT(0.75)
	capped.Add(true)
	inconclusive, err := CaptureAdaptiveDecision("structured_output", 0.75, 1, 1, capped)
	if err != nil {
		t.Fatal(err)
	}
	if inconclusive.Decision != AdaptiveInconclusive || inconclusive.StopReason != "trial_cap" {
		t.Fatalf("cap receipt = %+v", inconclusive)
	}
	if _, err := CaptureAdaptiveDecision("structured_output", 0.75, 1, 2, capped); err == nil {
		t.Fatal("accepted more passes than trials")
	}
}

func TestToolTerminationEvidenceSurvivesJSON(t *testing.T) {
	want := ToolLoopResult{
		Pass: false, Outcome: OutcomeInconclusive, Calls: 5, Repeats: 3,
		Looped: true, Ended: "clean_stop", Sequence: "RRRRT",
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got ToolLoopResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if err := got.ValidateTerminationEvidence(); err != nil {
		t.Fatal(err)
	}
	if got.Ended != want.Ended || got.Repeats != want.Repeats || !got.Looped || got.Sequence != want.Sequence {
		t.Fatalf("termination evidence changed: %+v", got)
	}
	got.Looped = false
	if err := got.ValidateTerminationEvidence(); err == nil {
		t.Fatal("accepted contradictory loop evidence")
	}
}
