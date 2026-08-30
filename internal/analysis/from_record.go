package analysis

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

// FromRecord derives a privacy-safe analysis from one sealed current result.
// Validation errors return the zero Report so no caller can accidentally render
// facts from an invalid or unsupported evidence contract.
func FromRecord(result *record.Record) (Report, error) {
	if result == nil {
		return Report{}, errors.New("analyze result: nil record")
	}
	if result.SchemaVersion != record.EvidenceSchemaVersion {
		return Report{}, fmt.Errorf("analyze result: schema %d is unsupported; require schema %d",
			result.SchemaVersion, record.EvidenceSchemaVersion)
	}
	if err := result.ValidateEvidenceContract(); err != nil {
		return Report{}, fmt.Errorf("analyze result: validate evidence: %w", err)
	}
	identityIssue := result.Manifest.Model.RankingIssue()
	if issue := result.EvidenceIntegrityIssue(); issue != "" && issue != identityIssue {
		return Report{}, fmt.Errorf("analyze result: evidence integrity: %s", issue)
	}
	return fromValidatedRecordWithIdentity(result, identityIssue), nil
}

// fromValidatedRecord is kept separate so extraction tests can exercise every
// evidence combination without reproducing record's signing implementation.
// FromRecord is the only production entry point and always validates first.
func fromValidatedRecord(result *record.Record) Report {
	return fromValidatedRecordWithIdentity(result, "")
}

func fromValidatedRecordWithIdentity(result *record.Record, identityIssue string) Report {
	identityUnbound := identityIssue != ""
	report := Report{
		Schema: ReportSchema,
		Policy: PolicySchema,
		Source: Source{
			Kind: SourceSealedResult, RecordSchema: result.SchemaVersion,
			ManifestSchema: result.Manifest.Schema, CompletionSchema: result.Completion.Schema,
			EvidenceSHA256: result.Completion.EvidenceSHA256, Validated: true,
		},
		Context:     contextFrom(result),
		Performance: performanceFrom(result, identityUnbound),
		Capacity:    capacityFrom(result, identityUnbound),
	}
	report.Gaps = gapsFrom(result, report, identityUnbound)
	report.Diagnoses = diagnosesFrom(result)
	report.NextActions = nextActionsFrom(result, identityUnbound)
	return report
}

func contextFrom(result *record.Record) Context {
	context := Context{Requested: result.ContextSize()}
	if result.DeviceV2 == nil {
		return context
	}
	verification := result.DeviceV2.Context
	context.Requested = verification.RequestedTokens
	context.Effective = cloneInt(verification.EffectiveTokens)
	context.State = string(verification.State())
	context.EffectiveSource = string(verification.EffectiveSource)
	return context
}

func performanceFrom(result *record.Record, identityUnbound bool) Performance {
	decode, prefill, ttft := speedValues(result.Speed)
	decodeSupport := []SupportClaim(nil)
	if result.DecodeSum.N > 0 {
		decodeSupport = []SupportClaim{ClaimObservedDecode}
	}
	prefillSupport := []SupportClaim(nil)
	if result.PrefillSum.N > 0 {
		prefillSupport = []SupportClaim{ClaimObservedPrefill}
		if cacheEligibility(result.Speed, prefillCacheReceipt) == cacheVerifiedUncached {
			prefillSupport = append(prefillSupport, ClaimUncachedPrefill)
		}
	}
	ttftSupport := []SupportClaim(nil)
	if result.TTFTSum.N > 0 {
		ttftSupport = []SupportClaim{ClaimObservedLoadedTTFT}
		if cacheEligibility(result.Speed, ttftCacheReceipt) == cacheVerifiedUncached {
			ttftSupport = append(ttftSupport, ClaimLoadedUncachedTTFT)
		}
	}
	contaminated := len(result.Contamination) > 0 || identityUnbound
	return Performance{
		DecodeTPS: observation(result.DecodeSum, decode, UnitTokensPerSecond,
			timingAcquisition(result.Speed), decodeSupport, contaminated, true),
		PrefillTPS: observation(result.PrefillSum, prefill, UnitTokensPerSecond,
			timingAcquisition(result.Speed), prefillSupport, contaminated, false),
		TTFTSeconds: observation(result.TTFTSum, ttft, UnitSeconds,
			AcquisitionClientWallClock, ttftSupport, contaminated, false),
	}
}

