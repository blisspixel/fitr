package fitting

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/score"
)

// This file projects one or more validated sealed records into the evidence
// classes a recommendation may use. It is the missing half of issue #26:
// Session.Recommendation passed no observations, so every class was unresolved
// even after collection.
//
// Two laws shape the mapping. A class is PASS only on direct evidence for that
// class and nothing else: a runtime capability declaration, a projected fit, or
// a sibling class never substitutes. And identity (artifact, runtime, device,
// tested context) comes only from validated observations, never from the session
// phase, model name, endpoint locality, or point count. Where a fact is absent,
// skipped, inconclusive, blocked, or descriptive-only, the class stays
// unresolved rather than becoming a pass.

// ObservationSource is one validated sealed record projected for a
// recommendation. Report and Scorecard are rebuilt from the validated record at
// load time (analysis.FromRecord re-verifies the evidence contract), never a
// stored conclusion.
type ObservationSource struct {
	// ArtifactDigest is the runtime-bound content digest. It is empty when the
	// artifact was only observed locally, which is not identity.
	ArtifactDigest string
	// Runtime and Backend name the observed serving runtime, never the plan's
	// endpoint or locality.
	Runtime string
	Backend string
	// DeviceKey is the runtime-bound comparison key from the validated device
	// fingerprint. It is empty when the effective context is unverified.
	DeviceKey string
	// RequestedContext is the context the record measured.
	RequestedContext int
	Report           analysis.Report
	Scorecard        score.Scorecard
}

// ObservationSourceFromRecord derives one projection from a sealed record. It
// validates the evidence contract through analysis.FromRecord before reading any
// field; a record that fails validation returns an error and stays unresolved.
func ObservationSourceFromRecord(result *record.Record) (ObservationSource, error) {
	if result == nil {
		return ObservationSource{}, errors.New("no sealed record to project")
	}
	report, err := analysis.FromRecord(result)
	if err != nil {
		return ObservationSource{}, err
	}
	source := ObservationSource{
		Report: report, Scorecard: result.Scorecard, RequestedContext: result.ContextSize(),
	}
	if result.Manifest != nil {
		source.ArtifactDigest = result.Manifest.Model.RuntimeBoundDigest()
		source.Runtime = result.Manifest.Model.Runtime
		source.Backend = result.Manifest.Model.Backend
	}
	if key, err := result.ComparableDeviceKey(); err == nil {
		source.DeviceKey = key
	}
	return source, nil
}

// Behavior needs map to the behavioral-reliability class. Tool-channel and
// executable-coding needs map to tool-or-coding. The bounded agent fixture is
// the harness class. Interactive response is the performance class. These are
// the splits the scorer already keeps separate (score.go NeedLabel).
var (
	behaviorNeeds = []string{"structured_output", "instruction_precision", "uncensored", "output_health"}
	toolNeeds     = []string{"coding", "tool_calling", "tool_restraint"}
)

const (
	performanceNeed = "fast_and_decent"
	harnessNeed     = "unattended_agentic"
)

// NeedClass maps one role behavior need to the evidence class that carries it.
// An unmapped need (for example vision) carries no class here.
func NeedClass(need string) string {
	switch need {
	case "structured_output", "instruction_precision", "uncensored", "output_health":
		return ClassBehavior
	case "coding", "tool_calling", "tool_restraint":
		return ClassToolWorkflow
	case "unattended_agentic":
		return ClassHarness
	case "fast_and_decent":
		return ClassPerformance
	case "low_footprint":
		return ClassLoad
	default:
		return ""
	}
}

// ProjectObservations maps validated record projections into the conservative
// evidence classes and the one scope those observations cover. A class is PASS
// only when a source establishes it directly; a FAIL fails the class, and any
// missing, skipped, inconclusive, blocked or descriptive-only observation
// leaves it unresolved. Multiple sources are emitted separately;
// AssessRecommendation merges duplicate classes, keeping the strongest state.
func ProjectObservations(sources []ObservationSource) ([]EvidenceObservation, RecommendationScope, []string) {
	var observations []EvidenceObservation
	for _, src := range sources {
		observations = append(observations, sourceObservations(src)...)
	}
	scope, conflicts := scopeFromSources(sources)
	return observations, scope, conflicts
}

