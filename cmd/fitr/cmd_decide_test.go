package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/decision"
)

func TestDecideEvaluatesSavedEvidenceAndUsesDecisionExitStates(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("FITR_RESULTS", directory)
	result := mockResult("decision-model", 20, 1, 200, 10, 0, 0, 16, 16)
	saveCurrentResults(t, result)

	eligibleSpec := writeDecisionSpec(t, directory, "eligible.json", 0.5)
	output, code := captureTopStdout(t, func() int {
		return cmdDecide(context.Background(), []string{
			"decision-model", "--spec", eligibleSpec, "--display", "json",
		})
	})
	if code != exitOK {
		t.Fatalf("eligible exit=%d output=%s", code, output)
	}
	var evaluation decision.Evaluation
	if err := json.Unmarshal([]byte(output), &evaluation); err != nil {
		t.Fatal(err)
	}
	if evaluation.State != decision.DecisionEligible || evaluation.SpecName != "local coding" ||
		evaluation.Subject.ID == "" {
		t.Fatalf("eligible evaluation = %+v", evaluation)
	}

	unresolvedSpec := writeDecisionSpec(t, directory, "unresolved.json", 0.95)
	output, code = captureTopStdout(t, func() int {
		return cmdDecide(context.Background(), []string{
			"decision-model", "--spec", unresolvedSpec, "--display", "plain",
		})
	})
	if code != exitUnresolved || !strings.Contains(output, "DECISION  UNRESOLVED") ||
		!strings.Contains(output, "NEXT") || !strings.Contains(output, "95% interval") ||
		!strings.Contains(output, "why not") {
		t.Fatalf("unresolved exit=%d output=%q", code, output)
	}
}

func TestDecideRejectsMissingAndMalformedSpecs(t *testing.T) {
	stderr, code := captureTopStderr(t, func() int {
		return cmdDecide(context.Background(), []string{"model"})
	})
	if code != exitUsage || !strings.Contains(stderr, "--spec is required") {
		t.Fatalf("missing spec exit=%d stderr=%q", code, stderr)
	}

	path := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(path, []byte(`{"schema":"wrong"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, code = captureTopStderr(t, func() int {
		return cmdDecide(context.Background(), []string{"model", "--spec", path})
	})
	if code != exitError || !strings.Contains(stderr, "could not load decision specification") {
		t.Fatalf("malformed spec exit=%d stderr=%q", code, stderr)
	}
}

func writeDecisionSpec(t *testing.T, directory, name string, minimum float64) string {
	t.Helper()
	path := filepath.Join(directory, name)
	contents := `{
  "schema": "fitr.decision.spec.v1",
  "name": "local coding",
  "evidence_level": "decide",
  "requirements": [
    {"id": "context", "context": {"minimum_effective_tokens": 8192}},
    {"id": "json", "behavior": {"need": "structured_output", "minimum_rate": ` +
		jsonNumber(minimum) + `}}
  ]
}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func jsonNumber(value float64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
