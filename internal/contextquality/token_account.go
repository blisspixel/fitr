package contextquality

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
)

// Token accounting is a separate receipt from the UTF-8 byte pack.
// Payload bytes, rune counts and a verified byte prefix are not usable-input
// tokens. A 64 KiB pack cannot certify a 64000 token target. Nothing here runs
// a model, and nothing here establishes Pi, Hermes, compaction, arbitrary
// coding or a maximum context.

const (
	TokenAccountSchema = "fitr.context-token-account.v1"
	TokenReportSchema  = "fitr.context-token-account.report.v1"
	TokenThreshold     = "required_cases_all_pass.v1"
	TokenStoppingRule  = "no_outcome_driven_retry"
	TokenOrder         = "declared"
	TokenUncertainty   = "fixed battery; not a confidence interval or a maximum context"

	AccountingNative   = "native"
	AccountingUnknown  = "unknown"
	AccountingBytePack = "byte-pack"

	SemanticsUsableInput = "usable-input"
	SemanticsTotalWindow = "total-window"

	ConcurrentNone         = "none_observed"
	ConcurrentUnobserved   = "unobserved"
	ConcurrentContaminated = "contaminated"

	TokenQualified         = "qualified"
	TokenQualityFailed     = "quality_failed"
	TokenCapacityLimited   = "capacity_limited"
	TokenMissingAccounting = "missing_accounting"
	TokenTransportFailed   = "transport_failed"
	TokenNotTested         = "not_tested"
	TokenRefused           = "refused"
	TokenContaminated      = "contaminated"

	CasePass              = "pass"
	CaseQualityFailed     = "quality_failed"
	CaseCapacityLimited   = "capacity_limited"
	CaseMissingAccounting = "missing_accounting"
	CaseTransportFailed   = "transport_failed"
	CaseNotTested         = "not_tested"

	MaxTokenPlanBytes = 256 << 10
)

var errTokenTotalWindow = errors.New("a total window is not usable input")
var errTokenText = errors.New("UTF-8 bytes and rune counts are not usable-input tokens")

var tokenDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var tokenLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
var usableInputDigits = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)

// TokenPlan is the schedule sealed before any token-target measurement.
// PlanSHA256 is computed with the field empty, then stored, so a finished
// phase cannot present a shorter schedule than the one that was hashed.
type TokenPlan struct {
	Schema                  string        `json:"schema"`
	TargetUsableInputTokens int           `json:"target_usable_input_tokens"`
	Semantics               string        `json:"semantics"`
	Identity                TokenIdentity `json:"identity"`
	GenerationReserveTokens int           `json:"generation_reserve_tokens"`
	ReasoningReserveTokens  int           `json:"reasoning_reserve_tokens"`
	RequestedWindowTokens   int           `json:"requested_window_tokens"`
	Cases                   []TokenCase   `json:"cases"`
	Repeats                 int           `json:"repeats"`
	Order                   string        `json:"order"`
	Threshold               string        `json:"threshold"`
	StoppingRule            string        `json:"stopping_rule"`
	RequestBudget           int           `json:"request_budget"`
	TimeBudgetSeconds       int           `json:"time_budget_seconds"`
	OutputBudgetTokens      int           `json:"output_budget_tokens"`
	PlanSHA256              string        `json:"plan_sha256"`
}

// TokenIdentity binds the configuration the token claim is about.
// Digests are lowercase sha256 tokens. A hostname, a path or the raw device
// key cannot occupy these fields.
type TokenIdentity struct {
	ArtifactDigest    string `json:"artifact_digest"`
	WeightQuant       string `json:"weight_quant"`
	TemplateDigest    string `json:"template_digest"`
	AccountingSource  string `json:"accounting_source"`
	RuntimeBuild      string `json:"runtime_build"`
	EndpointIdentity  string `json:"endpoint_identity"`
	DeviceFingerprint string `json:"device_fingerprint"`
	KVDtype           string `json:"kv_dtype"`
}

// TokenCase is one declared family, position and seed. Its ID is derived from
// those fields and the sealed target, so editing a descriptor changes the ID.
type TokenCase struct {
	ID       string   `json:"id"`
	Sequence int      `json:"sequence"`
	Family   Family   `json:"family"`
	Position Position `json:"position"`
	Seed     uint64   `json:"seed"`
}

// ParseUsableInputTarget accepts only the decimal integer. 64K, 64 KiB and
// 64,000 are rejected so K is never read as 1024 and a byte size is never
// relabeled as a token target.
func ParseUsableInputTarget(text string) (int, error) {
	if !usableInputDigits.MatchString(text) {
		return 0, errors.New("usable-input target must be the decimal integer, with no K or byte suffix")
	}
	value, err := strconv.Atoi(text)
	if err != nil || value < 1 || value > MaxOperatingWindowTokens {
		return 0, errors.New("usable-input target is outside the sealed integer range")
	}
	return value, nil
}

