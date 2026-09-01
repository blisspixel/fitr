package experiment

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/record"
)

const (
	ContextPlanSchema                = "fitr.experiment.context.plan.v1"
	ContextReportSchema              = "fitr.experiment.context.analysis.v1"
	maximumContextPoints             = 16
	maximumContextTokens             = 16 * 1024 * 1024
	maximumContextPerformanceSamples = 20
)

type ContextPlan struct {
	Schema                 string `json:"schema"`
	Model                  string `json:"model"`
	RequestedContexts      []int  `json:"requested_contexts"`
	PerformanceSamples     int    `json:"performance_samples"`
	MeasurePointAllocation bool   `json:"measure_point_allocation"`
	SeedSet                string `json:"seedset"`
	Level                  string `json:"level"`
	Order                  string `json:"order"`
}

type ContextPointState string

const (
	ContextPointVerified   ContextPointState = "verified"
	ContextPointAdjusted   ContextPointState = "adjusted"
	ContextPointUnverified ContextPointState = "unverified"
)

type ContextPoint struct {
	Sequence    int                             `json:"sequence"`
	Source      SourceReference                 `json:"source"`
	Subject     decision.ConfigurationUnderTest `json:"subject"`
	State       ContextPointState               `json:"state"`
	Context     analysis.Context                `json:"context"`
	Performance analysis.Performance            `json:"performance"`
	Capacity    analysis.Capacity               `json:"capacity"`
	Gaps        []analysis.EvidenceGap          `json:"gaps,omitempty"`
}

type ContextCoverage struct {
	VerifiedContexts         []int `json:"verified_contexts,omitempty"`
	AllocationContexts       []int `json:"allocation_contexts,omitempty"`
	PerformanceContexts      []int `json:"performance_contexts,omitempty"`
	MaximumVerifiedContext   *int  `json:"maximum_verified_context,omitempty"`
	MaximumAllocationContext *int  `json:"maximum_allocation_context,omitempty"`
}

type ContextReport struct {
	Schema      string          `json:"schema"`
	Stage       Stage           `json:"stage"`
	Plan        *ContextPlan    `json:"plan,omitempty"`
	PlanSHA256  string          `json:"plan_sha256,omitempty"`
	Predeclared bool            `json:"predeclared"`
	Comparison  Comparison      `json:"comparison"`
	Points      []ContextPoint  `json:"points"`
	Coverage    ContextCoverage `json:"coverage"`
	Gaps        []string        `json:"gaps,omitempty"`
	NextAction  *Action         `json:"next_action,omitempty"`
}

// AnalyzeContext builds an exploratory context experiment from sealed runs.
// It does not infer or insert unattempted points. Confirmation requires a
// separately predeclared fresh plan and is not produced by this function.
func AnalyzeContext(results []*record.Record) (ContextReport, error) {
	return analyzeContext(results, nil, "")
}

// NewContextPlan seals the finite point set and execution order before a live
// context experiment begins.
func NewContextPlan(model string, requestedContexts []int, performanceSamples int) (ContextPlan, string, error) {
	seedBytes := make([]byte, 16)
	if _, err := rand.Read(seedBytes); err != nil {
		return ContextPlan{}, "", fmt.Errorf("create context seed set: %w", err)
	}
	plan := ContextPlan{
		Schema: ContextPlanSchema, Model: strings.TrimSpace(model),
		RequestedContexts:  append([]int(nil), requestedContexts...),
		PerformanceSamples: performanceSamples, MeasurePointAllocation: true,
		SeedSet: "context-" + hex.EncodeToString(seedBytes), Level: "quick", Order: "declared",
	}
	if err := plan.validate(); err != nil {
		return ContextPlan{}, "", err
	}
	digest, err := contextPlanDigest(plan)
	if err != nil {
		return ContextPlan{}, "", err
	}
	return plan, digest, nil
}

