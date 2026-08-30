package analysis

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestFromRecordRejectsAnythingButValidatedSchemaSix(t *testing.T) {
	tests := []struct {
		name   string
		record *record.Record
		want   string
	}{
		{name: "nil", want: "nil record"},
		{name: "legacy", record: &record.Record{SchemaVersion: record.EvidenceSchemaVersion - 1}, want: "require schema 6"},
		{name: "future", record: &record.Record{SchemaVersion: record.EvidenceSchemaVersion + 1}, want: "require schema 6"},
		{name: "invalid current", record: &record.Record{SchemaVersion: record.EvidenceSchemaVersion}, want: "validate evidence"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report, err := FromRecord(test.record)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if !reflect.DeepEqual(report, Report{}) {
				t.Fatalf("invalid evidence returned a partial report: %+v", report)
			}
		})
	}
}

func TestFromRecordAcceptsACompletedSchemaSixReceipt(t *testing.T) {
	result := completedTestRecord(t)
	report, err := FromRecord(result)
	if err != nil {
		t.Fatal(err)
	}
	if report.Schema != ReportSchema || report.Policy != PolicySchema || !report.Source.Validated {
		t.Fatalf("report envelope = %+v", report)
	}
	if report.Source.RecordSchema != record.EvidenceSchemaVersion ||
		report.Source.ManifestSchema != record.RunManifestSchema ||
		report.Source.CompletionSchema != record.CompletionReceiptSchema ||
		report.Source.EvidenceSHA256 != result.Completion.EvidenceSHA256 {
		t.Fatalf("source = %+v", report.Source)
	}
}

