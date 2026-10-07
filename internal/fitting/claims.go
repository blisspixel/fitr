package fitting

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/blisspixel/fitr/internal/eval"
)

// UntestedContextProjection labels a larger window that weights plus KV can
// afford. The measured window is the only one with demonstrated task quality.
func UntestedContextProjection(suggested, measured int) string {
	if suggested <= 0 || measured <= 0 || suggested <= measured {
		return ""
	}
	return fmt.Sprintf("untested projection: weights plus KV suggest %d tokens may fit; measured at %d. This is not demonstrated task quality, a runtime-verified window, populated input, tool or system overhead, output reserve, or observed resident memory", suggested, measured)
}

// Paired reports whether two runs share this plan's generated cases.
// Equal repeats and context are not enough when the seeds differ.
func Paired(plan Plan, leftSeed, rightSeed string) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if plan.Comparison != ComparisonComparative {
		return errors.New("one candidate is a non-comparative assessment")
	}
	if leftSeed != plan.SeedSet || rightSeed != plan.SeedSet || leftSeed == "" {
		return errors.New("different generated cases are not a paired comparison")
	}
	return nil
}

// FreshConfirmation refuses to relabel exploration evidence. This fitting
// does not mint a second seed for an interrupted confirmation.
func FreshConfirmation(plan Plan, evidenceSeed string) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if evidenceSeed == "" || evidenceSeed == plan.SeedSet || evidenceSeed == plan.ID {
		return errors.New("historical exploration evidence is not fresh confirmation")
	}
	if plan.Comparison != ComparisonComparative {
		return errors.New("a non-comparative assessment has no confirmation winner")
	}
	return nil
}

// FailureLine describes one failed check without the prompt or the reply.
// Older records that lack hashes say so instead of reconstructing the input.
func FailureLine(outcome eval.CheckOutcome) string {
	if outcome.Pass || outcome.Outcome != eval.OutcomeFail {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "task %s family %s seed %d", outcome.TaskID, outcome.Family, outcome.Seed)
	if outcome.ParserOutcome != "" {
		fmt.Fprintf(&b, " parser %s", outcome.ParserOutcome)
	}
	if outcome.FailingField != "" {
		fmt.Fprintf(&b, " field %s", outcome.FailingField)
	}
	if outcome.Verifier != "" {
		fmt.Fprintf(&b, " verifier %s", outcome.Verifier)
	}
	if outcome.InputSHA256 == "" {
		b.WriteString(". input hash was not recorded")
	} else {
		fmt.Fprintf(&b, " input %s", outcome.InputSHA256)
		if outcome.CanonicalSHA256 != "" {
			fmt.Fprintf(&b, " canonical %s", outcome.CanonicalSHA256)
		}
	}
	if outcome.Family == "tool_args" {
		b.WriteString(". tool_args grades JSON written as text and normalization; it is not the native tool-call channel")
	}
	if !outcome.RawRetained {
		b.WriteString(". the response was not retained and cannot be reconstructed")
	}
	return b.String()
}

// kvClaim states the sealed plan's cache request. The flash-attention boolean
// is that request. Quantized KV is refused without it. The line is not a
// report that a running server has flash attention enabled.
func kvClaim(plan Plan) string {
	kv := plan.KVCacheType
	if !plan.KVExplicit {
		kv += " (owned-runtime default, not a requested setting)"
	}
	line := fmt.Sprintf("kv %s. Plan records flash attention %t. This record does not set or prove the server.", kv, plan.FlashAttention)
	if plan.KVCacheType == "q8_0" || plan.KVCacheType == "q4_0" {
		line += " Quantized KV is refused unless the plan requests flash attention."
	}
	return line
}