func sourceObservations(src ObservationSource) []EvidenceObservation {
	var observations []EvidenceObservation
	appendIf := func(o EvidenceObservation, ok bool) {
		if ok {
			observations = append(observations, o)
		}
	}
	appendIf(memoryProjectionObservation(src))
	appendIf(loadObservation(src))
	appendIf(simplePromptObservation(src))
	appendIf(longContextObservation(src))
	if o, ok := verdictObservation(ClassPerformance, src.Scorecard.Needs[performanceNeed], "interactive response verdict"); ok {
		observations = append(observations, o)
	}
	if o, ok := verdictObservation(ClassHarness, src.Scorecard.Needs[harnessNeed], "bounded agent-fixture verdict"); ok {
		observations = append(observations, o)
	}
	appendIf(needsObservation(src, ClassBehavior, behaviorNeeds))
	appendIf(needsObservation(src, ClassToolWorkflow, toolNeeds))
	return observations
}

// memoryProjectionObservation is the capacity floor against the sealed budget.
// It uses the observed fit, never the weights-plus-KV prediction, which is a
// projection and not a measured fit.
func memoryProjectionObservation(src ObservationSource) (EvidenceObservation, bool) {
	budget := src.Report.Capacity.Budget
	if budget == nil || budget.Status != analysis.StatusAvailable {
		return EvidenceObservation{}, false
	}
	switch budget.State {
	case analysis.CapacityBudgetFit:
		return EvidenceObservation{Class: ClassMemoryProjection, State: StatePass,
			Detail: "observed resident fit within the sealed budget"}, true
	case analysis.CapacityBudgetExceeded:
		return EvidenceObservation{Class: ClassMemoryProjection, State: StateFail,
			Detail: "observed resident exceeded the sealed budget"}, true
	default:
		return EvidenceObservation{}, false
	}
}

// loadObservation is the runtime's own resident allocation at the requested
// context. Its presence is the load evidence; the budget class carries the fit.
func loadObservation(src ObservationSource) (EvidenceObservation, bool) {
	resident := src.Report.Capacity.Resident
	if resident == nil || resident.Status != analysis.StatusAvailable {
		return EvidenceObservation{}, false
	}
	return EvidenceObservation{Class: ClassLoad, State: StatePass,
		Detail: "runtime resident allocation observed at the requested context"}, true
}

// simplePromptObservation is a decode observation. It proves output was
// produced, not that it was correct, so the detail says so.
func simplePromptObservation(src ObservationSource) (EvidenceObservation, bool) {
	decode := src.Report.Performance.DecodeTPS
	if decode.Status != analysis.StatusAvailable || decode.SampleCount <= 0 {
		return EvidenceObservation{}, false
	}
	return EvidenceObservation{Class: ClassSimplePrompt, State: StatePass,
		Detail: "decode observed; prompt correctness was not graded"}, true
}

// longContextObservation is the document-task pack at the operating window. A
// pass needs a complete run with a verified prefix; a memory projection can
// never satisfy it.
func longContextObservation(src ObservationSource) (EvidenceObservation, bool) {
	tasks := src.Report.ContextTasks
	if tasks == nil || tasks.Status != analysis.StatusAvailable {
		return EvidenceObservation{}, false
	}
	switch tasks.Outcome {
	case "pass":
		if !tasks.Complete || tasks.VerifiedPrefixBytes == nil {
			return EvidenceObservation{}, false
		}
		return EvidenceObservation{Class: ClassLongContext, State: StatePass,
			Detail: "document pack passed at the operating window"}, true
	case "fail":
		return EvidenceObservation{Class: ClassLongContext, State: StateFail,
			Detail: "document pack failed at the operating window"}, true
	default:
		return EvidenceObservation{}, false
	}
}

// verdictObservation maps one scorecard verdict. SKIP, n/a, blocked and
// inconclusive all stay unresolved: none is a pass and none is a fail.
func verdictObservation(class string, verdict score.Verdict, detail string) (EvidenceObservation, bool) {
	switch verdict.State {
	case score.Pass:
		return EvidenceObservation{Class: class, State: StatePass, Detail: detail}, true
	case score.Fail:
		return EvidenceObservation{Class: class, State: StateFail, Detail: detail}, true
	default:
		return EvidenceObservation{}, false
	}
}