func TestFromRecordKeepsUnreconciledStorageEvidenceDescriptive(t *testing.T) {
	requested := 32768
	result := completedTestRecordConfigured(t, "", func(result *record.Record) {
		result.TaskPlan.Memory = true
		result.Memory = measuredMemory(requested, &requested)
	})
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	externalPath := filepath.Join(t.TempDir(), "external.json")
	if err := os.WriteFile(externalPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := record.NewStore(t.TempDir()).Read(externalPath)
	if err != nil {
		t.Fatal(err)
	}
	report, err := FromRecord(loaded)
	if err != nil {
		t.Fatal(err)
	}
	for _, observation := range []PerformanceObservation{
		report.Performance.DecodeTPS, report.Performance.PrefillTPS, report.Performance.TTFTSeconds,
	} {
		if observation.Estimate == nil || observation.Status != StatusDescriptiveOnly || len(observation.Supports) != 0 {
			t.Fatalf("display-only performance retained claimability: %+v", observation)
		}
	}
	resident := report.Capacity.Resident
	if resident == nil || resident.Estimate == nil || resident.Status != StatusDescriptiveOnly || len(resident.Supports) != 0 {
		t.Fatalf("display-only resident evidence = %+v", resident)
	}
	if !hasGap(report.Gaps, GapStorageUnreconciled) || len(report.NextActions) != 0 {
		t.Fatalf("display-only report gaps/actions = %+v / %+v", report.Gaps, report.NextActions)
	}
}

func TestFromRecordStillRejectsTamperedCompletedEvidence(t *testing.T) {
	result := completedTestRecord(t)
	result.DecodeSum.Mean = 999
	report, err := FromRecord(result)
	if err == nil || !reflect.DeepEqual(report, Report{}) {
		t.Fatalf("tampered evidence returned report=%+v err=%v", report, err)
	}
}

func TestFromRecordActionCarriesSealedExperimentConfiguration(t *testing.T) {
	result := completedTestRecordConfigured(t, "", func(result *record.Record) {
		result.Level = "quick"
	})
	report, err := FromRecord(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.NextActions) != 1 || report.NextActions[0].Code != ActionCompleteBattery {
		t.Fatalf("next actions = %+v", report.NextActions)
	}
	action := report.NextActions[0]
	assertArgValue(t, action, "--ctx", "8192")
	assertArgValue(t, action, "--backend", "ollama")
	assertArgValue(t, action, "--profile", "default")
}

func TestPerformanceObservationsPreservePointFactsAndCacheEligibility(t *testing.T) {
	result := handRecord(validUncachedSamples(3))
	result.Speed[1].ClientDerived = true
	setSpeedSummaries(result)
	report := fromValidatedRecord(result)

	assertObservation(t, report.Performance.DecodeTPS, 22, UnitTokensPerSecond, AcquisitionMixed,
		ClaimObservedDecode)
	assertObservation(t, report.Performance.PrefillTPS, 210, UnitTokensPerSecond, AcquisitionMixed,
		ClaimObservedPrefill, ClaimUncachedPrefill)
	assertObservation(t, report.Performance.TTFTSeconds, 0.9, UnitSeconds, AcquisitionClientWallClock,
		ClaimObservedLoadedTTFT, ClaimLoadedUncachedTTFT)
	if report.Performance.DecodeTPS.SD == nil || report.Performance.DecodeTPS.Min == nil ||
		report.Performance.DecodeTPS.Max == nil {
		t.Fatalf("multi-sample shape was lost: %+v", report.Performance.DecodeTPS)
	}
	if hasGapPrefix(report.Gaps, "performance.prefill_cache_") || hasGapPrefix(report.Gaps, "performance.ttft_cache_") {
		t.Fatalf("verified cache misses produced cache gaps: %+v", report.Gaps)
	}
}

func TestFirstDecodeRepeatDriftIsDerivedCentrally(t *testing.T) {
	samples := validUncachedSamples(3)
	samples[0].DecodeTPS, samples[1].DecodeTPS, samples[2].DecodeTPS = 10, 20, 20
	result := handRecord(samples)
	report := fromValidatedRecord(result)
	if !report.Performance.DecodeTPS.FirstRunSlow || report.Performance.DecodeTPS.FirstRunRatio != 2 {
		t.Fatalf("decode first-run drift = %+v", report.Performance.DecodeTPS)
	}
	if report.Performance.PrefillTPS.FirstRunSlow || report.Performance.TTFTSeconds.FirstRunSlow {
		t.Fatalf("non-decode observations gained a decode drift claim: %+v", report.Performance)
	}
}

func TestCacheEligibilityNeverPromotesUnknownOrHitSamples(t *testing.T) {
	tests := []struct {
		name       string
		configure  func([]eval.SpeedResult)
		wantStatus []GapCode
	}{
		{name: "unknown", configure: func(samples []eval.SpeedResult) {
			for i := range samples {
				samples[i].PrefillCacheKnown, samples[i].GatedCacheKnown = false, false
			}
		}, wantStatus: []GapCode{GapPrefillCacheUnknown, GapTTFTCacheUnknown}},
		{name: "hit", configure: func(samples []eval.SpeedResult) {
			for i := range samples {
				samples[i].CachedPromptTok, samples[i].GatedCachedTok = 1, 1
			}
		}, wantStatus: []GapCode{GapPrefillCacheHit, GapTTFTCacheHit}},
		{name: "unknown and hit", configure: func(samples []eval.SpeedResult) {
			samples[0].PrefillCacheKnown, samples[0].GatedCacheKnown = false, false
			samples[1].CachedPromptTok, samples[1].GatedCachedTok = 1, 1
		}, wantStatus: []GapCode{GapPrefillCacheHit, GapPrefillCacheUnknown, GapTTFTCacheHit, GapTTFTCacheUnknown}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			samples := validUncachedSamples(3)
			test.configure(samples)
			report := fromValidatedRecord(handRecord(samples))
			if containsClaim(report.Performance.PrefillTPS.Supports, ClaimUncachedPrefill) ||
				containsClaim(report.Performance.TTFTSeconds.Supports, ClaimLoadedUncachedTTFT) {
				t.Fatalf("ineligible cache state retained an uncached claim: %+v", report.Performance)
			}
			if got := cacheGapCodes(report.Gaps); !reflect.DeepEqual(got, test.wantStatus) {
				t.Fatalf("cache gaps = %v, want %v", got, test.wantStatus)
			}
		})
	}
}

