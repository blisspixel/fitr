package analysis

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	capacityevidence "github.com/blisspixel/fitr/internal/capacity"
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
	displayOnly := displayOnlyNone
	identityIssue := result.Manifest.Model.RankingIssue()
	if issue := result.EvidenceIntegrityIssue(); issue != "" {
		if issue == identityIssue {
			displayOnly = displayOnlyIdentity
		} else {
			// The evidence contract already passed above. The only remaining
			// integrity state is Store's local current/history reconciliation.
			// It removes claimability, not the sealed observations themselves.
			displayOnly = displayOnlyStorage
		}
	}
	return fromValidatedRecordWithDisplayOnly(result, displayOnly), nil
}

type displayOnlyReason uint8

const (
	displayOnlyNone displayOnlyReason = iota
	displayOnlyIdentity
	displayOnlyStorage
)

// fromValidatedRecord is kept separate so extraction tests can exercise every
// evidence combination without reproducing record's signing implementation.
// FromRecord is the only production entry point and always validates first.
func fromValidatedRecord(result *record.Record) Report {
	return fromValidatedRecordWithDisplayOnly(result, displayOnlyNone)
}

func fromValidatedRecordWithIdentity(result *record.Record, identityIssue string) Report {
	reason := displayOnlyNone
	if identityIssue != "" {
		reason = displayOnlyIdentity
	}
	return fromValidatedRecordWithDisplayOnly(result, reason)
}

func fromValidatedRecordWithDisplayOnly(result *record.Record, displayOnly displayOnlyReason) Report {
	unclaimable := displayOnly != displayOnlyNone
	manifestSchema := ""
	if result.Manifest != nil {
		manifestSchema = result.Manifest.Schema
	}
	completionSchema := ""
	evidenceSHA256 := ""
	if result.Completion != nil {
		completionSchema = result.Completion.Schema
		evidenceSHA256 = result.Completion.EvidenceSHA256
	}
	report := Report{
		Schema: ReportSchema,
		Policy: PolicySchema,
		Source: Source{
			Kind: SourceSealedResult, RecordSchema: result.SchemaVersion,
			ManifestSchema: manifestSchema, CompletionSchema: completionSchema,
			EvidenceSHA256: evidenceSHA256, Validated: true,
		},
		Artifact:    artifactIdentityFrom(result),
		Context:     contextFrom(result),
		Performance: performanceFrom(result, unclaimable),
		Capacity:    capacityFrom(result, unclaimable),
	}
	report.Gaps = gapsFrom(result, report, displayOnly)
	report.Diagnoses = diagnosesFrom(result, unclaimable)
	report.NextActions = nextActionsFrom(result, unclaimable)
	return report
}

func artifactIdentityFrom(result *record.Record) ArtifactIdentity {
	identity := ArtifactIdentity{
		Quant:         result.ModelMeta.Details.QuantizationLevel,
		Family:        result.ModelMeta.Details.Family,
		ParameterSize: result.ModelMeta.Details.ParameterSize,
		SizeBytes:     max(result.ModelMeta.Size, 0),
	}
	if result.Manifest == nil {
		return identity
	}
	if result.Manifest.Model.SizeBytes > 0 {
		identity.SizeBytes = result.Manifest.Model.SizeBytes
	}
	identity.Digest = result.Manifest.Model.RuntimeBoundDigest()
	return identity
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
	runtimeUnloadedTTFT, runtimeLoad, loadedCacheHitTTFT := latencyStateValues(result)
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
		ttftSupport = []SupportClaim{ClaimObservedRequestTTFT}
		loaded := ttftResidencyEligibility(result.Speed) == residencyVerifiedLoaded
		if loaded {
			ttftSupport = append(ttftSupport, ClaimObservedLoadedTTFT)
		}
		if loaded && cacheEligibility(result.Speed, ttftCacheReceipt) == cacheVerifiedUncached {
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
		RuntimeUnloadedTTFTSeconds: observationFromValues(runtimeUnloadedTTFT, UnitSeconds,
			AcquisitionClientWallClock, ClaimObservedRuntimeUnloadedTTFT, contaminated),
		RuntimeLoadSeconds: observationFromValues(runtimeLoad, UnitSeconds,
			AcquisitionRuntimeReported, ClaimObservedRuntimeLoadTime, contaminated),
		LoadedCacheHitTTFTSeconds: observationFromValues(loadedCacheHitTTFT, UnitSeconds,
			AcquisitionClientWallClock, ClaimObservedLoadedCacheHitTTFT, contaminated),
	}
}

