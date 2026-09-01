package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/experiment"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/workload"
)

func TestContextPlanBindsSealedPointOrderAndAllocation(t *testing.T) {
	contexts := []int{8192, 4096}
	plan, digest, err := experiment.NewContextPlan("context-model", contexts, 3)
	if err != nil {
		t.Fatal(err)
	}
	firstBinding := experiment.ContextPlanBinding(digest, 1, len(contexts))
	secondBinding := experiment.ContextPlanBinding(digest, 2, len(contexts))
	first := contextMockResult(t, contexts[0], &firstBinding, "2026-08-31T10:00:00Z", plan)
	second := contextMockResult(t, contexts[1], &secondBinding, "2026-08-31T11:00:00Z", plan)
	report, err := experiment.AnalyzePlannedContext([]*Result{first, second}, plan, digest)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Predeclared || !report.Comparison.Ready || len(report.Points) != 2 {
		t.Fatalf("planned context report = %+v", report)
	}
	if report.Points[0].Context.Requested != 4096 || report.Points[0].Sequence != 2 ||
		report.Points[1].Context.Requested != 8192 || report.Points[1].Sequence != 1 {
		t.Fatalf("sorted points lost declared sequence: %+v", report.Points)
	}
	if len(report.Coverage.AllocationContexts) != 2 || report.Coverage.MaximumAllocationContext == nil ||
		*report.Coverage.MaximumAllocationContext != 8192 {
		t.Fatalf("allocation coverage = %+v", report.Coverage)
	}
	bundle, err := experiment.NewContextBundle(plan, digest, []*Result{first, second})
	if err != nil {
		t.Fatal(err)
	}
	bundleData, err := bundle.JSON()
	if err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), "context-bundle.json")
	if err := os.WriteFile(bundlePath, bundleData, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := experiment.LoadContextBundle(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt, err := loaded.Validate(); err != nil || !rebuilt.Predeclared {
		t.Fatalf("reloaded bundle report = %+v, %v", rebuilt, err)
	}
	loaded.Report.Comparison.Ready = false
	if _, err := loaded.Validate(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mutated bundle report error = %v", err)
	}

	second.Memory.RequestedCtx = 32768
	if _, err := experiment.AnalyzePlannedContext([]*Result{first, second}, plan, digest); err == nil ||
		!strings.Contains(err.Error(), "allocation was requested") {
		t.Fatalf("mutated allocation context error = %v", err)
	}
}

func TestContextExperimentRejectsFactorChangesWithoutDroppingPoints(t *testing.T) {
	first := contextMockResult(t, 4096, nil, "2026-08-31T10:00:00Z")
	second := mockResult("context-model", 20, 1, 200, 10, 0, 0, 16, 16)
	second.NumCtx = 8192
	second.SeedSet = "retrospective-b"
	second.StartedAt = "2026-08-31T11:00:00Z"
	second.Device.GPU = "different-gpu"
	second.Memory.ResidentGB = 6
	second.Memory.RequestedCtx = 8192
	if err := prepareMockEvidence(second); err != nil {
		t.Fatal(err)
	}
	report, err := experiment.AnalyzeContext([]*Result{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if report.Comparison.Ready || len(report.Points) != 2 || report.NextAction == nil ||
		report.NextAction.Code != "align_context_experiment_factors" {
		t.Fatalf("factor-change report = %+v", report)
	}
}

func TestExperimentContextReadsSealedPathsAndEmitsTypedJSON(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("FITR_RESULTS", directory)
	first := contextMockResult(t, 4096, nil, "2026-08-31T10:00:00Z")
	second := contextMockResult(t, 8192, nil, "2026-08-31T11:00:00Z")
	firstPath := writeReconciledResult(t, directory, "first.json", first)
	secondPath := writeReconciledResult(t, directory, "second.json", second)

	output, code := captureTopStdout(t, func() int {
		return cmdExperiment(context.Background(), []string{
			"context", firstPath, secondPath, "--display", "json",
		})
	})
	if code != exitOK {
		t.Fatalf("context experiment exit=%d output=%s", code, output)
	}
	var report experiment.ContextReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	if report.Schema != experiment.ContextReportSchema || !report.Comparison.Ready || report.Predeclared {
		t.Fatalf("context JSON = %+v", report)
	}
}

func TestExperimentQuantAppliesDecisionSpecAndEmitsExploratoryChoice(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("FITR_RESULTS", directory)
	first := quantMockResult(t, "quant-q8", "Q8_0", 18, "2026-08-31T10:00:00Z")
	second := quantMockResult(t, "quant-q4", "Q4_K_M", 30, "2026-08-31T11:00:00Z")
	firstPath := writeReconciledResult(t, directory, "q8.json", first)
	secondPath := writeReconciledResult(t, directory, "q4.json", second)
	specPath := writeQuantDecisionSpec(t, directory)

	output, code := captureTopStdout(t, func() int {
		return cmdExperiment(context.Background(), []string{
			"quant", firstPath, secondPath, "--spec", specPath, "--display", "json",
		})
	})
	if code != exitOK {
		t.Fatalf("quant experiment exit=%d output=%s", code, output)
	}
	var report experiment.QuantReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	if report.Schema != experiment.QuantReportSchema || !report.Comparison.Ready ||
		report.Objective == nil || report.Objective.State != "exploratory" || report.Lineage.Verified {
		t.Fatalf("quant report = %+v", report)
	}

	plain, code := captureTopStdout(t, func() int {
		return renderQuantExperiment(report, "plain")
	})
	if code != exitOK || !strings.Contains(plain, "CONSERVATIVE FRONTIER") ||
		!strings.Contains(plain, "configuration_comparison") {
		t.Fatalf("plain quant report exit=%d output=%s", code, plain)
	}
}

func TestExperimentConfirmationReopensFreshBoundBundle(t *testing.T) {
	firstTemplate := quantMockResult(t, "confirm-q8", "Q8_0", 18, "2026-08-31T10:00:00Z")
	secondTemplate := quantMockResult(t, "confirm-q4", "Q4_K_M", 30, "2026-08-31T11:00:00Z")
	spec := decision.DecisionSpec{
		Schema: decision.SpecSchema, Name: "confirmation CLI", Evidence: decision.EvidenceConfirm,
		Requirements: []decision.Requirement{{
			ID: "context", Context: &decision.ContextRequirement{MinimumEffectiveTokens: 4096},
		}},
		Objective: &decision.Objective{Metric: "decode_tps", Direction: decision.Maximize},
	}
	plan, err := experiment.NewConfirmationPlan([]record.ModelIdentity{
		firstTemplate.Manifest.Model, secondTemplate.Manifest.Model,
	}, firstTemplate.Device.Key(), spec, 8192, 3)
	if err != nil {
		t.Fatal(err)
	}
	first := confirmationMockResult(t, "confirm-q8", "Q8_0", 18,
		"2026-08-31T12:00:00Z", plan, 1)
	second := confirmationMockResult(t, "confirm-q4", "Q4_K_M", 30,
		"2026-08-31T13:00:00Z", plan, 2)
	bundle, err := experiment.NewConfirmationBundle(plan, spec, []*record.Record{first, second})
	if err != nil {
		t.Fatal(err)
	}
	data, err := bundle.JSON()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "confirmation-bundle")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	output, code := captureTopStdout(t, func() int {
		return cmdExperiment(context.Background(), []string{"confirm", path, "--display", "json"})
	})
	if code != exitOK {
		t.Fatalf("confirmation reopen exit=%d output=%s", code, output)
	}
	var reopened experiment.QuantReport
	if err := json.Unmarshal([]byte(output), &reopened); err != nil {
		t.Fatal(err)
	}
	if reopened.Stage != experiment.StageConfirm || reopened.Objective == nil ||
		reopened.Objective.State != "confirmed" {
		t.Fatalf("confirmation report = %+v", reopened)
	}
}

func TestExecuteConfirmationPlanCollectsFreshFullRuns(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	backend := &confirmationLiveBackend{runIntegrationBackend: &runIntegrationBackend{effectiveCtx: 8192}}
	identities := []record.ModelIdentity{
		confirmationIdentity(t, "confirm-a", confirmationDigestA),
		confirmationIdentity(t, "confirm-b", confirmationDigestB),
	}
	spec := decision.DecisionSpec{
		Schema: decision.SpecSchema, Name: "live confirmation", Evidence: decision.EvidenceConfirm,
		Requirements: []decision.Requirement{{
			ID: "context", Context: &decision.ContextRequirement{MinimumEffectiveTokens: 8192},
		}},
		Objective: &decision.Objective{Metric: "decode_tps", Direction: decision.Maximize},
	}
	fingerprint := device.Detect(context.Background(), backend)
	plan, err := experiment.NewConfirmationPlan(identities, fingerprint.Key(), spec, 8192, 3)
	if err != nil {
		t.Fatal(err)
	}
	output, code := captureTopStdout(t, func() int {
		return executeConfirmationPlan(context.Background(), backend, plan, spec, confirmationCommand{
			mode: "json", profile: "default", repeats: 3, ctx: 8192,
		})
	})
	if code != exitOK {
		t.Fatalf("live confirmation exit=%d output=%s", code, output)
	}
	var report experiment.QuantReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	if report.Objective == nil || report.Objective.State != "confirmed" ||
		report.Objective.Candidate == "" {
		t.Fatalf("live confirmation report = %+v", report)
	}
	files, err := filepath.Glob(filepath.Join(resultsDir(), ".experiments", "confirmation-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("confirmation bundles = %v, %v", files, err)
	}
}

func TestExperimentWorkloadReopensSignedBundleAndPreservesExitMeaning(t *testing.T) {
	identity, err := record.NewModelIdentity("model", "model", "fake", "fake-runtime-v1",
		integrationDigest(), "", 1024)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := workload.NewPlan(identity, "device-key", 3, 10, 30, 8192)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := sealed.Run(context.Background(), &cliWorkloadBackend{
		runIntegrationBackend: &runIntegrationBackend{},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := bundle.JSON()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "workload-bundle")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	output, code := captureTopStdout(t, func() int {
		return cmdExperiment(context.Background(), []string{"workload", path, "--display", "json"})
	})
	if code != exitOK {
		t.Fatalf("workload reopen exit=%d output=%s", code, output)
	}
	var reopened workload.Bundle
	if err := json.Unmarshal([]byte(output), &reopened); err != nil {
		t.Fatal(err)
	}
	if reopened.Report.Coverage != "established" || reopened.Report.Counts.Accepted != 3 {
		t.Fatalf("reopened workload = %+v", reopened.Report)
	}
	plain, code := captureTopStdout(t, func() int { return renderWorkloadExperiment(reopened, "plain") })
	if code != exitOK || !strings.Contains(plain, "VALIDATED WORK EXPERIMENT") ||
		!strings.Contains(plain, "DETERMINISTIC") && !strings.Contains(plain, "ESTABLISHED") {
		t.Fatalf("plain workload exit=%d output=%s", code, plain)
	}
}

func TestExperimentWorkloadRejectsBoundsBeforeBackendDiscovery(t *testing.T) {
	_, code := captureTopStdout(t, func() int {
		return cmdExperiment(context.Background(), []string{
			"workload", "not-installed", "--n", "0", "--backend", "openai",
		})
	})
	if code != exitUsage {
		t.Fatalf("invalid workload bounds exit=%d", code)
	}
}

func TestWorkloadBundlePathDoesNotMistakeLocalArtifactForBundle(t *testing.T) {
	directory := t.TempDir()
	artifact := filepath.Join(directory, "model.gguf")
	if err := os.WriteFile(artifact, []byte("GGUF\x03\x00\x00\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(directory, "workload-bundle")
	if err := os.WriteFile(bundle, []byte("  {\"schema\":\"fitr.workload.bundle.v1\"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if workloadBundlePath(artifact) {
		t.Fatal("local GGUF artifact was classified as a workload bundle")
	}
	if !workloadBundlePath(bundle) || !workloadBundlePath(filepath.Join(directory, "missing.json")) {
		t.Fatal("JSON workload bundle path was not recognized")
	}
}

func TestParseContextPointsRejectsAmbiguousPlans(t *testing.T) {
	if got, err := parseContextPoints("4096, 8192,16384"); err != nil || len(got) != 3 || got[1] != 8192 {
		t.Fatalf("parsed contexts = %v, %v", got, err)
	}
	for _, input := range []string{"4096", "4096,4096", "4096,zero", "0,4096"} {
		if _, err := parseContextPoints(input); err == nil {
			t.Fatalf("invalid context plan %q was accepted", input)
		}
	}
}

func contextMockResult(t *testing.T, requested int, binding *record.ExperimentBinding, started string,
	plans ...experiment.ContextPlan) *Result {
	t.Helper()
	result := mockResult("context-model", 20, 1, 200, 10, 0, 0, 16, 16)
	result.NumCtx = requested
	result.SeedSet = "context-fixture"
	if len(plans) > 0 {
		result.SeedSet, result.Level = plans[0].SeedSet, plans[0].Level
	}
	result.Experiment = binding
	result.StartedAt = started
	result.Memory.ResidentGB = 6
	result.Memory.PctOnGPU = 100
	result.Memory.RequestedCtx = requested
	if err := prepareMockEvidence(result); err != nil {
		t.Fatal(err)
	}
	return result
}

func quantMockResult(t *testing.T, model, quant string, decode float64, started string) *Result {
	t.Helper()
	result := mockResult(model, decode, 0.5, 200, 2, 0, 0, 16, 16)
	result.NumCtx = 8192
	result.SeedSet = "quant-fixture"
	result.StartedAt = started
	result.ModelMeta.Details.QuantizationLevel = quant
	result.Memory.ResidentGB = 6
	result.Memory.PctOnGPU = 100
	result.Memory.RequestedCtx = result.NumCtx
	if err := prepareMockEvidence(result); err != nil {
		t.Fatal(err)
	}
	return result
}

func confirmationMockResult(t *testing.T, model, quant string, decode float64, started string,
	plan experiment.ConfirmationPlan, index int) *Result {
	t.Helper()
	result := quantMockResult(t, model, quant, decode, started)
	binding := experiment.ConfirmationPlanBinding(plan, index)
	result.Experiment, result.SeedSet = &binding, plan.SeedSet
	if err := prepareMockEvidence(result); err != nil {
		t.Fatal(err)
	}
	return result
}

func writeQuantDecisionSpec(t *testing.T, directory string) string {
	t.Helper()
	spec := decision.DecisionSpec{
		Schema: decision.SpecSchema, Name: "quant CLI", Evidence: decision.EvidenceDecide,
		Requirements: []decision.Requirement{{
			ID: "context", Context: &decision.ContextRequirement{MinimumEffectiveTokens: 4096},
		}},
		Objective: &decision.Objective{Metric: "decode_tps", Direction: decision.Maximize},
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "decision.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type cliWorkloadBackend struct {
	*runIntegrationBackend
	calls int
}

const (
	confirmationDigestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	confirmationDigestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestWorkloadBindingRecheckRejectsModelDrift(t *testing.T) {
	backend := &runIntegrationBackend{digest: integrationDigest()}
	resolved, err := resolveRunModel(context.Background(), backend, "model")
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := device.Detect(context.Background(), backend)
	sealed, err := workload.NewPlan(resolved.Identity, fingerprint.Key(), 1, 1, 30, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyWorkloadBindings(context.Background(), backend, sealed.Plan); err != nil {
		t.Fatalf("stable workload binding = %v", err)
	}
	backend.digest = confirmationDigestA
	if err := verifyWorkloadBindings(context.Background(), backend, sealed.Plan); err == nil ||
		!strings.Contains(err.Error(), "differs") {
		t.Fatalf("changed workload model binding error = %v", err)
	}
}

type confirmationLiveBackend struct {
	*runIntegrationBackend
	active string
}

func (backend *confirmationLiveBackend) Tags(context.Context) ([]ollama.ModelInfo, error) {
	return []ollama.ModelInfo{
		{Name: "confirm-a", Digest: confirmationDigestA, Size: 1024},
		{Name: "confirm-b", Digest: confirmationDigestB, Size: 1024},
	}, nil
}

func (backend *confirmationLiveBackend) Show(_ context.Context, model string) (ollama.ModelInfo, error) {
	backend.active = model
	return ollama.ModelInfo{Name: model, Capabilities: []string{"completion"}}, nil
}

func (backend *confirmationLiveBackend) StopAll(context.Context) ([]string, error) {
	backend.active = ""
	return nil, nil
}

func (backend *confirmationLiveBackend) PS(context.Context) ([]ollama.RunningModel, error) {
	if backend.active == "" {
		return nil, nil
	}
	return []ollama.RunningModel{{Name: backend.active, Size: 1024, SizeVRAM: 1024}}, nil
}

func (backend *confirmationLiveBackend) Generate(_ context.Context, model, _ string,
	_ ollama.Sampling) (string, ollama.Metrics, error) {
	backend.active = model
	decode := 18.0
	if model == "confirm-b" {
		decode = 30
	}
	return "OK", ollama.Metrics{
		DecodeTPS: decode, PrefillTPS: 100, TTFTSeconds: 0.2, PromptTokens: 64, EvalCount: 8,
	}, nil
}

func (backend *confirmationLiveBackend) Chat(_ context.Context, model string, _ []ollama.Message,
	_ []ollama.Tool, _ ollama.Sampling) (ollama.Message, ollama.Metrics, error) {
	backend.active = model
	return ollama.Message{Role: "assistant", Content: "DONE"}, ollama.Metrics{}, nil
}

func confirmationIdentity(t *testing.T, model, digest string) record.ModelIdentity {
	t.Helper()
	identity, err := record.NewModelIdentity(model, model, "fake", "fake-runtime-v1", digest, "", 1024)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func (backend *cliWorkloadBackend) Chat(_ context.Context, _ string, _ []ollama.Message,
	_ []ollama.Tool, _ ollama.Sampling) (ollama.Message, ollama.Metrics, error) {
	sequence := []ollama.Message{
		cliToolMessage("list_files", map[string]any{}),
		cliToolMessage("read_file", map[string]any{"path": "REQUIREMENTS.txt"}),
		cliToolMessage("write_file", map[string]any{
			"path":    "policy.json",
			"content": `{"version":2,"enabled":true,"retries":3,"timeout_ms":1500,"modes":["safe","fast"]}`,
		}),
		cliToolMessage("run_checks", map[string]any{}),
		{Role: "assistant", Content: "DONE"},
	}
	message := sequence[backend.calls%len(sequence)]
	backend.calls++
	return message, ollama.Metrics{PromptTokens: 100}, nil
}

func cliToolMessage(name string, arguments map[string]any) ollama.Message {
	raw, _ := json.Marshal(arguments)
	call := ollama.ToolCall{ID: name + "-id"}
	call.Function.Name, call.Function.Arguments = name, raw
	return ollama.Message{Role: "assistant", ToolCalls: []ollama.ToolCall{call}}
}

func writeReconciledResult(t *testing.T, directory, name string, result *Result) string {
	t.Helper()
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	history := record.NewStore(directory).HistoryDir()
	if err := os.MkdirAll(history, 0o700); err != nil {
		t.Fatal(err)
	}
	historyName := strings.TrimSuffix(name, ".json") + "-" + result.StableRunID() + "-receipt.json"
	if err := os.WriteFile(filepath.Join(history, historyName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	// Keep this test coupled to Store's claimability rule: each selected current
	// file needs an exact immutable-history twin.
	if loaded, err := record.NewStore(directory).Read(path); err != nil || loaded.EvidenceIntegrityIssue() != "" {
		t.Fatalf("reconciled result = %+v, %v", loaded, err)
	}
	return path
}
