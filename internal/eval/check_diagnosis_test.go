package eval

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestFailedCheckRecordsDiagnosisWithoutTheReply(t *testing.T) {
	backend := &fakeBackend{genTexts: []string{"not the answer"}}
	spec := CheckSpec{ID: "exact-one", Family: "static", Need: "instruction_precision", Origin: "builtin", Params: map[string]any{
		"prompt": "Say yes.", "canon": "yes",
		"grader": map[string]any{"type": "exact", "expected": "yes"},
	}}
	out, err := RunCheck(context.Background(), backend, "model", spec, 9)
	if err != nil {
		t.Fatal(err)
	}
	if out.Pass || out.Outcome != OutcomeFail || out.InputSHA256 == "" || out.CanonicalSHA256 == "" || out.ParserOutcome != "text-grade" || out.Verifier != "fitr.eval.grade/v1" || out.RawRetained {
		t.Fatalf("%+v", out)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "not the answer") || strings.Contains(string(encoded), "Say yes.") {
		t.Fatalf("diagnosis stored private text: %s", encoded)
	}
	passed, err := RunCheck(context.Background(), &fakeBackend{genTexts: []string{"yes"}}, "model", spec, 9)
	if err != nil || !passed.Pass || passed.InputSHA256 != "" || passed.Verifier != "" {
		t.Fatalf("%+v %v", passed, err)
	}
	data, err := json.Marshal(passed)
	if err != nil || strings.Contains(string(data), "input_sha256") {
		t.Fatalf("pass gained diagnosis: %s", data)
	}
}

func TestFailureDetailKeepsTheFieldAndNotTheReply(t *testing.T) {
	parser, field := classifyFailureDetail(`missing_param: "batch"`, true)
	if parser != "missing_param" || field != "batch" {
		t.Fatalf("%s %s", parser, field)
	}
}

func TestToolFailureDiagnosisOmitsModelSuppliedText(t *testing.T) {
	for _, detail := range []string{
		`wrong_value: "batch" = "private reply", want "expected"`,
		`wrong_name: called "private reply", expected "lookup"`,
		`extra_param: "private reply" was not in the schema`,
		`extra_calls: made 2 calls where one was required (private reply)`,
		`bad_json: arguments are not a JSON object (private reply)`,
	} {
		t.Run(strings.Split(detail, ":")[0], func(t *testing.T) {
			out := CheckOutcome{Outcome: OutcomeFail, Detail: detail}
			noteCheckFailure(&out, "prompt", "canon", true)
			encoded, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "private reply") {
				t.Fatalf("model output retained in diagnosis: %s", encoded)
			}
		})
	}
}