func latencyStateValues(result *record.Record) (runtimeUnloadedTTFT, runtimeLoad, loadedCacheHitTTFT []float64) {
	ollamaLoadReceipt := result.Manifest != nil && result.Manifest.Provenance != nil &&
		result.Manifest.Provenance.BackendProtocol == record.BackendProtocolOllama
	for _, sample := range result.Speed {
		if ollamaLoadReceipt && sample.ColdLoad > 0.1 {
			runtimeLoad = append(runtimeLoad, sample.ColdLoad)
			if sample.ColdTTFT > 0 {
				runtimeUnloadedTTFT = append(runtimeUnloadedTTFT, sample.ColdTTFT)
			}
		}
		if sample.WarmTTFT > 0 && sample.WarmCacheReceiptValid() {
			loadedCacheHitTTFT = append(loadedCacheHitTTFT, sample.WarmTTFT)
		}
	}
	return runtimeUnloadedTTFT, runtimeLoad, loadedCacheHitTTFT
}

func observationFromValues(values []float64, unit Unit, acquisition Acquisition,
	support SupportClaim, descriptiveOnly bool) PerformanceObservation {
	var supports []SupportClaim
	if len(values) > 0 {
		supports = []SupportClaim{support}
	}
	return observation(stats.MeanSD(values), values, unit, acquisition, supports, descriptiveOnly, false)
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
	capacity := capacityPlanFrom(result)
	memory := result.Memory
	if !result.TaskPlan.Memory || memory.RequestedCtx <= 0 {
		return capacity
	}
	allocation, verified := memory.VerifiedAllocationAt(memory.RequestedCtx)
	if !verified {
		return capacity
	}
	status := StatusAvailable
	supports := []SupportClaim{ClaimExactContextResidentBytes}
	if len(result.Contamination) > 0 || identityUnbound {
		status = StatusDescriptiveOnly
		supports = nil
	}
	capacity.Resident = &ResidentObservation{
		Estimate: int64Pointer(allocation.ResidentBytes), Unit: UnitBytes,
		Status: status, Acquisition: AcquisitionRuntimeAllocation,
		RequestedContext: memory.RequestedCtx, EffectiveContext: cloneInt(memory.EffectiveCtx),
		Supports: supports,
	}
	capacity.Placement = placementFrom(allocation, status, identityUnbound || len(result.Contamination) > 0)
	capacity.Budget = capacityBudgetFrom(result.CapacityPlan, allocation.ResidentBytes, status)
	return capacity
}