// needsObservation aggregates several needs into one class. A fail in any
// present need fails the class; the class passes only when every present need
// passed; a present need that was skipped, blocked or inconclusive leaves the
// class unresolved.
func needsObservation(src ObservationSource, class string, needs []string) (EvidenceObservation, bool) {
	present := 0
	for _, need := range needs {
		verdict, ok := src.Scorecard.Needs[need]
		if !ok {
			continue
		}
		present++
		if verdict.State == score.Fail {
			return EvidenceObservation{Class: class, State: StateFail,
				Detail: "a required " + class + " behavior failed"}, true
		}
	}
	if present == 0 {
		return EvidenceObservation{}, false
	}
	for _, need := range needs {
		if verdict, ok := src.Scorecard.Needs[need]; ok && verdict.State != score.Pass {
			return EvidenceObservation{}, false
		}
	}
	return EvidenceObservation{Class: class, State: StatePass,
		Detail: "required " + class + " behaviors passed"}, true
}

// scopeFromSources derives the one configuration the observations cover. It
// takes identity only from validated observations and requires the sources to
// agree; disagreement blanks the field and records a conflict rather than
// joining model names or counting points.
func scopeFromSources(sources []ObservationSource) (RecommendationScope, []string) {
	scope := RecommendationScope{}
	if len(sources) == 0 {
		return scope, nil
	}
	var conflicts []string
	agree := func(name string, value func(ObservationSource) string) string {
		first := value(sources[0])
		for _, src := range sources[1:] {
			if value(src) != first {
				conflicts = append(conflicts, "candidates were measured on different "+name+"; identity is not comparable")
				return ""
			}
		}
		return first
	}
	scope.Artifact = agree("artifacts", func(s ObservationSource) string { return s.ArtifactDigest })
	scope.Runtime = agree("runtimes", func(s ObservationSource) string { return s.Runtime })
	scope.Device = agree("devices", func(s ObservationSource) string { return s.DeviceKey })
	effective := func(s ObservationSource) string {
		if s.Report.Context.Effective == nil {
			return ""
		}
		return strconv.Itoa(*s.Report.Context.Effective)
	}
	if tested := agree("effective contexts", effective); tested != "" {
		if n, err := parsePositiveInt(tested); err == nil {
			scope.TestedContext = n
		}
	}
	scope.Configuration = configurationText(sources[0])
	return scope, conflicts
}

// configurationText names the observed operating configuration. It uses the
// observed KV datatype and effective window when present, never the plan's
// requested settings.
func configurationText(src ObservationSource) string {
	parts := ""
	if src.Report.Context.Effective != nil {
		parts = fmt.Sprintf("context %d", *src.Report.Context.Effective)
	}
	if src.Report.Capacity.Prediction != nil && src.Report.Capacity.Prediction.KVDataType != "" {
		if parts != "" {
			parts += ", "
		}
		parts += "kv " + src.Report.Capacity.Prediction.KVDataType
	}
	return parts
}

func parsePositiveInt(value string) (int, error) {
	n := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not an integer: %q", value)
		}
		n = n*10 + int(r-'0')
	}
	if n <= 0 {
		return 0, fmt.Errorf("not positive: %q", value)
	}
	return n, nil
}

// RequiredClasses lists the evidence classes the sealed plan's role declares,
// so an assessment requires only the work it asked for rather than a fixed set
// that demands harness or long-context evidence a role never requested.
func RequiredClasses(plan Plan) []string {
	var classes []string
	seen := map[string]bool{}
	add := func(class string) {
		if class != "" && !seen[class] {
			seen[class] = true
			classes = append(classes, class)
		}
	}
	for _, req := range plan.Role.Decision.Requirements {
		if req.Behavior != nil {
			add(NeedClass(req.Behavior.Need))
		}
		if req.Context != nil {
			if req.Context.MinimumEffectiveTokens > 0 {
				add(ClassLoad)
			}
			if req.Context.MinimumUsableContextBytes != nil {
				add(ClassLongContext)
			}
		}
		if req.Performance != nil {
			add(ClassPerformance)
		}
		if req.Capacity != nil {
			add(ClassMemoryProjection)
		}
	}
	return classes
}
