// Package fitting records one guided fitting. It does not run inference,
// download weights, stop a serving process, or invent a second measurement engine.
// A sealed plan is the only settings object advice, collection, and adoption may share.
package fitting

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/blisspixel/fitr/internal/advise"
	"github.com/blisspixel/fitr/internal/automation"
	"github.com/blisspixel/fitr/internal/capacity"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/role"
	"github.com/blisspixel/fitr/internal/score"
)

const (
	PlanSchema    = "fitr.fitting.plan.v1"
	SessionSchema = "fitr.fitting.session.v1"
	ViewSchema    = "fitr.fitting.view.v1"

	ScopeQualify = "qualify"
	ScopeScreen  = "screen"

	ComparisonAssessment  = "assessment"
	ComparisonComparative = "comparative"

	PhaseBlocked         = "blocked"
	PhasePreviewed       = "previewed"
	PhaseApproved        = "approved"
	PhaseMeasuring       = "measuring"
	PhaseMeasured        = "measured"
	PhaseDelegated       = "delegated"
	PhaseAdoptionClosed  = "adoption-closed"
	defaultRepeats       = 3
	defaultMaxAgeDays    = 30
	defaultMaxRequests   = 600
	defaultMaxTokens     = 250000
	defaultMaxPoints     = 8
	defaultWallSeconds   = 2 * 60 * 60
	defaultConfirmWindow = 60 * 60
)

// CapacityKind is the operator's resource policy. A ceiling is an absolute
// usable budget. A reserve is subtracted from timestamped free memory.
// Neither is a hardware allocation cap, and one is never rewritten as the other.
type CapacityKind string

const (
	CapacityCeiling CapacityKind = "ceiling"
	CapacityReserve CapacityKind = "reserve"
)

var idPattern = regexp.MustCompile(`^fit-[0-9a-f]{32}$`)

// Request is the person's answers. Zero context is missing, not an 8192 default.
type Request struct {
	RoleName            string
	Outcomes            []string
	MinimumRate         *float64
	Scope               string
	Candidates          []string
	ContextTokens       int
	KVCacheType         string
	FlashAttention      bool
	KVExplicit          bool
	CapacityKind        CapacityKind
	CapacityBytes       int64
	ResidentLimitBytes  int64
	UsableContextBytes  int
	ContextTiers        []int
	Endpoint            string
	EndpointSource      string
	Locality            string
	EndpointNote        string
	Repeats             int
	OwnedRuntime        bool
	BuildVersion        string
	Now                 time.Time
	Workload            Workload
	ModelCard           string
	AlternativeContexts []int
	KVClientEnv         string
	KVServerReport      string
}

// Plan is sealed before inference. Later phases read it; they do not restate it.
type Plan struct {
	Schema                   string       `json:"schema"`
	ID                       string       `json:"id"`
	SHA256                   string       `json:"sha256"`
	CreatedAt                string       `json:"created_at"`
	BuildVersion             string       `json:"build_version"`
	RoleName                 string       `json:"role_name"`
	Scope                    string       `json:"scope"`
	Role                     role.Spec    `json:"role"`
	Candidates               []string     `json:"candidates"`
	Comparison               string       `json:"comparison"`
	ContextTokens            int          `json:"context_tokens"`
	KVCacheType              string       `json:"kv_cache_type"`
	FlashAttention           bool         `json:"flash_attention"`
	KVExplicit               bool         `json:"kv_explicit"`
	CapacityKind             CapacityKind `json:"capacity_kind"`
	CapacityBytes            int64        `json:"capacity_bytes"`
	ResidentLimitBytes       int64        `json:"resident_limit_bytes"`
	Endpoint                 string       `json:"endpoint"`
	EndpointSource           string       `json:"endpoint_source"`
	Locality                 string       `json:"locality"`
	EndpointNote             string       `json:"endpoint_note,omitempty"`
	Repeats                  int          `json:"repeats"`
	RepeatsDefaulted         bool         `json:"repeats_defaulted"`
	SeedSet                  string       `json:"seedset"`
	TaskScheduleSHA256       string       `json:"task_schedule_sha256"`
	ContextTiers             []int        `json:"context_tiers,omitempty"`
	Adoption                 string       `json:"adoption"`
	OwnedRuntime             bool         `json:"owned_runtime"`
	RawRetained              bool         `json:"raw_retained"`
	MaxRequests              int64        `json:"max_requests,omitempty"`
	MaxRequestedOutputTokens int64        `json:"max_requested_output_tokens,omitempty"`
	MaxPoints                int          `json:"max_points,omitempty"`
	WallSeconds              int64        `json:"wall_seconds,omitempty"`
	ConfirmationWallSeconds  int64        `json:"confirmation_wall_seconds,omitempty"`
	Blocked                  bool         `json:"blocked"`
	BlockReason              string       `json:"block_reason,omitempty"`
	ScreeningNote            string       `json:"screening_note,omitempty"`
	Workload                 *Workload    `json:"workload,omitempty"`
}