// UsableInputLabel prints the sealed integer. It does not abbreviate 64000
// to 64K or 64 KiB.
func UsableInputLabel(tokens int) (string, error) {
	if tokens < 1 || tokens > MaxOperatingWindowTokens {
		return "", errors.New("usable-input target is outside the sealed integer range")
	}
	return strconv.Itoa(tokens), nil
}

// UsableInputTokensFromText refuses to estimate tokens from text. Multi-byte
// characters make the UTF-8 length and the rune count disagree, and neither
// number is a tokenizer count.
func UsableInputTokensFromText(string) (int, error) {
	return 0, errTokenText
}

// NewTokenPlan seals a caller-declared schedule. Case IDs and the plan digest
// are derived here. The caller's slice is not retained.
func NewTokenPlan(plan TokenPlan) (TokenPlan, error) {
	return sealTokenPlan(plan)
}

func (plan TokenPlan) Validate() error {
	sealed, err := sealTokenPlan(plan)
	if err != nil {
		return err
	}
	if plan.Schema != sealed.Schema || plan.PlanSHA256 != sealed.PlanSHA256 || !sameTokenCases(plan.Cases, sealed.Cases) {
		return errors.New("token account plan differs from its sealed schedule")
	}
	if plan.TargetUsableInputTokens != sealed.TargetUsableInputTokens || plan.Semantics != sealed.Semantics ||
		plan.Identity != sealed.Identity || plan.GenerationReserveTokens != sealed.GenerationReserveTokens ||
		plan.ReasoningReserveTokens != sealed.ReasoningReserveTokens || plan.RequestedWindowTokens != sealed.RequestedWindowTokens ||
		plan.Repeats != sealed.Repeats || plan.Order != sealed.Order || plan.Threshold != sealed.Threshold ||
		plan.StoppingRule != sealed.StoppingRule || plan.RequestBudget != sealed.RequestBudget ||
		plan.TimeBudgetSeconds != sealed.TimeBudgetSeconds || plan.OutputBudgetTokens != sealed.OutputBudgetTokens {
		return errors.New("token account plan differs from its sealed schedule")
	}
	return nil
}

func (plan TokenPlan) Digest() (string, error) {
	if err := plan.Validate(); err != nil {
		return "", err
	}
	return plan.PlanSHA256, nil
}

func (plan TokenPlan) JSON() ([]byte, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(plan)
}

func (plan TokenPlan) Clone() TokenPlan {
	dup := plan
	dup.Cases = append([]TokenCase(nil), plan.Cases...)
	return dup
}

// DecodeTokenPlan accepts one canonical JSON plan and rejects anything that
// does not validate. Callers must not treat a decode error as a measured zero.
func DecodeTokenPlan(data []byte) (TokenPlan, error) {
	var plan TokenPlan
	if err := decodeExact(data, MaxTokenPlanBytes, &plan); err != nil {
		return TokenPlan{}, err
	}
	if err := plan.Validate(); err != nil {
		return TokenPlan{}, err
	}
	return plan, nil
}

func sealTokenPlan(plan TokenPlan) (TokenPlan, error) {
	if err := validateTokenPlanShape(plan); err != nil {
		return TokenPlan{}, err
	}
	sealed := plan
	sealed.Schema = TokenAccountSchema
	sealed.PlanSHA256 = ""
	sealed.Cases = make([]TokenCase, len(plan.Cases))
	for i, item := range plan.Cases {
		sealed.Cases[i] = TokenCase{Sequence: i + 1, Family: item.Family, Position: item.Position, Seed: item.Seed}
		id, err := tokenCaseID(plan.TargetUsableInputTokens, sealed.Cases[i])
		if err != nil {
			return TokenPlan{}, err
		}
		sealed.Cases[i].ID = id
	}
	sum, err := hashValue(TokenAccountSchema, sealed)
	if err != nil {
		return TokenPlan{}, err
	}
	sealed.PlanSHA256 = sum
	return sealed, nil
}

func validateTokenPlanShape(plan TokenPlan) error {
	if err := validateTokenBounds(plan); err != nil {
		return err
	}
	if err := validateTokenIdentity(plan.Identity); err != nil {
		return err
	}
	return validateTokenSchedule(plan)
}

