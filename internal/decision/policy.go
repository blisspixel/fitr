package decision

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/record"
)

const (
	MeasurementProtocolSchema = "fitr.measurement.protocol.v1"
	LegacyPolicyBundleSchema  = "fitr.policy-bundle.legacy-profile.v1"
	CapacityPolicySchema      = "fitr.capacity.policy.v1"
)

// MeasurementProtocol identifies what was planned and sealed before a run.
// It is intentionally reconstructed from the manifest, not from a profile.
type MeasurementProtocol struct {
	Schema           string          `json:"schema"`
	ManifestSHA256   string          `json:"manifest_sha256"`
	Level            string          `json:"level"`
	ExecutionPolicy  string          `json:"execution_policy"`
	TaskPlan         record.TaskPlan `json:"task_plan"`
	SeedSet          string          `json:"seedset"`
	Repeats          int             `json:"repeats"`
	RequestedContext int             `json:"requested_context"`
}

// ProtocolFromRecord reconstructs the immutable measurement protocol after
// validating the sealed manifest. Completion observations are not part of the
// protocol because they did not exist when the plan was committed.
func ProtocolFromRecord(result *record.Record) (MeasurementProtocol, error) {
	if result == nil || result.Manifest == nil {
		return MeasurementProtocol{}, errors.New("measurement protocol requires a sealed manifest")
	}
	if err := result.ValidateManifest(); err != nil {
		return MeasurementProtocol{}, fmt.Errorf("measurement protocol: %w", err)
	}
	manifest := result.Manifest
	return MeasurementProtocol{
		Schema: MeasurementProtocolSchema, ManifestSHA256: manifest.ManifestSHA256,
		Level: manifest.Level, ExecutionPolicy: manifest.ExecutionPolicy, TaskPlan: manifest.TaskPlan,
		SeedSet: manifest.SeedSet, Repeats: manifest.Repeats, RequestedContext: manifest.NumCtx,
	}, nil
}

// LegacyPolicyBundle is a compatibility projection of the old all-in-one
// profile. It does not synthesize a DecisionSpec. Workload requirements must
// remain explicit so changing them can re-evaluate evidence without changing
// the historical screening policy.
type LegacyPolicyBundle struct {
	Schema       string             `json:"schema"`
	Source       string             `json:"source_profile"`
	Calibration  DeviceCalibration  `json:"device_calibration"`
	Grading      GradingPolicy      `json:"grading_policy"`
	Capacity     *CapacityPolicy    `json:"capacity_policy,omitempty"`
	Presentation PresentationPreset `json:"presentation_preset"`
	Unclassified []string           `json:"unclassified_gates,omitempty"`
}

type DeviceCalibration struct {
	Match map[string]string `json:"match,omitempty"`
	Hints map[string]any    `json:"hints,omitempty"`
	Notes []string          `json:"notes,omitempty"`
}

type PresentationPreset struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type GradingPolicy struct {
	Performance  map[string]PerformanceScreen `json:"performance,omitempty"`
	Rates        map[string]RateScreen        `json:"rates,omitempty"`
	Refusal      *RefusalScreen               `json:"refusal,omitempty"`
	Agentic      *AgenticScreen               `json:"agentic,omitempty"`
	OutputHealth *OutputHealthScreen          `json:"output_health,omitempty"`
}

type PerformanceScreen struct {
	DecodeTPSMin   *float64 `json:"decode_tps_min,omitempty"`
	PrefillTPSMin  *float64 `json:"prefill_tps_min,omitempty"`
	TTFTSecondsMax *float64 `json:"ttft_seconds_max,omitempty"`
	Why            string   `json:"why,omitempty"`
}

type RateScreen struct {
	Minimum float64 `json:"minimum"`
	Why     string  `json:"why,omitempty"`
}

type RefusalScreen struct {
	MaximumRefusals int    `json:"maximum_refusals"`
	Why             string `json:"why,omitempty"`
}

type AgenticScreen struct {
	PrefillTPSMin       *float64 `json:"prefill_tps_min,omitempty"`
	MalformedCallsMax   *int     `json:"malformed_tool_calls_max,omitempty"`
	RequiresAgenticPass *bool    `json:"requires_agentic_pass,omitempty"`
	Why                 string   `json:"why,omitempty"`
}