// Point is one finished candidate. It identifies the managed record and
// carries no prompt, reply, hostname, or local path.
type Point struct {
	Model          string `json:"model"`
	RunID          string `json:"run_id"`
	EvidenceSHA256 string `json:"evidence_sha256"`
}

// Session is the resumable fitting. ExecutableSHA256 is the binary that
// wrote it, so a result can be tied to a build rather than only to a version tag.
type Session struct {
	Schema           string  `json:"schema"`
	Phase            string  `json:"phase"`
	Plan             Plan    `json:"plan"`
	Points           []Point `json:"points,omitempty"`
	AutoSessionID    string  `json:"auto_session_id,omitempty"`
	ExecutableSHA256 string  `json:"executable_sha256,omitempty"`
	ExplorationID    string  `json:"exploration_id,omitempty"`
	ConfirmationSeed string  `json:"confirmation_seed,omitempty"`
}

// Draft seals a plan from one set of answers. A blocked plan is still sealed:
// the refusal is part of what the person approved, and start will not measure it.
func Draft(req Request, tasks *eval.Spec) (Plan, error) {
	if tasks == nil {
		return Plan{}, errors.New("fitting feasibility needs the task schedule")
	}
	if req.Now.IsZero() {
		return Plan{}, errors.New("fitting plan needs a current time")
	}
	if err := validateRequest(req); err != nil {
		return Plan{}, err
	}
	expanded, expandErr := expandPresetOutcomes(req)
	if expandErr != nil {
		return Plan{}, expandErr
	}
	req.Outcomes = expanded
	outcomes, note, err := selectOutcomes(req)
	if err != nil {
		return Plan{}, err
	}
	spec, err := buildRole(req, outcomes)
	if err != nil {
		return Plan{}, err
	}
	id, err := newID()
	if err != nil {
		return Plan{}, err
	}
	kv := effectiveKV(req.KVCacheType)
	flash, err := effectiveFlash(req, kv)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{
		Schema: PlanSchema, ID: id, CreatedAt: req.Now.UTC().Format(time.RFC3339Nano),
		BuildVersion: req.BuildVersion, RoleName: req.RoleName, Scope: req.Scope, Role: spec,
		Candidates: append([]string(nil), req.Candidates...), Comparison: comparisonKind(len(req.Candidates)),
		ContextTokens: req.ContextTokens, KVCacheType: kv, FlashAttention: flash,
		KVExplicit: req.KVExplicit, CapacityKind: req.CapacityKind, CapacityBytes: req.CapacityBytes,
		ResidentLimitBytes: req.ResidentLimitBytes, Endpoint: req.Endpoint, EndpointSource: req.EndpointSource,
		Locality: req.Locality, EndpointNote: req.EndpointNote, Repeats: effectiveRepeats(req.Repeats),
		RepeatsDefaulted: req.Repeats == 0, SeedSet: id, ContextTiers: append([]int(nil), req.ContextTiers...),
		Adoption: "manual", OwnedRuntime: req.OwnedRuntime, ScreeningNote: note,
	}
	applyOwnedLimits(&plan)
	workload, workloadErr := normalizeWorkload(req, kv)
	if workloadErr != nil {
		return Plan{}, workloadErr
	}
	plan.Workload = workload
	hashes, err := eval.EffectiveHashes(tasks)
	if err != nil {
		return Plan{}, err
	}
	plan.TaskScheduleSHA256 = hashes.SpecSHA256
	blocked, reason := blockReason(plan, tasks)
	plan.Blocked, plan.BlockReason = blocked, reason
	if err := plan.seal(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func applyOwnedLimits(plan *Plan) {
	if !plan.OwnedRuntime {
		return
	}
	limits := defaultLimits()
	plan.MaxRequests = limits.MaxRequests
	plan.MaxRequestedOutputTokens = limits.MaxRequestedOutputTokens
	plan.MaxPoints = limits.MaxPoints
	plan.WallSeconds = limits.WallSeconds
	plan.ConfirmationWallSeconds = limits.ConfirmationWallSeconds
}

func comparisonKind(n int) string {
	if n == 1 {
		return ComparisonAssessment
	}
	return ComparisonComparative
}

func effectiveKV(value string) string {
	if value == "" {
		return "f16"
	}
	return value
}

func effectiveRepeats(value int) int {
	if value == 0 {
		return defaultRepeats
	}
	return value
}

// effectiveFlash keeps an omitted KV setting on the owned-runtime default.
// An explicit f16 request may turn flash attention off. Quantized KV cannot.
func effectiveFlash(req Request, kv string) (bool, error) {
	flash := true
	if req.KVExplicit {
		flash = req.FlashAttention
	}
	if kv != "q8_0" && kv != "q4_0" {
		return flash, nil
	}
	if req.KVExplicit && !req.FlashAttention {
		return false, errors.New("quantized KV cache requires flash attention")
	}
	return true, nil
}

func validateRequest(req Request) error {
	if err := validateIdentity(req); err != nil {
		return err
	}
	if err := validateCapacity(req); err != nil {
		return err
	}
	return validateSchedule(req)
}

func validateIdentity(req Request) error {
	if req.Scope != ScopeQualify && req.Scope != ScopeScreen {
		return errors.New("fitting scope must be qualify or screen")
	}
	if !roleNameOK(req.RoleName) {
		return errors.New("fitting role name must be a lowercase letter or digit, then lowercase letters, digits, or hyphens")
	}
	if req.ContextTokens < 512 || req.ContextTokens > 1<<20 {
		return errors.New("context is required and must be 512 to 1048576 tokens; there is no default window")
	}
	if req.Repeats != 0 && (req.Repeats < 3 || req.Repeats > 20) {
		return errors.New("repeats must be from 3 to 20")
	}
	if len(req.Candidates) < 1 || len(req.Candidates) > 4 {
		return errors.New("name one to four installed candidates; a missing candidate is not invented")
	}
	seen := map[string]bool{}
	for _, candidate := range req.Candidates {
		if !candidateOK(candidate) || seen[candidate] {
			return errors.New("candidates must be distinct non-empty model names")
		}
		seen[candidate] = true
	}
	return nil
}

func validateCapacity(req Request) error {
	if req.CapacityKind != CapacityCeiling && req.CapacityKind != CapacityReserve {
		return errors.New("capacity policy must be an absolute ceiling or a reserve")
	}
	if req.CapacityKind == CapacityCeiling && req.CapacityBytes <= 0 {
		return errors.New("an absolute ceiling needs a positive byte budget")
	}
	if req.CapacityKind == CapacityReserve && req.CapacityBytes < 0 {
		return errors.New("a reserve cannot be negative")
	}
	if req.ResidentLimitBytes <= 0 {
		return errors.New("the role needs its own maximum resident size in bytes")
	}
	if req.CapacityKind == CapacityCeiling && req.ResidentLimitBytes != req.CapacityBytes {
		return errors.New("an absolute ceiling and the role's resident limit are the same byte count")
	}
	if req.OwnedRuntime && req.CapacityKind != CapacityReserve {
		return errors.New("an owned runtime uses a reserve; an absolute ceiling is not written into OLLAMA_GPU_OVERHEAD")
	}
	return nil
}

func validateSchedule(req Request) error {
	if err := validateWorkloadInputs(req); err != nil {
		return err
	}
	if req.Endpoint == "" || req.EndpointSource == "" || req.Locality == "" {
		return errors.New("fitting needs one resolved endpoint")
	}
	switch req.KVCacheType {
	case "", "f16", "q8_0", "q4_0":
	default:
		return errors.New("KV cache must be f16, q8_0, or q4_0")
	}
	if req.BuildVersion == "" {
		return errors.New("fitting plan needs the build version that produced it")
	}
	if len(req.ContextTiers) > 0 && req.UsableContextBytes <= 0 {
		return errors.New("context tiers require a usable-context floor; tiers are payload bytes, not a token-window search")
	}
	if req.UsableContextBytes > 0 && len(req.ContextTiers) == 0 {
		return errors.New("a usable-context floor needs declared context tiers; tiers are payload bytes, not a token-window search")
	}
	if req.MinimumRate == nil {
		return nil
	}
	rate := *req.MinimumRate
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 || rate > 1 {
		return errors.New("minimum rate must be above 0 and at most 1; the floor is not lowered to produce a result")
	}
	return nil
}

func roleNameOK(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}

func candidateOK(name string) bool {
	if len(name) == 0 || len(name) > 256 {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return false
		}
	}
	return true
}

type keptOutcome struct {
	Need        string
	MinimumRate *float64
}

func selectOutcomes(req Request) ([]keptOutcome, string, error) {
	if len(req.Outcomes) == 0 || len(req.Outcomes) > 8 {
		return nil, "", errors.New("name one to eight required outcomes")
	}
	seen := map[string]bool{}
	var kept []keptOutcome
	var dropped []string
	for _, need := range req.Outcomes {
		if seen[need] || !knownNeed(need) {
			return nil, "", fmt.Errorf("required outcome %q is duplicated or not a role behavior", need)
		}
		seen[need] = true
		if req.Scope == ScopeScreen && executionOnly(need) {
			dropped = append(dropped, need)
			continue
		}
		item := keptOutcome{Need: need}
		if req.MinimumRate != nil && decision.SupportsBehaviorRate(need) {
			rate := *req.MinimumRate
			item.MinimumRate = &rate
		}
		kept = append(kept, item)
	}
	rateIgnored := req.MinimumRate != nil
	for _, item := range kept {
		if item.MinimumRate != nil {
			rateIgnored = false
		}
	}
	note := ""
	if len(dropped) > 0 {
		note = "Screening dropped " + strings.Join(dropped, ", ") + ". Those outcomes need independently checked code execution, which this battery does not run. Screening does not qualify a coder and does not lower a rate floor."
	}
	if len(kept) == 0 && req.Scope == ScopeScreen {
		kept = []keptOutcome{{Need: "structured_output"}}
		note += " No text outcome remained, so screening uses a structured-output floor of its own. That floor is not a coding result."
		if rateIgnored {
			note += " The requested rate was not copied onto that substitute."
		}
	}
	if req.Scope == ScopeQualify && rateIgnored {
		note = "A requested rate was not attached to an outcome that has no rate estimand. Coding still requires executed outcomes."
	}
	return kept, strings.TrimSpace(note), nil
}

func executionOnly(need string) bool {
	return need == "coding" || need == "unattended_agentic"
}

func knownNeed(need string) bool {
	switch need {
	case "coding", "structured_output", "instruction_precision", "reasoning",
		"uncensored", "tool_calling", "unattended_agentic", "tool_restraint",
		"output_health", "user_tasks":
		return true
	default:
		return false
	}
}

func buildRole(req Request, outcomes []keptOutcome) (role.Spec, error) {
	requirements := make([]decision.Requirement, 0, len(outcomes)+2)
	for _, outcome := range outcomes {
		behavior := &decision.BehaviorRequirement{Need: outcome.Need}
		if outcome.MinimumRate != nil {
			rate := *outcome.MinimumRate
			behavior.MinimumRate = &rate
		} else {
			behavior.RequiredState = score.Pass
		}
		requirements = append(requirements, decision.Requirement{ID: outcome.Need, Behavior: behavior})
	}
	contextReq := decision.ContextRequirement{MinimumEffectiveTokens: req.ContextTokens}
	if req.UsableContextBytes > 0 {
		floor := req.UsableContextBytes
		contextReq.MinimumUsableContextBytes = &floor
	}
	requirements = append(requirements,
		decision.Requirement{ID: "context", Context: &contextReq},
		decision.Requirement{ID: "memory", Capacity: &decision.CapacityRequirement{
			MaximumResidentBytes: req.ResidentLimitBytes, RequestedContext: req.ContextTokens,
		}},
	)
	spec := role.Spec{
		Schema: role.SpecSchema, Name: req.RoleName, MaxAgeDays: defaultMaxAgeDays,
		Description: "Sealed by one fitting plan. Adoption records this role only.",
		Decision: decision.DecisionSpec{
			Schema: decision.SpecSchema, Name: req.RoleName + " fitting", Evidence: decision.EvidenceDecide,
			Requirements: requirements,
		},
		Preferences: []role.Preference{{
			Requirement: "memory", Weight: 1, Worst: float64(req.ResidentLimitBytes), Best: 0,
		}},
	}
	if err := spec.Validate(); err != nil {
		return role.Spec{}, err
	}
	return spec, nil
}

func blockReason(plan Plan, tasks *eval.Spec) (bool, string) {
	text := joinNonEmpty([]string{workloadBlock(plan), scheduleBlock(plan, tasks)})
	return text != "", text
}

func scheduleBlock(plan Plan, tasks *eval.Spec) string {
	if plan.UsableFloor() > 0 && (!plan.OwnedRuntime || plan.CapacityKind != CapacityReserve) {
		return "A usable-context floor is collected by the owned auto schedule, which records a reserve. An absolute ceiling is not rewritten into that reserve, and this plan was not started."
	}
	limits := automation.Limits{}
	candidates := len(plan.Candidates)
	if plan.OwnedRuntime {
		limits = automation.Limits{
			MaxRequests: plan.MaxRequests, MaxRequestedOutputTokens: plan.MaxRequestedOutputTokens,
			MaxPoints: plan.MaxPoints, WallSeconds: plan.WallSeconds,
			ConfirmationWallSeconds: plan.ConfirmationWallSeconds,
		}
		if candidates < 2 {
			return "An owned runtime fitting compares two to four candidates. One candidate stays a non-comparative assessment through the selected endpoint."
		}
	}
	err := automation.ValidateFeasibilitySchedule(plan.Role, tasks, plan.Repeats, plan.ContextTokens, automation.FeasibilitySchedule{
		Tiers: plan.ContextTiers, Candidates: candidates, Limits: limits,
	})
	if err == nil {
		return ""
	}
	text := err.Error()
	switch {
	case strings.Contains(text, "generated-code execution"):
		return "This outcome needs independently checked code execution. The execution-disabled battery cannot establish it. Reasoning text and valid tool-call syntax do not establish coding. " +
			"Pass --scope screen to collect structured output, instruction following, and native tool-channel checks as screening. Screening does not promise a qualified coder and does not lower the floor. " + text
	case strings.Contains(text, "optimistic unclustered"):
		return "This success-rate floor cannot be established with the proposed repeats even when every planned check passes. The floor was not lowered. Raise the repeat count or leave the result unresolved. " + text
	case strings.Contains(text, "no supported rate"), strings.Contains(text, "unavailable in the auto"), strings.Contains(text, "reasoning has rate"):
		return "The requested outcome is not established by this battery. Nothing was run, and the floor was not relaxed. " + text
	default:
		return text
	}
}

func defaultLimits() automation.Limits {
	return automation.Limits{
		MaxRequests: defaultMaxRequests, MaxRequestedOutputTokens: defaultMaxTokens,
		MaxPoints: defaultMaxPoints, WallSeconds: defaultWallSeconds, ConfirmationWallSeconds: defaultConfirmWindow,
	}
}

// UsableFloor reports the role's document-byte floor, or zero when the role
// only names a runtime window. A token window is not that floor.
func (plan Plan) UsableFloor() int {
	floor, err := role.UsableContextFloor(plan.Role)
	if err != nil || floor == nil {
		return 0
	}
	return *floor
}

func (plan *Plan) seal() error {
	plan.Schema = PlanSchema
	plan.SHA256 = ""
	sum, err := digest(*plan)
	if err != nil {
		return err
	}
	plan.SHA256 = sum
	return plan.Validate()
}

// Validate checks the seal and the refusal to treat a ceiling as a reserve.
func (plan Plan) Validate() error {
	if plan.Schema != PlanSchema || !idPattern.MatchString(plan.ID) || plan.SeedSet != plan.ID {
		return errors.New("invalid fitting plan identity")
	}
	if plan.BuildVersion == "" || plan.Adoption != "manual" || plan.RawRetained {
		return errors.New("fitting plan provenance, adoption, or retention is invalid")
	}
	if !validDigest(plan.TaskScheduleSHA256) {
		return errors.New("fitting plan needs its task schedule digest")
	}
	if err := plan.validateSettings(); err != nil {
		return err
	}
	if err := plan.Workload.validate(); err != nil {
		return err
	}
	if plan.Comparison != comparisonKind(len(plan.Candidates)) {
		return errors.New("fitting comparison does not match the candidate count")
	}
	if plan.OwnedRuntime && plan.CapacityKind != CapacityReserve {
		return errors.New("owned runtime plan cannot carry an absolute ceiling")
	}
	if plan.OwnedRuntime && (plan.MaxRequests <= 0 || plan.MaxRequestedOutputTokens <= 0 || plan.MaxPoints <= 0 || plan.WallSeconds <= 0 || plan.ConfirmationWallSeconds <= 0 || plan.ConfirmationWallSeconds >= plan.WallSeconds) {
		return errors.New("owned fitting plan is missing its allowance")
	}
	if plan.CapacityKind == CapacityCeiling && plan.ResidentLimitBytes != plan.CapacityBytes {
		return errors.New("ceiling plan changed its resident limit")
	}
	expected := plan.SHA256
	plan.SHA256 = ""
	actual, err := digest(plan)
	if err != nil || expected == "" || expected != actual {
		return errors.New("fitting plan seal does not match")
	}
	return nil
}

// ValidateTasks prevents start or resume from replacing the approved battery,
// including user checks added or edited after the plan was previewed.
func (plan Plan) ValidateTasks(tasks *eval.Spec) error {
	if tasks == nil {
		return errors.New("fitting task schedule is unavailable")
	}
	actual, err := eval.EffectiveHashes(tasks)
	if err != nil || actual.SpecSHA256 != plan.TaskScheduleSHA256 {
		return errors.New("fitting task schedule changed; draft a new plan before inference")
	}
	return nil
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil
}

func (plan Plan) validateSettings() error {
	req := Request{
		RoleName: plan.RoleName, Scope: plan.Scope, Candidates: plan.Candidates,
		ContextTokens: plan.ContextTokens, Repeats: plan.Repeats, KVCacheType: plan.KVCacheType,
		CapacityKind: plan.CapacityKind, CapacityBytes: plan.CapacityBytes,
		ResidentLimitBytes: plan.ResidentLimitBytes, OwnedRuntime: plan.OwnedRuntime,
		Endpoint: plan.Endpoint, EndpointSource: plan.EndpointSource, Locality: plan.Locality,
		BuildVersion: plan.BuildVersion, ContextTiers: plan.ContextTiers, UsableContextBytes: plan.UsableFloor(),
	}
	if plan.Workload != nil {
		req.Workload = *plan.Workload
	}
	if plan.Repeats == 0 {
		return errors.New("sealed fitting plan needs its repeat count")
	}
	if err := validateRequest(req); err != nil {
		return err
	}
	if err := plan.Role.Validate(); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, plan.CreatedAt); err != nil {
		return errors.New("fitting plan has an invalid creation time")
	}
	if plan.OwnedRuntime {
		if plan.Endpoint != "owned-process" || plan.EndpointSource != "owned-runtime" || plan.Locality != "owned-process" {
			return errors.New("owned fitting must use its own endpoint")
		}
	} else if endpoint, err := canonicalClientURL(plan.Endpoint); err != nil || endpoint == "" {
		return errors.New("fitting plan has an invalid endpoint")
	}
	if (plan.KVCacheType == "q8_0" || plan.KVCacheType == "q4_0") && !plan.FlashAttention {
		return errors.New("quantized KV cache requires flash attention")
	}
	return nil
}

func digest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func newID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "fit-" + hex.EncodeToString(buf[:]), nil
}

// ParseGiB converts a display quantity to bytes. Positive rejects zero.
// The rounding matches the capacity policy used by run and advise.
func ParseGiB(value float64, positive bool) (int64, error) {
	return capacity.GiBBytes(value, positive)
}

// FormatGiB renders bytes as an explicit GiB quantity.
func FormatGiB(bytes int64) string {
	return fmt.Sprintf("%.2f GiB", float64(bytes)/advise.GiB)
}
