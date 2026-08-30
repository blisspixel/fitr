package record

import (
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

func TestValidateDerivedEvidenceRejectsForgedReceipts(t *testing.T) {
	for _, tc := range derivedEvidenceForgeryCases {
		t.Run(tc.name, func(t *testing.T) {
			r := derivedEvidenceBase(t)
			tc.mutate(t, r)
			if err := r.validateDerivedEvidence(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateDerivedEvidence() error = %v, want %q", err, tc.want)
			}
		})
	}
}

type derivedEvidenceForgeryCase struct {
	name   string
	mutate func(*testing.T, *Record)
	want   string
}

func derivedEvidenceBase(t *testing.T) *Record {
	t.Helper()
	return completedEvidenceRecord(t,
		[]eval.ExecResult{{Outcome: eval.OutcomeInconclusive}},
		[]eval.CheckOutcome{{TaskID: "check", Pass: true, Outcome: eval.OutcomePass}},
	)
}

func addDerivedSpeed(r *Record, sample eval.SpeedResult) {
	sample.FirstOutputObserved = true
	r.TaskPlan.SpeedSamples = 1
	r.Speed = []eval.SpeedResult{sample}
}

func addDerivedRefusal(t *testing.T, r *Record, verdict eval.RefusalVerdict) {
	t.Helper()
	r.TaskPlan.RefusalTrials = 1
	r.Refusal = map[string]eval.RefusalVerdict{"refusal": verdict}
	digest, err := ObservedRefusalPlanSHA256(r.Refusal)
	if err != nil {
		t.Fatal(err)
	}
	r.TaskPlan.RefusalPlanSHA256 = digest
	r.EvidenceCounts["refusal"] = eval.CountOutcomes(1, verdict.Outcome)
}

var derivedEvidenceForgeryCases = []derivedEvidenceForgeryCase{
	{
		name: "non-finite wall time",
		mutate: func(_ *testing.T, r *Record) {
			r.WallSeconds = math.NaN()
		},
		want: "wall time",
	},
	{
		name: "speed denominator",
		mutate: func(_ *testing.T, r *Record) {
			r.Speed = []eval.SpeedResult{{}}
		},
		want: "speed observations 1 do not match planned 0",
	},
	{
		name: "negative speed measurement",
		mutate: func(_ *testing.T, r *Record) {
			addDerivedSpeed(r, eval.SpeedResult{DecodeTPS: -1})
		},
		want: "negative measurement",
	},
	{
		name: "missing first output receipt",
		mutate: func(_ *testing.T, r *Record) {
			addDerivedSpeed(r, eval.SpeedResult{DecodeTPS: 2, TTFT: 1, PrefillTPS: 3})
			r.Speed[0].FirstOutputObserved = false
		},
		want: "no first-output receipt",
	},
	{
		name: "non-finite speed measurement",
		mutate: func(_ *testing.T, r *Record) {
			addDerivedSpeed(r, eval.SpeedResult{DecodeTPS: math.Inf(1)})
		},
		want: "non-finite measurement",
	},
	{
		name: "cold label without load receipt",
		mutate: func(_ *testing.T, r *Record) {
			addDerivedSpeed(r, eval.SpeedResult{ColdTTFT: 1, ColdLoad: 0.1})
		},
		want: "cold TTFT without a cold-load receipt",
	},
	{
		name: "warm label without cache hit",
		mutate: func(_ *testing.T, r *Record) {
			addDerivedSpeed(r, eval.SpeedResult{WarmTTFT: 1})
		},
		want: "warm TTFT without a cache-hit receipt",
	},
	{
		name: "cached tokens without receipt",
		mutate: func(_ *testing.T, r *Record) {
			addDerivedSpeed(r, eval.SpeedResult{CachedPromptTok: 1})
		},
		want: "cached tokens without a cache receipt",
	},
	{
		name: "known empty gated cache receipt",
		mutate: func(_ *testing.T, r *Record) {
			addDerivedSpeed(r, eval.SpeedResult{GatedCacheKnown: true})
		},
		want: "empty gated cache receipt",
	},
	{
		name: "forged speed summary",
		mutate: func(_ *testing.T, r *Record) {
			addDerivedSpeed(r, eval.SpeedResult{DecodeTPS: 2, TTFT: 1, PrefillTPS: 3})
		},
		want: "speed summaries",
	},
	{
		name: "refusal verdict disagreement",
		mutate: func(t *testing.T, r *Record) {
			t.Helper()
			addDerivedRefusal(t, r, eval.RefusalVerdict{Outcome: eval.OutcomePass, Verdict: "refused"})
		},
		want: "disagrees with its verdict",
	},
	{
		name: "forged refused count",
		mutate: func(t *testing.T, r *Record) {
			t.Helper()
			addDerivedRefusal(t, r, eval.RefusalVerdict{Outcome: eval.OutcomeFail, Verdict: "refused"})
		},
		want: "refused count",
	},
	{
		name: "forged output summary",
		mutate: func(_ *testing.T, r *Record) {
			r.Rep.Words++
		},
		want: "output summaries",
	},
	{
		name: "forged scorecard identity",
		mutate: func(_ *testing.T, r *Record) {
			r.Scorecard.Profile = "other"
		},
		want: "scorecard identity",
	},
}

func TestSealedCheckPlanRejectsReplacementReorderingAndRemoval(t *testing.T) {
	base := completedEvidenceRecord(t, nil, []eval.CheckOutcome{
		{TaskID: "first", Family: "family-a", Need: "structured_output", Origin: "builtin", Seed: 1, Pass: true, Outcome: eval.OutcomePass},
		{TaskID: "second", Family: "family-b", Need: "instruction_precision", Origin: "builtin", Seed: 2, Pass: true, Outcome: eval.OutcomePass},
	})
	tests := []struct {
		name   string
		mutate func(*Record)
		want   string
	}{
		{"replacement", func(r *Record) { r.Checks[0].Family = "easier-family" }, "sealed plan"},
		{"reordering", func(r *Record) { r.Checks[0], r.Checks[1] = r.Checks[1], r.Checks[0] }, "sealed plan"},
		{"seed", func(r *Record) { r.Checks[0].Seed++ }, "sealed plan"},
		{"removal", func(r *Record) { r.Checks = r.Checks[:1] }, "do not match planned"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clone := *base
			clone.Checks = append([]eval.CheckOutcome(nil), base.Checks...)
			tc.mutate(&clone)
			if err := clone.validateDerivedEvidence(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("check-plan mutation error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSealedRefusalPlanRejectsPromptReplacement(t *testing.T) {
	base := completeContractRecord(t, TaskPlan{RefusalTrials: 3}, func(r *Record) {
		r.Refusal = map[string]eval.RefusalVerdict{
			"political": {Verdict: "answered", Outcome: eval.OutcomePass},
			"fiction":   {Verdict: "answered", Outcome: eval.OutcomePass},
			"rewrite":   {Verdict: "answered", Outcome: eval.OutcomePass},
		}
	})
	clone := *base
	clone.Refusal = map[string]eval.RefusalVerdict{
		"political": {Verdict: "answered", Outcome: eval.OutcomePass},
		"fiction":   {Verdict: "answered", Outcome: eval.OutcomePass},
		"easy":      {Verdict: "answered", Outcome: eval.OutcomePass},
	}
	if err := clone.validateDerivedEvidence(); err == nil || !strings.Contains(err.Error(), "sealed plan") {
		t.Fatalf("refusal-plan replacement error = %v, want sealed-plan rejection", err)
	}
}

func TestMeasuredPartialTTFTCacheHitCannotEstablishLoadedUncachedLatency(t *testing.T) {
	r := &Record{
		Model: "model", TaskPlan: TaskPlan{SpeedSamples: 1},
		Speed: []eval.SpeedResult{{
			DecodeTPS: 20, TTFT: 0.2, PrefillTPS: 100,
			GatedCacheKnown: true, GatedPromptTok: 99, GatedCachedTok: 1,
		}},
		DecodeSum:  stats.Summary{Mean: 20, N: 1},
		TTFTSum:    stats.Summary{Mean: 0.2, N: 1},
		PrefillSum: stats.Summary{Mean: 100, N: 1},
	}
	m := r.Measured()
	if !m.TTFTCacheKnown || !m.TTFTCacheContaminated {
		t.Fatalf("partial cache receipt was relabeled uncached: %+v", m)
	}
	profile := device.Profile{Name: "test", Gates: map[string]device.Gate{
		"fast_chat": {"decode_tps_min": 10, "ttft_s_max": 1},
	}}
	verdict := score.Score(m, profile).Needs["fast_and_decent"]
	if verdict.State != score.Inconclusive || !strings.Contains(verdict.Why, "cache hit") {
		t.Fatalf("partial cache hit verdict = %+v", verdict)
	}
}

func TestExecutorEvidencePolicyCoversAllResultKinds(t *testing.T) {
	fixture := newExecutorEvidenceFixture(t)
	t.Run("disabled coding verifier", fixture.testDisabledCodingVerifier)
	t.Run("disabled tool observation", fixture.testDisabledToolObservation)
	t.Run("unsafe manifest missing executor", fixture.testUnsafeManifestMissingExecutor)
	t.Run("unsafe receipts bind across result kinds", fixture.testUnsafeReceiptsAcrossResultKinds)
	t.Run("tool summary requires observation history", fixture.testToolSummaryRequiresHistory)
	t.Run("observation must bind to executor", fixture.testObservationBindsToExecutor)
}

type executorEvidenceFixture struct {
	executor eval.ExecutorReceipt
	receipt  eval.VerificationReceipt
}

func newExecutorEvidenceFixture(t *testing.T) executorEvidenceFixture {
	t.Helper()
	executor := eval.ExecutorReceipt{
		Kind: "python", Path: filepath.Join(t.TempDir(), "python.exe"),
		Version: "Python 3.12.7", SHA256: testArtifactDigest,
	}
	receipt := eval.VerificationReceipt{
		Protocol: "fitr.verifier.v2", InterpreterPath: executor.Path,
		InterpreterVer: executor.Version, InterpreterHash: executor.SHA256,
	}
	return executorEvidenceFixture{executor: executor, receipt: receipt}
}

func (fixture executorEvidenceFixture) toolReceipt() eval.ToolLoopResult {
	return eval.ToolLoopResult{
		Outcome: eval.OutcomeInconclusive, Verifier: &fixture.receipt,
		VerifierObservations: []eval.VerificationReceipt{fixture.receipt},
	}
}

func (fixture executorEvidenceFixture) testDisabledCodingVerifier(t *testing.T) {
	r := &Record{
		Manifest:  &RunManifest{Schema: RunManifestSchema, ExecutionPolicy: ExecutionDisabled},
		CodeWrite: []eval.ExecResult{{Outcome: eval.OutcomeInconclusive, Verifier: &fixture.receipt}},
	}
	if err := r.validateExecutorEvidence(); err == nil || !strings.Contains(err.Error(), "disabled execution") {
		t.Fatalf("disabled coding verifier error = %v", err)
	}
}

func (fixture executorEvidenceFixture) testDisabledToolObservation(t *testing.T) {
	r := &Record{
		Manifest: &RunManifest{Schema: RunManifestSchema, ExecutionPolicy: ExecutionDisabled},
		Agentic: &eval.ToolLoopResult{
			Outcome:              eval.OutcomeInconclusive,
			VerifierObservations: []eval.VerificationReceipt{fixture.receipt},
		},
	}
	if err := r.validateExecutorEvidence(); err == nil || !strings.Contains(err.Error(), "disabled execution") {
		t.Fatalf("disabled tool observation error = %v", err)
	}
}

func (fixture executorEvidenceFixture) testUnsafeManifestMissingExecutor(t *testing.T) {
	r := &Record{Manifest: &RunManifest{Schema: RunManifestSchema, ExecutionPolicy: ExecutionUnsafe}}
	if err := r.validateExecutorEvidence(); err == nil || !strings.Contains(err.Error(), "missing its manifest executor") {
		t.Fatalf("missing executor error = %v", err)
	}
}

func (fixture executorEvidenceFixture) testUnsafeReceiptsAcrossResultKinds(t *testing.T) {
	tool, withdrawal, agentic := fixture.toolReceipt(), fixture.toolReceipt(), fixture.toolReceipt()
	r := &Record{
		Manifest:  &RunManifest{Schema: RunManifestSchema, ExecutionPolicy: ExecutionUnsafe, Executor: &fixture.executor},
		CodeWrite: []eval.ExecResult{{Outcome: eval.OutcomeInconclusive, Verifier: &fixture.receipt}},
		CodeFix:   []eval.ExecResult{{Outcome: eval.OutcomeInconclusive, Verifier: &fixture.receipt}},
		Tools:     []eval.ToolLoopResult{tool}, Withdrawal: &withdrawal, Agentic: &agentic,
	}
	if err := r.validateExecutorEvidence(); err != nil {
		t.Fatalf("valid unsafe executor evidence: %v", err)
	}
}

func (fixture executorEvidenceFixture) testToolSummaryRequiresHistory(t *testing.T) {
	tool := fixture.toolReceipt()
	tool.VerifierObservations = nil
	r := &Record{
		Manifest: &RunManifest{Schema: RunManifestSchema, ExecutionPolicy: ExecutionUnsafe, Executor: &fixture.executor},
		Tools:    []eval.ToolLoopResult{tool},
	}
	if err := r.validateExecutorEvidence(); err == nil || !strings.Contains(err.Error(), "missing its verifier observation history") {
		t.Fatalf("missing verifier history error = %v", err)
	}
}

func (fixture executorEvidenceFixture) testObservationBindsToExecutor(t *testing.T) {
	tool := fixture.toolReceipt()
	tool.VerifierObservations[0].InterpreterHash = "sha256:" + strings.Repeat("f", 64)
	r := &Record{
		Manifest: &RunManifest{Schema: RunManifestSchema, ExecutionPolicy: ExecutionUnsafe, Executor: &fixture.executor},
		Tools:    []eval.ToolLoopResult{tool},
	}
	if err := r.validateExecutorEvidence(); err == nil || !strings.Contains(err.Error(), "observation 0") {
		t.Fatalf("mismatched verifier observation error = %v", err)
	}
}

func TestRecomputeOutcomeCountsDoesNotInventMissingObservations(t *testing.T) {
	r := &Record{
		TaskPlan: TaskPlan{
			CodeTrials: 2, CheckTrialsLimit: 3, ToolTrials: 1, RefusalTrials: 1,
			Plumbing: true, Withdrawal: true, AgenticTrials: 1,
		},
		CodeWrite: []eval.ExecResult{{Outcome: eval.OutcomeInconclusive}},
		CodeFix:   []eval.ExecResult{{Outcome: eval.OutcomeSkipped}},
		Checks:    []eval.CheckOutcome{{TaskID: "check", Pass: true, Outcome: eval.OutcomePass}},
		Tools:     []eval.ToolLoopResult{{Outcome: eval.OutcomeInconclusive}},
		Refusal: map[string]eval.RefusalVerdict{
			"refusal": {Outcome: eval.OutcomePass, Verdict: "answered"},
		},
		Plumbing:   &eval.PlumbingResult{Outcome: eval.OutcomeFail},
		Withdrawal: &eval.ToolLoopResult{Outcome: eval.OutcomeSkipped},
		Agentic:    &eval.ToolLoopResult{Outcome: eval.OutcomeInconclusive},
	}
	counts, err := r.DeriveEvidenceCounts()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]eval.OutcomeCounts{
		"coding":     eval.CountOutcomes(2, eval.OutcomeInconclusive, eval.OutcomeSkipped),
		"checks":     eval.CountOutcomes(3, eval.OutcomePass),
		"tools":      eval.CountOutcomes(1, eval.OutcomeInconclusive),
		"refusal":    eval.CountOutcomes(1, eval.OutcomePass),
		"plumbing":   eval.CountOutcomes(1, eval.OutcomeFail),
		"withdrawal": eval.CountOutcomes(1, eval.OutcomeSkipped),
		"agentic":    eval.CountOutcomes(1, eval.OutcomeInconclusive),
	}
	for phase, expected := range want {
		got := counts[phase]
		if got != expected {
			t.Errorf("%s counts = %+v, want %+v", phase, got, expected)
		}
		if phase == "checks" && got.Complete() {
			t.Errorf("missing checks were padded into complete evidence: %+v", got)
		} else if phase != "checks" && !got.Complete() {
			t.Errorf("%s unexpectedly incomplete: %+v", phase, got)
		}
	}
}

func TestRecomputeOutcomeCountsRejectsInvalidRawOutcomes(t *testing.T) {
	tests := []struct {
		name string
		r    *Record
		want string
	}{
		{
			name: "unknown outcome",
			r: &Record{
				TaskPlan: TaskPlan{RefusalTrials: 1},
				Refusal:  map[string]eval.RefusalVerdict{"x": {Outcome: eval.Outcome("forged")}},
			},
			want: "refusal contains unknown outcome",
		},
		{
			name: "coding pass flag",
			r: &Record{
				TaskPlan:  TaskPlan{CodeTrials: 1},
				CodeWrite: []eval.ExecResult{{Outcome: eval.OutcomePass}},
			},
			want: "coding observation 0 outcome disagrees",
		},
		{
			name: "withdrawal pass flag",
			r: &Record{
				TaskPlan:   TaskPlan{Withdrawal: true},
				Withdrawal: &eval.ToolLoopResult{Pass: true, Outcome: eval.OutcomeFail},
			},
			want: "withdrawal observation outcome disagrees",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.r.DeriveEvidenceCounts(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("DeriveEvidenceCounts() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCurrentEvidenceContractRejectsReachableInvalidStates(t *testing.T) {
	assertCurrentContractBoundaries(t)
	for _, tc := range currentContractInvalidCases {
		t.Run(tc.name, func(t *testing.T) {
			r := completeContractRecord(t, tc.plan, tc.configure)
			if err := r.ValidateEvidenceContract(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateEvidenceContract() error = %v, want %q", err, tc.want)
			}
		})
	}
	assertValidCurrentContract(t)
}

func assertCurrentContractBoundaries(t *testing.T) {
	t.Helper()
	if err := (*Record)(nil).ValidateEvidenceContract(); err == nil || !strings.Contains(err.Error(), "nil result") {
		t.Fatalf("nil contract error = %v", err)
	}
	if err := (&Record{SchemaVersion: EvidenceSchemaVersion - 1}).ValidateEvidenceContract(); err != nil {
		t.Fatalf("legacy contract should remain readable: %v", err)
	}
	if err := (&Record{SchemaVersion: EvidenceSchemaVersion}).ValidateEvidenceContract(); err == nil ||
		!strings.Contains(err.Error(), "no sealed run manifest") {
		t.Fatalf("missing manifest error = %v", err)
	}
}

type currentContractInvalidCase struct {
	name      string
	plan      TaskPlan
	configure func(*Record)
	want      string
}

var currentContractInvalidCases = []currentContractInvalidCase{
	{
		name: "adaptive check plan",
		plan: TaskPlan{CheckTrialsLimit: 1, AdaptiveChecks: true},
		configure: func(r *Record) {
			r.Checks = []eval.CheckOutcome{{TaskID: "check", Pass: true, Outcome: eval.OutcomePass}}
		},
		want: "fixed generated-check plan",
	},
	{
		name: "planned memory without requested context",
		plan: TaskPlan{Memory: true},
		want: "planned memory probe has no requested context",
	},
	{
		name: "unplanned memory outcome",
		plan: TaskPlan{CheckTrialsLimit: 1},
		configure: func(r *Record) {
			r.Checks = []eval.CheckOutcome{{TaskID: "check", Outcome: eval.OutcomeSkipped}}
			r.Memory.Outcome = eval.OutcomeSkipped
		},
		want: "unplanned memory probe",
	},
	{
		name: "invalid memory receipt",
		plan: TaskPlan{Memory: true},
		configure: func(r *Record) {
			effective := 0
			r.Memory = eval.MemoryResult{
				Outcome: eval.OutcomePass, RequestedCtx: 32768, EffectiveCtx: &effective,
			}
		},
		want: "memory receipt",
	},
	{
		name: "invalid tool termination receipt",
		plan: TaskPlan{ToolTrials: 1},
		configure: func(r *Record) {
			r.Tools = []eval.ToolLoopResult{{Outcome: eval.OutcomeInconclusive}}
		},
		want: "missing its termination reason",
	},
	{
		name: "infrastructure error",
		plan: TaskPlan{CheckTrialsLimit: 1},
		configure: func(r *Record) {
			r.Checks = []eval.CheckOutcome{{TaskID: "check", Outcome: eval.OutcomeError}}
		},
		want: "infrastructure error outcome",
	},
	{
		name: "executable evidence remains unscoreable",
		plan: TaskPlan{CodeTrials: 1},
		configure: func(r *Record) {
			r.CodeWrite = []eval.ExecResult{{Pass: true, Outcome: eval.OutcomePass}}
		},
		want: "cannot be scoreable before isolation",
	},
}

func assertValidCurrentContract(t *testing.T) {
	t.Helper()
	valid := completeContractRecord(t, TaskPlan{CheckTrialsLimit: 1}, func(r *Record) {
		r.Checks = []eval.CheckOutcome{{TaskID: "check", Pass: true, Outcome: eval.OutcomePass}}
	})
	if err := valid.ValidateEvidenceContract(); err != nil {
		t.Fatalf("valid current evidence contract: %v", err)
	}
}

func TestUnplannedMemoryRejectsEveryReceiptField(t *testing.T) {
	effective := 32768
	tests := map[string]eval.MemoryResult{
		"outcome":            {Outcome: eval.OutcomeSkipped},
		"unavailable reason": {UnavailableReason: "unavailable"},
		"disk GiB":           {DiskGB: 1},
		"resident GiB":       {ResidentGB: 1},
		"GPU percentage":     {PctOnGPU: 1},
		"load seconds":       {LoadS: 1},
		"requested context":  {RequestedCtx: 32768},
		"effective context":  {EffectiveCtx: &effective},
		"resident bytes":     {ResidentBytes: 1},
		"accelerator bytes":  {AcceleratorBytes: 1},
	}
	for name, memory := range tests {
		t.Run(name, func(t *testing.T) {
			r := completeContractRecord(t, TaskPlan{CheckTrialsLimit: 1}, func(r *Record) {
				r.Checks = []eval.CheckOutcome{{TaskID: "check", Outcome: eval.OutcomeSkipped}}
				r.Memory = memory
			})
			if err := r.ValidateEvidenceContract(); err == nil || !strings.Contains(err.Error(), "unplanned memory probe") {
				t.Fatalf("unplanned memory error = %v", err)
			}
		})
	}
}

func TestComparableDeviceKeyEnforcesSchemaAndEffectiveContext(t *testing.T) {
	if _, err := (*Record)(nil).ComparableDeviceKey(); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("nil comparable key error = %v", err)
	}
	if _, err := (&Record{SchemaVersion: EvidenceSchemaVersion}).ComparableDeviceKey(); err == nil ||
		!strings.Contains(err.Error(), "missing fingerprint v2") {
		t.Fatalf("missing current fingerprint error = %v", err)
	}
	if _, err := (&Record{SchemaVersion: EvidenceSchemaVersion - 1}).ComparableDeviceKey(); err == nil ||
		!strings.Contains(err.Error(), "no device key") {
		t.Fatalf("missing legacy key error = %v", err)
	}
	legacy := &Record{SchemaVersion: EvidenceSchemaVersion - 1, DeviceKey: "legacy|device|ctx=8192"}
	if got, err := legacy.ComparableDeviceKey(); err != nil || got != legacy.DeviceKey {
		t.Fatalf("legacy comparable key = %q, err=%v", got, err)
	}

	current := completedEvidenceRecord(t, nil,
		[]eval.CheckOutcome{{TaskID: "check", Outcome: eval.OutcomeSkipped}})
	if got, err := current.ComparableDeviceKey(); err != nil || got != current.DeviceKey {
		t.Fatalf("current comparable key = %q, want %q, err=%v", got, current.DeviceKey, err)
	}
	unverified := *current
	fingerprint := *current.DeviceV2
	fingerprint.Context.EffectiveTokens = nil
	fingerprint.Context.EffectiveSource = ""
	unverified.DeviceV2 = &fingerprint
	if _, err := unverified.ComparableDeviceKey(); err == nil || !strings.Contains(err.Error(), "effective context is unverified") {
		t.Fatalf("unverified context error = %v", err)
	}
}

func completeContractRecord(t *testing.T, plan TaskPlan, configure func(*Record)) *Record {
	t.Helper()
	profile := device.Profile{Name: "default", Description: "test", Gates: map[string]device.Gate{}}
	r := manifestRecord("model", "2026-08-21T12:00:00Z")
	r.SchemaVersion = EvidenceSchemaVersion
	r.TaskPlan = plan
	r.CodeWrite, r.CodeFix, r.Checks, r.Tools = nil, nil, nil, nil
	r.Withdrawal, r.Agentic, r.Plumbing = nil, nil, nil
	r.Refusal, r.Memory, r.Speed = nil, eval.MemoryResult{}, nil
	if configure != nil {
		configure(r)
	}
	normalizeCheckPlanForTest(t, r)
	counts, err := r.DeriveEvidenceCounts()
	if err != nil {
		t.Fatalf("recompute fixture counts: %v", err)
	}
	r.EvidenceCounts = counts
	r.Rep = score.RepetitionMetrics("")
	r.Density = score.InformationDensity("")
	r.Scorecard = score.Score(r.Measured(), profile)
	addTestFingerprintV2(t, r)
	hashes, err := eval.BuiltinHashes()
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := NewRunProvenance(hashes.TaskSetSHA256, hashes.SpecSHA256, profile,
		CurrentScoringPolicy(), testSoftwareReceipt())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AttachManifest(digestIdentity(t, "model", "model"), provenance); err != nil {
		t.Fatal(err)
	}
	if err := r.CompleteEvidence(profile); err != nil {
		t.Fatal(err)
	}
	return r
}