func observation(summary stats.Summary, samples []float64, unit Unit, acquisition Acquisition,
	supports []SupportClaim, contaminated, detectFirstRun bool) PerformanceObservation {
	out := PerformanceObservation{
		Unit: unit, Status: StatusAvailable, Acquisition: acquisition, SampleCount: summary.N,
		Samples: append([]float64(nil), samples...), Supports: append([]SupportClaim(nil), supports...),
	}
	if summary.N == 0 {
		out.Status = StatusUnavailable
		out.Acquisition = AcquisitionUnavailable
		return out
	}
	if contaminated {
		out.Status = StatusDescriptiveOnly
		out.Supports = nil
	}
	out.Estimate = float64Pointer(summary.Mean)
	out.Min, out.Max = float64Pointer(summary.Min), float64Pointer(summary.Max)
	if summary.N >= 2 {
		out.SD = float64Pointer(summary.SD)
	}
	if detectFirstRun {
		out.FirstRunSlow, out.FirstRunRatio = stats.FirstRunSlow(samples)
	}
	return out
}

func speedValues(results []eval.SpeedResult) (decode, prefill, ttft []float64) {
	decode = make([]float64, 0, len(results))
	prefill = make([]float64, 0, len(results))
	ttft = make([]float64, 0, len(results))
	for _, result := range results {
		decode = append(decode, result.DecodeTPS)
		prefill = append(prefill, result.PrefillTPS)
		ttft = append(ttft, result.TTFT)
	}
	return decode, prefill, ttft
}

func timingAcquisition(results []eval.SpeedResult) Acquisition {
	if len(results) == 0 {
		return AcquisitionUnavailable
	}
	clientDerived := 0
	for _, result := range results {
		if result.ClientDerived {
			clientDerived++
		}
	}
	switch clientDerived {
	case 0:
		return AcquisitionRuntimeReported
	case len(results):
		return AcquisitionClientDerived
	default:
		return AcquisitionMixed
	}
}

func capacityFrom(result *record.Record, identityUnbound bool) Capacity {
	memory := result.Memory
	if !result.TaskPlan.Memory || memory.RequestedCtx <= 0 {
		return Capacity{}
	}
	if _, verified := memory.VerifiedAt(memory.RequestedCtx); !verified {
		return Capacity{}
	}
	status := StatusAvailable
	supports := []SupportClaim{ClaimExactContextResidentBytes}
	if len(result.Contamination) > 0 || identityUnbound {
		status = StatusDescriptiveOnly
		supports = nil
	}
	return Capacity{Resident: &ResidentObservation{
		Estimate: int64Pointer(memory.ResidentBytes), Unit: UnitBytes,
		Status: status, Acquisition: AcquisitionRuntimeAllocation,
		RequestedContext: memory.RequestedCtx, EffectiveContext: cloneInt(memory.EffectiveCtx),
		Supports: supports,
	}}
}

type cacheStatus int

const (
	cacheVerifiedUncached cacheStatus = iota
	cacheUnknown
	cacheHit
	cacheUnknownAndHit
)

type cacheReceipt func(eval.SpeedResult) (valid, hit bool)

func prefillCacheReceipt(result eval.SpeedResult) (bool, bool) {
	return result.PrefillCacheReceiptValid(), result.PrefillContaminated()
}

func ttftCacheReceipt(result eval.SpeedResult) (bool, bool) {
	return result.GatedCacheReceiptValid(), result.GatedTTFTContaminated()
}

