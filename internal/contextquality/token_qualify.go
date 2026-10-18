package contextquality

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// TokenObservationSet is what a phase observed. Qualify derives the claim
// from the sealed plan and these observations. A stored claim that disagrees
// is rejected by the record loader rather than believed.
type TokenObservationSet struct {
	Capacity TokenCapacityObservation `json:"capacity"`
	Cases    []TokenCaseObservation   `json:"cases"`
}

// TokenCapacityObservation records the free-capacity policy around the phase.
// A nil byte count is unmeasured. It is not zero and it is not spare room.
type TokenCapacityObservation struct {
	ObservedAt               string `json:"observed_at"`
	FreeBytes                *int64 `json:"free_bytes,omitempty"`
	ReserveBytes             *int64 `json:"reserve_bytes,omitempty"`
	AppliedBeforeAllocation  bool   `json:"applied_before_allocation"`
	AppliedBeforePhase       bool   `json:"applied_before_phase"`
	PeakBytes                *int64 `json:"peak_bytes,omitempty"`
	MemoryOverheadUnmeasured bool   `json:"memory_overhead_unmeasured,omitempty"`
	ConcurrentLoad           string `json:"concurrent_load"`
}

// TokenCaseObservation is one sealed case. GeneratedTokens is retained as an
// observation and is not the reservation: a short answer cannot shrink the
// sealed generation reserve.
type TokenCaseObservation struct {
	CaseID               string        `json:"case_id"`
	Outcome              string        `json:"outcome"`
	Identity             TokenIdentity `json:"identity"`
	PayloadTokens        *int          `json:"payload_tokens,omitempty"`
	OverheadTokens       *int          `json:"overhead_tokens,omitempty"`
	PayloadUTF8Bytes     *int          `json:"payload_utf8_bytes,omitempty"`
	EnforcedWindowTokens *int          `json:"enforced_window_tokens,omitempty"`
	Truncated            bool          `json:"truncated,omitempty"`
	Shifted              bool          `json:"shifted,omitempty"`
	CacheObserved        bool          `json:"cache_observed"`
	CachedTokens         *int          `json:"cached_tokens,omitempty"`
	GeneratedTokens      *int          `json:"generated_tokens,omitempty"`
}

// TokenReport is derived. Loaders recompute it and reject a stored copy that
// disagrees. State qualified is the only token-qualification claim.
type TokenReport struct {
	Schema                  string            `json:"schema"`
	PlanSHA256              string            `json:"plan_sha256"`
	TargetUsableInputTokens int               `json:"target_usable_input_tokens"`
	TargetLabel             string            `json:"target_label"`
	Semantics               string            `json:"semantics"`
	State                   string            `json:"state"`
	Reason                  string            `json:"reason"`
	Uncertainty             string            `json:"uncertainty"`
	ObservedPayloadTokens   *int              `json:"observed_payload_tokens,omitempty"`
	ObservedOverheadTokens  *int              `json:"observed_overhead_tokens,omitempty"`
	GenerationReserveTokens int               `json:"generation_reserve_tokens"`
	ReasoningReserveTokens  int               `json:"reasoning_reserve_tokens"`
	RequestedWindowTokens   int               `json:"requested_window_tokens"`
	EnforcedWindowTokens    *int              `json:"enforced_window_tokens,omitempty"`
	Passed                  int               `json:"passed"`
	Failed                  int               `json:"failed"`
	Unresolved              int               `json:"unresolved"`
	Seeds                   int               `json:"seeds"`
	FreeMemory              string            `json:"free_memory"`
	NextAction              string            `json:"next_action"`
	Cases                   []TokenCaseResult `json:"cases"`
}

// TokenCaseResult is one declared case after qualification. Outcome is the
// observation's disposition, not a rate.
type TokenCaseResult struct {
	CaseID   string   `json:"case_id"`
	Sequence int      `json:"sequence"`
	Family   Family   `json:"family"`
	Position Position `json:"position"`
	Seed     uint64   `json:"seed"`
	Outcome  string   `json:"outcome"`
}

type builtCase struct {
	result TokenCaseResult
	obs    *TokenCaseObservation
}

type tokenDecision struct {
	state, reason, next string
}

func (d tokenDecision) held() bool { return d.state != "" }

func holdToken(state, reason, next string) tokenDecision {
	return tokenDecision{state, reason, next}
}

// QualifyBytePack refuses to read a v1 document report as a usable-input
// token claim. The verified prefix stays a byte count. NativeTokenAccounting
// on that report is not this receipt, including when a caller overwrites it.
func QualifyBytePack(report Report, target int) (TokenReport, error) {
	bytes := 0
	if report.VerifiedPrefixUTF8Bytes != nil {
		bytes = *report.VerifiedPrefixUTF8Bytes
	}
	claim, err := bytePackRefusal(target, bytes)
	if err != nil {
		return TokenReport{}, err
	}
	return claim, nil
}

