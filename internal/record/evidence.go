package record

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

const (
	LegacyCompletionReceiptSchema = "fitr.evidence.completion.v1"
	CompletionReceiptSchema       = "fitr.evidence.completion.v2"
)

// CompletionReceipt binds everything learned after the immutable run manifest
// was sealed. Profile is the exact effective profile, not a mutable name lookup.
type CompletionReceipt struct {
	Schema         string         `json:"schema"`
	EvidenceSHA256 string         `json:"evidence_sha256"`
	Signature      string         `json:"signature"`
	Profile        device.Profile `json:"profile"`
}

type completedEvidencePayload struct {
	ManifestSHA256    string                         `json:"manifest_sha256"`
	RunID             string                         `json:"run_id"`
	WallSeconds       float64                        `json:"wall_s"`
	ModelMeta         ollama.ModelInfo               `json:"model_meta"`
	Speed             []eval.SpeedResult             `json:"speed_repeats"`
	DecodeSum         stats.Summary                  `json:"decode_summary"`
	TTFTSum           stats.Summary                  `json:"ttft_summary"`
	PrefillSum        stats.Summary                  `json:"prefill_summary"`
	Memory            eval.MemoryResult              `json:"memory"`
	CodeWrite         []eval.ExecResult              `json:"code_write"`
	CodeFix           []eval.ExecResult              `json:"code_fix"`
	Checks            []eval.CheckOutcome            `json:"checks"`
	Tools             []eval.ToolLoopResult          `json:"tools"`
	Withdrawal        *eval.ToolLoopResult           `json:"withdrawal"`
	Agentic           *eval.ToolLoopResult           `json:"agentic"`
	Refusal           map[string]eval.RefusalVerdict `json:"refusal"`
	Refused           int                            `json:"refused"`
	Plumbing          *eval.PlumbingResult           `json:"plumbing"`
	Rep               score.Repetition               `json:"repetition"`
	Density           score.Density                  `json:"density"`
	Contamination     []string                       `json:"contamination"`
	EvidenceCounts    map[string]eval.OutcomeCounts  `json:"evidence_counts"`
	AdaptiveDecisions []eval.AdaptiveDecision        `json:"adaptive_decisions"`
	Scorecard         score.Scorecard                `json:"scorecard"`
	Profile           device.Profile                 `json:"profile"`
}

// CompleteEvidence seals a finished current-schema run. It must be called exactly
// once after derived summaries and Scorecard are final, and before Store.Save.
func (r *Record) CompleteEvidence(profile device.Profile) error {
	if r == nil {
		return errors.New("cannot complete nil evidence")
	}
	if r.Completion != nil {
		return errors.New("evidence is already completed")
	}
	if r.SchemaVersion != EvidenceSchemaVersion || r.Manifest == nil || r.Manifest.Schema != RunManifestSchema {
		return fmt.Errorf("completion requires a schema-%d reproducibility manifest", EvidenceSchemaVersion)
	}
	if len(r.completionPrivateKey) != ed25519.PrivateKeySize {
		return errors.New("completion signing key is unavailable")
	}
	profileCopy, err := cloneProfile(profile)
	if err != nil {
		return err
	}
	if err := r.validateDerivedEvidence(); err != nil {
		return err
	}
	if err := r.validateProfileSnapshot(profileCopy); err != nil {
		return err
	}
	if err := r.validateScorecard(profileCopy); err != nil {
		return err
	}
	payload, err := r.completedEvidenceJSON(profileCopy)
	if err != nil {
		return err
	}
	digest := digestBytes("fitr.completed-evidence.v2", payload)
	r.Completion = &CompletionReceipt{
		Schema: CompletionReceiptSchema, EvidenceSHA256: digest,
		Signature: base64.RawStdEncoding.EncodeToString(ed25519.Sign(r.completionPrivateKey, payload)),
		Profile:   profileCopy,
	}
	r.completionPrivateKey = nil
	return r.validateCompletedEvidence()
}