func cacheEligibility(results []eval.SpeedResult, receipt cacheReceipt) cacheStatus {
	unknown, hit := len(results) == 0, false
	for _, result := range results {
		valid, contaminated := receipt(result)
		unknown = unknown || !valid
		hit = hit || contaminated
	}
	switch {
	case unknown && hit:
		return cacheUnknownAndHit
	case unknown:
		return cacheUnknown
	case hit:
		return cacheHit
	default:
		return cacheVerifiedUncached
	}
}

func gapsFrom(result *record.Record, report Report, identityUnbound bool) []EvidenceGap {
	gaps := []EvidenceGap{gap(GapCapacityPolicyUnsealed, "capacity",
		"no sealed usable-capacity policy exists, so resident bytes cannot establish headroom or fit",
		ClaimCapacityHeadroom, ClaimFit)}
	if identityUnbound {
		gaps = append(gaps, gap(GapModelIdentityUnbound, "artifact",
			"the observed artifact was not bound to the serving runtime, so measurements remain descriptive only"))
	}
	gaps = appendUnavailablePerformanceGaps(gaps, report.Performance)
	gaps = appendCacheGaps(gaps, result.Speed)
	if lowPerformanceSampleCount(report.Performance) {
		gaps = append(gaps, gap(GapPerformanceSampleCountLow, "performance",
			"fewer than three observations cannot establish stable performance", ClaimStablePerformance))
	}
	gaps = appendCapacityGaps(gaps, result)
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].Code < gaps[j].Code })
	return gaps
}

func appendUnavailablePerformanceGaps(gaps []EvidenceGap, performance Performance) []EvidenceGap {
	if performance.DecodeTPS.Estimate == nil {
		gaps = append(gaps, gap(GapDecodeUnavailable, "performance",
			"decode throughput was not observed", ClaimObservedDecode))
	}
	if performance.PrefillTPS.Estimate == nil {
		gaps = append(gaps, gap(GapPrefillUnavailable, "performance",
			"prefill throughput was not observed", ClaimObservedPrefill, ClaimUncachedPrefill))
	}
	if performance.TTFTSeconds.Estimate == nil {
		gaps = append(gaps, gap(GapTTFTUnavailable, "performance",
			"loaded time to first token was not observed", ClaimObservedLoadedTTFT, ClaimLoadedUncachedTTFT))
	}
	return gaps
}

func appendCacheGaps(gaps []EvidenceGap, results []eval.SpeedResult) []EvidenceGap {
	if len(results) == 0 {
		return gaps
	}
	gaps = appendCacheStatusGaps(gaps, cacheEligibility(results, prefillCacheReceipt),
		GapPrefillCacheUnknown, GapPrefillCacheHit, "prefill", ClaimUncachedPrefill)
	return appendCacheStatusGaps(gaps, cacheEligibility(results, ttftCacheReceipt),
		GapTTFTCacheUnknown, GapTTFTCacheHit, "TTFT", ClaimLoadedUncachedTTFT)
}

func appendCacheStatusGaps(gaps []EvidenceGap, status cacheStatus, unknownCode, hitCode GapCode,
	label string, claim SupportClaim) []EvidenceGap {
	if status == cacheUnknown || status == cacheUnknownAndHit {
		gaps = append(gaps, gap(unknownCode, "performance",
			label+" cache state was not proven by a positive-denominator receipt", claim))
	}
	if status == cacheHit || status == cacheUnknownAndHit {
		gaps = append(gaps, gap(hitCode, "performance",
			label+" observed cached prompt tokens and remains descriptive only", claim))
	}
	return gaps
}

func lowPerformanceSampleCount(performance Performance) bool {
	for _, observation := range []PerformanceObservation{
		performance.DecodeTPS, performance.PrefillTPS, performance.TTFTSeconds,
	} {
		if observation.SampleCount > 0 && observation.SampleCount < 3 {
			return true
		}
	}
	return false
}