// Qualify derives the token claim. Malformed plans and observations return
// an error. A missing count, a refusal or a failed case returns a report
// whose state is not a model-quality zero.
func Qualify(plan TokenPlan, observed TokenObservationSet) (TokenReport, error) {
	if err := plan.Validate(); err != nil {
		return TokenReport{}, err
	}
	if err := validateTokenObservations(plan, observed); err != nil {
		return TokenReport{}, err
	}
	built := buildTokenCases(plan, observed)
	report := tokenReportShell(plan, observed, built)
	decision := decideTokenAccount(plan, observed, built)
	report.State = decision.state
	report.Reason = decision.reason
	report.NextAction = decision.next
	return report, nil
}

func (set TokenObservationSet) Clone() TokenObservationSet {
	dup := set
	dup.Capacity = set.Capacity.clone()
	dup.Cases = make([]TokenCaseObservation, len(set.Cases))
	for i := range set.Cases {
		dup.Cases[i] = set.Cases[i].clone()
	}
	return dup
}

func (obs TokenCapacityObservation) clone() TokenCapacityObservation {
	dup := obs
	dup.FreeBytes = copyInt64(obs.FreeBytes)
	dup.ReserveBytes = copyInt64(obs.ReserveBytes)
	dup.PeakBytes = copyInt64(obs.PeakBytes)
	return dup
}

func (obs TokenCaseObservation) clone() TokenCaseObservation {
	dup := obs
	dup.PayloadTokens = copyInt(obs.PayloadTokens)
	dup.OverheadTokens = copyInt(obs.OverheadTokens)
	dup.PayloadUTF8Bytes = copyInt(obs.PayloadUTF8Bytes)
	dup.EnforcedWindowTokens = copyInt(obs.EnforcedWindowTokens)
	dup.CachedTokens = copyInt(obs.CachedTokens)
	dup.GeneratedTokens = copyInt(obs.GeneratedTokens)
	return dup
}

func (report TokenReport) Clone() TokenReport {
	dup := report
	dup.ObservedPayloadTokens = copyInt(report.ObservedPayloadTokens)
	dup.ObservedOverheadTokens = copyInt(report.ObservedOverheadTokens)
	dup.EnforcedWindowTokens = copyInt(report.EnforcedWindowTokens)
	dup.Cases = append([]TokenCaseResult(nil), report.Cases...)
	return dup
}

func validateTokenObservations(plan TokenPlan, observed TokenObservationSet) error {
	if err := validateTokenCapacity(observed.Capacity); err != nil {
		return err
	}
	if len(observed.Cases) > len(plan.Cases) {
		return errors.New("token account has observations outside the sealed schedule")
	}
	known := map[string]struct{}{}
	for _, item := range plan.Cases {
		known[item.ID] = struct{}{}
	}
	seen := map[string]struct{}{}
	for _, item := range observed.Cases {
		if _, ok := seen[item.CaseID]; ok {
			return errors.New("token account repeats an observation")
		}
		seen[item.CaseID] = struct{}{}
		if _, ok := known[item.CaseID]; !ok {
			return errors.New("token account observation is not in the sealed schedule")
		}
		if err := validateCaseObservation(item); err != nil {
			return err
		}
	}
	return nil
}

func validateTokenCapacity(obs TokenCapacityObservation) error {
	switch obs.ConcurrentLoad {
	case ConcurrentNone, ConcurrentUnobserved, ConcurrentContaminated:
	default:
		return errors.New("token account concurrent load is not an observation")
	}
	if obs.AppliedBeforeAllocation || obs.AppliedBeforePhase {
		if _, err := time.Parse(time.RFC3339Nano, obs.ObservedAt); err != nil {
			return errors.New("token account free-capacity time is not RFC3339Nano")
		}
	}
	for _, value := range []*int64{obs.FreeBytes, obs.ReserveBytes, obs.PeakBytes} {
		if value != nil && *value < 0 {
			return errors.New("token account memory observation is negative")
		}
	}
	return nil
}

func validateCaseObservation(obs TokenCaseObservation) error {
	if !validCaseOutcome(obs.Outcome) {
		return errors.New("token account case outcome is unknown")
	}
	if err := validateTokenIdentity(obs.Identity); err != nil {
		return err
	}
	for _, value := range []*int{obs.PayloadTokens, obs.OverheadTokens, obs.EnforcedWindowTokens, obs.CachedTokens, obs.GeneratedTokens} {
		if err := boundedCount(value, MaxOperatingWindowTokens); err != nil {
			return err
		}
	}
	return boundedCount(obs.PayloadUTF8Bytes, 1<<30)
}

