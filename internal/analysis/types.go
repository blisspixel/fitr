// Package analysis derives renderer-neutral facts from a sealed fitr result.
//
// Reports are presentation projections. They are rebuilt from the persisted
// record and must never be written back into that record as evidence.
package analysis

const (
	ReportSchema = "fitr.analysis.run.v1"
	PolicySchema = "fitr.analysis.policy.direct-evidence.v1"
)

type SourceKind string

const SourceSealedResult SourceKind = "sealed_result"

// Source identifies the evidence contract from which a report was derived.
// A Report exists only after the source record passed its own validation.
type Source struct {
	Kind             SourceKind `json:"kind"`
	RecordSchema     int        `json:"record_schema"`
	ManifestSchema   string     `json:"manifest_schema"`
	CompletionSchema string     `json:"completion_schema"`
	EvidenceSHA256   string     `json:"evidence_sha256"`
	Validated        bool       `json:"validated"`
}

type Unit string

const (
	UnitTokensPerSecond Unit = "tokens_per_second"
	UnitSeconds         Unit = "seconds"
	UnitBytes           Unit = "bytes"
)

type Acquisition string

const (
	AcquisitionUnavailable       Acquisition = "unavailable"
	AcquisitionRuntimeReported   Acquisition = "runtime_reported"
	AcquisitionClientDerived     Acquisition = "client_derived"
	AcquisitionMixed             Acquisition = "mixed"
	AcquisitionClientWallClock   Acquisition = "client_wall_clock"
	AcquisitionRuntimeAllocation Acquisition = "runtime_allocation"
)

type ObservationStatus string

const (
	StatusAvailable       ObservationStatus = "available"
	StatusDescriptiveOnly ObservationStatus = "descriptive_only"
	StatusUnavailable     ObservationStatus = "unavailable"
)

type SupportClaim string

const (
	ClaimObservedDecode               SupportClaim = "observed_decode_throughput"
	ClaimObservedPrefill              SupportClaim = "observed_prefill_throughput"
	ClaimUncachedPrefill              SupportClaim = "uncached_prefill_throughput"
	ClaimObservedRequestTTFT          SupportClaim = "observed_request_ttft"
	ClaimObservedLoadedTTFT           SupportClaim = "observed_loaded_ttft"
	ClaimLoadedUncachedTTFT           SupportClaim = "loaded_uncached_ttft"
	ClaimObservedRuntimeUnloadedTTFT  SupportClaim = "observed_runtime_unloaded_ttft"
	ClaimObservedRuntimeLoadTime      SupportClaim = "observed_runtime_load_time"
	ClaimObservedLoadedCacheHitTTFT   SupportClaim = "observed_loaded_cache_hit_ttft"
	ClaimExactContextResidentBytes    SupportClaim = "exact_context_resident_bytes"
	ClaimExactContextAcceleratorBytes SupportClaim = "exact_context_accelerator_bytes"
	ClaimExactContextPlacement        SupportClaim = "exact_context_allocation_placement"
	ClaimStablePerformance            SupportClaim = "stable_performance"
	ClaimCapacityHeadroom             SupportClaim = "capacity_headroom"
	ClaimFit                          SupportClaim = "fit"
)

// PerformanceObservation is a point estimate and its observed sample shape.
// Estimate is a pointer so an absent observation differs from an observed
// zero. SD is absent for a single observation; it is never presented as zero
// precision merely because only one sample exists.
type PerformanceObservation struct {
	Estimate      *float64          `json:"estimate"`
	Unit          Unit              `json:"unit"`
	Status        ObservationStatus `json:"status"`
	Acquisition   Acquisition       `json:"acquisition"`
	SampleCount   int               `json:"sample_count"`
	SD            *float64          `json:"sd,omitempty"`
	Min           *float64          `json:"min,omitempty"`
	Max           *float64          `json:"max,omitempty"`
	Samples       []float64         `json:"samples,omitempty"`
	Supports      []SupportClaim    `json:"supports,omitempty"`
	FirstRunSlow  bool              `json:"first_run_slow,omitempty"`
	FirstRunRatio float64           `json:"first_run_ratio,omitempty"`
}

type Performance struct {
	DecodeTPS                  PerformanceObservation `json:"decode_tps"`
	PrefillTPS                 PerformanceObservation `json:"prefill_tps"`
	TTFTSeconds                PerformanceObservation `json:"ttft_seconds"`
	RuntimeUnloadedTTFTSeconds PerformanceObservation `json:"runtime_unloaded_ttft_seconds"`
	RuntimeLoadSeconds         PerformanceObservation `json:"runtime_load_seconds"`
	LoadedCacheHitTTFTSeconds  PerformanceObservation `json:"loaded_cache_hit_ttft_seconds"`
}

