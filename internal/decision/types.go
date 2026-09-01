// Package decision evaluates sealed fitr evidence against user-declared
// workload requirements. It never mutates or supersedes the source record.
package decision

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/score"
)

const (
	SpecSchema          = "fitr.decision.spec.v1"
	EvaluationSchema    = "fitr.decision.evaluation.v1"
	ConfigurationSchema = "fitr.configuration.v1"
)

type EvidenceLevel string

const (
	EvidenceScreen    EvidenceLevel = "screen"
	EvidenceDecide    EvidenceLevel = "decide"
	EvidenceConfirm   EvidenceLevel = "confirm"
	EvidenceCalibrate EvidenceLevel = "calibrate"
	EvidenceOperate   EvidenceLevel = "operate"
)

type DecisionSpec struct {
	Schema       string             `json:"schema"`
	Name         string             `json:"name"`
	Evidence     EvidenceLevel      `json:"evidence_level"`
	Requirements []Requirement      `json:"requirements"`
	Objective    *Objective         `json:"objective,omitempty"`
	Confirmation ConfirmationPolicy `json:"confirmation,omitempty"`
	Fallback     FallbackPolicy     `json:"fallback,omitempty"`
}

type ConfirmationPolicy struct {
	FreshEvidence bool `json:"fresh_evidence,omitempty"`
	FreshTasks    bool `json:"fresh_tasks,omitempty"`
}

type FallbackPolicy struct {
	Unresolved string `json:"unresolved,omitempty"`
	Disproven  string `json:"disproven,omitempty"`
}

type ObjectiveDirection string

const (
	Maximize ObjectiveDirection = "maximize"
	Minimize ObjectiveDirection = "minimize"
)

type Objective struct {
	Metric    string             `json:"metric"`
	Direction ObjectiveDirection `json:"direction"`
}

type Requirement struct {
	ID          string                  `json:"id"`
	Behavior    *BehaviorRequirement    `json:"behavior,omitempty"`
	Capability  *CapabilityRequirement  `json:"capability,omitempty"`
	Context     *ContextRequirement     `json:"context,omitempty"`
	Performance *PerformanceRequirement `json:"performance,omitempty"`
	Capacity    *CapacityRequirement    `json:"capacity,omitempty"`
}

type BehaviorRequirement struct {
	Need          string      `json:"need"`
	MinimumRate   *float64    `json:"minimum_rate,omitempty"`
	RequiredState score.State `json:"required_state,omitempty"`
}

type CapabilityRequirement struct {
	Name           string                  `json:"name"`
	MinimumSupport score.CapabilitySupport `json:"minimum_support"`
}

type ContextRequirement struct {
	MinimumEffectiveTokens int `json:"minimum_effective_tokens"`
}

type PerformanceMetric string

const (
	MetricDecodeTPS   PerformanceMetric = "decode_tps"
	MetricPrefillTPS  PerformanceMetric = "prefill_tps"
	MetricRequestTTFT PerformanceMetric = "request_ttft_seconds"
	MetricLoadedTTFT  PerformanceMetric = "loaded_ttft_seconds"
)

type PerformanceRequirement struct {
	Metric  PerformanceMetric `json:"metric"`
	AtLeast *float64          `json:"at_least,omitempty"`
	AtMost  *float64          `json:"at_most,omitempty"`
}

type CapacityRequirement struct {
	MaximumResidentBytes int64   `json:"maximum_resident_bytes,omitempty"`
	MaximumResidentGB    float64 `json:"maximum_resident_gb,omitempty"`
	RequestedContext     int     `json:"requested_context,omitempty"`
}