func validCaseOutcome(value string) bool {
	switch value {
	case CasePass, CaseQualityFailed, CaseCapacityLimited, CaseMissingAccounting, CaseTransportFailed, CaseNotTested:
		return true
	default:
		return false
	}
}

func boundedCount(value *int, limit int) error {
	if value == nil {
		return nil
	}
	if *value < 0 || *value > limit {
		return errors.New("token account count is negative or unbounded")
	}
	return nil
}

func buildTokenCases(plan TokenPlan, observed TokenObservationSet) []builtCase {
	index := map[string]TokenCaseObservation{}
	for _, item := range observed.Cases {
		index[item.CaseID] = item
	}
	built := make([]builtCase, 0, len(plan.Cases))
	for _, item := range plan.Cases {
		result := TokenCaseResult{
			CaseID: item.ID, Sequence: item.Sequence, Family: item.Family,
			Position: item.Position, Seed: item.Seed, Outcome: CaseNotTested,
		}
		obs, ok := index[item.ID]
		if !ok {
			built = append(built, builtCase{result: result})
			continue
		}
		result.Outcome = obs.Outcome
		obsCopy := obs
		built = append(built, builtCase{result: result, obs: &obsCopy})
	}
	return built
}

func tokenReportShell(plan TokenPlan, observed TokenObservationSet, built []builtCase) TokenReport {
	label, _ := UsableInputLabel(plan.TargetUsableInputTokens)
	report := TokenReport{
		Schema: TokenReportSchema, PlanSHA256: plan.PlanSHA256,
		TargetUsableInputTokens: plan.TargetUsableInputTokens, TargetLabel: label,
		Semantics: plan.Semantics, Uncertainty: TokenUncertainty,
		GenerationReserveTokens: plan.GenerationReserveTokens,
		ReasoningReserveTokens:  plan.ReasoningReserveTokens,
		RequestedWindowTokens:   plan.RequestedWindowTokens,
		FreeMemory:              freeMemoryLine(observed.Capacity),
	}
	seeds := map[uint64]struct{}{}
	var window *int
	sameWindow := true
	for _, item := range built {
		report.Cases = append(report.Cases, item.result)
		seeds[item.result.Seed] = struct{}{}
		switch item.result.Outcome {
		case CasePass:
			report.Passed++
		case CaseQualityFailed:
			report.Failed++
		default:
			report.Unresolved++
		}
		if item.obs == nil {
			continue
		}
		report.ObservedPayloadTokens = minCount(report.ObservedPayloadTokens, item.obs.PayloadTokens)
		report.ObservedOverheadTokens = minCount(report.ObservedOverheadTokens, item.obs.OverheadTokens)
		window, sameWindow = sameCount(window, sameWindow, item.obs.EnforcedWindowTokens)
	}
	report.Seeds = len(seeds)
	if sameWindow {
		report.EnforcedWindowTokens = window
	}
	return report
}

func decideTokenAccount(plan TokenPlan, observed TokenObservationSet, built []builtCase) tokenDecision {
	if decision := decideTokenPhase(plan, observed, built); decision.held() {
		return decision
	}
	if decision := decideTokenTasks(plan, built); decision.held() {
		return decision
	}
	if decision := decideTokenCounts(plan, observed, built); decision.held() {
		return decision
	}
	return holdToken(TokenQualified,
		"the sealed usable-input target passed as a fixed battery",
		"larger targets and untested families remain unmeasured")
}

func decideTokenPhase(plan TokenPlan, observed TokenObservationSet, built []builtCase) tokenDecision {
	if observed.Capacity.ConcurrentLoad == ConcurrentContaminated {
		return holdToken(TokenContaminated, "concurrent load contaminated the measurement",
			"repeat the sealed plan when no other model is resident")
	}
	if !observed.Capacity.AppliedBeforeAllocation || !observed.Capacity.AppliedBeforePhase {
		return holdToken(TokenRefused, "the free-capacity policy was not applied before allocation and again before the phase",
			"apply the timestamped reserve policy before measuring this target")
	}
	if plan.Identity.AccountingSource == AccountingBytePack || observationSource(built, AccountingBytePack) {
		return holdToken(TokenRefused, bytePackReason(plan, observed),
			"measure the sealed usable-input target with native payload and overhead counts")
	}
	if reason := mismatchedIdentity(plan, built); reason != "" {
		return holdToken(TokenRefused, reason, "keep the sealed artifact, template, endpoint and device fingerprint")
	}
	if observationFlag(built) {
		return holdToken(TokenRefused, "truncation or context shift was observed",
			"do not shrink the payload or the window to earn a pass")
	}
	if reason := windowRefusal(plan, built); reason != "" {
		return holdToken(TokenRefused, reason, "seal a new plan at the window the runtime actually enforced")
	}
	if plan.Identity.AccountingSource == AccountingUnknown {
		return holdToken(TokenMissingAccounting, "the accounting source is unknown",
			"bind a validated native accounting source before claiming this target")
	}
	if plan.Identity.KVDtype == "unobserved" {
		return holdToken(TokenMissingAccounting, "KV dtype is unobserved",
			"record the KV dtype the runtime actually used")
	}
	return tokenDecision{}
}