func (r *Record) validateCompletedEvidence() error {
	if r.Completion == nil {
		return errors.New("completed evidence receipt is missing")
	}
	c := r.Completion
	if c.Schema != CompletionReceiptSchema {
		return fmt.Errorf("unsupported completion receipt schema %q", c.Schema)
	}
	if err := r.validateDerivedEvidence(); err != nil {
		return err
	}
	if err := r.validateProfileSnapshot(c.Profile); err != nil {
		return err
	}
	if err := r.validateScorecard(c.Profile); err != nil {
		return err
	}
	payload, err := r.completedEvidenceJSON(c.Profile)
	if err != nil {
		return err
	}
	want := digestBytes("fitr.completed-evidence.v2", payload)
	if !sha256Digest.MatchString(c.EvidenceSHA256) || subtle.ConstantTimeCompare([]byte(want), []byte(strings.ToLower(c.EvidenceSHA256))) != 1 {
		return errors.New("completed evidence does not match its receipt")
	}
	publicKey, err := base64.RawStdEncoding.DecodeString(r.Manifest.CompletionPublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("completion manifest public key is invalid")
	}
	signature, err := base64.RawStdEncoding.DecodeString(c.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, payload, signature) {
		return errors.New("completed evidence signature is invalid")
	}
	return nil
}

func (r *Record) validateProfileSnapshot(profile device.Profile) error {
	if strings.TrimSpace(profile.Name) == "" || profile.Name != r.Profile {
		return errors.New("completed profile snapshot does not match the selected profile")
	}
	if r.Manifest == nil || r.Manifest.Provenance == nil {
		return errors.New("completed profile snapshot has no provenance receipt")
	}
	digest, err := canonicalJSONDigest("fitr.profile.v1", profile)
	if err != nil {
		return fmt.Errorf("hash completed profile snapshot: %w", err)
	}
	if digest != r.Manifest.Provenance.ProfileSHA256 {
		return errors.New("completed profile snapshot differs from provenance")
	}
	return nil
}

func (r *Record) validateScorecard(profile device.Profile) error {
	if r.Manifest == nil || r.Manifest.Provenance == nil {
		return errors.New("persisted scorecard has no scoring policy receipt")
	}
	currentHash, err := canonicalJSONDigest("fitr.scoring-policy.v1", CurrentScoringPolicy())
	if err != nil {
		return fmt.Errorf("hash current scoring policy: %w", err)
	}
	legacyHash, err := canonicalJSONDigest("fitr.scoring-policy.v1", legacyScoringPolicyV3())
	if err != nil {
		return fmt.Errorf("hash legacy scoring policy: %w", err)
	}
	legacyV4Hash, err := canonicalJSONDigest("fitr.scoring-policy.v1", legacyScoringPolicyV4())
	if err != nil {
		return fmt.Errorf("hash legacy v4 scoring policy: %w", err)
	}
	var expected score.Scorecard
	switch r.Manifest.Provenance.ScoringPolicySHA256 {
	case currentHash:
		expected = score.Score(r.Measured(), profile)
	case legacyHash:
		expected = score.ScoreLegacyV3(r.Measured(), profile)
	case legacyV4Hash:
		expected = score.ScoreLegacyV4(r.Measured(), profile)
	default:
		return errors.New("persisted scorecard uses an unsupported scoring policy")
	}
	if !reflect.DeepEqual(expected, r.Scorecard) {
		return errors.New("persisted scorecard does not match sealed raw evidence and profile")
	}
	return nil
}

func (r *Record) completedEvidenceJSON(profile device.Profile) ([]byte, error) {
	manifestSHA256 := ""
	if r.Manifest != nil {
		manifestSHA256 = r.Manifest.ManifestSHA256
	}
	return json.Marshal(completedEvidencePayload{
		ManifestSHA256: manifestSHA256,
		RunID:          r.RunID, WallSeconds: r.WallSeconds, ModelMeta: r.ModelMeta,
		Speed: r.Speed, DecodeSum: r.DecodeSum, TTFTSum: r.TTFTSum, PrefillSum: r.PrefillSum,
		Memory: r.Memory, CodeWrite: r.CodeWrite, CodeFix: r.CodeFix, Checks: r.Checks,
		Tools: r.Tools, Withdrawal: r.Withdrawal, Agentic: r.Agentic, Refusal: r.Refusal,
		Refused: r.Refused, Plumbing: r.Plumbing, Rep: r.Rep, Density: r.Density,
		Contamination: r.Contamination, EvidenceCounts: r.EvidenceCounts,
		AdaptiveDecisions: r.AdaptiveDecisions, Scorecard: r.Scorecard, Profile: profile,
	})
}