// Lines is the single preview used by text and JSON.
func (plan Plan) Lines(phase string) []string {
	policy := "reserve subtracted from timestamped free memory when the run policy is sealed. It does not by itself change the server's GPU overhead"
	if plan.CapacityKind == CapacityCeiling {
		policy = "absolute ceiling. It is not a reserve subtracted from free memory, and it is not written to OLLAMA_GPU_OVERHEAD. A capacity policy is not a hardware allocation cap"
	}
	if plan.OwnedRuntime {
		policy = "reserve passed to an owned child as OLLAMA_GPU_OVERHEAD, with the requested context. The user's other Ollama process is not stopped"
	}
	repeats := strconv.Itoa(plan.Repeats)
	if plan.RepeatsDefaulted {
		repeats += " (minimum rankable schedule; no repeat count was requested)"
	}
	comparison := "non-comparative assessment. A second candidate was not invented"
	if plan.Comparison == ComparisonComparative {
		comparison = "comparative shortlist. Pairing requires this plan's seed. Equal repeats and context alone do not pair runs that generated different cases. Fresh confirmation is a later seed and is not minted by resume"
	}
	lines := []string{
		"fitting " + plan.ID + " phase " + phase,
		"build " + plan.BuildVersion,
		"role " + plan.RoleName + " scope " + plan.Scope,
		"candidates " + strings.Join(plan.Candidates, ", "),
		"comparison " + comparison,
		fmt.Sprintf("context %d tokens. The requested window is not demonstrated until a run verifies it. Advertised model limits and byte-sized document tiers are separate.", plan.ContextTokens),
		kvClaim(plan),
		"capacity " + string(plan.CapacityKind) + " " + FormatGiB(plan.CapacityBytes) + " (" + policy + ")",
		"role resident limit " + FormatGiB(plan.ResidentLimitBytes),
		"endpoint " + plan.Endpoint + " source " + plan.EndpointSource + " locality " + plan.Locality,
		"repeats " + repeats + " seed " + plan.SeedSet,
		"schedule full execution-disabled battery. Requested output caps are admission allowance, not tokens generated and not percent complete.",
		"ordinary saved results are not imported. A different seed, context, capacity policy, or endpoint is not paired evidence.",
		"retention model responses are not kept. Failed checks can keep task, family, seed, parser, field, verifier, and hashes.",
		"adoption records a fitr role selection only, and only after a separate approval. It does not change an Ollama alias, an external agent, or restart a runtime.",
		"permitted actions: no install, no download, no paid call, no provider switch, no stop of an unrelated Ollama.",
		"tool_args grades JSON written as text. Native tool calls are tool_call, tool_call_strict, and tool_fanout. A tool_args miss does not show that native tool support is broken.",
		"executable coding and the bounded-agent fixture stay unscored while execution is disabled. Inconclusive rates, an uncalibrated profile, and unknown cache state stay visible. Faster decode does not repair a failed required behavior.",
	}
	return append(lines, plan.extraLines()...)
}

func (plan Plan) extraLines() []string {
	var lines []string
	if plan.EndpointNote != "" {
		lines = append(lines, "endpoint note "+plan.EndpointNote)
	}
	if plan.OwnedRuntime {
		lines = append(lines,
			"owned runtime approved. Context and reserve come from this plan, not from the 8192 token and 2 GiB inspection defaults.",
			"When fitr starts that owned child, the child environment receives this plan's KV cache type and flash-attention request. The launch is a request to that child. It is not proof the server applied them, and it leaves an already running Ollama unchanged.",
			fmt.Sprintf("allowance %d requests, %d reserved output tokens, %d points, %ds wall, %ds confirmation. Reserved output is an admission allowance, not tokens generated and not percent complete.",
				plan.MaxRequests, plan.MaxRequestedOutputTokens, plan.MaxPoints, plan.WallSeconds, plan.ConfirmationWallSeconds))
	} else {
		lines = append(lines, "owned runtime not approved. Measurement uses the selected endpoint and does not launch a private server.")
		if plan.KVExplicit {
			lines = append(lines, "KV and flash attention are recorded on this plan. They are not written into the running server.")
		}
	}
	if len(plan.ContextTiers) > 0 {
		lines = append(lines, fmt.Sprintf("document tiers %v bytes. These are not token windows and this plan does not search context sizes.", plan.ContextTiers))
	}
	if plan.ScreeningNote != "" {
		lines = append(lines, plan.ScreeningNote)
	}
	lines = append(lines, plan.uncertaintyLines()...)
	if plan.Blocked {
		lines = append(lines, "stopped before inference: "+plan.BlockReason)
	}
	return lines
}

// ResumeAction says what a later invocation may do. Measuring never mints a
// confirmation seed, and a finished exploration is not rerun.
func (session Session) ResumeAction() (string, error) {
	if err := session.Plan.Validate(); err != nil {
		return "", err
	}
	switch session.Phase {
	case PhaseBlocked:
		return "", errors.New("a blocked fitting does not resume into inference")
	case PhasePreviewed:
		return "", errors.New("approve the preview with tailor start before measuring")
	case PhaseApproved, PhaseMeasuring:
		return "measure", nil
	case PhaseMeasured:
		return "status", nil
	case PhaseDelegated:
		if session.AutoSessionID == "" {
			return "", errors.New("delegated fitting has no auto session id")
		}
		return "auto", nil
	case PhaseAdoptionClosed:
		return "", errors.New("interrupted confirmation ended adoption; a new confirmation seed was not created")
	default:
		return "", errors.New("unknown fitting phase")
	}
}