func capacityPlanFrom(result *record.Record) Capacity {
	if result == nil || result.CapacityPlan == nil {
		return Capacity{}
	}
	plan := result.CapacityPlan
	policy := plan.Policy
	prediction := plan.Prediction
	policyStatus := StatusDescriptiveOnly
	policySupports := []SupportClaim{ClaimSealedCapacityPolicy}
	if policy.UsableBudgetBytes != nil {
		policyStatus = StatusAvailable
	}
	predictionStatus := StatusUnavailable
	var predictionSupports []SupportClaim
	if prediction.State == capacityevidence.PredictionComponentProjection {
		predictionStatus = StatusAvailable
		predictionSupports = []SupportClaim{ClaimProjectedCapacityComponents}
	}
	return Capacity{
		Policy: &CapacityPolicyObservation{
			ResourceDomain:         string(policy.ResourceDomain),
			AddressableBytes:       observationBytes(policy.Addressable),
			AddressableSource:      observationSource(policy.Addressable),
			CurrentAvailableBytes:  observationBytes(policy.CurrentAvailable),
			CurrentAvailableSource: observationSource(policy.CurrentAvailable),
			CurrentAvailableAt:     observationTime(policy.CurrentAvailable),
			ContainerHeadroomBytes: containerHeadroom(policy.Container),
			ContainerSource:        containerSource(policy.Container),
			OperatorReserveBytes:   cloneInt64(policy.OperatorReserveBytes),
			UsableBudgetBytes:      cloneInt64(policy.UsableBudgetBytes),
			Formula:                string(policy.Formula), SwapPolicy: string(policy.Swap),
			Status: policyStatus, Supports: policySupports,
		},
		Prediction: &CapacityPredictionObservation{
			CreatedAt: prediction.CreatedAt, RequestedContext: prediction.RequestedContext,
			Architecture: prediction.Architecture, KVDataType: prediction.KVDataType,
			ArtifactBytes: cloneInt64(prediction.ArtifactBytes), KVBytes: cloneInt64(prediction.KVBytes),
			KnownComponentBytes: cloneInt64(prediction.KnownComponentBytes),
			PlacementAssumption: prediction.PlacementAssumption,
			Missing:             append([]string(nil), prediction.Missing...),
			Excluded:            append([]string(nil), prediction.Excluded...),
			Status:              predictionStatus, Supports: predictionSupports,
		},
		Budget: capacityBudgetFrom(plan, 0, StatusUnavailable),
	}
}

func capacityBudgetFrom(plan *capacityevidence.Plan, residentBytes int64,
	residentStatus ObservationStatus) *CapacityBudgetObservation {
	if plan == nil || plan.Policy.UsableBudgetBytes == nil {
		return nil
	}
	budget := *plan.Policy.UsableBudgetBytes
	observation := &CapacityBudgetObservation{
		State: CapacityBudgetUnresolved, BudgetBytes: budget,
		Status: StatusUnavailable, Acquisition: AcquisitionMixed,
	}
	if residentBytes <= 0 {
		return observation
	}
	observation.ObservedBytes = int64Pointer(residentBytes)
	headroom := budget - residentBytes
	observation.HeadroomBytes = &headroom
	observation.Status = residentStatus
	if residentBytes <= budget {
		observation.State = CapacityBudgetFit
		if residentStatus == StatusAvailable {
			observation.Supports = []SupportClaim{ClaimSafeBudgetFit, ClaimCapacityHeadroom, ClaimFit}
		}
		return observation
	}
	observation.State = CapacityBudgetExceeded
	if residentStatus == StatusAvailable {
		observation.Supports = []SupportClaim{ClaimSafeBudgetExceeded, ClaimCapacityHeadroom}
	}
	return observation
}

func observationBytes(observation *capacityevidence.MemoryObservation) *int64 {
	if observation == nil {
		return nil
	}
	return int64Pointer(observation.Bytes)
}

func observationSource(observation *capacityevidence.MemoryObservation) string {
	if observation == nil {
		return ""
	}
	return observation.Source
}

func observationTime(observation *capacityevidence.MemoryObservation) string {
	if observation == nil {
		return ""
	}
	return observation.ObservedAt
}

func containerHeadroom(observation *capacityevidence.ContainerReceipt) *int64 {
	if observation == nil {
		return nil
	}
	return int64Pointer(observation.HeadroomBytes)
}

func containerSource(observation *capacityevidence.ContainerReceipt) string {
	if observation == nil {
		return ""
	}
	return observation.Source
}

