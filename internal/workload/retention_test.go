package workload

import (
	"context"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/ollama"
)

func TestWorkloadRetainsNoRawPolicyDiagnostics(t *testing.T) {
	const marker = "PRIVATE_POLICY_MARKER"
	for _, content := range []string{
		`{"PRIVATE_POLICY_MARKER":1}`,
		`{"PRIVATE_POLICY_MARKER":1,"PRIVATE_POLICY_MARKER":2}`,
		`{"version":1e999999,"PRIVATE_POLICY_MARKER":1}`,
	} {
		t.Run(content, func(t *testing.T) {
			backend := &scriptedWorkflowBackend{responses: []ollama.Message{
				toolMessage("write_file", map[string]any{"path": policyFile, "content": content}),
				{Role: "assistant", Content: "DONE"},
			}}
			bundle, err := workloadTestPlan(t, 1).Run(context.Background(), backend)
			if err != nil {
				t.Fatal(err)
			}
			assertPrivateReceipt(t, bundle, marker)
		})
	}
}

func TestWorkloadHashesUnknownToolNamesWithoutLosingDuplicates(t *testing.T) {
	const marker = "PRIVATE_TOOL_MARKER"
	backend := &scriptedWorkflowBackend{responses: []ollama.Message{
		toolMessage(marker, map[string]any{}),
		toolMessage(marker+"_different", map[string]any{}),
		toolMessage(marker, map[string]any{}),
		{Role: "assistant", Content: "DONE"},
	}}
	bundle, err := workloadTestPlan(t, 1).Run(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if trial := bundle.Trials[0]; trial.DuplicateCalls != 1 || trial.AuthorityViolations != 3 {
		t.Fatalf("unknown tool accounting = %+v", trial)
	}
	assertPrivateReceipt(t, bundle, marker)
}

func TestWorkloadRecordsEmptyToolNameAsDenied(t *testing.T) {
	backend := &scriptedWorkflowBackend{responses: []ollama.Message{
		toolMessage("", map[string]any{}), {Role: "assistant", Content: "DONE"},
	}}
	bundle, err := workloadTestPlan(t, 1).Run(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Trials[0].AuthorityViolations != 1 || bundle.Trials[0].Outcome != OutcomeRejected {
		t.Fatalf("empty tool outcome = %+v", bundle.Trials[0])
	}
}

func assertPrivateReceipt(t *testing.T, bundle Bundle, marker string) {
	t.Helper()
	data, err := bundle.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), marker) {
		t.Fatal("raw model text survived in a hash-retention bundle")
	}
	if bundle.Trials[0].Outcome != OutcomeRejected {
		t.Fatal("malformed or unauthorized work was accepted")
	}
	roundTripWorkloadBundle(t, bundle)
}