func (r *Record) validateDerivedEvidence() error {
	if r.WallSeconds < 0 || math.IsNaN(r.WallSeconds) || math.IsInf(r.WallSeconds, 0) {
		return errors.New("run wall time is not a finite non-negative measurement")
	}
	if err := r.validateExecutorEvidence(); err != nil {
		return err
	}
	if err := r.validatePlannedObservations(); err != nil {
		return err
	}
	if err := r.validateEvidenceCounts(); err != nil {
		return err
	}
	if err := r.validateSpeedEvidence(); err != nil {
		return err
	}
	if err := r.validateRefusalEvidence(); err != nil {
		return err
	}
	if err := r.validateOutputSummaries(); err != nil {
		return err
	}
	if r.Scorecard.Model != r.Model || r.Scorecard.Profile != r.Profile {
		return errors.New("scorecard identity does not match the completed run")
	}
	return nil
}

func (r *Record) validatePlannedObservations() error {
	if len(r.Checks) != r.TaskPlan.CheckTrialsLimit {
		return fmt.Errorf("check observations %d do not match planned %d", len(r.Checks), r.TaskPlan.CheckTrialsLimit)
	}
	if r.TaskPlan.CheckTrialsLimit > 0 {
		observedPlan, err := ObservedCheckPlanSHA256(r.Checks)
		if err != nil {
			return err
		}
		if observedPlan != r.TaskPlan.CheckPlanSHA256 {
			return errors.New("generated-check observations do not match the sealed plan")
		}
	}
	if len(r.Refusal) != r.TaskPlan.RefusalTrials {
		return fmt.Errorf("refusal observations %d do not match planned %d", len(r.Refusal), r.TaskPlan.RefusalTrials)
	}
	if r.TaskPlan.RefusalTrials > 0 {
		observedPlan, err := ObservedRefusalPlanSHA256(r.Refusal)
		if err != nil {
			return err
		}
		if observedPlan != r.TaskPlan.RefusalPlanSHA256 {
			return errors.New("refusal observations do not match the sealed plan")
		}
	}
	return nil
}

func (r *Record) validateEvidenceCounts() error {
	counts, err := r.DeriveEvidenceCounts()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(counts, r.EvidenceCounts) {
		return errors.New("persisted evidence counts do not match raw observations")
	}
	return nil
}

func (r *Record) validateSpeedEvidence() error {
	if len(r.Speed) != r.TaskPlan.SpeedSamples {
		return fmt.Errorf("speed observations %d do not match planned %d", len(r.Speed), r.TaskPlan.SpeedSamples)
	}
	decode, ttft, prefill := make([]float64, 0, len(r.Speed)), make([]float64, 0, len(r.Speed)), make([]float64, 0, len(r.Speed))
	for i, sample := range r.Speed {
		if err := validateSpeedSample(i, sample); err != nil {
			return err
		}
		decode, ttft, prefill = append(decode, sample.DecodeTPS), append(ttft, sample.TTFT), append(prefill, sample.PrefillTPS)
	}
	if r.DecodeSum != stats.MeanSD(decode) || r.TTFTSum != stats.MeanSD(ttft) || r.PrefillSum != stats.MeanSD(prefill) {
		return errors.New("persisted speed summaries do not match raw observations")
	}
	return nil
}