func placementFrom(allocation eval.VerifiedAllocation, status ObservationStatus,
	descriptiveOnly bool) *PlacementObservation {
	// Schema 6 has no presence bit for accelerator bytes. Zero therefore cannot
	// distinguish an explicit CPU-only classification from a missing field.
	if allocation.AcceleratorBytes <= 0 || allocation.ResidentBytes <= 0 || allocation.ResidentBytes < allocation.AcceleratorBytes {
		return nil
	}
	percent := 100 * float64(allocation.AcceleratorBytes) / float64(allocation.ResidentBytes)
	kind := PlacementAcceleratorSharePartial
	if allocation.AcceleratorBytes == allocation.ResidentBytes {
		kind = PlacementAcceleratorShareFull
	}
	supports := []SupportClaim{ClaimExactContextAcceleratorBytes, ClaimExactContextPlacement}
	if descriptiveOnly {
		supports = nil
	}
	return &PlacementObservation{
		AcceleratorBytes:    allocation.AcceleratorBytes,
		NonAcceleratorBytes: allocation.ResidentBytes - allocation.AcceleratorBytes,
		AcceleratorPercent:  percent, Kind: kind, Status: status,
		Acquisition: AcquisitionRuntimeAllocation, RemainderAcquisition: AcquisitionClientDerived,
		RequestedContext: allocation.RequestedCtx, EffectiveContext: intPointer(allocation.EffectiveCtx),
		Supports: supports, Boundary: AllocationAttributionBoundary,
	}
}

type cacheStatus int

const (
	cacheVerifiedUncached cacheStatus = iota
	cacheUnknown
	cacheHit
	cacheUnknownAndHit
)

type residencyStatus int

const (
	residencyVerifiedLoaded residencyStatus = iota
	residencyUnknown
	residencyNotResident
)

func ttftResidencyEligibility(results []eval.SpeedResult) residencyStatus {
	if len(results) == 0 {
		return residencyUnknown
	}
	unknown, notResident := false, false
	for _, result := range results {
		unknown = unknown || !result.GatedResidencyKnown
		notResident = notResident || result.GatedResidencyKnown && !result.GatedResident
	}
	if notResident {
		return residencyNotResident
	}
	if unknown {
		return residencyUnknown
	}
	return residencyVerifiedLoaded
}

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

func gapsFrom(result *record.Record, report Report, displayOnly displayOnlyReason) []EvidenceGap {
	var gaps []EvidenceGap
	if result.CapacityPlan == nil {
		gaps = append(gaps, gap(GapCapacityPolicyUnsealed, "capacity",
			"no pre-observation capacity plan was sealed, so resident bytes cannot establish headroom or fit",
			ClaimSealedCapacityPolicy, ClaimCapacityHeadroom, ClaimFit))
	} else if result.CapacityPlan.Policy.UsableBudgetBytes == nil {
		gaps = append(gaps, gap(GapCapacityBudgetUnavailable, "capacity",
			"the sealed plan preserves capacity and availability facts but no operator budget or reserve defines usable capacity",
			ClaimSafeBudgetFit, ClaimCapacityHeadroom, ClaimFit))
	}
	switch displayOnly {
	case displayOnlyIdentity:
		gaps = append(gaps, gap(GapModelIdentityUnbound, "artifact",
			"the observed artifact was not bound to the serving runtime, so measurements remain descriptive only"))
	case displayOnlyStorage:
		gaps = append(gaps, gap(GapStorageUnreconciled, "artifact",
			"the saved copy is not reconciled with canonical current history, so measurements remain descriptive only"))
	}
	gaps = appendUnavailablePerformanceGaps(gaps, report.Performance)
	gaps = appendTTFTResidencyGaps(gaps, result.Speed)
	gaps = appendCacheGaps(gaps, result.Speed)
	gaps = appendLatencyStateGaps(gaps, result, report.Performance)
	if lowPerformanceSampleCount(report.Performance) {
		gaps = append(gaps, gap(GapPerformanceSampleCountLow, "performance",
			"fewer than three observations cannot establish stable performance", ClaimStablePerformance))
	}
	gaps = appendCapacityGaps(gaps, result)
	if report.Capacity.Resident != nil && report.Capacity.Placement == nil {
		gaps = append(gaps, gap(GapPlacementUnavailable, "capacity",
			"schema 6 cannot distinguish a missing accelerator byte field from an explicit zero; allocation attribution is unavailable",
			ClaimExactContextAcceleratorBytes, ClaimExactContextPlacement))
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].Code < gaps[j].Code })
	return gaps
}