func (plan ContextPlan) validate() error {
	if plan.Schema != ContextPlanSchema || plan.Level != "quick" || plan.Order != "declared" ||
		!plan.MeasurePointAllocation {
		return errors.New("unsupported context plan schema or execution policy")
	}
	if plan.Model == "" {
		return errors.New("context plan model is required")
	}
	if strings.TrimSpace(plan.Model) != plan.Model {
		return errors.New("context plan model cannot have leading or trailing whitespace")
	}
	seed, ok := strings.CutPrefix(plan.SeedSet, "context-")
	decoded, seedErr := hex.DecodeString(seed)
	if !ok || seedErr != nil || len(decoded) != 16 || hex.EncodeToString(decoded) != seed {
		return errors.New("context plan seed set is invalid")
	}
	if len(plan.RequestedContexts) < 2 || len(plan.RequestedContexts) > maximumContextPoints {
		return fmt.Errorf("context plan requires between 2 and %d points", maximumContextPoints)
	}
	if plan.PerformanceSamples < 1 || plan.PerformanceSamples > maximumContextPerformanceSamples {
		return fmt.Errorf("context plan requires between 1 and %d performance samples",
			maximumContextPerformanceSamples)
	}
	seen := make(map[int]bool, len(plan.RequestedContexts))
	for _, context := range plan.RequestedContexts {
		if context < 1 || context > maximumContextTokens {
			return fmt.Errorf("context plan points must be between 1 and %d tokens",
				maximumContextTokens)
		}
		if seen[context] {
			return fmt.Errorf("context plan repeats requested context %d", context)
		}
		seen[context] = true
	}
	return nil
}

func contextPlanDigest(plan ContextPlan) (string, error) {
	return factorDigest("context_plan", plan)
}

// ContextPlanBinding is recorded in a point manifest so later analysis can
// prove that the finite point set and point position existed before inference.
func ContextPlanBinding(planSHA256 string, pointIndex, pointCount int) record.ExperimentBinding {
	return record.ExperimentBinding{
		Schema: record.ExperimentBindingSchema, Kind: "context", Stage: string(StageExplore),
		PlanSHA256: planSHA256, PointIndex: pointIndex, PointCount: pointCount,
	}
}

// AnalyzePlannedContext verifies source order, requested contexts, point
// allocation, sample count, and the manifest plan binding before marking the
// exploratory report predeclared.
func AnalyzePlannedContext(results []*record.Record, plan ContextPlan, planSHA256 string) (ContextReport, error) {
	if err := plan.validate(); err != nil {
		return ContextReport{}, err
	}
	wantDigest, err := contextPlanDigest(plan)
	if err != nil || planSHA256 != wantDigest {
		return ContextReport{}, errors.New("context plan digest or semantics do not match")
	}
	if len(results) != len(plan.RequestedContexts) {
		return ContextReport{}, fmt.Errorf("context plan has %d points but %d sealed runs were supplied",
			len(plan.RequestedContexts), len(results))
	}
	for index, result := range results {
		if result == nil || result.Manifest == nil {
			return ContextReport{}, fmt.Errorf("context point %d has no sealed manifest", index+1)
		}
		if result.Manifest.Model.Requested != plan.Model {
			return ContextReport{}, fmt.Errorf("context point %d requested model %q, plan requires %q",
				index+1, result.Manifest.Model.Requested, plan.Model)
		}
		if result.ContextSize() != plan.RequestedContexts[index] {
			return ContextReport{}, fmt.Errorf("context point %d requested %d, plan requires %d",
				index+1, result.ContextSize(), plan.RequestedContexts[index])
		}
		wantBinding := ContextPlanBinding(planSHA256, index+1, len(results))
		if result.Manifest.Experiment == nil || *result.Manifest.Experiment != wantBinding {
			return ContextReport{}, fmt.Errorf("context point %d is not bound to the predeclared plan", index+1)
		}
		if result.TaskPlan.SpeedSamples != plan.PerformanceSamples {
			return ContextReport{}, fmt.Errorf("context point %d planned %d performance samples, want %d",
				index+1, result.TaskPlan.SpeedSamples, plan.PerformanceSamples)
		}
		if result.Level != plan.Level || result.SeedSet != plan.SeedSet {
			return ContextReport{}, fmt.Errorf("context point %d measurement level or seed set differs from the plan",
				index+1)
		}
		if plan.MeasurePointAllocation && result.Memory.RequestedCtx != plan.RequestedContexts[index] {
			return ContextReport{}, fmt.Errorf("context point %d allocation was requested at %d, want %d",
				index+1, result.Memory.RequestedCtx, plan.RequestedContexts[index])
		}
	}
	return analyzeContext(results, &plan, wantDigest)
}