func validateSpeedSample(i int, sample eval.SpeedResult) error {
	measurements := []float64{
		sample.DecodeTPS, sample.TTFT, sample.GatedLoad, sample.ColdTTFT, sample.ColdLoad,
		sample.WarmTTFT, sample.PrefillTPS,
	}
	for _, measurement := range measurements {
		if math.IsNaN(measurement) || math.IsInf(measurement, 0) {
			return fmt.Errorf("speed observation %d contains a non-finite measurement", i)
		}
	}
	if hasNegativeSpeedMeasurement(sample) {
		return fmt.Errorf("speed observation %d contains a negative measurement", i)
	}
	if !sample.FirstOutputObserved {
		return fmt.Errorf("speed observation %d has no first-output receipt", i)
	}
	if sample.ColdTTFT > 0 && sample.ColdLoad <= 0.1 {
		return fmt.Errorf("speed observation %d labels cold TTFT without a cold-load receipt", i)
	}
	if !sample.GatedLoadKnown && sample.GatedLoad != 0 {
		return fmt.Errorf("speed observation %d has gated load duration without a load-duration receipt", i)
	}
	if !sample.GatedResidencyKnown && sample.GatedResident {
		return fmt.Errorf("speed observation %d claims gated residency without a status receipt", i)
	}
	if sample.GatedCacheKnown && !sample.GatedCacheReceiptValid() {
		return fmt.Errorf("speed observation %d has an empty gated cache receipt", i)
	}
	if sample.PrefillCacheKnown && !sample.PrefillCacheReceiptValid() {
		return fmt.Errorf("speed observation %d has an empty prefill cache receipt", i)
	}
	if sample.WarmCacheKnown && !sample.WarmCacheReceiptValid() {
		return fmt.Errorf("speed observation %d has an invalid warm cache receipt", i)
	}
	if sample.WarmTTFT > 0 && !sample.WarmCacheReceiptValid() {
		return fmt.Errorf("speed observation %d labels warm TTFT without a cache-hit receipt", i)
	}
	if hasUnreceiptedCachedTokens(sample) {
		return fmt.Errorf("speed observation %d has cached tokens without a cache receipt", i)
	}
	return nil
}

func hasNegativeSpeedMeasurement(sample eval.SpeedResult) bool {
	return sample.DecodeTPS < 0 || sample.TTFT < 0 || sample.GatedLoad < 0 || sample.ColdTTFT < 0 || sample.ColdLoad < 0 ||
		sample.WarmTTFT < 0 || sample.PrefillTPS < 0 || sample.PromptTok < 0 ||
		sample.CachedPromptTok < 0 || sample.GatedCachedTok < 0 || sample.GatedPromptTok < 0 ||
		sample.WarmPromptTok < 0 || sample.WarmCachedTok < 0
}

func hasUnreceiptedCachedTokens(sample eval.SpeedResult) bool {
	return !sample.GatedCacheKnown && sample.GatedCachedTok != 0 ||
		!sample.PrefillCacheKnown && sample.CachedPromptTok != 0 ||
		!sample.WarmCacheKnown && sample.WarmCachedTok != 0
}

func (r *Record) validateRefusalEvidence() error {
	refused := 0
	for key, verdict := range r.Refusal {
		if verdict.Outcome == eval.OutcomePass && verdict.Verdict != "answered" ||
			verdict.Outcome == eval.OutcomeFail && verdict.Verdict == "answered" {
			return fmt.Errorf("refusal outcome %q disagrees with its verdict", key)
		}
		if verdict.Outcome == eval.OutcomeFail {
			refused++
		}
	}
	if refused != r.Refused {
		return errors.New("refused count does not match raw refusal outcomes")
	}
	return nil
}

func (r *Record) validateOutputSummaries() error {
	longest := ""
	for _, result := range append(append([]eval.ExecResult{}, r.CodeWrite...), r.CodeFix...) {
		if len(result.Raw) > len(longest) {
			longest = result.Raw
		}
	}
	for _, verdict := range r.Refusal {
		if len(verdict.Text) > len(longest) {
			longest = verdict.Text
		}
	}
	if r.Rep != score.RepetitionMetrics(longest) || r.Density != score.InformationDensity(longest) {
		return errors.New("persisted output summaries do not match raw observations")
	}
	return nil
}

func (r *Record) validateExecutorEvidence() error {
	if r == nil || r.Manifest == nil || r.Manifest.Schema != RunManifestSchema {
		return nil
	}
	if r.Manifest.ExecutionPolicy != ExecutionUnsafe {
		return r.validateDisabledExecutorEvidence()
	}
	if r.Manifest.Executor == nil {
		return errors.New("unsafe evidence is missing its manifest executor receipt")
	}
	return r.validateUnsafeExecutorEvidence(*r.Manifest.Executor)
}

