package eval

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Every check family must be self-consistent: the canonical correct answer it
// computes must pass its own grader, and garbage must not. This is the entire
// battery tested without a model - a grader that rejects right answers would
// report model failures that are harness bugs, which is the sin this repo
// exists to avoid.
func TestEveryCheckGradesItsOwnCanon(t *testing.T) {
	s, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Checks) < 16 {
		t.Fatalf("expected the full check battery, got %d", len(s.Checks))
	}
	for _, cs := range s.Checks {
		for _, seed := range []uint64{1, 7, 42, 1234, 99999} {
			inst := Generate(cs, seed)
			if strings.TrimSpace(inst.Prompt) == "" {
				t.Fatalf("%s seed %d: empty prompt", cs.ID, seed)
			}
			if strings.TrimSpace(inst.Canon) == "" {
				t.Fatalf("%s seed %d: no canonical answer to self-test with", cs.ID, seed)
			}
			if ok, why := inst.Grade(inst.Canon); !ok {
				t.Errorf("%s seed %d: grader rejects its own canonical answer: %s", cs.ID, seed, why)
			}
			if ok, _ := inst.Grade("I cannot help with that request."); ok {
				t.Errorf("%s seed %d: grader accepts a refusal", cs.ID, seed)
			}
			if ok, _ := inst.Grade(""); ok {
				t.Errorf("%s seed %d: grader accepts empty output", cs.ID, seed)
			}
		}
	}
}

// A canonical answer wrapped in a fence must still pass for JSON-shaped
// families: every real consumer strips a single fence, so the grader does too.
func TestFencedCanonStillPasses(t *testing.T) {
	s, _ := LoadSpec()
	for _, cs := range s.Checks {
		if cs.Need != "structured_output" {
			continue
		}
		inst := Generate(cs, 42)
		fenced := "```json\n" + inst.Canon + "\n```"
		if ok, why := inst.Grade(fenced); !ok {
			t.Errorf("%s: fenced canonical answer rejected: %s", cs.ID, why)
		}
		buried := "Here is the JSON you asked for:\n\n" + inst.Canon + "\n\nLet me know if you need anything else!"
		if ok, _ := inst.Grade(buried); ok {
			t.Errorf("%s: JSON buried in prose must fail - no pipeline can consume it", cs.ID)
		}
	}
}

// The seed IS the instance: same seed, same prompt; different seeds must
// actually vary the instantiation, or the contamination argument is theater.
func TestSeedsAreReproducibleAndVarying(t *testing.T) {
	s, _ := LoadSpec()
	for _, cs := range s.Checks {
		a, b := Generate(cs, 42), Generate(cs, 42)
		if a.Prompt != b.Prompt || a.Canon != b.Canon {
			t.Fatalf("%s: same seed produced different instances", cs.ID)
		}
		varied := false
		base := Generate(cs, 1)
		for _, seed := range []uint64{2, 3, 4, 5, 6} {
			if Generate(cs, seed).Prompt != base.Prompt {
				varied = true
				break
			}
		}
		if !varied {
			t.Errorf("%s: six seeds produced one prompt - the family is not actually parameterized", cs.ID)
		}
	}
}

func TestInstanceSeedFoldsInRepAndTask(t *testing.T) {
	a := InstanceSeed("run1", "math_chain", 0)
	if InstanceSeed("run1", "math_chain", 1) == a {
		t.Fatal("repeat index must change the seed - repeats are independent trials")
	}
	if InstanceSeed("run1", "date_math", 0) == a {
		t.Fatal("task id must change the seed")
	}
	if InstanceSeed("run2", "math_chain", 0) == a {
		t.Fatal("run id must change the seed")
	}
	if InstanceSeed("run1", "math_chain", 0) != a {
		t.Fatal("the seed must be reproducible from its inputs")
	}
}