func appendTTFTResidencyGaps(gaps []EvidenceGap, results []eval.SpeedResult) []EvidenceGap {
	if len(results) == 0 {
		return gaps
	}
	switch ttftResidencyEligibility(results) {
	case residencyUnknown:
		return append(gaps, gap(GapTTFTResidencyUnknown, "performance",
			"the gated request has no runtime residency receipt, so its latency is request TTFT, not proven loaded TTFT",
			ClaimObservedLoadedTTFT, ClaimLoadedUncachedTTFT))
	case residencyNotResident:
		return append(gaps, gap(GapTTFTNotResident, "performance",
			"the runtime did not report the model resident immediately before at least one gated request, so the aggregate is request TTFT, not loaded TTFT",
			ClaimObservedLoadedTTFT, ClaimLoadedUncachedTTFT))
	default:
		return gaps
	}
}

func appendLatencyStateGaps(gaps []EvidenceGap, result *record.Record, performance Performance) []EvidenceGap {
	if result.TaskPlan.SpeedSamples <= 0 {
		return gaps
	}
	if performance.RuntimeUnloadedTTFTSeconds.Estimate == nil {
		gaps = append(gaps, gap(GapRuntimeUnloadedTTFTUnavailable, "performance",
			"runtime-unloaded TTFT was not observed; operating-system page-cache state is not measured",
			ClaimObservedRuntimeUnloadedTTFT))
	}
	if performance.RuntimeLoadSeconds.Estimate == nil {
		gaps = append(gaps, gap(GapRuntimeLoadUnavailable, "performance",
			"the backend did not provide a versioned runtime-load duration receipt",
			ClaimObservedRuntimeLoadTime))
	}
	if performance.LoadedCacheHitTTFTSeconds.Estimate == nil {
		gaps = append(gaps, gap(GapLoadedCacheHitTTFTUnavailable, "performance",
			"no positive cached-token receipt established this latency state",
			ClaimObservedLoadedCacheHitTTFT))
	}
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
			"request time to first token was not observed", ClaimObservedRequestTTFT,
			ClaimObservedLoadedTTFT, ClaimLoadedUncachedTTFT))
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

func diagnosesFrom(result *record.Record, unclaimable bool) []Diagnosis {
	var diagnoses []Diagnosis
	if len(result.Contamination) > 0 {
		diagnoses = append(diagnoses, directDiagnosis(Diagnosis{
			Code:      DiagnosisContaminated,
			Statement: "another resident model contaminated the measured run",
			Evidence:  []string{"sealed_completion.contamination"},
		}, Action{Code: ActionRerunUncontaminated, Argv: runActionArgv(result),
			Reason: "repeat the run after removing unrelated resident models"}))
	}
	if result.DeviceV2 != nil && result.DeviceV2.Context.State() == device.ContextAdjusted {
		diagnoses = append(diagnoses, directDiagnosis(Diagnosis{
			Code:      DiagnosisContextAdjusted,
			Statement: "the runtime reported an effective context different from the request",
			Evidence:  []string{"device_fingerprint_v2.context"},
		}, Action{Code: ActionRunContextExperiment, Argv: []string{"fitr", "experiment", "context", CurrentModelPlaceholder,
			"--ctx", contextExperimentPair(result)}, Reason: "measure the requested and adjusted contexts as separate points"}))
	}
	diagnoses = append(diagnoses, performanceDiagnoses(result)...)
	diagnoses = append(diagnoses, behaviorDiagnoses(result)...)
	if !unclaimable && result.TaskPlan.Memory && result.Memory.RequestedCtx > 0 {
		if allocation, ok := result.Memory.VerifiedAllocationAt(result.Memory.RequestedCtx); ok &&
			allocation.AcceleratorBytes > 0 && allocation.AcceleratorBytes < allocation.ResidentBytes {
			diagnoses = append(diagnoses, directDiagnosis(Diagnosis{
				Code:      DiagnosisPartialPlacement,
				Statement: "the runtime reported a partial accelerator share at the exact-context allocation point",
				Evidence: []string{"memory.resident_bytes", "memory.accelerator_bytes",
					"memory.requested_ctx", "memory.effective_ctx"},
			}, Action{Code: ActionOpenBoard, Argv: []string{"fitr", "board"},
				Reason: "compare only with compatible exact-context allocation receipts"}))
		}
	}
	sort.Slice(diagnoses, func(i, j int) bool { return diagnoses[i].Code < diagnoses[j].Code })
	return diagnoses
}