func (r *Record) toolLoopResults() []eval.ToolLoopResult {
	results := append([]eval.ToolLoopResult{}, r.Tools...)
	if r.Withdrawal != nil {
		results = append(results, *r.Withdrawal)
	}
	if r.Agentic != nil {
		results = append(results, *r.Agentic)
	}
	return results
}

func (r *Record) validateDisabledExecutorEvidence() error {
	for _, result := range append(append([]eval.ExecResult{}, r.CodeWrite...), r.CodeFix...) {
		if result.Verifier != nil {
			return errors.New("disabled execution record contains a verifier observation")
		}
	}
	for _, result := range r.toolLoopResults() {
		if result.Verifier != nil || len(result.VerifierObservations) != 0 {
			return errors.New("disabled execution record contains a verifier observation")
		}
	}
	return nil
}

func (r *Record) validateUnsafeExecutorEvidence(executor eval.ExecutorReceipt) error {
	for i, result := range append(append([]eval.ExecResult{}, r.CodeWrite...), r.CodeFix...) {
		if err := validateExecutorReceipt(fmt.Sprintf("coding verifier %d", i), result.Verifier, executor); err != nil {
			return err
		}
	}
	for i, result := range r.toolLoopResults() {
		if err := validateExecutorReceipt(fmt.Sprintf("tool verifier %d", i), result.Verifier, executor); err != nil {
			return err
		}
		for j := range result.VerifierObservations {
			label := fmt.Sprintf("tool verifier %d observation %d", i, j)
			if err := validateExecutorReceipt(label, &result.VerifierObservations[j], executor); err != nil {
				return err
			}
		}
		if result.Verifier != nil && len(result.VerifierObservations) == 0 {
			return fmt.Errorf("tool verifier %d is missing its verifier observation history", i)
		}
	}
	return nil
}

