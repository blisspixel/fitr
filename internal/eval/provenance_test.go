package eval

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
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

func TestLegacyWaldAdaptiveReceiptRemainsReadable(t *testing.T) {
	receipt := AdaptiveDecision{
		Need: "structured_output", Method: AdaptiveMethodWaldSPRT,
		Gate: 0.75, NullRate: 0.65, AltRate: 0.85,
		Alpha: 0.05, Beta: 0.05, MaxTrials: 1, Trials: 1,
		Passes: 1, Failures: 0, LogRatio: 0.2,
		Decision: AdaptiveInconclusive, StopReason: "trial_cap",
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("legacy Wald receipt became unreadable: %v", err)
	}
	b, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"need":"structured_output","method":"wald_sprt_v1","gate":0.75,` +
		`"null_rate":0.65,"alternative_rate":0.85,"alpha":0.05,"beta":0.05,` +
		`"max_trials":1,"trials":1,"passes":1,"failures":0,` +
		`"log_likelihood_ratio":0.2,"decision":"inconclusive","stop_reason":"trial_cap"}`
	if string(b) != want {
		t.Fatalf("legacy Wald wire shape changed:\n got %s\nwant %s", b, want)
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