func decideTokenTasks(plan TokenPlan, built []builtCase) tokenDecision {
	if outcomePresent(built, CaseQualityFailed) {
		return holdToken(TokenQualityFailed, "a required case failed on quality, and a passing case does not erase it",
			"do not adopt this target from another position or a higher tier")
	}
	if outcomePresent(built, CaseCapacityLimited) || reservationMiss(plan, built) {
		reason := "a required case was refused for capacity"
		if !outcomePresent(built, CaseCapacityLimited) {
			reason = "the generation reserve does not fit the enforced window"
		}
		return holdToken(TokenCapacityLimited, reason, "do not shrink the target, window or reserve to earn a pass")
	}
	if outcomePresent(built, CaseTransportFailed) {
		return holdToken(TokenTransportFailed, "a transport fault is not a model-quality result",
			"repeat the sealed plan; this fault is not a failed answer")
	}
	if caseMissing(built) || outcomePresent(built, CaseNotTested) {
		return holdToken(TokenNotTested, "a declared case was not run",
			"run the sealed schedule before reading a result for this target")
	}
	return tokenDecision{}
}

func decideTokenCounts(plan TokenPlan, observed TokenObservationSet, built []builtCase) tokenDecision {
	if observed.Capacity.ConcurrentLoad == ConcurrentUnobserved {
		return holdToken(TokenMissingAccounting, "concurrent load is unobserved",
			"record whether any other model was resident before claiming this target")
	}
	if reason := missingCountReason(built); reason != "" {
		return holdToken(TokenMissingAccounting, reason,
			"record payload tokens, template overhead and cached tokens from the bound source")
	}
	if belowTarget(plan, built) {
		return holdToken(TokenRefused, "the observed payload is below the sealed target",
			"do not shrink the target to earn a pass")
	}
	return tokenDecision{}
}

func bytePackRefusal(target int, bytes int) (TokenReport, error) {
	label, err := UsableInputLabel(target)
	if err != nil {
		return TokenReport{}, err
	}
	reason := "a document pack measured in UTF-8 bytes is not a " + label + " usable-input token qualification"
	if bytes > 0 {
		reason = fmt.Sprintf("a %d byte document pack is not a %s usable-input token qualification", bytes, label)
	}
	return TokenReport{
		Schema: TokenReportSchema, TargetUsableInputTokens: target, TargetLabel: label,
		Semantics: SemanticsUsableInput, State: TokenRefused, Reason: reason,
		Uncertainty: TokenUncertainty,
		NextAction:  "measure the sealed usable-input target with native payload and overhead counts",
	}, nil
}

func bytePackReason(plan TokenPlan, observed TokenObservationSet) string {
	bytes := 0
	for _, item := range observed.Cases {
		if item.PayloadUTF8Bytes != nil && *item.PayloadUTF8Bytes > bytes {
			bytes = *item.PayloadUTF8Bytes
		}
	}
	claim, err := bytePackRefusal(plan.TargetUsableInputTokens, bytes)
	if err != nil {
		return "a document pack measured in UTF-8 bytes is not a usable-input token qualification"
	}
	return claim.Reason
}

func mismatchedIdentity(plan TokenPlan, built []builtCase) string {
	for _, item := range built {
		if item.obs == nil {
			continue
		}
		if reason := identityField(plan.Identity, item.obs.Identity); reason != "" {
			return reason
		}
	}
	return ""
}