func analyzeContext(results []*record.Record, plan *ContextPlan, planSHA256 string) (ContextReport, error) {
	if len(results) < 2 {
		return ContextReport{}, errors.New("context experiment requires at least two sealed runs")
	}
	report := ContextReport{
		Schema: ContextReportSchema, Stage: StageExplore, Plan: plan,
		PlanSHA256: planSHA256, Predeclared: plan != nil,
	}
	report.Points = make([]ContextPoint, 0, len(results))
	for index, result := range results {
		point, err := contextPoint(result)
		if err != nil {
			return ContextReport{}, fmt.Errorf("context experiment source %d: %w", index+1, err)
		}
		point.Sequence = index + 1
		report.Points = append(report.Points, point)
	}
	sort.SliceStable(report.Points, func(i, j int) bool {
		if report.Points[i].Context.Requested != report.Points[j].Context.Requested {
			return report.Points[i].Context.Requested < report.Points[j].Context.Requested
		}
		return report.Points[i].Source.RunID < report.Points[j].Source.RunID
	})
	report.Comparison = contextComparison(results)
	report.Coverage = contextCoverage(report.Points)
	report.Gaps, report.NextAction = contextGapsAndAction(report)
	return report, nil
}

func contextPoint(result *record.Record) (ContextPoint, error) {
	derived, err := analysis.FromRecord(result)
	if err != nil {
		return ContextPoint{}, err
	}
	subject, err := decision.ConfigurationFromRecord(result)
	if err != nil {
		return ContextPoint{}, err
	}
	state := ContextPointUnverified
	if derived.Context.Effective != nil {
		state = ContextPointVerified
		if *derived.Context.Effective != derived.Context.Requested {
			state = ContextPointAdjusted
		}
	}
	return ContextPoint{
		Source: SourceReference{
			RunID: result.StableRunID(), EvidenceSHA256: derived.Source.EvidenceSHA256, Source: derived.Source,
		},
		Subject: subject, State: state, Context: derived.Context,
		Performance: derived.Performance, Capacity: derived.Capacity,
		Gaps: append([]analysis.EvidenceGap(nil), derived.Gaps...),
	}, nil
}

func contextComparison(results []*record.Record) Comparison {
	factors := []FactorComparison{
		compareFactor(results, "artifact_identity", artifactFactor),
		compareFactor(results, "backend_runtime", backendRuntimeFactor),
		compareFactor(results, "device_configuration", deviceFactor),
		compareFactor(results, "measurement_protocol", contextProtocolFactor),
	}
	comparison := Comparison{
		Schema: ComparisonSchema, Treatment: "requested_context_tokens", Ready: true, Factors: factors,
	}
	for _, factor := range factors {
		if factor.State != FactorEqual {
			comparison.Ready = false
			comparison.Missing = append(comparison.Missing, factor.Code+": "+factor.Reason)
		}
	}
	if distinctRequestedContexts(results) < 2 {
		comparison.Ready = false
		comparison.Missing = append(comparison.Missing, "at least two distinct requested contexts")
	}
	sort.Strings(comparison.Missing)
	return comparison
}