func validateExecutorReceipt(label string, receipt *eval.VerificationReceipt, executor eval.ExecutorReceipt) error {
	if receipt == nil {
		return nil
	}
	if err := receipt.ValidateExecutor(executor); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

// DeriveEvidenceCounts reconstructs every immutable denominator from the raw
// observations. The runner and the persisted-record validator call this same
// function so the producer cannot silently use weaker counting rules than the
// verifier.
func (r *Record) DeriveEvidenceCounts() (map[string]eval.OutcomeCounts, error) {
	values, err := r.deriveEvidenceOutcomes()
	if err != nil {
		return nil, err
	}
	phases := []struct {
		name     string
		expected int
	}{
		{"coding", r.TaskPlan.CodeTrials}, {"checks", r.TaskPlan.CheckTrialsLimit},
		{"tools", r.TaskPlan.ToolTrials}, {"refusal", r.TaskPlan.RefusalTrials},
		{"plumbing", boolCount(r.TaskPlan.Plumbing)}, {"withdrawal", boolCount(r.TaskPlan.Withdrawal)},
		{"agentic", r.TaskPlan.AgenticTrials},
	}
	result := make(map[string]eval.OutcomeCounts, len(phases))
	for _, phase := range phases {
		counts, err := collectEvidenceOutcomes(phase.name, phase.expected, values[phase.name])
		if err != nil {
			return nil, err
		}
		result[phase.name] = counts
	}
	return result, nil
}

func (r *Record) deriveEvidenceOutcomes() (map[string][]eval.Outcome, error) {
	coding, err := execOutcomes("coding", append(append([]eval.ExecResult{}, r.CodeWrite...), r.CodeFix...))
	if err != nil {
		return nil, err
	}
	checks, err := checkOutcomes(r.Checks)
	if err != nil {
		return nil, err
	}
	tools, err := toolOutcomes("tool", r.Tools)
	if err != nil {
		return nil, err
	}
	refusal := make([]eval.Outcome, 0, len(r.Refusal))
	for _, value := range r.Refusal {
		refusal = append(refusal, value.Outcome)
	}
	values := map[string][]eval.Outcome{
		"coding": coding, "checks": checks, "tools": tools, "refusal": refusal,
		"plumbing": optionalPlumbingOutcome(r.Plumbing),
	}
	if values["withdrawal"], err = optionalToolOutcome("withdrawal observation", r.Withdrawal); err != nil {
		return nil, err
	}
	if values["agentic"], err = optionalToolOutcome("agentic observation", r.Agentic); err != nil {
		return nil, err
	}
	return values, nil
}

func execOutcomes(phase string, results []eval.ExecResult) ([]eval.Outcome, error) {
	values := make([]eval.Outcome, 0, len(results))
	for i, result := range results {
		if err := validatePassFlag(fmt.Sprintf("%s observation %d", phase, i), result.Outcome, result.Pass); err != nil {
			return nil, err
		}
		values = append(values, result.Outcome)
	}
	return values, nil
}

func checkOutcomes(results []eval.CheckOutcome) ([]eval.Outcome, error) {
	values := make([]eval.Outcome, 0, len(results))
	for i, result := range results {
		if err := validatePassFlag(fmt.Sprintf("check observation %d", i), result.Outcome, result.Pass); err != nil {
			return nil, err
		}
		values = append(values, result.Outcome)
	}
	return values, nil
}

func toolOutcomes(phase string, results []eval.ToolLoopResult) ([]eval.Outcome, error) {
	values := make([]eval.Outcome, 0, len(results))
	for i, result := range results {
		if err := validatePassFlag(fmt.Sprintf("%s observation %d", phase, i), result.Outcome, result.Pass); err != nil {
			return nil, err
		}
		values = append(values, result.Outcome)
	}
	return values, nil
}

func optionalPlumbingOutcome(result *eval.PlumbingResult) []eval.Outcome {
	if result == nil {
		return nil
	}
	return []eval.Outcome{result.Outcome}
}

func optionalToolOutcome(label string, result *eval.ToolLoopResult) ([]eval.Outcome, error) {
	if result == nil {
		return nil, nil
	}
	if err := validatePassFlag(label, result.Outcome, result.Pass); err != nil {
		return nil, err
	}
	return []eval.Outcome{result.Outcome}, nil
}

func collectEvidenceOutcomes(phase string, expected int, values []eval.Outcome) (eval.OutcomeCounts, error) {
	for _, value := range values {
		switch value {
		case eval.OutcomePass, eval.OutcomeFail, eval.OutcomeInconclusive, eval.OutcomeError, eval.OutcomeSkipped:
		default:
			return eval.OutcomeCounts{}, fmt.Errorf("%s contains unknown outcome %q", phase, value)
		}
	}
	return eval.CountOutcomes(expected, values...), nil
}

func validatePassFlag(label string, outcome eval.Outcome, pass bool) error {
	if outcome == eval.OutcomePass && !pass || outcome == eval.OutcomeFail && pass {
		return fmt.Errorf("%s outcome disagrees with pass flag", label)
	}
	return nil
}

func cloneProfile(profile device.Profile) (device.Profile, error) {
	b, err := json.Marshal(profile)
	if err != nil {
		return device.Profile{}, fmt.Errorf("copy profile snapshot: %w", err)
	}
	var out device.Profile
	if err := json.Unmarshal(b, &out); err != nil {
		return device.Profile{}, fmt.Errorf("copy profile snapshot: %w", err)
	}
	return out, nil
}

// OriginalProfile returns an isolated copy of the exact profile used to score
// this run, after the completion signature and provenance hash are verified.
func (r *Record) OriginalProfile() (device.Profile, error) {
	if err := r.ValidateEvidenceContract(); err != nil {
		return device.Profile{}, err
	}
	return cloneProfile(r.Completion.Profile)
}

// ProvenanceCompatibilityError rejects definition drift between two records.
func ProvenanceCompatibilityError(a, b *Record) error {
	if a == nil || b == nil || a.Manifest == nil || b.Manifest == nil || a.Manifest.Provenance == nil || b.Manifest.Provenance == nil {
		return errors.New("run provenance is unavailable")
	}
	if err := a.ValidateEvidenceContract(); err != nil {
		return fmt.Errorf("first record: %w", err)
	}
	if err := b.ValidateEvidenceContract(); err != nil {
		return fmt.Errorf("second record: %w", err)
	}
	return a.Manifest.Provenance.CompatibilityError(*b.Manifest.Provenance)
}

func digestBytes(domain string, payload []byte) string {
	return digestToken(append(append([]byte(domain), 0), payload...))
}

func digestToken(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}