func TestCapacityRequiresAnExactContextReceiptAndNeverSealsPolicy(t *testing.T) {
	requested, adjusted := 32768, 16384
	tests := []struct {
		name   string
		plan   bool
		memory eval.MemoryResult
		want   GapCode
		have   bool
	}{
		{name: "not planned", want: GapResidentNotPlanned},
		{name: "unavailable", plan: true, memory: eval.MemoryResult{
			Outcome: eval.OutcomeSkipped, RequestedCtx: requested, UnavailableReason: "private/runtime/detail",
		}, want: GapResidentUnavailable},
		{name: "unverified", plan: true, memory: measuredMemory(requested, nil), want: GapResidentContextUnverified},
		{name: "adjusted", plan: true, memory: measuredMemory(requested, &adjusted), want: GapResidentContextAdjusted},
		{name: "exact", plan: true, memory: measuredMemory(requested, &requested), have: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := handRecord(validUncachedSamples(3))
			result.TaskPlan.Memory, result.Memory = test.plan, test.memory
			report := fromValidatedRecord(result)
			if (report.Capacity.Resident != nil) != test.have {
				t.Fatalf("resident = %+v, want present=%v", report.Capacity.Resident, test.have)
			}
			if test.want != "" && !hasGap(report.Gaps, test.want) {
				t.Fatalf("gaps = %+v, want %s", report.Gaps, test.want)
			}
			if !hasGap(report.Gaps, GapCapacityPolicyUnsealed) {
				t.Fatalf("capacity policy gap missing: %+v", report.Gaps)
			}
		})
	}
}

func TestExactResidentUsesBytesAndExplicitSupport(t *testing.T) {
	requested := 32768
	result := handRecord(validUncachedSamples(3))
	result.TaskPlan.Memory = true
	result.Memory = measuredMemory(requested, &requested)
	report := fromValidatedRecord(result)
	resident := report.Capacity.Resident
	if resident == nil || resident.Estimate == nil || *resident.Estimate != result.Memory.ResidentBytes ||
		resident.Unit != UnitBytes || resident.Status != StatusAvailable ||
		resident.Acquisition != AcquisitionRuntimeAllocation ||
		!containsClaim(resident.Supports, ClaimExactContextResidentBytes) {
		t.Fatalf("resident = %+v", resident)
	}
}

func TestContaminationMakesEveryObservationDescriptiveOnly(t *testing.T) {
	requested := 32768
	result := handRecord(validUncachedSamples(3))
	result.TaskPlan.Memory = true
	result.Memory = measuredMemory(requested, &requested)
	result.Contamination = []string{`C:\private\models\secret.gguf`}
	report := fromValidatedRecord(result)
	for _, observation := range []PerformanceObservation{
		report.Performance.DecodeTPS, report.Performance.PrefillTPS, report.Performance.TTFTSeconds,
	} {
		if observation.Estimate == nil || observation.Status != StatusDescriptiveOnly || len(observation.Supports) != 0 {
			t.Fatalf("contaminated performance made a claim: %+v", observation)
		}
	}
	if resident := report.Capacity.Resident; resident == nil || resident.Status != StatusDescriptiveOnly || len(resident.Supports) != 0 {
		t.Fatalf("contaminated resident made a claim: %+v", resident)
	}
	if !hasDiagnosis(report.Diagnoses, DiagnosisContaminated) || report.NextActions[0].Code != ActionRerunUncontaminated {
		t.Fatalf("contamination response = diagnoses %+v actions %+v", report.Diagnoses, report.NextActions)
	}
}

func TestUnboundArtifactLeavesFactsDescriptiveAndNoRankingAction(t *testing.T) {
	requested := 32768
	result := handRecord(validUncachedSamples(3))
	result.TaskPlan.Memory = true
	result.Memory = measuredMemory(requested, &requested)
	report := fromValidatedRecordWithIdentity(result, "not runtime bound")
	for _, observation := range []PerformanceObservation{
		report.Performance.DecodeTPS, report.Performance.PrefillTPS, report.Performance.TTFTSeconds,
	} {
		if observation.Status != StatusDescriptiveOnly || len(observation.Supports) != 0 {
			t.Fatalf("unbound artifact retained a performance claim: %+v", observation)
		}
	}
	if resident := report.Capacity.Resident; resident == nil || resident.Status != StatusDescriptiveOnly ||
		len(resident.Supports) != 0 {
		t.Fatalf("unbound artifact retained a resident claim: %+v", resident)
	}
	if !hasGap(report.Gaps, GapModelIdentityUnbound) || len(report.NextActions) != 0 {
		t.Fatalf("unbound artifact response = gaps %+v actions %+v", report.Gaps, report.NextActions)
	}
}