type factorValueFunc func(*record.Record) (any, bool)

func compareFactor(results []*record.Record, code string, valueFn factorValueFunc) FactorComparison {
	factor := FactorComparison{Code: code, Role: FactorRequiredEqual, State: FactorEqual}
	unique := make(map[string]bool)
	missing := false
	for _, result := range results {
		value, present := valueFn(result)
		observation := FactorObservation{RunID: result.StableRunID(), Present: present}
		if present {
			digest, err := factorDigest(code, value)
			if err == nil {
				observation.ValueSHA256 = digest
				unique[digest] = true
			} else {
				observation.Present = false
				missing = true
			}
		} else {
			missing = true
		}
		factor.Observations = append(factor.Observations, observation)
	}
	switch {
	case missing:
		factor.State = FactorMissing
		factor.Reason = "one or more points lack the required factor receipt"
	case len(unique) != 1:
		factor.State = FactorDifferent
		factor.Reason = "the factor changed across points"
	default:
		factor.Reason = "the factor is equal across every point"
	}
	return factor
}

func artifactFactor(result *record.Record) (any, bool) {
	if result == nil || result.Manifest == nil {
		return nil, false
	}
	identity := result.Manifest.Model
	digest := identity.RuntimeBoundDigest()
	if digest == "" {
		return nil, false
	}
	return struct {
		Digest string `json:"digest"`
		Kind   string `json:"kind"`
	}{Digest: digest, Kind: identity.Kind}, true
}

func backendRuntimeFactor(result *record.Record) (any, bool) {
	if result == nil || result.Manifest == nil || result.Manifest.Provenance == nil {
		return nil, false
	}
	identity := result.Manifest.Model
	if identity.Backend == "" || identity.Runtime == "" || result.Manifest.Provenance.BackendProtocol == "" {
		return nil, false
	}
	return struct {
		Backend  string `json:"backend"`
		Runtime  string `json:"runtime"`
		Protocol string `json:"protocol"`
	}{identity.Backend, identity.Runtime, result.Manifest.Provenance.BackendProtocol}, true
}

func deviceFactor(result *record.Record) (any, bool) {
	if result == nil || result.DeviceV2 == nil {
		return nil, false
	}
	device := result.DeviceV2.Device
	// InferenceDevice is an observed placement outcome and may legitimately
	// change because requested context is the treatment. The required-equal
	// factor retains machine identity, runtime configuration, backend, and
	// capacity inputs without conditioning on that outcome.
	return struct {
		Host          string            `json:"host"`
		OS            string            `json:"os"`
		CPU           string            `json:"cpu"`
		RAMGB         float64           `json:"ram_gb"`
		GPU           string            `json:"gpu"`
		GPUDriver     string            `json:"gpu_driver"`
		GPUDriverDate string            `json:"gpu_driver_date"`
		GPUBackend    string            `json:"gpu_backend"`
		VRAMGB        float64           `json:"vram_gb"`
		VRAMSource    string            `json:"vram_source"`
		Config        map[string]string `json:"config"`
	}{
		Host: device.Host, OS: device.OS, CPU: device.CPU, RAMGB: device.RAMGb,
		GPU: device.GPU, GPUDriver: device.GPUDriver, GPUDriverDate: device.GPUDriverDate,
		GPUBackend: device.GPUBackend, VRAMGB: device.VRAMGb, VRAMSource: device.VRAMSource,
		Config: device.Config,
	}, true
}