type OutputHealthScreen struct {
	DuplicateParagraphRatioMax *float64 `json:"duplicate_paragraph_ratio_max,omitempty"`
	DuplicateLineRatioMax      *float64 `json:"duplicate_line_ratio_max,omitempty"`
	TopFourGramCharFractionMax *float64 `json:"top_four_gram_char_fraction_max,omitempty"`
	GzipCompressionRatioMax    *float64 `json:"gzip_compression_ratio_max,omitempty"`
	RepetitionScoreMax         *float64 `json:"repetition_score_max,omitempty"`
	AllowTruncation            *bool    `json:"allow_truncation,omitempty"`
	Why                        string   `json:"why,omitempty"`
}

// CapacityPolicy records the operator-facing resident-memory constraint that
// the legacy profile called always_on_capable. It does not claim sustained or
// always-on operation. Those claims require an operational experiment.
type CapacityPolicy struct {
	Schema               string `json:"schema"`
	SourceProfile        string `json:"source_profile"`
	MaximumResidentBytes int64  `json:"maximum_resident_bytes"`
	RequestedContext     int    `json:"requested_context"`
	Why                  string `json:"why,omitempty"`
}

// SplitLegacyProfile gives every known legacy gate one typed destination.
// Unknown extension gates remain disclosed in Unclassified instead of being
// silently converted into a different kind of policy.
func SplitLegacyProfile(profile device.Profile) (LegacyPolicyBundle, error) {
	if strings.TrimSpace(profile.Name) == "" {
		return LegacyPolicyBundle{}, errors.New("legacy profile name is required")
	}
	bundle := newLegacyPolicyBundle(profile)
	known := make(map[string]bool)
	addPerformanceScreens(profile, &bundle, known)
	if err := addRateScreens(profile, &bundle, known); err != nil {
		return LegacyPolicyBundle{}, err
	}
	if err := addSpecialScreens(profile, &bundle, known); err != nil {
		return LegacyPolicyBundle{}, err
	}
	classifyLegacyGates(profile, &bundle, known)
	return bundle, nil
}

func newLegacyPolicyBundle(profile device.Profile) LegacyPolicyBundle {
	return LegacyPolicyBundle{
		Schema: LegacyPolicyBundleSchema, Source: profile.Name,
		Calibration: DeviceCalibration{
			Match: cloneStringMap(profile.Match), Hints: cloneAnyMap(profile.Hints),
			Notes: append([]string(nil), profile.Notes...),
		},
		Grading: GradingPolicy{
			Performance: make(map[string]PerformanceScreen),
			Rates:       make(map[string]RateScreen),
		},
		Presentation: PresentationPreset{Name: profile.Name, Description: profile.Description},
	}
}

func addPerformanceScreens(profile device.Profile, bundle *LegacyPolicyBundle, known map[string]bool) {
	for _, name := range []string{"fast_chat", "interactive_coding"} {
		if gate, ok := profile.Gates[name]; ok {
			bundle.Grading.Performance[name] = PerformanceScreen{
				DecodeTPSMin:   floatField(profile, name, "decode_tps_min"),
				PrefillTPSMin:  floatField(profile, name, "prefill_tps_min"),
				TTFTSecondsMax: floatField(profile, name, "ttft_s_max"), Why: stringField(gate, "why"),
			}
			known[name] = true
		}
	}
}

func addRateScreens(profile device.Profile, bundle *LegacyPolicyBundle, known map[string]bool) error {
	for _, name := range []string{"structured_output", "instruction_precision", "tool_calling", "tool_restraint"} {
		if gate, ok := profile.Gates[name]; ok {
			minimum, present := profile.Float(name, "pass_rate_min")
			if !present {
				return fmt.Errorf("legacy profile gate %q is missing pass_rate_min", name)
			}
			bundle.Grading.Rates[name] = RateScreen{Minimum: minimum, Why: stringField(gate, "why")}
			known[name] = true
		}
	}
	return nil
}

func addSpecialScreens(profile device.Profile, bundle *LegacyPolicyBundle, known map[string]bool) error {
	if err := addRefusalScreen(profile, bundle, known); err != nil {
		return err
	}
	addAgenticScreen(profile, bundle, known)
	addOutputHealthScreen(profile, bundle, known)
	return addCapacityPolicy(profile, bundle, known)
}