func TestFromRecordDetectsObservedOnlyArtifactBinding(t *testing.T) {
	artifact := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(artifact, []byte("test artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := FromRecord(completedTestRecordWithLocalArtifact(t, artifact))
	if err != nil {
		t.Fatal(err)
	}
	if report.Performance.DecodeTPS.Status != StatusDescriptiveOnly ||
		!hasGap(report.Gaps, GapModelIdentityUnbound) || len(report.NextActions) != 0 {
		t.Fatalf("observed-only identity was promoted: %+v", report)
	}
}

func TestOnlyDirectReceiptDiagnosesAreProduced(t *testing.T) {
	effective := 4096
	result := handRecord(validUncachedSamples(3))
	result.Contamination = []string{"secret-model"}
	result.DeviceV2.Context.EffectiveTokens = &effective
	report := fromValidatedRecord(result)
	want := []DiagnosisCode{DiagnosisContextAdjusted, DiagnosisContaminated}
	if got := diagnosisCodes(report.Diagnoses); !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnoses = %v, want %v", got, want)
	}
	data, err := json.Marshal(report.Diagnoses)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"confidence", "bandwidth", "compute-bound", "thermal", "limiter"} {
		if strings.Contains(strings.ToLower(string(data)), forbidden) {
			t.Fatalf("diagnosis invented %q: %s", forbidden, data)
		}
	}
}

func TestNextActionPriorityAndTemplates(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*record.Record)
		want      ActionCode
	}{
		{name: "board", want: ActionOpenBoard},
		{name: "quick", configure: func(r *record.Record) { r.Level = "quick" }, want: ActionCompleteBattery},
		{name: "few repeats", configure: func(r *record.Record) { r.Repeats = 1 }, want: ActionIncreaseRepeats},
		{name: "tools blocked", configure: func(r *record.Record) {
			r.Scorecard.Needs["tool_calling"] = score.Verdict{State: score.Blocked}
		}, want: ActionDiagnoseTools},
		{name: "contamination first", configure: func(r *record.Record) {
			r.Contamination = []string{"secret"}
			r.Scorecard.Needs["tool_calling"] = score.Verdict{State: score.Blocked}
			r.Repeats = 1
		}, want: ActionRerunUncontaminated},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := handRecord(validUncachedSamples(3))
			if test.configure != nil {
				test.configure(result)
			}
			actions := fromValidatedRecord(result).NextActions
			if len(actions) != 1 || actions[0].Code != test.want {
				t.Fatalf("actions = %+v, want %s", actions, test.want)
			}
			assertSafeArgvTemplate(t, actions[0])
			assertActionPreservesSealedConfiguration(t, result, actions[0])
		})
	}
}

func TestReportIsDeterministicDetachedAndPrivacySafe(t *testing.T) {
	requested := 8192
	result := handRecord(validUncachedSamples(3))
	result.Model = `C:\Users\private\secret-model.gguf`
	result.Device.Host = "private-host"
	result.Device.Runtime = "https://private-endpoint.example/v1"
	result.Contamination = []string{"private-resident-model"}
	result.TaskPlan.Memory = true
	result.Memory = measuredMemory(requested, &requested)
	first := fromValidatedRecord(result)
	second := fromValidatedRecord(result)
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("analysis is not deterministic:\n%s\n%s", firstJSON, secondJSON)
	}
	for _, secret := range []string{result.Model, result.Device.Host, result.Device.Runtime, result.Contamination[0]} {
		if strings.Contains(string(firstJSON), secret) {
			t.Fatalf("report leaked %q: %s", secret, firstJSON)
		}
	}
	assertDetached(t, result, first)
}

func TestObservedZeroIsNotMissingAndSingleSampleHasNoSD(t *testing.T) {
	result := handRecord(validUncachedSamples(1))
	result.Speed[0].TTFT = 0
	setSpeedSummaries(result)
	report := fromValidatedRecord(result)
	observation := report.Performance.TTFTSeconds
	if observation.Estimate == nil || *observation.Estimate != 0 || observation.Status != StatusAvailable {
		t.Fatalf("observed zero became missing: %+v", observation)
	}
	if observation.SD != nil {
		t.Fatalf("single observation acquired fake precision: %+v", observation)
	}
	if !hasGap(report.Gaps, GapPerformanceSampleCountLow) {
		t.Fatalf("small-sample gap missing: %+v", report.Gaps)
	}
}

func completedTestRecord(t *testing.T) *record.Record {
	t.Helper()
	return completedTestRecordWithLocalArtifact(t, "")
}

func completedTestRecordWithLocalArtifact(t *testing.T, localArtifact string) *record.Record {
	t.Helper()
	return completedTestRecordConfigured(t, localArtifact, nil)
}