func contextProtocolFactor(result *record.Record) (any, bool) {
	protocol, err := decision.ProtocolFromRecord(result)
	if err != nil || result.Manifest == nil || result.Manifest.Provenance == nil {
		return nil, false
	}
	provenance := result.Manifest.Provenance
	return struct {
		Level           string          `json:"level"`
		ExecutionPolicy string          `json:"execution_policy"`
		TaskPlan        record.TaskPlan `json:"task_plan"`
		SeedSet         string          `json:"seedset"`
		Repeats         int             `json:"repeats"`
		TaskSetSHA256   string          `json:"task_set_sha256"`
		SpecSHA256      string          `json:"spec_sha256"`
	}{
		Level: protocol.Level, ExecutionPolicy: protocol.ExecutionPolicy,
		TaskPlan: protocol.TaskPlan, SeedSet: protocol.SeedSet, Repeats: protocol.Repeats,
		TaskSetSHA256: provenance.TaskSetSHA256, SpecSHA256: provenance.SpecSHA256,
	}, true
}

func distinctRequestedContexts(results []*record.Record) int {
	values := make(map[int]bool)
	for _, result := range results {
		if result != nil {
			values[result.ContextSize()] = true
		}
	}
	return len(values)
}

func contextCoverage(points []ContextPoint) ContextCoverage {
	verified := make(map[int]bool)
	allocation := make(map[int]bool)
	performance := make(map[int]bool)
	for _, point := range points {
		if point.State == ContextPointVerified {
			verified[point.Context.Requested] = true
		}
		if residentClaimable(point.Capacity.Resident) {
			allocation[point.Context.Requested] = true
		}
		if performanceClaimable(point.Performance) {
			performance[point.Context.Requested] = true
		}
	}
	coverage := ContextCoverage{
		VerifiedContexts: sortedInts(verified), AllocationContexts: sortedInts(allocation),
		PerformanceContexts: sortedInts(performance),
	}
	coverage.MaximumVerifiedContext = lastInt(coverage.VerifiedContexts)
	coverage.MaximumAllocationContext = lastInt(coverage.AllocationContexts)
	return coverage
}

func residentClaimable(observation *analysis.ResidentObservation) bool {
	if observation == nil || observation.Estimate == nil || observation.Status != analysis.StatusAvailable {
		return false
	}
	for _, claim := range observation.Supports {
		if claim == analysis.ClaimExactContextResidentBytes {
			return true
		}
	}
	return false
}

func performanceClaimable(performance analysis.Performance) bool {
	return performance.DecodeTPS.Estimate != nil && performance.DecodeTPS.Status == analysis.StatusAvailable &&
		analysis.Supports(performance.DecodeTPS, analysis.ClaimObservedDecode)
}

func contextGapsAndAction(report ContextReport) ([]string, *Action) {
	var gaps []string
	if !report.Comparison.Ready {
		gaps = append(gaps, report.Comparison.Missing...)
	}
	for _, point := range report.Points {
		switch point.State {
		case ContextPointUnverified:
			gaps = append(gaps, fmt.Sprintf("requested context %d lacks a verified effective context", point.Context.Requested))
		case ContextPointAdjusted:
			gaps = append(gaps, fmt.Sprintf("requested context %d was adjusted to %d", point.Context.Requested, *point.Context.Effective))
		}
	}
	gaps = uniqueSorted(gaps)
	if !report.Comparison.Ready {
		return gaps, &Action{Code: "align_context_experiment_factors",
			Reason: "repeat points with the same artifact, backend/runtime, device configuration, and measurement protocol"}
	}
	for _, point := range report.Points {
		if point.State != ContextPointVerified {
			return gaps, &Action{Code: "repeat_context_point",
				Argv:   []string{"fitr", "run", "<current-model>", "--ctx", strconv.Itoa(point.Context.Requested)},
				Reason: "obtain a runtime-verified effective context for the unresolved point"}
		}
	}
	return gaps, nil
}

func factorDigest(domain string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("fitr.experiment.factor.v1\x00" + domain + "\x00"))
	_, _ = hash.Write(data)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func sortedInts(values map[int]bool) []int {
	result := make([]int, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Ints(result)
	return result
}

func lastInt(values []int) *int {
	if len(values) == 0 {
		return nil
	}
	value := values[len(values)-1]
	return &value
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}