// AdoptionAction reports the only change adopt may attempt.
func (session Session) AdoptionAction() (string, error) {
	if err := session.Plan.Validate(); err != nil {
		return "", err
	}
	switch session.Phase {
	case PhaseDelegated:
		if session.AutoSessionID == "" {
			return "", errors.New("delegated fitting has no auto session id")
		}
		return "auto", nil
	case PhaseMeasured:
		return "", errors.New("this assessment has no fresh confirmation, so no role selection was recorded. No Ollama alias, external agent, or serving runtime was changed")
	case PhaseAdoptionClosed:
		return "", errors.New("interrupted confirmation ended this session's adoption path. No alias, agent, or runtime was changed")
	default:
		return "", errors.New("adoption needs a finished fitting. Nothing was changed")
	}
}

// StatusLines is the session view shared by text and JSON.
func (session Session) StatusLines() []string {
	lines := session.Plan.Lines(session.Phase)
	lines = append(lines, fmt.Sprintf("completed evidence points %d of %d", len(session.Points), len(session.Plan.Candidates)))
	for _, point := range session.Points {
		lines = append(lines, "evidence "+point.Model+" "+point.RunID+" "+point.EvidenceSHA256)
	}
	if session.AutoSessionID != "" {
		lines = append(lines, "owned session "+session.AutoSessionID,
			"inspect measured facts: fitr auto status "+session.AutoSessionID)
	}
	action, err := session.ResumeAction()
	if err != nil {
		lines = append(lines, "remaining decision: "+err.Error())
	} else {
		lines = append(lines, "remaining decision: "+remainingDecision(action))
	}
	if session.ExecutableSHA256 != "" {
		lines = append(lines, "executable "+session.ExecutableSHA256)
	}
	return append(lines, session.Recommendation().Lines...)
}

func remainingDecision(action string) string {
	switch action {
	case "measure":
		return "measure the remaining candidates under this plan"
	case "status":
		return "measurement finished. No role selection is recorded yet"
	case "auto":
		return "continue the owned auto session"
	default:
		return action
	}
}

// HistoryDecision is the only way an older point may enter this plan.
// Reuse never means fresh confirmation.
type HistoryDecision struct {
	Reuse  bool
	Fresh  bool
	Reason string
}

// ConsiderHistory accepts an older point only when the comparison factors match.
// Equal repeats alone do not match. A match is still exploration evidence.
func ConsiderHistory(plan Plan, model, seed string, contextTokens int, kind CapacityKind, capacityBytes int64, endpoint string) (HistoryDecision, error) {
	if err := plan.Validate(); err != nil {
		return HistoryDecision{}, err
	}
	known := false
	for _, candidate := range plan.Candidates {
		if candidate == model {
			known = true
		}
	}
	if !known || seed != plan.SeedSet || contextTokens != plan.ContextTokens || kind != plan.CapacityKind || capacityBytes != plan.CapacityBytes || endpoint != plan.Endpoint {
		return HistoryDecision{Reason: "historical evidence differs in candidate, seed, context, capacity policy, or endpoint, so it is not paired evidence"}, nil
	}
	reason := "historical evidence matches this plan and is not fresh confirmation"
	if plan.Workload != nil && !SatisfiesContextGoal(plan.ContextTokens, plan.Workload.DesiredContextTokens) {
		reason += ". It does not satisfy a larger desired context"
	}
	return HistoryDecision{Reuse: true, Fresh: false, Reason: reason}, nil
}

const RecommendationSchema = "fitr.recommendation.v1"

const (
	ClassMemoryProjection = "memory-projection"
	ClassLoad             = "load"
	ClassSimplePrompt     = "simple-prompt"
	ClassPerformance      = "performance"
	ClassBehavior         = "behavior"
	ClassToolWorkflow     = "tool-or-coding"
	ClassLongContext      = "measured-long-context"
	ClassHarness          = "harness"
)

const (
	StatePass       = "pass"
	StateFail       = "fail"
	StateUnmeasured = "unmeasured"
	StateSkipped    = "skipped"
	StateUnresolved = "unresolved"
)