func completedTestRecordConfigured(t *testing.T, localArtifact string, configure func(*record.Record)) *record.Record {
	t.Helper()
	result := handRecord(validUncachedSamples(3))
	if configure != nil {
		configure(result)
	}
	result.Manifest, result.Completion = nil, nil
	profile := device.Profile{Name: "default", Description: "test", Gates: map[string]device.Gate{}}
	result.Profile = profile.Name
	counts, err := result.DeriveEvidenceCounts()
	if err != nil {
		t.Fatal(err)
	}
	result.EvidenceCounts = counts
	result.Rep = score.RepetitionMetrics("")
	result.Density = score.InformationDensity("")
	result.Scorecard = score.Score(result.Measured(), profile)
	hashes, err := eval.BuiltinHashes()
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := record.NewRunProvenance(hashes.TaskSetSHA256, hashes.SpecSHA256, profile,
		record.CurrentScoringPolicy(), record.SoftwareReceipt{
			FitrVersion: "test", SoftwareBuildSHA256: testDigest,
			BackendProtocol: record.BackendProtocol("ollama"),
		})
	if err != nil {
		t.Fatal(err)
	}
	digest := testDigest
	if localArtifact != "" {
		digest = ""
	}
	identity, err := record.NewModelIdentity("model", "model", "ollama", "ollama test", digest, localArtifact, 1234)
	if err != nil {
		t.Fatal(err)
	}
	if err := result.AttachManifest(identity, provenance); err != nil {
		t.Fatal(err)
	}
	if err := result.CompleteEvidence(profile); err != nil {
		t.Fatal(err)
	}
	return result
}

func handRecord(samples []eval.SpeedResult) *record.Record {
	effective := 8192
	fingerprint := device.Fingerprint{
		Host: "host", OS: "linux", CPU: "cpu", GPU: "gpu", Runtime: "ollama test",
		InferenceDevice: "GPU 100%", Config: map[string]string{},
	}
	v2, err := device.NewFingerprintV2(fingerprint, device.ContextVerification{
		RequestedTokens: effective, EffectiveTokens: &effective,
		EffectiveSource: device.ContextSourceRuntimeReport,
	})
	if err != nil {
		panic(err)
	}
	result := &record.Record{
		SchemaVersion: record.EvidenceSchemaVersion,
		Manifest: &record.RunManifest{Schema: record.RunManifestSchema,
			Model: record.ModelIdentity{Backend: "ollama"}, Profile: "default", NumCtx: effective},
		Completion: &record.CompletionReceipt{Schema: record.CompletionReceiptSchema},
		Model:      "model", StartedAt: "2026-08-30T12:00:00Z", Level: "full",
		ExecutionPolicy: record.ExecutionDisabled, SeedSet: "analysis-test", Repeats: len(samples), NumCtx: effective,
		Device: fingerprint, DeviceV2: &v2, Profile: "default",
		TaskPlan: record.TaskPlan{SpeedSamples: len(samples)}, Speed: samples,
		Scorecard: score.Scorecard{Model: "model", Profile: "default", Needs: map[string]score.Verdict{}},
	}
	key, err := v2.ComparabilityKey()
	if err != nil {
		panic(err)
	}
	result.DeviceKey = key
	setSpeedSummaries(result)
	return result
}

func validUncachedSamples(count int) []eval.SpeedResult {
	samples := make([]eval.SpeedResult, count)
	for i := range samples {
		samples[i] = eval.SpeedResult{
			DecodeTPS: 21 + float64(i), PrefillTPS: 200 + 10*float64(i), TTFT: 0.8 + 0.1*float64(i),
			PromptTok: 200, PrefillCacheKnown: true, GatedPromptTok: 100, GatedCacheKnown: true,
			FirstOutputObserved: true,
		}
	}
	return samples
}

func setSpeedSummaries(result *record.Record) {
	decode, prefill, ttft := speedValues(result.Speed)
	result.DecodeSum = stats.MeanSD(decode)
	result.PrefillSum = stats.MeanSD(prefill)
	result.TTFTSum = stats.MeanSD(ttft)
}

func measuredMemory(requested int, effective *int) eval.MemoryResult {
	const bytes = int64(20 * 1024 * 1024 * 1024)
	return eval.MemoryResult{
		Outcome: eval.OutcomePass, RequestedCtx: requested, EffectiveCtx: cloneInt(effective),
		ResidentBytes: bytes, ResidentGB: 20, AcceleratorBytes: bytes, PctOnGPU: 100,
	}
}

