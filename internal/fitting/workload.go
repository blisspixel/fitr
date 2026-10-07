package fitting

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	ContextTotalWindow = "total-window"
	ContextUsableInput = "usable-input"

	ClassMandatory      = "mandatory"
	ClassRecommendation = "recommendation"
	ClassPreference     = "preference"

	// HermesOllamaMinContext is the floor Hermes Agent documents for Ollama
	// agentic work. Checked 2026-10-06 against
	// https://hermes-agent.nousresearch.com/docs/guides/local-ollama-setup
	// ("Hermes requires at least 64,000 tokens for agentic work with tools").
	// Meeting the floor does not establish a larger desired workflow.
	HermesOllamaMinContext = 64000

	// Qwen3.6-27B card figures, checked 2026-10-06 against
	// https://huggingface.co/Qwen/Qwen3.6-27B. They are model-card statements,
	// not a measurement of any quantized local build.
	Qwen36NativeContext    = 262144
	Qwen36ExtensionContext = 1010000
	Qwen36ThinkingContext  = 131072
)

// Requirement is one visible workload rule. A preset name is not a substitute
// for the rows it expands into.
type Requirement struct {
	Class  string `json:"class"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

// ModelCardNote records someone else's published claim. Kind is capability
// or recommendation. Neither kind is local evidence.
type ModelCardNote struct {
	Model   string `json:"model"`
	Source  string `json:"source"`
	Kind    string `json:"kind"`
	Context int    `json:"context_tokens,omitempty"`
	Claim   string `json:"claim"`
}

// SearchOption is one predeclared configuration. The review does not invent
// rows and does not replace the requested window.
type SearchOption struct {
	Candidate     string `json:"candidate,omitempty"`
	ContextTokens int    `json:"context_tokens"`
	KVCacheType   string `json:"kv_cache_type,omitempty"`
	WeightQuant   string `json:"weight_quant,omitempty"`
}

// SearchItem keeps a rejected row and the reason. Eligible does not mean adopted.
type SearchItem struct {
	Option   SearchOption `json:"option"`
	Eligible bool         `json:"eligible"`
	Reason   string       `json:"reason"`
}

// Workload is the goal sealed before settings are treated as an answer.
// Weight quantization and KV-cache precision are different fields.
type Workload struct {
	Preset                    string          `json:"preset,omitempty"`
	Requirements              []Requirement   `json:"requirements,omitempty"`
	DesiredContextTokens      int             `json:"desired_context_tokens,omitempty"`
	MinimumContextTokens      int             `json:"minimum_context_tokens,omitempty"`
	ContextMeaning            string          `json:"context_meaning,omitempty"`
	Harness                   string          `json:"harness,omitempty"`
	HarnessVersion            string          `json:"harness_version,omitempty"`
	HarnessProtocol           string          `json:"harness_protocol,omitempty"`
	HarnessMinContextTokens   int             `json:"harness_min_context_tokens,omitempty"`
	HarnessFloorSource        string          `json:"harness_floor_source,omitempty"`
	ReservedSystemTokens      int             `json:"reserved_system_tokens,omitempty"`
	ReservedOutputTokens      int             `json:"reserved_output_tokens,omitempty"`
	ReservedReasoningTokens   int             `json:"reserved_reasoning_tokens,omitempty"`
	WeightQuant               string          `json:"weight_quant,omitempty"`
	WeightPreference          string          `json:"weight_preference,omitempty"`
	WeightMinimum             string          `json:"weight_minimum,omitempty"`
	KVPermitted               []string        `json:"kv_permitted,omitempty"`
	ModelCards                []ModelCardNote `json:"model_cards,omitempty"`
	RequirePopulatedContext   bool            `json:"require_populated_context,omitempty"`
	PopulatedContextQualified bool            `json:"populated_context_qualified"`
	KVServerVerified          bool            `json:"kv_server_verified"`
	KVVerification            string          `json:"kv_verification,omitempty"`
	CloudApproved             bool            `json:"cloud_approved"`
	Concurrency               int             `json:"concurrency,omitempty"`
	ProjectedResidentBytes    int64           `json:"projected_resident_bytes,omitempty"`
	OccupiedTokens            int             `json:"occupied_tokens,omitempty"`
	ObservedResidentBytes     int64           `json:"observed_resident_bytes,omitempty"`
	MeasuredPeakBytes         int64           `json:"measured_peak_bytes,omitempty"`
	PeakKnown                 bool            `json:"peak_known"`
	AvailabilityAt            string          `json:"availability_at,omitempty"`
	AvailabilityBytes         int64           `json:"availability_bytes,omitempty"`
	Search                    []SearchItem    `json:"search,omitempty"`
	PermittedDownloads        bool            `json:"permitted_downloads"`
	PermittedSpending         bool            `json:"permitted_spending"`
	PermittedRuntimeChange    bool            `json:"permitted_runtime_change"`
	OperatorHarnessFloor      int             `json:"operator_harness_floor,omitempty"`
}

// CandidateObservation is the behavior a role decision may see.
// Decode speed never repairs a failed mandatory behavior.
type CandidateObservation struct {
	Model           string
	DecodePerSecond float64
	FailedMandatory []string
}

// Allowance is the approved policy plus the remainder availability can shrink.
// ApprovedBytes is the planning choice. It is not rewritten upward.
type Allowance struct {
	Kind           CapacityKind
	ApprovedBytes  int64
	ObservedFree   int64
	RemainderBytes int64
}

func (w *Workload) validate() error {
	if w == nil {
		return nil
	}
	if w.PopulatedContextQualified {
		return errors.New("populated-context qualification is not established by this schedule")
	}
	if w.CloudApproved || w.PermittedDownloads || w.PermittedSpending || w.PermittedRuntimeChange {
		return errors.New("cloud, download, spending, and runtime replacement are not approved by this fitting")
	}
	if w.KVServerVerified && !strings.HasPrefix(w.KVVerification, "server reported ") {
		return errors.New("KV cache verification requires a server report")
	}
	if w.PeakKnown && w.MeasuredPeakBytes <= 0 {
		return errors.New("a known peak needs a measured byte count")
	}
	return nil
}

func validateWorkloadInputs(req Request) error {
	w := req.Workload
	if w.Preset != "" && !knownPreset(w.Preset) {
		return fmt.Errorf("unknown workload preset %q", w.Preset)
	}
	if w.DesiredContextTokens < 0 || w.MinimumContextTokens < 0 || w.ReservedSystemTokens < 0 || w.ReservedOutputTokens < 0 || w.ReservedReasoningTokens < 0 || w.Concurrency < 0 {
		return errors.New("workload counts cannot be negative")
	}
	goal := w.DesiredContextTokens > 0 || w.MinimumContextTokens > 0 || w.ReservedSystemTokens > 0 || w.ReservedOutputTokens > 0 || w.ReservedReasoningTokens > 0
	if goal && w.ContextMeaning != ContextTotalWindow && w.ContextMeaning != ContextUsableInput {
		return errors.New("context meaning is required when a context goal is set: total-window or usable-input")
	}
	if w.ContextMeaning != "" && w.ContextMeaning != ContextTotalWindow && w.ContextMeaning != ContextUsableInput {
		return errors.New("context meaning must be total-window or usable-input")
	}
	if w.DesiredContextTokens > 1<<20 || w.MinimumContextTokens > 1<<20 {
		return errors.New("context goal is above 1048576 tokens")
	}
	if err := validateContextReserves(w); err != nil {
		return err
	}
	for _, ctx := range req.AlternativeContexts {
		if ctx < 512 || ctx > 1<<20 {
			return errors.New("each predeclared alternative context must be 512 to 1048576 tokens")
		}
	}
	if w.Harness != "" && !candidateOK(w.Harness) {
		return errors.New("harness name is invalid")
	}
	for _, kv := range w.KVPermitted {
		switch kv {
		case "f16", "q8_0", "q4_0":
		default:
			return errors.New("permitted KV cache must be f16, q8_0, or q4_0")
		}
	}
	return nil
}

func validateContextReserves(w Workload) error {
	if w.ReservedSystemTokens > 1<<20 || w.ReservedOutputTokens > 1<<20 || w.ReservedReasoningTokens > 1<<20 || w.HarnessMinContextTokens < 0 || w.HarnessMinContextTokens > 1<<20 {
		return errors.New("context reserves and harness floor must be from 0 to 1048576 tokens")
	}
	return nil
}

func workloadRequested(req Request) bool {
	w := req.Workload
	if w.Preset != "" || w.DesiredContextTokens > 0 || w.MinimumContextTokens > 0 || w.ContextMeaning != "" || w.Harness != "" || w.HarnessVersion != "" {
		return true
	}
	if w.ReservedSystemTokens > 0 || w.ReservedOutputTokens > 0 || w.ReservedReasoningTokens > 0 || w.WeightQuant != "" || w.WeightPreference != "" || w.WeightMinimum != "" {
		return true
	}
	if len(w.KVPermitted) > 0 || w.RequirePopulatedContext || w.Concurrency > 0 || w.ProjectedResidentBytes > 0 || len(w.Requirements) > 0 {
		return true
	}
	if req.ModelCard != "" || len(req.AlternativeContexts) > 0 || req.KVClientEnv != "" || req.KVServerReport != "" {
		return true
	}
	return req.KVExplicit || req.KVCacheType == "q8_0" || req.KVCacheType == "q4_0"
}

func expandPresetOutcomes(req Request) ([]string, error) {
	extra, err := presetOutcomes(req.Workload.Preset)
	if err != nil {
		return nil, err
	}
	if req.Workload.Preset == "" {
		return append([]string(nil), req.Outcomes...), nil
	}
	seen := map[string]bool{}
	var out []string
	for _, need := range append(append([]string{}, req.Outcomes...), extra...) {
		if seen[need] {
			continue
		}
		seen[need] = true
		out = append(out, need)
	}
	if len(out) == 0 || len(out) > 8 {
		return nil, errors.New("preset expansion needs one to eight outcomes")
	}
	return out, nil
}

func normalizeWorkload(req Request, kv string) (*Workload, error) {
	if !workloadRequested(req) {
		return nil, nil
	}
	w := req.Workload
	w.PopulatedContextQualified = false
	w.CloudApproved = false
	w.PermittedDownloads = false
	w.PermittedSpending = false
	w.PermittedRuntimeChange = false
	w.PeakKnown = false
	w.MeasuredPeakBytes = 0
	if spec, ok := presetByName(w.Preset); ok && spec.populated {
		w.RequirePopulatedContext = true
	}
	applyHarness(&w)
	w.Requirements = append(presetRequirements(w.Preset, req.Scope), w.Requirements...)
	w.ModelCards = modelCardsFor(req.Candidates, req.ModelCard)
	noteQuantPreference(&w)
	verified, text := KVVerification(req.KVClientEnv, req.KVServerReport, kv)
	w.KVServerVerified = verified
	w.KVVerification = text
	if len(req.AlternativeContexts) > 0 {
		options := make([]SearchOption, len(req.AlternativeContexts))
		for i, ctx := range req.AlternativeContexts {
			options[i] = SearchOption{ContextTokens: ctx, KVCacheType: kv, WeightQuant: w.WeightQuant}
		}
		w.Search = ReviewSearch(w, options)
	}
	return &w, nil
}

func applyHarness(w *Workload) {
	if !strings.EqualFold(w.Harness, "hermes") {
		if w.HarnessMinContextTokens > 0 && w.HarnessFloorSource == "" {
			w.HarnessFloorSource = "operator stated harness floor"
		}
		return
	}
	w.Harness = "hermes"
	w.OperatorHarnessFloor = w.HarnessMinContextTokens
	w.HarnessProtocol = "Ollama num_ctx has to meet the harness floor. A client request does not prove the server window."
	w.HarnessFloorSource = "Hermes Agent Ollama guide, checked 2026-10-06: at least 64000 tokens for agentic work with tools"
	if w.HarnessMinContextTokens > HermesOllamaMinContext {
		w.HarnessFloorSource += ". The operator stated a higher floor, which this plan keeps."
		return
	}
	if w.HarnessMinContextTokens > 0 && w.HarnessMinContextTokens < HermesOllamaMinContext {
		w.Requirements = append(w.Requirements, Requirement{
			Class: ClassMandatory, Name: "harness-floor",
			Detail: fmt.Sprintf("the operator stated %d tokens. The documented Hermes Ollama floor is %d. The floor was not lowered.", w.HarnessMinContextTokens, HermesOllamaMinContext),
		})
	}
	w.HarnessMinContextTokens = HermesOllamaMinContext
}

func noteQuantPreference(w *Workload) {
	if w.WeightPreference == "" {
		return
	}
	detail := "This is a preference, not a universal quality threshold. Nominal bit width is not a ranking: mixed-tensor quantization, provenance, and calibration are separate, and a smaller higher-precision model is not automatically superior."
	if w.WeightQuant == "" {
		detail = "The declared weight quantization is unknown, so the preference cannot be checked. " + detail
	} else if !meetsMinimum(w.WeightQuant, w.WeightPreference) {
		detail = "The declared label is below this preference. A different artifact was not substituted. " + detail
	}
	w.Requirements = append(w.Requirements, Requirement{Class: ClassPreference, Name: "weight-quantization", Detail: detail})
}

// SatisfiesContextGoal reports whether the measured window meets the desired
// token count. A smaller successful run does not.
func SatisfiesContextGoal(measured, desired int) bool {
	if desired <= 0 {
		return true
	}
	return measured >= desired
}

// EligibleContext refuses a recommendation below a harness floor.
// 59392 is not eligible for a 64000 floor.
func EligibleContext(tokens, harnessMin int) (bool, string) {
	if harnessMin <= 0 {
		return true, ""
	}
	if tokens < harnessMin {
		return false, fmt.Sprintf("%d tokens is below the harness minimum of %d and is not an eligible recommendation", tokens, harnessMin)
	}
	return true, ""
}

// KVVerification refuses to treat a client environment variable as proof that
// the serving runtime applied the cache type. An empty server report stays
// unverified even when the client value matches the request.
func KVVerification(clientEnv, serverReported, requested string) (bool, string) {
	if strings.TrimSpace(serverReported) == "" {
		return false, unverifiedKVText()
	}
	if requested != "" && !strings.EqualFold(serverReported, requested) {
		return false, "server reported " + serverReported + ", which does not match the requested KV cache type. The client environment is not authority."
	}
	if clientEnv != "" && !strings.EqualFold(clientEnv, serverReported) {
		return false, "server reported " + serverReported + ", and the client environment disagrees. The client environment is not authority."
	}
	return true, "server reported " + serverReported
}

func unverifiedKVText() string {
	return "unverified. A client environment variable does not prove the serving runtime applied the KV cache type. Quantized KV requires flash attention as a server setting."
}

// PopulatedQualification is false for this schedule. A large configured window
// with a short prompt is not token-accounted long-context evidence.
func PopulatedQualification(window, occupied int) (bool, string) {
	if window > 0 && (occupied <= 0 || occupied < window) {
		return false, fmt.Sprintf("a configured window of %d with %d occupied tokens is not populated-context qualification", window, occupied)
	}
	return false, "a configured window with the short battery prompt is not populated-context qualification"
}

// RecommendContext labels a weights-plus-KV hint. A hint below a harness floor
// is not eligible, and a hint below the desired context does not satisfy it.
func RecommendContext(suggested, measured, harnessMin, desired int) string {
	var parts []string
	if sentence := UntestedContextProjection(suggested, measured); sentence != "" {
		parts = append(parts, sentence)
	}
	if ok, why := EligibleContext(suggested, harnessMin); !ok {
		parts = append(parts, why)
	}
	if desired > 0 && !SatisfiesContextGoal(measured, desired) {
		parts = append(parts, desiredUnmetReason(desired, measured))
	}
	return strings.Join(parts, " ")
}

func desiredUnmetReason(desired, measured int) string {
	return fmt.Sprintf("desired context %d is not met by measuring %d. This plan does not satisfy that workload. The requirement was not lowered. Pass --ctx %d to measure the desired window, or name a smaller workload with --desired-context. A different model, cache configuration, hardware, or harness is a separate plan. Cloud is not an automatic substitute.", desired, measured, desired)
}

// ReviewSearch judges a finite predeclared list. It does not walk context
// downward and it does not choose a replacement window.
func ReviewSearch(workload Workload, options []SearchOption) []SearchItem {
	items := make([]SearchItem, 0, len(options))
	for _, option := range options {
		items = append(items, reviewOption(workload, option))
	}
	return items
}

func reviewOption(workload Workload, option SearchOption) SearchItem {
	item := SearchItem{Option: option}
	var reasons []string
	if option.ContextTokens <= 0 {
		reasons = append(reasons, "a predeclared context is required")
	}
	if ok, why := EligibleContext(option.ContextTokens, workload.HarnessMinContextTokens); !ok {
		reasons = append(reasons, why)
	}
	if workload.MinimumContextTokens > 0 && option.ContextTokens < workload.MinimumContextTokens {
		reasons = append(reasons, fmt.Sprintf("minimum acceptable context %d is above %d", workload.MinimumContextTokens, option.ContextTokens))
	}
	if workload.DesiredContextTokens > 0 && !SatisfiesContextGoal(option.ContextTokens, workload.DesiredContextTokens) {
		reasons = append(reasons, desiredUnmetReason(workload.DesiredContextTokens, option.ContextTokens))
	}
	quant := option.WeightQuant
	if quant == "" {
		quant = workload.WeightQuant
	}
	if workload.WeightMinimum != "" && !meetsMinimum(quant, workload.WeightMinimum) {
		reasons = append(reasons, "no eligible configuration: weight quantization does not meet the minimum")
	}
	kv := option.KVCacheType
	if kv == "" {
		kv = "f16"
	}
	if len(workload.KVPermitted) > 0 && !sliceHas(workload.KVPermitted, kv) {
		reasons = append(reasons, "KV cache "+kv+" is not permitted")
	}
	if len(reasons) == 0 {
		item.Eligible = true
		item.Reason = "meets the predeclared hard requirements. It does not replace the requested window."
		return item
	}
	item.Reason = strings.Join(reasons, " ")
	return item
}

// AdoptedContext returns the requested window. A search review cannot replace it.
func AdoptedContext(requested int, _ []SearchItem) int { return requested }

// SelectEligible drops any candidate that failed a mandatory behavior.
// The survivor is not chosen because it was faster.
func SelectEligible(obs []CandidateObservation) (string, bool, string) {
	var eligible []string
	for _, item := range obs {
		if len(item.FailedMandatory) > 0 {
			continue
		}
		eligible = append(eligible, item.Model)
	}
	switch len(eligible) {
	case 0:
		return "", false, "no eligible configuration. A faster decode does not repair a failed required behavior."
	case 1:
		return eligible[0], true, "the only candidate that met the mandatory behavior. Decode speed was not the decider."
	default:
		return "", false, "more than one candidate met the mandatory behavior. Exploration does not certify its own winner, and decode speed is not the decider."
	}
}

// ResolveAllowance keeps the approved ceiling or reserve. Observed free memory
// may shrink the remainder. It cannot raise the approved allowance.
func ResolveAllowance(kind CapacityKind, approved, observedFree int64) (Allowance, error) {
	if approved < 0 || observedFree < 0 {
		return Allowance{}, errors.New("capacity bytes cannot be negative")
	}
	out := Allowance{Kind: kind, ApprovedBytes: approved, ObservedFree: observedFree}
	switch kind {
	case CapacityCeiling:
		out.RemainderBytes = approved
		if observedFree < approved {
			out.RemainderBytes = observedFree
		}
	case CapacityReserve:
		out.RemainderBytes = observedFree - approved
		if out.RemainderBytes < 0 {
			out.RemainderBytes = 0
		}
	default:
		return Allowance{}, errors.New("capacity policy must be an absolute ceiling or a reserve")
	}
	if out.ApprovedBytes != approved {
		return Allowance{}, errors.New("approved allowance changed")
	}
	return out, nil
}

func workloadBlock(plan Plan) string {
	if plan.Workload == nil {
		return ""
	}
	return joinNonEmpty([]string{contextBlock(plan), quantBlock(plan), evidenceBlock(plan)})
}

func contextBlock(plan Plan) string {
	w := plan.Workload
	var parts []string
	if ok, why := EligibleContext(plan.ContextTokens, w.HarnessMinContextTokens); !ok {
		parts = append(parts, why)
	}
	if w.MinimumContextTokens > plan.ContextTokens {
		parts = append(parts, fmt.Sprintf("minimum acceptable context %d is above the configured window %d", w.MinimumContextTokens, plan.ContextTokens))
	}
	if why := windowOverflow(plan); why != "" {
		parts = append(parts, why)
	}
	if plan.Scope == ScopeQualify && w.DesiredContextTokens > 0 && !SatisfiesContextGoal(plan.ContextTokens, w.DesiredContextTokens) {
		parts = append(parts, desiredUnmetReason(w.DesiredContextTokens, plan.ContextTokens))
		if w.HarnessMinContextTokens > 0 && plan.ContextTokens >= w.HarnessMinContextTokens {
			parts = append(parts, "Meeting the harness floor does not establish the desired context.")
		}
	}
	return joinNonEmpty(parts)
}

func windowOverflow(plan Plan) string {
	w := plan.Workload
	sum := w.ReservedSystemTokens + w.ReservedOutputTokens + w.ReservedReasoningTokens
	if w.ContextMeaning == ContextUsableInput && w.DesiredContextTokens > 0 && w.DesiredContextTokens+sum > plan.ContextTokens {
		return fmt.Sprintf("desired input plus reserves exceed the effective window of %d", plan.ContextTokens)
	}
	if sum > 0 && sum >= plan.ContextTokens {
		return fmt.Sprintf("reserved system, output, and reasoning tokens consume the configured window of %d", plan.ContextTokens)
	}
	return ""
}

func quantBlock(plan Plan) string {
	w := plan.Workload
	if w.WeightMinimum != "" && !meetsMinimum(w.WeightQuant, w.WeightMinimum) {
		if w.WeightQuant == "" {
			return "no eligible configuration: weight quantization is unknown, so the minimum cannot be established. A lower-bit artifact was not selected."
		}
		return fmt.Sprintf("no eligible configuration: declared weight quantization %s does not meet the minimum %s. A lower-bit artifact was not selected.", w.WeightQuant, w.WeightMinimum)
	}
	if len(w.KVPermitted) > 0 && !sliceHas(w.KVPermitted, plan.KVCacheType) {
		return fmt.Sprintf("KV cache %s is not in the permitted set. The setting was not replaced.", plan.KVCacheType)
	}
	if w.ProjectedResidentBytes > 0 && w.ProjectedResidentBytes > plan.ResidentLimitBytes {
		return fmt.Sprintf("projected resident %s exceeds the approved %s policy of %s. The policy was not enlarged.", FormatGiB(w.ProjectedResidentBytes), plan.CapacityKind, FormatGiB(plan.ResidentLimitBytes))
	}
	return ""
}

func evidenceBlock(plan Plan) string {
	if plan.Scope != ScopeQualify || plan.Workload == nil {
		return ""
	}
	var parts []string
	if plan.Workload.RequirePopulatedContext {
		_, why := PopulatedQualification(plan.ContextTokens, plan.Workload.OccupiedTokens)
		parts = append(parts, "populated token-accounted context cannot be established by this schedule. "+why+" Pass --scope screen to collect screening without calling the workload qualified.")
	}
	if plan.Workload.Preset == "agentic-coding" {
		parts = append(parts, "Tool-call syntax does not establish autonomous competence, and this schedule cannot execute isolated acceptance checks.")
	}
	return joinNonEmpty(parts)
}

func (plan Plan) uncertaintyLines() []string {
	lines := []string{
		"kv verification: " + plan.kvVerificationText(),
		populatedLine(plan),
		"measured peak is unknown. A resident margin is not an in-flight peak, a second request, or another application.",
		"no configuration search walked context downward. A smaller passing window is not substituted for the requested one.",
		fmt.Sprintf("declared %s %s stays the approved allowance. Availability may shrink the remainder and cannot enlarge it.", plan.CapacityKind, FormatGiB(plan.CapacityBytes)),
	}
	if sentence := kvMemoryLine(plan.KVCacheType); sentence != "" {
		lines = append(lines, sentence)
	}
	if plan.Workload == nil {
		lines = append(lines, "workload profile was not requested. The configured window is the only context this plan names.")
		return lines
	}
	lines = append(lines, plan.profileLines()...)
	lines = append(lines, plan.layerLines()...)
	lines = append(lines, plan.searchLines()...)
	return lines
}

func (plan Plan) kvVerificationText() string {
	if plan.Workload != nil && plan.Workload.KVVerification != "" {
		return plan.Workload.KVVerification
	}
	return unverifiedKVText()
}

func populatedLine(plan Plan) string {
	occupied := 0
	if plan.Workload != nil {
		occupied = plan.Workload.OccupiedTokens
	}
	_, text := PopulatedQualification(plan.ContextTokens, occupied)
	return text
}

func kvMemoryLine(kv string) string {
	switch kv {
	case "q8_0":
		return "q8_0 reduces KV cache memory relative to f16. It does not halve total model and runtime memory."
	case "q4_0":
		return "q4_0 reduces KV cache memory relative to f16. It does not quarter total model and runtime memory."
	default:
		return ""
	}
}

func (plan Plan) profileLines() []string {
	w := plan.Workload
	var lines []string
	if w.Preset != "" {
		lines = append(lines, "workload preset "+w.Preset+". The rows below are the requirements. The name is not the requirement.")
	}
	for _, item := range w.Requirements {
		lines = append(lines, item.Class+": "+item.Name+": "+item.Detail)
	}
	if w.WeightQuant != "" || plan.KVCacheType != "" {
		weight := w.WeightQuant
		if weight == "" {
			weight = "not declared"
		}
		lines = append(lines, "weight quantization "+weight+" is model weights. KV cache "+plan.KVCacheType+" is cache precision. They are separate.")
	}
	if w.Harness != "" {
		lines = append(lines, fmt.Sprintf("harness %s minimum %d. %s. Passing this floor does not establish a larger desired context.", w.Harness, w.HarnessMinContextTokens, w.HarnessFloorSource))
	}
	if w.DesiredContextTokens > 0 && !SatisfiesContextGoal(plan.ContextTokens, w.DesiredContextTokens) {
		lines = append(lines, "workload result: desired context unmet", desiredUnmetReason(w.DesiredContextTokens, plan.ContextTokens))
		if w.HarnessMinContextTokens > 0 && plan.ContextTokens >= w.HarnessMinContextTokens {
			lines = append(lines, "Meeting the harness floor does not establish the desired context.")
		}
	}
	for _, card := range w.ModelCards {
		lines = append(lines, "model-card "+card.Kind+": "+card.Claim)
	}
	return lines
}

func (plan Plan) layerLines() []string {
	w := plan.Workload
	advertised := "unknown"
	if len(w.ModelCards) > 0 && w.ModelCards[0].Context > 0 {
		advertised = fmt.Sprintf("%d from the model card (%s), not a local measurement", w.ModelCards[0].Context, w.ModelCards[0].Source)
	}
	meaning := w.ContextMeaning
	if meaning == "" {
		meaning = "not declared"
	}
	desired := "not declared"
	if w.DesiredContextTokens > 0 {
		desired = strconv.Itoa(w.DesiredContextTokens)
	}
	minimum := "not declared"
	if w.MinimumContextTokens > 0 {
		minimum = strconv.Itoa(w.MinimumContextTokens)
	}
	occupied := "unmeasured"
	if w.OccupiedTokens > 0 {
		occupied = strconv.Itoa(w.OccupiedTokens)
	}
	resident := "unknown"
	if w.ObservedResidentBytes > 0 {
		resident = FormatGiB(w.ObservedResidentBytes)
	}
	projected := "not supplied"
	if w.ProjectedResidentBytes > 0 {
		projected = FormatGiB(w.ProjectedResidentBytes) + " projected resident, not a demonstrated fit"
	}
	availability := "not timestamped"
	if w.AvailabilityAt != "" || w.AvailabilityBytes > 0 {
		availability = fmt.Sprintf("%s %s", w.AvailabilityAt, FormatGiB(w.AvailabilityBytes))
	}
	runtime := "unknown; verification method: not read from the server"
	return []string{
		"advertised model context: " + advertised,
		fmt.Sprintf("requested context: configured %d; desired %s; minimum %s; meaning %s", plan.ContextTokens, desired, minimum, meaning),
		"runtime-reported context: " + runtime,
		fmt.Sprintf("input occupancy: %s; reserved system %d; output %d; reasoning %d", occupied, w.ReservedSystemTokens, w.ReservedOutputTokens, w.ReservedReasoningTokens),
		"demonstrated task performance: not established at a populated occupancy",
		"projected components: " + projected,
		"observed resident allocation: " + resident,
		"availability: " + availability,
	}
}

func (plan Plan) searchLines() []string {
	w := plan.Workload
	kept := AdoptedContext(plan.ContextTokens, w.Search)
	lines := []string{fmt.Sprintf("search kept the requested window %d. Predeclared rows were not adopted.", kept)}
	if len(w.Search) == 0 {
		return lines
	}
	for _, item := range w.Search {
		state := "rejected"
		if item.Eligible {
			state = "eligible, not adopted"
		}
		lines = append(lines, fmt.Sprintf("search %s %d: %s", state, item.Option.ContextTokens, item.Reason))
	}
	return lines
}

func joinNonEmpty(parts []string) string {
	var kept []string
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, " ")
}

func sliceHas(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// nominalBits reads a single published label. It is not a quality ranking.
func nominalBits(label string) (int, bool) {
	switch strings.ToUpper(strings.TrimSpace(label)) {
	case "F32", "FP32":
		return 32, true
	case "F16", "FP16", "BF16":
		return 16, true
	case "Q8_0", "Q8":
		return 8, true
	case "Q6_K", "Q6":
		return 6, true
	case "Q5_K_M", "Q5_K_S", "Q5_0", "Q5":
		return 5, true
	case "Q4_K_M", "Q4_K_S", "Q4_0", "Q4":
		return 4, true
	case "Q3_K_M", "Q3_K_S", "Q3":
		return 3, true
	case "Q2_K", "Q2":
		return 2, true
	default:
		return 0, false
	}
}

func policyBits(label string) (int, bool) {
	switch strings.ToUpper(strings.TrimSpace(label)) {
	case "Q4-OR-BETTER":
		return 4, true
	case "Q5-OR-BETTER":
		return 5, true
	case "Q8-OR-BETTER":
		return 8, true
	default:
		return nominalBits(label)
	}
}

func meetsMinimum(declared, minimum string) bool {
	need, ok := policyBits(minimum)
	got, declaredOK := nominalBits(declared)
	if !ok || !declaredOK {
		return false
	}
	return got >= need
}