type Context struct {
	Requested       int    `json:"requested"`
	Effective       *int   `json:"effective,omitempty"`
	State           string `json:"state"`
	EffectiveSource string `json:"effective_source,omitempty"`
}

// ResidentObservation exists only for an observed allocation whose effective
// context exactly equals the requested memory-probe context. Estimate is exact
// bytes from the receipt, not a rounded GiB value or a capacity projection.
type ResidentObservation struct {
	Estimate         *int64            `json:"estimate"`
	Unit             Unit              `json:"unit"`
	Status           ObservationStatus `json:"status"`
	Acquisition      Acquisition       `json:"acquisition"`
	RequestedContext int               `json:"requested_context"`
	EffectiveContext *int              `json:"effective_context"`
	Supports         []SupportClaim    `json:"supports"`
}

type PlacementKind string

const (
	PlacementAcceleratorSharePartial PlacementKind = "accelerator_share_partial"
	PlacementAcceleratorShareFull    PlacementKind = "accelerator_share_full"
)

const AllocationAttributionBoundary = "Runtime allocation attribution only; not proof of exclusive physical pools, layer placement, or host traffic."

// PlacementObservation describes one exact-context runtime allocation point.
// AcceleratorBytes is runtime classified. NonAcceleratorBytes is only the
// arithmetic remainder of resident minus accelerator bytes. On unified-memory
// systems neither value identifies an exclusive physical memory pool, layer
// placement, or host traffic.
type PlacementObservation struct {
	AcceleratorBytes     int64             `json:"accelerator_bytes"`
	NonAcceleratorBytes  int64             `json:"non_accelerator_bytes"`
	AcceleratorPercent   float64           `json:"accelerator_percent"`
	Kind                 PlacementKind     `json:"kind"`
	Status               ObservationStatus `json:"status"`
	Acquisition          Acquisition       `json:"acquisition"`
	RemainderAcquisition Acquisition       `json:"remainder_acquisition"`
	RequestedContext     int               `json:"requested_context"`
	EffectiveContext     *int              `json:"effective_context"`
	Supports             []SupportClaim    `json:"supports,omitempty"`
	Boundary             string            `json:"boundary"`
}

type Capacity struct {
	Resident  *ResidentObservation  `json:"resident,omitempty"`
	Placement *PlacementObservation `json:"placement,omitempty"`
}

type GapCode string

const (
	GapDecodeUnavailable              GapCode = "performance.decode_unavailable"
	GapPrefillUnavailable             GapCode = "performance.prefill_unavailable"
	GapTTFTUnavailable                GapCode = "performance.ttft_unavailable"
	GapPrefillCacheUnknown            GapCode = "performance.prefill_cache_unknown"
	GapPrefillCacheHit                GapCode = "performance.prefill_cache_hit"
	GapTTFTCacheUnknown               GapCode = "performance.ttft_cache_unknown"
	GapTTFTCacheHit                   GapCode = "performance.ttft_cache_hit"
	GapTTFTResidencyUnknown           GapCode = "performance.ttft_residency_unknown"
	GapTTFTNotResident                GapCode = "performance.ttft_not_resident"
	GapRuntimeUnloadedTTFTUnavailable GapCode = "performance.runtime_unloaded_ttft_unavailable"
	GapRuntimeLoadUnavailable         GapCode = "performance.runtime_load_unavailable"
	GapLoadedCacheHitTTFTUnavailable  GapCode = "performance.loaded_cache_hit_ttft_unavailable"
	GapPerformanceSampleCountLow      GapCode = "performance.sample_count_low"
	GapResidentNotPlanned             GapCode = "capacity.resident_not_planned"
	GapResidentUnavailable            GapCode = "capacity.resident_unavailable"
	GapResidentContextUnverified      GapCode = "capacity.resident_context_unverified"
	GapResidentContextAdjusted        GapCode = "capacity.resident_context_adjusted"
	GapPlacementUnavailable           GapCode = "capacity.placement_unavailable"
	GapCapacityPolicyUnsealed         GapCode = "capacity.policy_unsealed"
	GapModelIdentityUnbound           GapCode = "artifact.identity_unbound"
	GapStorageUnreconciled            GapCode = "artifact.storage_unreconciled"
)

type EvidenceGap struct {
	Code              GapCode        `json:"code"`
	Section           string         `json:"section"`
	Message           string         `json:"message"`
	UnsupportedClaims []SupportClaim `json:"unsupported_claims,omitempty"`
}