func assertObservation(t *testing.T, got PerformanceObservation, estimate float64, unit Unit,
	acquisition Acquisition, supports ...SupportClaim) {
	t.Helper()
	if got.Estimate == nil || *got.Estimate != estimate || got.Unit != unit || got.Status != StatusAvailable ||
		got.Acquisition != acquisition || !reflect.DeepEqual(got.Supports, supports) {
		t.Fatalf("observation = %+v, want estimate=%v unit=%s acquisition=%s supports=%v",
			got, estimate, unit, acquisition, supports)
	}
}

func cacheGapCodes(gaps []EvidenceGap) []GapCode {
	var codes []GapCode
	for _, gap := range gaps {
		if strings.Contains(string(gap.Code), "cache_") {
			codes = append(codes, gap.Code)
		}
	}
	return codes
}

func diagnosisCodes(diagnoses []Diagnosis) []DiagnosisCode {
	codes := make([]DiagnosisCode, len(diagnoses))
	for i, diagnosis := range diagnoses {
		codes[i] = diagnosis.Code
	}
	return codes
}

func containsClaim(claims []SupportClaim, claim SupportClaim) bool {
	return slices.Contains(claims, claim)
}

func hasGap(gaps []EvidenceGap, code GapCode) bool {
	for _, gap := range gaps {
		if gap.Code == code {
			return true
		}
	}
	return false
}

func hasGapPrefix(gaps []EvidenceGap, prefix string) bool {
	for _, gap := range gaps {
		if strings.HasPrefix(string(gap.Code), prefix) {
			return true
		}
	}
	return false
}

func hasDiagnosis(diagnoses []Diagnosis, code DiagnosisCode) bool {
	for _, diagnosis := range diagnoses {
		if diagnosis.Code == code {
			return true
		}
	}
	return false
}

func assertSafeArgvTemplate(t *testing.T, action Action) {
	t.Helper()
	joined := strings.Join(action.Argv, " ")
	if strings.ContainsAny(joined, `\"'`) || strings.Contains(joined, "secret") ||
		(strings.Contains(joined, "run") || strings.Contains(joined, "diag")) &&
			!strings.Contains(joined, CurrentModelPlaceholder) {
		t.Fatalf("unsafe argv template: %+v", action)
	}
	for _, forbidden := range []string{"sh", "-c", "powershell", "cmd.exe", "apply"} {
		if slices.Contains(action.Argv, forbidden) {
			t.Fatalf("action contains forbidden shell or saved-state operation: %+v", action)
		}
	}
}

func assertActionPreservesSealedConfiguration(t *testing.T, result *record.Record, action Action) {
	t.Helper()
	if action.Code == ActionOpenBoard {
		return
	}
	if !slices.Contains(action.Argv, "--ctx") || !slices.Contains(action.Argv, "--backend") {
		t.Fatalf("action omitted sealed context or backend: %+v", action)
	}
	assertArgValue(t, action, "--ctx", "8192")
	assertArgValue(t, action, "--backend", result.Manifest.Model.Backend)
	if action.Code != ActionDiagnoseTools {
		assertArgValue(t, action, "--profile", result.Manifest.Profile)
	}
}

func assertArgValue(t *testing.T, action Action, flag, want string) {
	t.Helper()
	for i, arg := range action.Argv {
		if arg == flag && i+1 < len(action.Argv) {
			if action.Argv[i+1] != want {
				t.Fatalf("%s value = %q, want %q in %+v", flag, action.Argv[i+1], want, action)
			}
			return
		}
	}
	t.Fatalf("action omitted %s: %+v", flag, action)
}

func assertDetached(t *testing.T, result *record.Record, report Report) {
	t.Helper()
	originalSample := result.Speed[0].DecodeTPS
	originalContext := *result.DeviceV2.Context.EffectiveTokens
	originalBytes := result.Memory.ResidentBytes
	report.Performance.DecodeTPS.Samples[0] = 999
	*report.Context.Effective = 1
	*report.Capacity.Resident.Estimate = 1
	if result.Speed[0].DecodeTPS != originalSample || *result.DeviceV2.Context.EffectiveTokens != originalContext ||
		result.Memory.ResidentBytes != originalBytes {
		t.Fatal("mutating analysis changed the source record")
	}
}