func (s DecisionSpec) Validate() error {
	if s.Schema != SpecSchema {
		return fmt.Errorf("unsupported decision spec schema %q", s.Schema)
	}
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("decision spec name is required")
	}
	switch s.Evidence {
	case EvidenceScreen, EvidenceDecide, EvidenceConfirm, EvidenceCalibrate, EvidenceOperate:
	default:
		return fmt.Errorf("unsupported evidence level %q", s.Evidence)
	}
	if len(s.Requirements) == 0 {
		return errors.New("decision spec requires at least one requirement")
	}
	seen := make(map[string]bool, len(s.Requirements))
	for i, requirement := range s.Requirements {
		if err := requirement.validate(); err != nil {
			return fmt.Errorf("requirement %d: %w", i+1, err)
		}
		if seen[requirement.ID] {
			return fmt.Errorf("duplicate requirement id %q", requirement.ID)
		}
		seen[requirement.ID] = true
	}
	if s.Objective != nil {
		metric := strings.TrimSpace(s.Objective.Metric)
		if metric == "" {
			return errors.New("decision objective metric is required")
		}
		if metric != s.Objective.Metric || !supportedObjectiveMetric(metric) {
			return fmt.Errorf("unsupported decision objective metric %q", s.Objective.Metric)
		}
		if s.Objective.Direction != Maximize && s.Objective.Direction != Minimize {
			return fmt.Errorf("unsupported objective direction %q", s.Objective.Direction)
		}
	}
	return nil
}

func (r Requirement) validate() error {
	id := strings.TrimSpace(r.ID)
	if id == "" {
		return errors.New("id is required")
	}
	if id != r.ID {
		return errors.New("id cannot have leading or trailing whitespace")
	}
	kinds := 0
	for _, present := range []bool{
		r.Behavior != nil, r.Capability != nil, r.Context != nil,
		r.Performance != nil, r.Capacity != nil,
	} {
		if present {
			kinds++
		}
	}
	if kinds != 1 {
		return errors.New("exactly one typed requirement is required")
	}
	switch {
	case r.Behavior != nil:
		return r.Behavior.validate()
	case r.Capability != nil:
		return r.Capability.validate()
	case r.Context != nil:
		if r.Context.MinimumEffectiveTokens <= 0 {
			return errors.New("minimum effective context must be positive")
		}
	case r.Performance != nil:
		return r.Performance.validate()
	case r.Capacity != nil:
		bytesSet := r.Capacity.MaximumResidentBytes != 0
		gbSet := r.Capacity.MaximumResidentGB != 0
		if bytesSet == gbSet {
			return errors.New("capacity requirement needs exactly one resident-memory maximum")
		}
		if r.Capacity.MaximumResidentBytes < 0 || r.Capacity.MaximumResidentGB < 0 ||
			!finite(r.Capacity.MaximumResidentGB) {
			return errors.New("resident-memory maximum must be finite and positive")
		}
		if r.Capacity.MaximumResidentGB > float64(math.MaxInt64)/(1024*1024*1024) {
			return errors.New("resident-memory maximum exceeds the supported range")
		}
		if r.Capacity.RequestedContext < 0 {
			return errors.New("capacity requested context cannot be negative")
		}
	}
	return nil
}

func (r BehaviorRequirement) validate() error {
	need := strings.TrimSpace(r.Need)
	if need == "" {
		return errors.New("behavior need is required")
	}
	if need != r.Need {
		return errors.New("behavior need cannot have leading or trailing whitespace")
	}
	if need == "vision" {
		return errors.New("vision is capability evidence until a behavioral image protocol is measured")
	}
	if !supportedBehaviorNeed(need) {
		return fmt.Errorf("unsupported behavior need %q", need)
	}
	if r.MinimumRate != nil {
		if !finite(*r.MinimumRate) || *r.MinimumRate < 0 || *r.MinimumRate > 1 {
			return errors.New("behavior minimum rate must be between 0 and 1")
		}
		if r.RequiredState != "" {
			return errors.New("behavior requirement cannot set both minimum rate and required state")
		}
		return nil
	}
	if r.RequiredState != "" && r.RequiredState != score.Pass {
		return fmt.Errorf("unsupported required behavior state %q", r.RequiredState)
	}
	return nil
}

func supportedObjectiveMetric(metric string) bool {
	switch metric {
	case "decode_tps", "prefill_tps", "request_ttft_seconds", "loaded_ttft_seconds",
		"resident_bytes", "artifact_bytes":
		return true
	default:
		return false
	}
}

func supportedBehaviorNeed(need string) bool {
	switch need {
	case "coding", "structured_output", "instruction_precision", "reasoning",
		"uncensored", "tool_calling", "unattended_agentic", "tool_restraint",
		"output_health", "user_tasks":
		return true
	default:
		return false
	}
}

