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

func TestDecideTextNamesRequestedEffortAndDoesNotFillADefault(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("FITR_RESULTS", directory)
	saveCurrentResults(t, mockResult("decision-model", 20, 1, 200, 10, 0, 0, 16, 16))
	asked := writeReasoningSpec(t, directory, "asked.json", `"reasoning": {"effort": "low"},`)
	output, code := captureTopStdout(t, func() int {
		return cmdDecide(context.Background(), []string{"decision-model", "--spec", asked, "--display", "plain"})
	})
	if code != exitUnresolved || !decisionField(output, "requested", "low") || !decisionField(output, "application", "unknown") || !strings.Contains(output, "record_reasoning_application") || strings.Contains(output, "\n  experiment \n") {
		t.Fatalf("requested effort was not named: exit=%d output=%q", code, output)
	}
	if !strings.Contains(output, "effort comparison is not qualified") {
		t.Fatalf("effort comparison was not named: %s", output)
	}
	if decisionField(output, "requested", "xhigh") || decisionField(output, "application", "observed") {
		t.Fatalf("effort was certified or filled with a default: %s", output)
	}
	omitted := writeReasoningSpec(t, directory, "omitted.json", `"reasoning": {},`)
	output, code = captureTopStdout(t, func() int {
		return cmdDecide(context.Background(), []string{"decision-model", "--spec", omitted, "--display", "plain"})
	})
	if code != exitUnresolved || !decisionField(output, "requested", "omitted") || decisionField(output, "requested", "low") {
		t.Fatalf("omitted effort was filled: exit=%d output=%q", code, output)
	}
	plain := writeDecisionSpec(t, directory, "plain.json", 0.5)
	output, _ = captureTopStdout(t, func() int {
		return cmdDecide(context.Background(), []string{"decision-model", "--spec", plain, "--display", "plain"})
	})
	if strings.Contains(output, "REASONING") {
		t.Fatalf("a spec without reasoning grew a reasoning section: %s", output)
	}
}

func decisionField(output, label, value string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == label && fields[1] == value {
			return true
		}
	}
	return false
}

func writeReasoningSpec(t *testing.T, directory, name, reasoning string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	contents := `{
  "schema": "fitr.decision.spec.v1",
  "name": "effort",
  "evidence_level": "decide",
  ` + reasoning + `
  "requirements": [{"id": "context", "context": {"minimum_effective_tokens": 8192}}]
}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
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
