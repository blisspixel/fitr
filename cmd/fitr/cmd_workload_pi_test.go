package main

import (
	"context"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/workload"
)

func TestWorkloadWorkflowFlagKeepsItsValue(t *testing.T) {
	command, code, ok := parseWorkloadCommand([]string{"model", "--workflow", "pi-workspace"})
	if !ok || code != exitOK {
		t.Fatalf("parse = ok %v code %d", ok, code)
	}
	if command.workflow != "pi-workspace" || len(command.positional) != 1 || command.positional[0] != "model" {
		t.Fatalf("command = %+v", command)
	}
}

func TestHarborIsNotAWorkload(t *testing.T) {
	code := cmdExperimentWorkload(context.Background(), []string{"--workflow", "harbor", "model"})
	if code != exitUsage {
		t.Fatalf("harbor workflow exit = %d", code)
	}
}

func TestPiWorkspaceTextProjectsTheSealedProof(t *testing.T) {
	identity, err := record.NewModelIdentity("model", "model", "fake", "fake-runtime-v1",
		integrationDigest(), "", 1024)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := workload.NewPlan(identity, "device-key", 1, 12, 30, 8192)
	if err != nil {
		t.Fatal(err)
	}
	pi, err := workload.NewPiPlan(identity, "device-key", 1, 30, 8192, workload.PiProviderLocal)
	if err != nil {
		t.Fatal(err)
	}
	policyText := workloadPlain(t, policy.Plan)
	piText := workloadPlain(t, pi.Plan)
	const policyProof = "  proof        deterministic assertion; retries not permitted; human wait and escalation unsupported\n"
	if !strings.Contains(policyText, policyProof) || strings.Contains(policyText, "does not launch Pi") {
		t.Fatalf("policy-repair text changed its sealed proof:\n%s", policyText)
	}
	if strings.Contains(piText, "deterministic assertion") ||
		!strings.Contains(piText, "independent verifier") ||
		!strings.Contains(piText, "forced-split-summary") ||
		!strings.Contains(piText, "this process does not launch Pi") {
		t.Fatalf("pi-workspace text restated a proof the receipt does not seal:\n%s", piText)
	}
	bare := workloadPlain(t, workload.Plan{Workflow: workload.WorkflowID, WorkflowVersion: workload.WorkflowVersion})
	if strings.Contains(bare, "proof") || strings.Contains(bare, "deterministic") {
		t.Fatalf("a plan with no contract grew a proof sentence:\n%s", bare)
	}
}

func TestWorkloadProofLabelFollowsTheSealedClass(t *testing.T) {
	cases := []struct {
		class workload.EvidenceClass
		want  string
	}{
		{workload.EvidenceDeterministic, "deterministic assertion"},
		{workload.EvidenceExternalState, "external system state"},
		{workload.EvidenceIndependent, "independent verifier"},
		{workload.EvidenceHarness, "harness state machine"},
		{workload.EvidenceHeuristic, "heuristic"},
		{workload.EvidenceModelJudged, "model judged"},
		{workload.EvidenceSelfReported, "self reported"},
		{workload.EvidenceExternalProtocol, "external protocol receipt"},
		{workload.EvidenceNone, "none"},
		{"not-a-class", "not-a-class"},
	}
	for _, tc := range cases {
		if got := workloadProofLabel(tc.class); got != tc.want {
			t.Fatalf("label(%q) = %q, want %q", tc.class, got, tc.want)
		}
	}
	undeclared := &workload.WorkflowContract{Proof: workload.EvidenceIndependent}
	if got := workloadProofText(undeclared); strings.Contains(got, "deterministic") ||
		!strings.Contains(got, "independent verifier") ||
		!strings.Contains(got, "retry policy not declared") ||
		!strings.Contains(got, "approval policy not declared") {
		t.Fatalf("undeclared policies were invented: %s", got)
	}
	other := &workload.WorkflowContract{
		Proof: workload.EvidenceExternalState, RetryPolicy: "twice", ApprovalPolicy: "human",
		ContextPolicy: "measured", CompactionPolicy: "manual",
	}
	if got := workloadProofText(other); got != "external system state; retry twice; approval human; compaction manual" {
		t.Fatalf("sealed policies = %s", got)
	}
	if got := workloadContextText(other); got != "measured; effective context is not established" {
		t.Fatalf("context = %s", got)
	}
	if got := workloadContextText(&workload.WorkflowContract{}); got != "context policy not declared; effective context is not established" {
		t.Fatalf("missing context = %s", got)
	}
}

func workloadPlain(t *testing.T, plan workload.Plan) string {
	t.Helper()
	plain, code := captureTopStdout(t, func() int {
		return renderWorkloadExperiment(workload.Bundle{Plan: plan}, "plain")
	})
	if code != exitOK {
		t.Fatalf("plain workload exit=%d output=%s", code, plain)
	}
	return plain
}

func TestPinnedSessionRejectsAForeignTurnCap(t *testing.T) {
	args := []string{"model", "--workflow", "pi-workspace", "--max-turns", "4"}
	if code := cmdExperimentWorkload(context.Background(), args); code != exitUsage {
		t.Fatalf("foreign turn cap exit = %d", code)
	}
}