func performanceDiagnoses(result *record.Record) []Diagnosis {
	var diagnoses []Diagnosis
	if ttftResidencyEligibility(result.Speed) == residencyNotResident {
		diagnoses = append(diagnoses, directDiagnosis(Diagnosis{
			Code: DiagnosisTTFTNotResident, Statement: "the model was not resident before at least one timed request",
			Evidence: []string{"speed_repeats.gated_residency_known", "speed_repeats.gated_resident"},
		}, repeatMeasurementAction(result, "repeat after establishing a loaded runtime state")))
	}
	if status := cacheEligibility(result.Speed, prefillCacheReceipt); status == cacheHit || status == cacheUnknownAndHit {
		diagnoses = append(diagnoses, directDiagnosis(Diagnosis{
			Code: DiagnosisPrefillCacheHit, Statement: "cached prompt tokens contaminated at least one prefill observation",
			Evidence: []string{"speed_repeats.prompt_tokens", "speed_repeats.cached_prompt_tokens"},
		}, repeatMeasurementAction(result, "repeat the nonce-bound prefill probe without cached prompt tokens")))
	}
	if status := cacheEligibility(result.Speed, ttftCacheReceipt); status == cacheHit || status == cacheUnknownAndHit {
		diagnoses = append(diagnoses, directDiagnosis(Diagnosis{
			Code: DiagnosisTTFTCacheHit, Statement: "cached prompt tokens contaminated at least one gated TTFT observation",
			Evidence: []string{"speed_repeats.gated_prompt_tokens", "speed_repeats.gated_cached_tokens"},
		}, repeatMeasurementAction(result, "repeat the gated request without cached prompt tokens")))
	}
	decode, prefill, _ := speedValues(result.Speed)
	if slow, _ := stats.FirstRunSlow(decode); slow {
		action := repeatMeasurementAction(result, "repeat enough times to separate a first-run effect from unstable throughput")
		action.Argv = append(action.Argv, "-k", "5")
		diagnoses = append(diagnoses, directDiagnosis(Diagnosis{
			Code: DiagnosisFirstDecodeSlow, Statement: "the first decode sample was materially slower than the remaining samples",
			Evidence: []string{"speed_repeats.decode_tps"},
		}, action))
	}
	if phase := prefillSlowerDiagnosis(decode, prefill); phase != nil {
		diagnoses = append(diagnoses, *phase)
	}
	return diagnoses
}

func prefillSlowerDiagnosis(decode, prefill []float64) *Diagnosis {
	decodeMean, decodeOK := meanPositive(decode)
	prefillMean, prefillOK := meanPositive(prefill)
	if !decodeOK || !prefillOK || prefillMean >= decodeMean {
		return nil
	}
	diagnosis := directDiagnosis(Diagnosis{
		Code:      DiagnosisPrefillSlowerPhase,
		Statement: "prompt processing was the slower observed throughput phase; this does not identify a hardware limiter",
		Evidence:  []string{"speed_repeats.prefill_tps", "speed_repeats.decode_tps"},
		Missing:   []string{"memory-traffic counters", "compute counters", "kernel identity"},
	}, Action{Code: ActionRunContextExperiment, Argv: []string{"fitr", "experiment", "context", CurrentModelPlaceholder},
		Reason: "measure prefill and decode at more than one verified context before attributing a limiter"})
	return &diagnosis
}