// The battery's composition is a design decision: weighted toward structured
// output, because that is what quantization breaks first.
func TestBatteryComposition(t *testing.T) {
	s, _ := LoadSpec()
	byNeed := map[string]int{}
	for _, cs := range s.Checks {
		byNeed[cs.Need]++
	}
	if byNeed["structured_output"] < 6 {
		t.Errorf("structured_output has %d checks; the battery is deliberately weighted toward it", byNeed["structured_output"])
	}
	if byNeed["instruction_precision"] < 3 {
		t.Errorf("instruction_precision has %d checks, want >=3", byNeed["instruction_precision"])
	}
	if byNeed["reasoning"] < 4 {
		t.Errorf("reasoning has %d checks, want >=4", byNeed["reasoning"])
	}
}

func TestValidateCheckRejectsBadSpecs(t *testing.T) {
	good := CheckSpec{ID: "x", Kind: "check", Need: "reasoning", Family: "math_chain", NumPredict: 100}
	if err := ValidateCheck(good); err != nil {
		t.Fatalf("valid check rejected: %v", err)
	}
	for name, cs := range map[string]CheckSpec{
		"no id":          {Kind: "check", Need: "reasoning", Family: "math_chain", NumPredict: 100},
		"wrong kind":     {ID: "x", Kind: "execute", Need: "reasoning", Family: "math_chain", NumPredict: 100},
		"unknown family": {ID: "x", Kind: "check", Need: "reasoning", Family: "nope", NumPredict: 100},
		"unknown need":   {ID: "x", Kind: "check", Need: "vibes", Family: "math_chain", NumPredict: 100},
		"no num_predict": {ID: "x", Kind: "check", Need: "reasoning", Family: "math_chain"},
		"static, no prompt": {ID: "x", Kind: "check", Need: "user_tasks", Family: "static", NumPredict: 100,
			Params: map[string]any{"grader": map[string]any{"type": "exact", "expected": "4"}}},
		"static, bad grader": {ID: "x", Kind: "check", Need: "user_tasks", Family: "static", NumPredict: 100,
			Params: map[string]any{"prompt": "2+2?", "grader": map[string]any{"type": "llm_judge"}}},
	} {
		if err := ValidateCheck(cs); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

func TestMergeChecksRejectsCollisions(t *testing.T) {
	builtin := []CheckSpec{{ID: "math_chain"}}
	if _, err := MergeChecks(builtin, []CheckSpec{{ID: "math_chain"}}); err == nil {
		t.Fatal("id collision must be rejected - two tasks would silently pool into one interval")
	}
	out, err := MergeChecks(builtin, []CheckSpec{{ID: "mine"}})
	if err != nil || len(out) != 2 {
		t.Fatalf("merge failed: %v", err)
	}
}

func TestStaticFamilyGraders(t *testing.T) {
	mk := func(grader map[string]any) Instance {
		return Generate(CheckSpec{
			ID: "t", Kind: "check", Need: "user_tasks", Family: "static", NumPredict: 50,
			Params: map[string]any{"prompt": "p", "grader": grader},
		}, 1)
	}
	cases := []struct {
		name   string
		grader map[string]any
		pass   []string
		fail   []string
	}{
		{"exact", map[string]any{"type": "exact", "expected": "Paris"},
			[]string{"Paris", "  Paris\n"}, []string{"paris", "Paris is the capital"}},
		{"exact ci", map[string]any{"type": "exact", "expected": "Paris", "case_insensitive": true},
			[]string{"paris", "PARIS"}, []string{"London"}},
		{"contains", map[string]any{"type": "contains", "expected": "42"},
			[]string{"the answer is 42, obviously"}, []string{"forty-two"}},
		{"regex", map[string]any{"type": "regex", "pattern": `^\d{4}-\d{2}-\d{2}$`},
			[]string{"2026-08-18"}, []string{"18 Aug 2026"}},
		{"json", map[string]any{"type": "json_object", "expected_object": map[string]any{"a": float64(1), "b": "x"}},
			[]string{`{"a":1,"b":"x"}`, "```json\n{\"b\":\"x\",\"a\":1}\n```"},
			[]string{`{"a":1}`, `{"a":1,"b":"x","c":2}`, `not json`}},
		{"number", map[string]any{"type": "number", "expected_number": float64(12.5), "tolerance": float64(0.01)},
			[]string{"Answer: 12.5", "12.50"}, []string{"Answer: 13", "no digits"}},
	}
	for _, tc := range cases {
		inst := mk(tc.grader)
		for _, s := range tc.pass {
			if ok, why := inst.Grade(s); !ok {
				t.Errorf("%s: %q should pass: %s", tc.name, s, why)
			}
		}
		for _, s := range tc.fail {
			if ok, _ := inst.Grade(s); ok {
				t.Errorf("%s: %q should fail", tc.name, s)
			}
		}
	}
}

func TestUserChecksLoadFromDir(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := writeFile(dir, name, body); err != nil {
			t.Fatal(err)
		}
	}
	write("mine.json", `{
	  "id": "my_task", "kind": "check", "family": "static", "num_predict": 50,
	  "why": "my own workload",
	  "params": {"prompt": "What is 2+2?", "grader": {"type": "number", "expected_number": 4}}
	}`)
	got, err := LoadUserChecks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "my_task" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Need != "user_tasks" {
		t.Fatalf("need must default to user_tasks, got %q", got[0].Need)
	}
	if got[0].Origin != "user" {
		t.Fatalf("origin must be set by the loader, got %q", got[0].Origin)
	}

	write("broken.json", `{"id": "oops", "kind": "check"`)
	if _, err := LoadUserChecks(dir); err == nil {
		t.Fatal("a malformed user task must be a hard error, not a silently dropped task")
	}
}

func TestUserChecksRejectExecutableIntent(t *testing.T) {
	tests := map[string]string{
		"case-insensitive kind": `{"id":"unsafe","kind":" EXEC "}`,
	}
	for kind := range executableUserTaskKinds {
		tests["kind "+kind] = `{"id":"unsafe","kind":` + strconv.Quote(kind) + `}`
	}
	for _, field := range executableUserTaskFields {
		tests["field "+field] = `{"id":"unsafe","kind":"check",` + strconv.Quote(field) + `:true}`
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := writeFile(dir, "unsafe.json", body); err != nil {
				t.Fatal(err)
			}
			got, err := LoadUserChecks(dir)
			if err == nil || got != nil {
				t.Fatalf("got checks=%v error=%v, want a hard unsafe-task error", got, err)
			}
			var unsafe *Failure
			if !errors.As(err, &unsafe) || unsafe.Kind != FailureUnsafeTask {
				t.Fatalf("error = %#v, want %q failure", err, FailureUnsafeTask)
			}
		})
	}
}