func addRefusalScreen(profile device.Profile, bundle *LegacyPolicyBundle, known map[string]bool) error {
	if gate, ok := profile.Gates["uncensored"]; ok {
		maximum, present := integerField(gate, "refused_max")
		if !present {
			return errors.New("legacy profile gate \"uncensored\" is missing refused_max")
		}
		bundle.Grading.Refusal = &RefusalScreen{MaximumRefusals: maximum, Why: stringField(gate, "why")}
		known["uncensored"] = true
	}
	return nil
}

func addAgenticScreen(profile device.Profile, bundle *LegacyPolicyBundle, known map[string]bool) {
	if gate, ok := profile.Gates["unattended_agentic"]; ok {
		bundle.Grading.Agentic = &AgenticScreen{
			PrefillTPSMin:       floatField(profile, "unattended_agentic", "prefill_tps_min"),
			MalformedCallsMax:   intPointerField(gate, "malformed_tool_calls_max"),
			RequiresAgenticPass: boolPointerField(gate, "requires_agentic_pass"), Why: stringField(gate, "why"),
		}
		known["unattended_agentic"] = true
	}
}

func addOutputHealthScreen(profile device.Profile, bundle *LegacyPolicyBundle, known map[string]bool) {
	if gate, ok := profile.Gates["quality"]; ok {
		bundle.Grading.OutputHealth = &OutputHealthScreen{
			DuplicateParagraphRatioMax: floatField(profile, "quality", "dup_paragraph_ratio_max"),
			DuplicateLineRatioMax:      floatField(profile, "quality", "dup_line_ratio_max"),
			TopFourGramCharFractionMax: floatField(profile, "quality", "top_4gram_char_frac_max"),
			GzipCompressionRatioMax:    floatField(profile, "quality", "gzip_compression_ratio_max"),
			RepetitionScoreMax:         floatField(profile, "quality", "repetition_score_max"),
			AllowTruncation:            boolPointerField(gate, "allow_truncation"), Why: stringField(gate, "why"),
		}
		known["quality"] = true
	}
}

func addCapacityPolicy(profile device.Profile, bundle *LegacyPolicyBundle, known map[string]bool) error {
	if gate, ok := profile.Gates["always_on_capable"]; ok {
		maximumGB, present := profile.Float("always_on_capable", "resident_gb_at_32k_max")
		if !present || maximumGB <= 0 {
			return errors.New("legacy profile capacity gate requires positive resident_gb_at_32k_max")
		}
		bundle.Capacity = &CapacityPolicy{
			Schema: CapacityPolicySchema, SourceProfile: profile.Name,
			MaximumResidentBytes: int64(maximumGB * float64(device.GB)), RequestedContext: 32768,
			Why: stringField(gate, "why"),
		}
		known["always_on_capable"] = true
	}
	return nil
}

func classifyLegacyGates(profile device.Profile, bundle *LegacyPolicyBundle, known map[string]bool) {
	for name := range profile.Gates {
		if !known[name] {
			bundle.Unclassified = append(bundle.Unclassified, name)
		}
	}
	sort.Strings(bundle.Unclassified)
	if len(bundle.Grading.Performance) == 0 {
		bundle.Grading.Performance = nil
	}
	if len(bundle.Grading.Rates) == 0 {
		bundle.Grading.Rates = nil
	}
}

func floatField(profile device.Profile, gate, key string) *float64 {
	value, ok := profile.Float(gate, key)
	if !ok {
		return nil
	}
	return &value
}

func integerField(gate device.Gate, key string) (int, bool) {
	value, ok := gate[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case float64:
		if typed == float64(int(typed)) {
			return int(typed), true
		}
	}
	return 0, false
}

func intPointerField(gate device.Gate, key string) *int {
	value, ok := integerField(gate, key)
	if !ok {
		return nil
	}
	return &value
}

func boolPointerField(gate device.Gate, key string) *bool {
	value, ok := gate[key].(bool)
	if !ok {
		return nil
	}
	return &value
}

func stringField(gate device.Gate, key string) string {
	value, _ := gate[key].(string)
	return value
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func cloneAnyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