// GapLabel provides one renderer-neutral human label while Code remains the
// stable machine-readable identifier.
func GapLabel(code GapCode) string {
	switch code {
	case GapDecodeUnavailable:
		return "decode throughput"
	case GapPrefillUnavailable:
		return "prefill throughput"
	case GapTTFTUnavailable:
		return "loaded TTFT"
	case GapPrefillCacheUnknown, GapPrefillCacheHit:
		return "prefill cache state"
	case GapTTFTCacheUnknown, GapTTFTCacheHit:
		return "gated TTFT cache state"
	case GapTTFTResidencyUnknown, GapTTFTNotResident:
		return "TTFT residency state"
	case GapRuntimeUnloadedTTFTUnavailable:
		return "runtime-unloaded TTFT"
	case GapRuntimeLoadUnavailable:
		return "runtime load"
	case GapLoadedCacheHitTTFTUnavailable:
		return "loaded cache-hit TTFT"
	case GapPerformanceSampleCountLow:
		return "repeat strength"
	case GapResidentNotPlanned, GapResidentUnavailable, GapResidentContextUnverified, GapResidentContextAdjusted:
		return "resident allocation"
	case GapPlacementUnavailable:
		return "allocation attribution"
	case GapCapacityPolicyUnsealed:
		return "usable capacity"
	case GapModelIdentityUnbound:
		return "artifact identity"
	case GapStorageUnreconciled:
		return "evidence storage"
	default:
		return string(code)
	}
}

// Supports reports whether an observation can establish a claim.
func Supports(observation PerformanceObservation, claim SupportClaim) bool {
	for _, supported := range observation.Supports {
		if supported == claim {
			return true
		}
	}
	return false
}

// TTFTLabel names the request state actually established by evidence.
func TTFTLabel(observation PerformanceObservation) string {
	if Supports(observation, ClaimObservedLoadedTTFT) {
		return "loaded TTFT"
	}
	return "request TTFT"
}

// AcquisitionLabel is stable human wording for an observation source.
func AcquisitionLabel(acquisition Acquisition) string {
	switch acquisition {
	case AcquisitionRuntimeReported:
		return "runtime reported"
	case AcquisitionClientDerived:
		return "client derived"
	case AcquisitionMixed:
		return "mixed runtime and client"
	case AcquisitionClientWallClock:
		return "client wall clock"
	case AcquisitionRuntimeAllocation:
		return "runtime allocation"
	default:
		return "unavailable"
	}
}

// ObservationQualifier keeps evidence status and acquisition visible in all
// renderers instead of allowing descriptive-only values to resemble claims.
func ObservationQualifier(status ObservationStatus, acquisition Acquisition) string {
	source := AcquisitionLabel(acquisition)
	if status == StatusDescriptiveOnly {
		return "descriptive only; " + source
	}
	return source
}

type DiagnosisCode string

const (
	DiagnosisContextAdjusted  DiagnosisCode = "context.adjusted"
	DiagnosisContaminated     DiagnosisCode = "run.resident_contamination"
	DiagnosisPartialPlacement DiagnosisCode = "placement.partial_accelerator_allocation"
)

// Diagnosis is limited to a direct statement carried by a sealed receipt.
// It deliberately has no numeric confidence and cannot name a hardware cause.
type Diagnosis struct {
	Code      DiagnosisCode `json:"code"`
	Statement string        `json:"statement"`
	Evidence  []string      `json:"evidence"`
}

func DiagnosisLabel(code DiagnosisCode) string {
	switch code {
	case DiagnosisContextAdjusted:
		return "effective context"
	case DiagnosisContaminated:
		return "run contamination"
	case DiagnosisPartialPlacement:
		return "allocation attribution"
	default:
		return string(code)
	}
}

type ActionCode string

const (
	ActionRerunUncontaminated ActionCode = "rerun_without_contamination"
	ActionDiagnoseTools       ActionCode = "diagnose_tool_plumbing"
	ActionIncreaseRepeats     ActionCode = "increase_repeats"
	ActionCompleteBattery     ActionCode = "complete_standard_battery"
	ActionOpenBoard           ActionCode = "open_comparison_board"
)

const CurrentModelPlaceholder = "<current-model>"

// Action is an argv template, not a shell command. It never carries the model
// name, a local path, a hostname, or an endpoint from the source record.
type Action struct {
	Code   ActionCode `json:"code"`
	Argv   []string   `json:"argv"`
	Reason string     `json:"reason"`
}

// Report is a derived, non-persistent analysis of one validated schema-6 run.
// It intentionally has no fit, headroom, available-memory, limiter, or global
// score field because the source record cannot support those claims.
type Report struct {
	Schema      string        `json:"schema"`
	Policy      string        `json:"policy"`
	Source      Source        `json:"source"`
	Context     Context       `json:"context"`
	Performance Performance   `json:"performance"`
	Capacity    Capacity      `json:"capacity"`
	Gaps        []EvidenceGap `json:"gaps,omitempty"`
	Diagnoses   []Diagnosis   `json:"diagnoses,omitempty"`
	NextActions []Action      `json:"next_actions"`
}
