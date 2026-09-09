package main

import (
	"context"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/contextquality"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
)

func TestContextTiersSelectTheContextLevelAndSealNoOtherWork(t *testing.T) {
	command, code, ok := parseRunCommand([]string{"model", "--context-tiers", "2048,8192,32768"}, nil)
	if !ok || code != exitOK {
		t.Fatalf("parseRunCommand = ok %v, code %d", ok, code)
	}
	if command.level != levelContext {
		t.Fatalf("level = %q, want %q", command.level, levelContext)
	}
	if got, want := command.contextTiers, []int{2048, 8192, 32768}; len(got) != len(want) {
		t.Fatalf("contextTiers = %v, want %v", got, want)
	}
	// The context phase is the whole plan. A battery field left set here would
	// commit the run to work it never intends to measure.
	plan := runTaskPlan(command.level, command.reps, command.checksReps, 22, 3)
	if plan != (record.TaskPlan{}) {
		t.Fatalf("context-level task plan = %+v, want every battery field unset", plan)
	}
}

func TestContextTiersAreRejectedWithoutRunningAnything(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"one tier", []string{"model", "--context-tiers", "2048"}},
		{"five tiers", []string{"model", "--context-tiers", "2048,4096,8192,16384,32768"}},
		{"not a byte count", []string{"model", "--context-tiers", "2048,large"}},
		{"below the minimum payload", []string{"model", "--context-tiers", "512,8192"}},
		{"above the maximum payload", []string{"model", "--context-tiers", "8192,131072"}},
		{"not strictly increasing", []string{"model", "--context-tiers", "8192,8192"}},
		{"window below the reserve", []string{"model", "--context-tiers", "2048,8192", "--ctx", "64"}},
		{"another run level", []string{"model", "--context-tiers", "2048,8192", "--quick"}},
		{"a backend that cannot send the controls", []string{
			"model", "--context-tiers", "2048,8192", "--backend", "llama-server"}},
		{"executable diagnostics", []string{
			"model", "--context-tiers", "2048,8192", "--allow-unsafe-exec"}},
		{"html scorecard", []string{
			"model", "--context-tiers", "2048,8192", "--html"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, code, ok := parseRunCommand(tc.args, nil)
			if ok || code != exitUsage {
				t.Fatalf("parseRunCommand = ok %v, code %d, want a usage refusal", ok, code)
			}
		})
	}
}

func TestOrdinaryRunPlansNoContextPhase(t *testing.T) {
	command, code, ok := parseRunCommand([]string{"model"}, nil)
	if !ok || code != exitOK {
		t.Fatalf("parseRunCommand = ok %v, code %d", ok, code)
	}
	if command.level == levelContext || len(command.contextTiers) != 0 {
		t.Fatalf("level = %q, tiers = %v, want an ordinary run", command.level, command.contextTiers)
	}
}

// The pack is submitted at the resolved operating window, so the plan a run
// seals must carry the run's own context rather than the flag's raw value.
func TestSealContextTaskPlanBindsTheResolvedWindowAndRunSeedSet(t *testing.T) {
	run := &runExecution{
		opts:   runOpts{contextTiers: []int{2048, 8192}},
		result: &Result{NumCtx: 16384, SeedSet: "2026-09-08T10:00:00Z"},
	}
	if err := run.sealContextTaskPlan(); err != nil {
		t.Fatalf("sealContextTaskPlan: %v", err)
	}
	if run.contextPlan == nil {
		t.Fatal("sealed plan was not retained for the measurement")
	}
	if got := run.contextPlan.Policy.OperatingWindowTokens; got != 16384 {
		t.Fatalf("operating window = %d, want the run's resolved 16384", got)
	}
	digest, err := run.contextPlan.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if run.result.TaskPlan.ContextPlanSHA256 != digest {
		t.Fatalf("sealed digest %q does not match the retained plan %q",
			run.result.TaskPlan.ContextPlanSHA256, digest)
	}
	if run.result.TaskPlan.ContextCells != len(run.contextPlan.Cells) {
		t.Fatalf("sealed cells = %d, plan has %d",
			run.result.TaskPlan.ContextCells, len(run.contextPlan.Cells))
	}
	// A phase attached against the sealed plan must satisfy the record's own
	// re-derivation, which is what proves the two halves agree.
	observations := make([]contextquality.Observation, 0, len(run.contextPlan.Cells))
	for _, cell := range run.contextPlan.Cells {
		observations = append(observations, contextquality.Observation{
			CellID: cell.ID, PayloadSHA256: cell.PayloadSHA256, PromptSHA256: cell.PromptSHA256,
			Disposition: contextquality.NotAvailable, UnavailableReason: "transport_error",
		})
	}
	if err := run.result.AttachContextQuality(*run.contextPlan, observations); err != nil {
		t.Fatalf("attach against the sealed plan: %v", err)
	}
}

// MLX silently reduces a requested output reserve and reports no field saying
// it did, so the reserve gate the pack qualifies on is not testable there.
func TestSealContextTaskPlanRefusesAnMLXServedModel(t *testing.T) {
	run := &runExecution{
		model:  "some/model:mlx",
		opts:   runOpts{contextTiers: []int{2048, 8192}},
		result: &Result{NumCtx: 16384, SeedSet: "2026-09-08T10:00:00Z"},
	}
	run.resolved.Info.Details.Format = "safetensors"
	err := run.sealContextTaskPlan()
	if err == nil {
		t.Fatal("sealContextTaskPlan accepted an MLX-served model")
	}
	if !strings.Contains(err.Error(), "output reserve") {
		t.Fatalf("diagnostic %q does not explain which claim cannot be established", err)
	}
	if run.contextPlan != nil || run.result.TaskPlan.ContextCells != 0 {
		t.Fatal("a refused model still sealed a context plan")
	}
}