// EvidenceObservation is one measured class. A pass for one class does not
// pass another. memory-projection never satisfies measured-long-context.
type EvidenceObservation struct {
	Class  string `json:"class"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// RecommendationScope names the only configuration the conclusion covers.
// A changed artifact, runtime, device, or context needs a new conclusion.
type RecommendationScope struct {
	Artifact      string   `json:"artifact,omitempty"`
	Runtime       string   `json:"runtime,omitempty"`
	Configuration string   `json:"configuration,omitempty"`
	Device        string   `json:"device,omitempty"`
	Workload      string   `json:"workload,omitempty"`
	TestedContext int      `json:"tested_context,omitempty"`
	Uncertainty   []string `json:"uncertainty,omitempty"`
}

// RecommendationInput is the evidence a recommendation may use. Conflicts are
// stated and left in place. The requested context is not rewritten downward,
// and paid compute is not selected here.
type RecommendationInput struct {
	Role             string
	Required         []string
	Scope            RecommendationScope
	Observations     []EvidenceObservation
	ProjectionTokens int
	MeasuredTokens   int
	RequestedContext int
	Conflicts        []string
}

// Recommendation is the machine-readable receipt. Lines is the same text the
// CLI prints. Qualified is false while a required class failed, evidence is
// missing, or a constraint is in conflict.
type Recommendation struct {
	Schema            string              `json:"schema"`
	Qualified         bool                `json:"qualified"`
	Outcome           string              `json:"outcome"`
	Role              string              `json:"role,omitempty"`
	Scope             RecommendationScope `json:"scope"`
	Passed            []string            `json:"passed,omitempty"`
	Failed            []string            `json:"failed,omitempty"`
	Unresolved        []string            `json:"unresolved,omitempty"`
	RequestedContext  int                 `json:"requested_context,omitempty"`
	LongContextTested bool                `json:"long_context_tested"`
	PaidSubstitute    bool                `json:"paid_substitute"`
	Lines             []string            `json:"lines"`
}

// Recommendation derives the current receipt from the sealed plan. It does
// not change the plan bytes. Missing checks stay unresolved.
func (session Session) Recommendation() Recommendation {
	plan := session.Plan
	workload := plan.RoleName
	if plan.Workload != nil && plan.Workload.Preset != "" {
		workload = plan.Workload.Preset
	}
	// A phase or saved point identifies work, not a verified runtime window.
	// Until canonical observations are projected here, measurement identity
	// remains absent even for a delegated or finished session.
	tested := 0
	uncertainty := []string{"a changed configuration invalidates this conclusion"}
	if plan.Workload == nil || !plan.Workload.KVServerVerified {
		uncertainty = append(uncertainty, "unmeasured cache type")
	}
	uncertainty = append(uncertainty, "skipped executable tests")
	if plan.Repeats > 0 && plan.Repeats < 3 {
		uncertainty = append(uncertainty, "small sample")
	}
	if plan.Workload == nil || !plan.Workload.PeakKnown {
		uncertainty = append(uncertainty, "unknown runtime overhead")
	}
	var conflicts []string
	if plan.Workload != nil && plan.Workload.DesiredContextTokens > 0 && plan.Workload.DesiredContextTokens > plan.ContextTokens {
		conflicts = append(conflicts, desiredUnmetReason(plan.Workload.DesiredContextTokens, plan.ContextTokens))
	}
	return AssessRecommendation(RecommendationInput{
		Role: plan.RoleName, Required: []string{ClassLoad, ClassSimplePrompt, ClassToolWorkflow, ClassLongContext, ClassHarness},
		Scope: RecommendationScope{
			Artifact: strings.Join(plan.Candidates, ", "), Runtime: plan.Endpoint,
			Configuration: fmt.Sprintf("context %d, kv %s, %s %s", plan.ContextTokens, plan.KVCacheType, plan.CapacityKind, FormatGiB(plan.CapacityBytes)),
			Workload:      workload, TestedContext: tested, Uncertainty: uncertainty,
		},
		RequestedContext: plan.ContextTokens, MeasuredTokens: tested, Conflicts: conflicts,
	})
}

// AssessRecommendation keeps each evidence class visible. A comfortable load
// and a simple prompt do not qualify a failed tool or coding workflow. A
// weights-plus-KV projection does not become tested long-context quality.
func AssessRecommendation(in RecommendationInput) Recommendation {
	rec := Recommendation{
		Schema: RecommendationSchema, Role: in.Role, Scope: in.Scope,
		RequestedContext: in.RequestedContext,
	}
	byClass := conservativeObservations(in.Observations)
	var passed, failed, unresolved []string
	for _, class := range []string{ClassMemoryProjection, ClassLoad, ClassSimplePrompt, ClassPerformance, ClassBehavior, ClassToolWorkflow, ClassLongContext, ClassHarness} {
		obs, ok := byClass[class]
		if !ok {
			if sliceHas(in.Required, class) {
				unresolved = append(unresolved, class)
			}
			continue
		}
		switch obs.State {
		case StatePass:
			if class == ClassLongContext {
				rec.LongContextTested = true
			}
			passed = append(passed, class)
		case StateFail:
			failed = append(failed, class)
		default:
			unresolved = append(unresolved, class)
		}
	}
	if !rec.LongContextTested {
		passed = withoutClass(passed, ClassLongContext)
	}
	unresolved = appendUncertainty(unresolved, in.Scope.Uncertainty)
	rec.Passed, rec.Failed, rec.Unresolved = passed, failed, unresolved
	missingRequired := false
	for _, class := range in.Required {
		if !sliceHas(passed, class) {
			missingRequired = true
		}
	}
	switch {
	case len(failed) > 0:
		rec.Outcome = "no-qualified-candidate"
	case len(in.Observations) == 0 || missingRequired || len(in.Conflicts) > 0 || len(in.Required) == 0:
		rec.Outcome = "unresolved"
	default:
		rec.Outcome = "supported"
		rec.Qualified = true
	}
	if len(in.Conflicts) > 0 || len(unresolved) > 0 {
		rec.Qualified = false
		if rec.Outcome == "supported" {
			rec.Outcome = "unresolved"
		}
	}
	rec.Lines = recommendationLines(rec, in)
	return rec
}

func appendUncertainty(unresolved, uncertainty []string) []string {
	for _, item := range uncertainty {
		if item != "" && !sliceHas(unresolved, item) {
			unresolved = append(unresolved, item)
		}
	}
	return unresolved
}

func conservativeObservations(observations []EvidenceObservation) map[string]EvidenceObservation {
	byClass := map[string]EvidenceObservation{}
	for _, obs := range observations {
		prev, ok := byClass[obs.Class]
		if !ok || evidenceRank(obs.State) > evidenceRank(prev.State) {
			byClass[obs.Class] = obs
		}
	}
	return byClass
}

func evidenceRank(state string) int {
	switch state {
	case StateFail:
		return 3
	case StatePass:
		return 1
	default:
		return 2
	}
}

func withoutClass(values []string, class string) []string {
	var kept []string
	for _, value := range values {
		if value != class {
			kept = append(kept, value)
		}
	}
	return kept
}

func recommendationLines(rec Recommendation, in RecommendationInput) []string {
	tested := "unmeasured"
	if rec.Scope.TestedContext > 0 {
		tested = strconv.Itoa(rec.Scope.TestedContext)
	}
	lines := []string{
		"recommendation " + rec.Schema + " outcome " + rec.Outcome,
		fmt.Sprintf("scope artifact %s; runtime %s; configuration %s; device %s; workload %s; tested context %s",
			scopeText(rec.Scope.Artifact), scopeText(rec.Scope.Runtime), scopeText(rec.Scope.Configuration),
			scopeText(rec.Scope.Device), scopeText(rec.Scope.Workload), tested),
		"projected memory fit, successful loading, measured performance, behavioral reliability, and end-to-end harness qualification are separate. None establishes the next.",
	}
	if len(rec.Passed) > 0 {
		lines = append(lines, "passed: "+strings.Join(rec.Passed, ", "))
	}
	if len(rec.Failed) > 0 {
		lines = append(lines, "failed: "+strings.Join(rec.Failed, ", "))
	}
	for _, item := range rec.Unresolved {
		lines = append(lines, "unresolved: "+item)
	}
	if sliceHas(rec.Failed, ClassToolWorkflow) {
		role := rec.Role
		if role == "" {
			role = "the requested role"
		}
		lines = append(lines, "role "+role+" remains unsupported")
	}
	lines = append(lines, "a memory projection is not tested long-context quality")
	if sentence := UntestedContextProjection(in.ProjectionTokens, in.MeasuredTokens); sentence != "" {
		lines = append(lines, sentence)
	}
	for _, conflict := range in.Conflicts {
		if conflict != "" {
			lines = append(lines, "tradeoff: "+conflict)
		}
	}
	if len(in.Conflicts) > 0 || rec.Outcome == "no-qualified-candidate" {
		lines = append(lines, "The requirement was not lowered. Cloud is not an automatic substitute. A quality requirement was not relaxed.")
	}
	switch rec.Outcome {
	case "no-qualified-candidate":
		lines = append(lines, "no qualified candidate. This is a result, not a failed product experience.")
	case "unresolved":
		lines = append(lines, "we do not have enough evidence")
	}
	return lines
}

func scopeText(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unspecified"
	}
	return value
}

// Completed reports whether a candidate already has a point. Resume uses it
// so a finished point is not measured again under the same plan.
func (session Session) Completed(model string) bool {
	for _, point := range session.Points {
		if point.Model == model && point.RunID != "" && point.EvidenceSHA256 != "" {
			return true
		}
	}
	return false
}