func identityField(plan, got TokenIdentity) string {
	switch {
	case plan.ArtifactDigest != got.ArtifactDigest:
		return "artifact digest changed"
	case plan.WeightQuant != got.WeightQuant:
		return "weight quant changed"
	case plan.TemplateDigest != got.TemplateDigest:
		return "template or tokenizer changed"
	case plan.AccountingSource != got.AccountingSource:
		return "accounting source changed"
	case plan.RuntimeBuild != got.RuntimeBuild:
		return "runtime build changed"
	case plan.EndpointIdentity != got.EndpointIdentity:
		return "endpoint identity changed"
	case plan.DeviceFingerprint != got.DeviceFingerprint:
		return "device fingerprint changed"
	case plan.KVDtype != got.KVDtype:
		return "KV dtype changed"
	default:
		return ""
	}
}

func windowRefusal(plan TokenPlan, built []builtCase) string {
	seen := false
	window := 0
	for _, item := range built {
		if item.obs == nil || item.obs.EnforcedWindowTokens == nil {
			continue
		}
		value := *item.obs.EnforcedWindowTokens
		if !seen {
			seen = true
			window = value
			continue
		}
		if window != value {
			return "the enforced window changed between cases"
		}
	}
	if !seen {
		return ""
	}
	if window < plan.RequestedWindowTokens {
		return "the enforced window is smaller than the sealed window"
	}
	if window > plan.RequestedWindowTokens {
		return "the enforced window is not the sealed window"
	}
	return ""
}

func reservationMiss(plan TokenPlan, built []builtCase) bool {
	for _, item := range built {
		if item.obs == nil || item.obs.PayloadTokens == nil || item.obs.OverheadTokens == nil || item.obs.EnforcedWindowTokens == nil {
			continue
		}
		if !reservationFits(*item.obs.PayloadTokens, *item.obs.OverheadTokens, plan.GenerationReserveTokens, plan.ReasoningReserveTokens, *item.obs.EnforcedWindowTokens) {
			return true
		}
	}
	return false
}

// reservationFits counts the sealed generation reserve, never the shorter
// number of tokens the model happened to emit.
func reservationFits(payload, overhead, generation, reasoning, window int) bool {
	if payload < 0 || overhead < 0 || generation < 0 || reasoning < 0 || window < 0 {
		return false
	}
	sum := int64(payload) + int64(overhead) + int64(generation) + int64(reasoning)
	return sum <= int64(window)
}

func missingCountReason(built []builtCase) string {
	for _, item := range built {
		if item.obs == nil {
			continue
		}
		switch {
		case item.obs.PayloadTokens == nil:
			return "the payload token count is unmeasured"
		case item.obs.OverheadTokens == nil:
			return "template overhead is unmeasured"
		case !item.obs.CacheObserved || item.obs.CachedTokens == nil:
			return "the cached token count is unmeasured"
		case item.obs.EnforcedWindowTokens == nil:
			return "the enforced window is unmeasured"
		}
	}
	return ""
}

func belowTarget(plan TokenPlan, built []builtCase) bool {
	for _, item := range built {
		if item.obs == nil || item.obs.PayloadTokens == nil || *item.obs.PayloadTokens < plan.TargetUsableInputTokens {
			return true
		}
	}
	return false
}

func observationSource(built []builtCase, source string) bool {
	for _, item := range built {
		if item.obs != nil && item.obs.Identity.AccountingSource == source {
			return true
		}
	}
	return false
}

func observationFlag(built []builtCase) bool {
	for _, item := range built {
		if item.obs != nil && (item.obs.Truncated || item.obs.Shifted) {
			return true
		}
	}
	return false
}

func outcomePresent(built []builtCase, outcome string) bool {
	for _, item := range built {
		if item.result.Outcome == outcome {
			return true
		}
	}
	return false
}

func caseMissing(built []builtCase) bool {
	for _, item := range built {
		if item.obs == nil {
			return true
		}
	}
	return false
}

func freeMemoryLine(obs TokenCapacityObservation) string {
	if obs.FreeBytes == nil {
		if obs.ObservedAt == "" {
			return "free memory was not read; the capacity line is the sealed policy, not a hardware allocation cap"
		}
		return "free memory was unmeasured at " + obs.ObservedAt + "; the sealed policy is not a hardware allocation cap"
	}
	reserve := "unmeasured"
	if obs.ReserveBytes != nil {
		reserve = strconv.FormatInt(*obs.ReserveBytes, 10) + " bytes"
	}
	return fmt.Sprintf("free memory %d bytes at %s; reserve %s", *obs.FreeBytes, obs.ObservedAt, reserve)
}

func minCount(current, next *int) *int {
	if next == nil {
		return current
	}
	if current == nil || *next < *current {
		return copyInt(next)
	}
	return current
}

func sameCount(current *int, same bool, next *int) (*int, bool) {
	if next == nil {
		return current, same
	}
	if current == nil {
		return copyInt(next), same
	}
	if *current != *next {
		return current, false
	}
	return current, same
}

func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func copyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