func (r CapabilityRequirement) validate() error {
	name := strings.TrimSpace(r.Name)
	if name == "" {
		return errors.New("capability name is required")
	}
	if name != r.Name {
		return errors.New("capability name cannot have leading or trailing whitespace")
	}
	switch r.MinimumSupport {
	case score.CapabilityDeclared, score.CapabilityProtocolVerified:
		return nil
	default:
		return fmt.Errorf("unsupported capability support %q", r.MinimumSupport)
	}
}

func (r PerformanceRequirement) validate() error {
	switch r.Metric {
	case MetricDecodeTPS, MetricPrefillTPS, MetricRequestTTFT, MetricLoadedTTFT:
	default:
		return fmt.Errorf("unsupported performance metric %q", r.Metric)
	}
	if (r.AtLeast == nil) == (r.AtMost == nil) {
		return errors.New("performance requirement needs exactly one bound")
	}
	bound := r.AtLeast
	if bound == nil {
		bound = r.AtMost
	}
	if !finite(*bound) || *bound < 0 {
		return errors.New("performance bound must be finite and nonnegative")
	}
	return nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

type ConfigurationUnderTest struct {
	Schema           string                         `json:"schema"`
	ID               string                         `json:"id"`
	RequestedModel   string                         `json:"requested_model"`
	ResolvedModel    string                         `json:"resolved_model"`
	ArtifactKind     string                         `json:"artifact_kind"`
	ArtifactDigest   string                         `json:"artifact_digest,omitempty"`
	ArtifactBinding  string                         `json:"artifact_binding"`
	Backend          string                         `json:"backend"`
	Runtime          string                         `json:"runtime"`
	Quant            string                         `json:"quant,omitempty"`
	Family           string                         `json:"family,omitempty"`
	ParameterSize    string                         `json:"parameter_size,omitempty"`
	RequestedContext int                            `json:"requested_context"`
	EffectiveContext *int                           `json:"effective_context,omitempty"`
	ContextState     string                         `json:"context_state"`
	ComparabilityKey string                         `json:"comparability_key,omitempty"`
	Placement        *analysis.PlacementObservation `json:"placement,omitempty"`
}

type EvaluationState string

const (
	DecisionEligible   EvaluationState = "eligible"
	DecisionIneligible EvaluationState = "ineligible"
	DecisionUnresolved EvaluationState = "unresolved"
)

type RequirementState string

const (
	RequirementEstablished RequirementState = "established"
	RequirementDisproven   RequirementState = "disproven"
	RequirementUnresolved  RequirementState = "unresolved"
	RequirementBlocked     RequirementState = "blocked"
)

type RequirementResult struct {
	ID               string           `json:"id"`
	State            RequirementState `json:"state"`
	Observed         *float64         `json:"observed,omitempty"`
	Unit             analysis.Unit    `json:"unit,omitempty"`
	IntervalLow      *float64         `json:"interval_low,omitempty"`
	IntervalHigh     *float64         `json:"interval_high,omitempty"`
	IndependentUnits int              `json:"independent_units,omitempty"`
	Reason           string           `json:"reason"`
	EvidenceRefs     []string         `json:"evidence_refs,omitempty"`
	Missing          []string         `json:"missing,omitempty"`
	NextAction       *Action          `json:"next_action,omitempty"`
}

type Action struct {
	Code       string   `json:"code"`
	Experiment string   `json:"experiment,omitempty"`
	Argv       []string `json:"argv,omitempty"`
	Reason     string   `json:"reason"`
}

type Evaluation struct {
	Schema       string                 `json:"schema"`
	SpecSHA256   string                 `json:"spec_sha256"`
	SpecName     string                 `json:"spec_name"`
	Evidence     EvidenceLevel          `json:"evidence_level"`
	Source       analysis.Source        `json:"source"`
	Subject      ConfigurationUnderTest `json:"subject"`
	Eligibility  EvaluationState        `json:"eligibility"`
	State        EvaluationState        `json:"state"`
	Requirements []RequirementResult    `json:"requirements"`
	Objective    *ObjectiveResult       `json:"objective,omitempty"`
	Gaps         []string               `json:"gaps,omitempty"`
	NextAction   *Action                `json:"next_action,omitempty"`
}

type ObjectiveResult struct {
	Metric    string             `json:"metric"`
	Direction ObjectiveDirection `json:"direction"`
	State     RequirementState   `json:"state"`
	Reason    string             `json:"reason"`
	Missing   []string           `json:"missing,omitempty"`
}