func TestServedByMLXReadsTheRuntimeArtifactFormat(t *testing.T) {
	for _, tc := range []struct {
		format string
		mlx    bool
	}{
		{"safetensors", true},
		{"gguf", false},
		// Ollama treats an absent format as GGUF, so absence must not read as MLX.
		{"", false},
	} {
		var info ollama.ModelInfo
		info.Details.Format = tc.format
		if got := info.ServedByMLX(); got != tc.mlx {
			t.Fatalf("ServedByMLX(%q) = %v, want %v", tc.format, got, tc.mlx)
		}
	}
}

// contextTaskBackend serves the context level: it reports the adopted request
// policy and returns the complete native accounting a cell needs to resolve.
// The answers are wrong on purpose. What this proves is that a run whose only
// planned work is the phase completes, seals and saves; whether a model answers
// correctly is contextquality's concern, not the run path's.
type contextTaskBackend struct {
	*runIntegrationBackend
	promptTokens int
}

func (b *contextTaskBackend) ContextRequestPolicy() ollama.ContextRequestPolicy {
	return ollama.PreserveContextV1
}

func (b *contextTaskBackend) Generate(ctx context.Context, model, prompt string,
	sampling ollama.Sampling,
) (string, ollama.Metrics, error) {
	text, metrics, err := b.runIntegrationBackend.Generate(ctx, model, prompt, sampling)
	if err != nil {
		return text, metrics, err
	}
	cached, output := 0, 4
	prompted := b.promptTokens
	metrics.ContextAccounting = &ollama.ContextTokenAccounting{
		PromptTokens: &prompted, CachedPromptTokens: &cached, OutputTokens: &output,
	}
	return `{"answer":"none"}`, metrics, nil
}

func TestContextLevelRunCompletesWithThePhaseAsItsOnlyWork(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	display := render.New("none")
	defer display.Close()
	backend := &contextTaskBackend{
		runIntegrationBackend: &runIntegrationBackend{
			digest: integrationDigest(), effectiveCtx: eval.NumCtx,
		},
		promptTokens: 64,
	}
	result, err := execute(context.Background(), backend, "model", runOpts{
		level: levelContext, profile: "default", reps: 1, checksReps: 1,
		contextTiers: []int{2048, 4096},
	}, display)
	if err != nil {
		t.Fatalf("context-level run did not complete: %v", err)
	}
	if result.ContextQuality == nil {
		t.Fatal("a completed context-level run carries no phase")
	}
	if got, want := len(result.ContextQuality.Observations), 2*contextquality.CellsPerTier; got != want {
		t.Fatalf("observations = %d, want one per planned cell (%d)", got, want)
	}
	// Wrong answers are still answers: the cells resolved rather than going
	// unavailable, which is what proves the accounting and reserve gate passed.
	for _, observation := range result.ContextQuality.Observations {
		if observation.Disposition != contextquality.Answered {
			t.Fatalf("cell %s = %q (%s), want every cell to have resolved",
				observation.CellID, observation.Disposition, observation.UnavailableReason)
		}
	}
	if result.ContextQuality.Report.Outcome != contextquality.Fail {
		t.Fatalf("outcome = %q, want fail for deliberately wrong answers",
			result.ContextQuality.Report.Outcome)
	}
	// The saved evidence must survive the same validation a loader applies,
	// which re-derives the report from the observations it stored.
	if err := result.ValidateEvidenceContract(); err != nil {
		t.Fatalf("a completed context-level run does not satisfy the evidence contract: %v", err)
	}
	report, err := analysis.FromRecord(result)
	if err != nil {
		t.Fatalf("analyze a context-level record: %v", err)
	}
	if report.ContextTasks == nil || report.ContextTasks.Planned != 2*contextquality.CellsPerTier {
		t.Fatalf("analysis projection = %+v, want the sealed phase", report.ContextTasks)
	}
}

// A prompt that leaves no room for the declared reserve is a context limit, not
// a wrong answer and not a transport fault. It must never look like either.
func TestContextLevelRunRecordsAReserveOverflowAsAContextLimit(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	display := render.New("none")
	defer display.Close()
	backend := &contextTaskBackend{
		runIntegrationBackend: &runIntegrationBackend{
			digest: integrationDigest(), effectiveCtx: eval.NumCtx,
		},
		// One token past the window minus the declared 128-token reserve.
		promptTokens: eval.NumCtx - contextquality.OutputReserveTokens + 1,
	}
	result, err := execute(context.Background(), backend, "model", runOpts{
		level: levelContext, profile: "default", reps: 1, checksReps: 1,
		contextTiers: []int{2048, 4096},
	}, display)
	if err != nil {
		t.Fatalf("context-level run did not complete: %v", err)
	}
	for _, observation := range result.ContextQuality.Observations {
		if observation.Disposition != contextquality.ContextLimit {
			t.Fatalf("cell %s = %q, want context_limit when the reserve does not fit",
				observation.CellID, observation.Disposition)
		}
	}
}