func meanPositive(values []float64) (float64, bool) {
	sum, n := 0.0, 0
	for _, value := range values {
		if value > 0 {
			sum += value
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return sum / float64(n), true
}

func behaviorDiagnoses(result *record.Record) []Diagnosis {
	for index, check := range result.Checks {
		if strings.Contains(strings.ToLower(check.Detail), "tool call as text") ||
			strings.Contains(strings.ToLower(check.Detail), "not through the tool channel") {
			return []Diagnosis{directDiagnosis(Diagnosis{
				Code:      DiagnosisToolCallInContent,
				Statement: "a tool-shaped call appeared in assistant content instead of the tool channel",
				Evidence:  []string{"checks[" + strconv.Itoa(index) + "].detail"},
			}, Action{Code: ActionDiagnoseTools, Argv: diagnosticActionArgv(result),
				Reason: "separate chat-template or parser plumbing from model behavior"})}
		}
	}
	return nil
}

func directDiagnosis(diagnosis Diagnosis, action Action) Diagnosis {
	diagnosis.Support = DiagnosisDirect
	actionCopy := action
	diagnosis.NextExperiment = &actionCopy
	return diagnosis
}

func repeatMeasurementAction(result *record.Record, reason string) Action {
	return Action{Code: ActionRepeatMeasurement, Argv: runActionArgv(result), Reason: reason}
}

func contextExperimentPair(result *record.Record) string {
	requested := result.ContextSize()
	effective := requested
	if result.DeviceV2 != nil && result.DeviceV2.Context.EffectiveTokens != nil {
		effective = *result.DeviceV2.Context.EffectiveTokens
	}
	return strconv.Itoa(requested) + "," + strconv.Itoa(effective)
}

func nextActionsFrom(result *record.Record, displayOnly bool) []Action {
	if displayOnly {
		return nil
	}
	action := Action{Code: ActionOpenBoard, Argv: []string{"fitr", "board"},
		Reason: "compare this validated result only with compatible saved runs"}
	switch {
	case len(result.Contamination) > 0:
		action = Action{Code: ActionRerunUncontaminated,
			Argv:   runActionArgv(result),
			Reason: "replace timing evidence contaminated by another resident model"}
	case toolsBlocked(result.Scorecard):
		action = Action{Code: ActionDiagnoseTools,
			Argv:   diagnosticActionArgv(result),
			Reason: "separate runtime tool plumbing from model behavior"}
	case result.Repeats > 0 && result.Repeats < 3:
		action = Action{Code: ActionIncreaseRepeats,
			Argv:   append(runActionArgv(result), "-k", "3"),
			Reason: "collect enough repeats for a stable comparison"}
	case result.Level == "quick" || result.Level == "checks" || result.Level == "checks-only":
		action = Action{Code: ActionCompleteBattery,
			Argv:   runActionArgv(result),
			Reason: "measure the standard behavior and performance battery"}
	}
	return []Action{action}
}

func runActionArgv(result *record.Record) []string {
	manifest := result.Manifest
	return []string{
		"fitr", "run", CurrentModelPlaceholder,
		"--ctx", strconv.Itoa(manifest.NumCtx),
		"--backend", manifest.Model.Backend,
		"--profile", manifest.Profile,
	}
}

func diagnosticActionArgv(result *record.Record) []string {
	manifest := result.Manifest
	return []string{
		"fitr", "diag", CurrentModelPlaceholder,
		"--ctx", strconv.Itoa(manifest.NumCtx),
		"--backend", manifest.Model.Backend,
	}
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
func intPointer(value int) *int             { return &value }

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