func validateTokenBounds(plan TokenPlan) error {
	if plan.Semantics == SemanticsTotalWindow {
		return errTokenTotalWindow
	}
	if plan.Semantics != SemanticsUsableInput {
		return errors.New("unsupported token account semantics")
	}
	if plan.TargetUsableInputTokens < 1 || plan.TargetUsableInputTokens > MaxOperatingWindowTokens {
		return errors.New("usable-input target is outside the sealed integer range")
	}
	reserve := int64(plan.GenerationReserveTokens) + int64(plan.ReasoningReserveTokens)
	if plan.GenerationReserveTokens < 1 || plan.ReasoningReserveTokens < 0 || reserve < 1 {
		return errors.New("token account reserve is invalid")
	}
	if int64(plan.RequestedWindowTokens) <= reserve || plan.RequestedWindowTokens > MaxOperatingWindowTokens {
		return errors.New("token account window does not leave room for the reserve")
	}
	if plan.OutputBudgetTokens < plan.GenerationReserveTokens || plan.OutputBudgetTokens > plan.RequestedWindowTokens {
		return errors.New("token account output budget does not cover the generation reserve")
	}
	return nil
}

func validateTokenIdentity(id TokenIdentity) error {
	if !tokenDigest.MatchString(id.ArtifactDigest) || !tokenDigest.MatchString(id.EndpointIdentity) || !tokenDigest.MatchString(id.DeviceFingerprint) {
		return errors.New("token account identity digest is missing or not lowercase sha256")
	}
	if !tokenLabel.MatchString(id.WeightQuant) || !tokenLabel.MatchString(id.RuntimeBuild) || !tokenLabel.MatchString(id.KVDtype) {
		return errors.New("token account quant, runtime build or KV dtype is missing")
	}
	switch id.AccountingSource {
	case AccountingNative:
		if !tokenDigest.MatchString(id.TemplateDigest) {
			return errors.New("native token accounting requires a template digest")
		}
	case AccountingUnknown, AccountingBytePack:
		if id.TemplateDigest != "" && !tokenDigest.MatchString(id.TemplateDigest) {
			return errors.New("token account template digest is not lowercase sha256")
		}
	default:
		return errors.New("unsupported token accounting source")
	}
	return nil
}

func validateTokenSchedule(plan TokenPlan) error {
	if plan.Order != TokenOrder || plan.Threshold != TokenThreshold || plan.StoppingRule != TokenStoppingRule {
		return errors.New("unsupported token account order, threshold or stopping rule")
	}
	if plan.Repeats < 1 || plan.Repeats > 8 {
		return errors.New("token account repeats must be from 1 to 8")
	}
	if len(plan.Cases) < 1 || len(plan.Cases) > 128 {
		return errors.New("token account schedule is empty or too large")
	}
	if plan.RequestBudget < len(plan.Cases) || plan.RequestBudget > 256 {
		return errors.New("token account request budget does not cover the sealed schedule")
	}
	if plan.TimeBudgetSeconds < 1 || plan.TimeBudgetSeconds > 24*60*60 {
		return errors.New("token account time budget is not a finite positive bound")
	}
	return validateTokenCases(plan)
}

type tokenPair struct {
	family   Family
	position Position
}

func validateTokenCases(plan TokenPlan) error {
	counts := map[tokenPair]int{}
	seen := map[tokenPair]map[uint64]struct{}{}
	for _, item := range plan.Cases {
		if !knownTokenFamily(item.Family) || !knownTokenPosition(item.Position) {
			return errors.New("token account case has an unknown family or position")
		}
		key := tokenPair{item.Family, item.Position}
		if seen[key] == nil {
			seen[key] = map[uint64]struct{}{}
		}
		if _, ok := seen[key][item.Seed]; ok {
			return errors.New("token account repeats a seed at one position")
		}
		seen[key][item.Seed] = struct{}{}
		counts[key]++
	}
	for _, count := range counts {
		if count != plan.Repeats {
			return errors.New("token account repeats do not match the sealed schedule")
		}
	}
	return nil
}

func knownTokenFamily(family Family) bool {
	switch family {
	case IndirectRetrieval, DistantDependency, InstructionRetention:
		return true
	default:
		return false
	}
}

func knownTokenPosition(position Position) bool {
	switch position {
	case Beginning, Middle, End:
		return true
	default:
		return false
	}
}

func tokenCaseID(target int, item TokenCase) (string, error) {
	return hashValue("fitr.context-token-account.case.v1", struct {
		Target   int      `json:"target_usable_input_tokens"`
		Sequence int      `json:"sequence"`
		Family   Family   `json:"family"`
		Position Position `json:"position"`
		Seed     uint64   `json:"seed"`
	}{target, item.Sequence, item.Family, item.Position, item.Seed})
}

func sameTokenCases(left, right []TokenCase) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