func TestUserChecksRejectUnknownTopLevelFields(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(dir, "typo.json", `{
	  "id":"typo", "kind":"check", "family":"static", "num_predict":20,
	  "runer":["python"],
	  "params":{"prompt":"Reply OK", "grader":{"type":"exact", "expected":"OK"}}
	}`); err != nil {
		t.Fatal(err)
	}
	got, err := LoadUserChecks(dir)
	if err == nil || got != nil {
		t.Fatalf("got checks=%v error=%v, want unknown-field error", got, err)
	}
	if !strings.Contains(err.Error(), `unknown field "runer"`) {
		t.Fatalf("error = %q, want the unknown field named", err)
	}
	var invalid *Failure
	if !errors.As(err, &invalid) || invalid.Kind != FailureInvalidSpec {
		t.Fatalf("error = %#v, want %q failure", err, FailureInvalidSpec)
	}
}

func writeFile(dir, name, body string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
}

func TestMissingUserDirIsNotAnError(t *testing.T) {
	got, err := LoadUserChecks(`Z:\does\not\exist\anywhere`)
	if err != nil || got != nil {
		t.Fatalf("missing dir: got %v, %v", got, err)
	}
}

func TestUserChecksRejectOversizedFile(t *testing.T) {
	dir := t.TempDir()
	body := `{"id":"large"}` + strings.Repeat(" ", maxUserCheckBytes)
	if err := writeFile(dir, "large.json", body); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadUserChecks(dir); err == nil || got != nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized task = %v, %v", got, err)
	}
}