func appendCapacityGaps(gaps []EvidenceGap, result *record.Record) []EvidenceGap {
	if !result.TaskPlan.Memory {
		return append(gaps, gap(GapResidentNotPlanned, "capacity",
			"an exact-context resident allocation was not planned", ClaimExactContextResidentBytes))
	}
	memory := result.Memory
	if memory.Outcome == eval.OutcomeSkipped {
		return append(gaps, gap(GapResidentUnavailable, "capacity",
			"the runtime did not provide an exact resident allocation", ClaimExactContextResidentBytes))
	}
	if memory.EffectiveCtx == nil {
		return append(gaps, gap(GapResidentContextUnverified, "capacity",
			"resident bytes lack a runtime-verified effective context", ClaimExactContextResidentBytes))
	}
	if *memory.EffectiveCtx != memory.RequestedCtx {
		return append(gaps, gap(GapResidentContextAdjusted, "capacity",
			"resident bytes were observed at a different effective context", ClaimExactContextResidentBytes))
	}
	return gaps
}

func gap(code GapCode, section, message string, claims ...SupportClaim) EvidenceGap {
	return EvidenceGap{Code: code, Section: section, Message: message,
		UnsupportedClaims: append([]SupportClaim(nil), claims...)}
}

func diagnosesFrom(result *record.Record) []Diagnosis {
	var diagnoses []Diagnosis
	if len(result.Contamination) > 0 {
		diagnoses = append(diagnoses, Diagnosis{
			Code:      DiagnosisContaminated,
			Statement: "another resident model contaminated the measured run",
			Evidence:  []string{"sealed_completion.contamination"},
		})
	}
	if result.DeviceV2 != nil && result.DeviceV2.Context.State() == device.ContextAdjusted {
		diagnoses = append(diagnoses, Diagnosis{
			Code:      DiagnosisContextAdjusted,
			Statement: "the runtime reported an effective context different from the request",
			Evidence:  []string{"device_fingerprint_v2.context"},
		})
	}
	sort.Slice(diagnoses, func(i, j int) bool { return diagnoses[i].Code < diagnoses[j].Code })
	return diagnoses
}

func nextActionsFrom(result *record.Record, identityUnbound bool) []Action {
	if identityUnbound && len(result.Contamination) == 0 {
		return nil
	}
	action := Action{Code: ActionOpenBoard, Argv: []string{"fitr", "board"},
		Reason: "compare this validated result only with compatible saved runs"}
	switch {
	case len(result.Contamination) > 0:
		action = Action{Code: ActionRerunUncontaminated,
			Argv:   []string{"fitr", "run", CurrentModelPlaceholder},
			Reason: "replace timing evidence contaminated by another resident model"}
	case toolsBlocked(result.Scorecard):
		action = Action{Code: ActionDiagnoseTools,
			Argv:   []string{"fitr", "diag", CurrentModelPlaceholder},
			Reason: "separate runtime tool plumbing from model behavior"}
	case result.Repeats > 0 && result.Repeats < 3:
		action = Action{Code: ActionIncreaseRepeats,
			Argv:   []string{"fitr", "run", CurrentModelPlaceholder, "-k", "3"},
			Reason: "collect enough repeats for a stable comparison"}
	case result.Level == "quick" || result.Level == "checks" || result.Level == "checks-only":
		action = Action{Code: ActionCompleteBattery,
			Argv:   []string{"fitr", "run", CurrentModelPlaceholder},
			Reason: "measure the standard behavior and performance battery"}
	}
	return []Action{action}
}

func toolsBlocked(card score.Scorecard) bool {
	for need, verdict := range card.Needs {
		if verdict.State == score.Blocked && (strings.Contains(need, "tool") || need == "unattended_agentic") {
			return true
		}
	}
	return false
}

func float64Pointer(value float64) *float64 { return &value }
func int64Pointer(value int64) *int64       { return &value }

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
