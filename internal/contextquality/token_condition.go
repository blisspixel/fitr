package contextquality

import (
	"encoding/json"
	"errors"
)

// Condition identity is a separate receipt from fitr.context-token-account.v1.
// A v1 case id is family, position and seed. Two packs that share those three
// and differ in distractors are different cells here. Nothing in this file
// runs a model, and a qualified fixed battery is not a rate or a maximum
// context. fitr does not submit this schedule to a runtime.

const (
	TokenConditionSchema  = "fitr.context-token-account.v2"
	TokenConditionReport  = "fitr.context-token-account.condition-report.v1"
	ConditionGenerator    = "fitr.context-condition.generator.v1"
	ConditionAnswer       = "kestrel-44"
	ConditionFocused      = "focused_baseline"
	ConditionUnrelated    = "unrelated_filler"
	ConditionTopical      = "topical_distractor"
	ConditionStale        = "stale_corrected"
	ConditionAnswerAbsent = "answer_absent"
	ConditionRetrieval    = "retrieval_policy"
	conditionUncertainty  = "fixed battery; not a confidence interval or a maximum context"
	MaxConditionPlanBytes = 256 << 10
)

var conditionOrder = []string{
	ConditionFocused, ConditionUnrelated, ConditionTopical, ConditionStale, ConditionAnswerAbsent,
}

// TokenConditionPlan is the sealed comparison. PlanSHA256 is computed with
// the field empty, then stored.
type TokenConditionPlan struct {
	Schema                  string               `json:"schema"`
	TargetUsableInputTokens int                  `json:"target_usable_input_tokens"`
	Semantics               string               `json:"semantics"`
	Identity                TokenIdentity        `json:"identity"`
	Generator               string               `json:"generator"`
	RetrievalPolicySHA256   string               `json:"retrieval_policy_sha256,omitempty"`
	Cases                   []TokenConditionCase `json:"cases"`
	PlanSHA256              string               `json:"plan_sha256"`
}

// TokenConditionCase binds the condition and the pack digest into the cell.
// Family, position and seed are not enough to call two packs the same cell.
type TokenConditionCase struct {
	ID            string   `json:"id"`
	Sequence      int      `json:"sequence"`
	Family        Family   `json:"family"`
	Position      Position `json:"position"`
	Seed          uint64   `json:"seed"`
	Condition     string   `json:"condition"`
	ContentSHA256 string   `json:"content_sha256"`
}

// ConditionObservation is one cell the caller observed. A nil token count is
// unmeasured. Qualify does not read a stored verdict from this struct.
type ConditionObservation struct {
	CaseID         string `json:"case_id"`
	Outcome        string `json:"outcome"`
	PayloadTokens  *int   `json:"payload_tokens,omitempty"`
	OverheadTokens *int   `json:"overhead_tokens,omitempty"`
}

// ConditionObservationSet is the phase observation. Order is not authority.
// Each case id must be one sealed cell.
type ConditionObservationSet struct {
	Cases []ConditionObservation `json:"cases"`
}

// ConditionReport is derived. A loader recomputes it and rejects a stored
// copy that disagrees. Qualified is the only passing claim, and it is a
// fixed battery.
type ConditionReport struct {
	Schema                  string                `json:"schema"`
	PlanSHA256              string                `json:"plan_sha256"`
	TargetUsableInputTokens int                   `json:"target_usable_input_tokens"`
	TargetLabel             string                `json:"target_label"`
	Semantics               string                `json:"semantics"`
	State                   string                `json:"state"`
	Reason                  string                `json:"reason"`
	Uncertainty             string                `json:"uncertainty"`
	Passed                  int                   `json:"passed"`
	Failed                  int                   `json:"failed"`
	Unresolved              int                   `json:"unresolved"`
	NextAction              string                `json:"next_action"`
	Cases                   []ConditionCaseResult `json:"cases"`
}

// ConditionCaseResult is one sealed cell after qualification.
type ConditionCaseResult struct {
	CaseID    string   `json:"case_id"`
	Sequence  int      `json:"sequence"`
	Family    Family   `json:"family"`
	Position  Position `json:"position"`
	Seed      uint64   `json:"seed"`
	Condition string   `json:"condition"`
	Outcome   string   `json:"outcome"`
}

// ConditionPack is the text the named condition contributes. The answer is
// present except in the answer-absent pack. The focused pack is not padded
// up to a longer condition.
func ConditionPack(condition, retrievalPolicy string) (string, error) {
	focused := "evidence: " + ConditionAnswer + "\nquestion: which code?\n"
	switch condition {
	case ConditionFocused:
		return focused, nil
	case ConditionUnrelated:
		return focused + "filler: the oak table is brown.\n", nil
	case ConditionTopical:
		return focused + "distractor: kestrel-17 was withdrawn.\n", nil
	case ConditionStale:
		return "stale: kestrel-17\ncorrected: " + ConditionAnswer + "\nquestion: which code?\n", nil
	case ConditionAnswerAbsent:
		return "evidence: withheld\nquestion: which code?\n", nil
	case ConditionRetrieval:
		if !tokenDigest.MatchString(retrievalPolicy) {
			return "", errors.New("retrieval policy digest is missing or not lowercase sha256")
		}
		return focused + "policy: " + retrievalPolicy + "\n", nil
	default:
		return "", errors.New("unknown token condition")
	}
}

// NewMatchedConditionPlan seals one family, position and seed across the
// matched conditions. An empty retrieval digest omits that cell. The caller's
// case slice is not used.
func NewMatchedConditionPlan(plan TokenConditionPlan, family Family, position Position, seed uint64) (TokenConditionPlan, error) {
	names := append([]string{}, conditionOrder...)
	if plan.RetrievalPolicySHA256 != "" {
		names = append(names, ConditionRetrieval)
	}
	cases := make([]TokenConditionCase, 0, len(names))
	for _, name := range names {
		pack, err := ConditionPack(name, plan.RetrievalPolicySHA256)
		if err != nil {
			return TokenConditionPlan{}, err
		}
		sum := hashBytes(ConditionGenerator, []byte(pack))
		cases = append(cases, TokenConditionCase{
			Family: family, Position: position, Seed: seed, Condition: name, ContentSHA256: sum,
		})
	}
	plan.Cases = cases
	return sealConditionPlan(plan)
}

func (plan TokenConditionPlan) Validate() error {
	sealed, err := sealConditionPlan(plan)
	if err != nil {
		return err
	}
	if plan.Schema != sealed.Schema || plan.PlanSHA256 != sealed.PlanSHA256 || !sameConditionCases(plan.Cases, sealed.Cases) {
		return errors.New("token condition plan differs from its sealed schedule")
	}
	if plan.TargetUsableInputTokens != sealed.TargetUsableInputTokens || plan.Semantics != sealed.Semantics ||
		plan.Identity != sealed.Identity || plan.Generator != sealed.Generator ||
		plan.RetrievalPolicySHA256 != sealed.RetrievalPolicySHA256 {
		return errors.New("token condition plan differs from its sealed schedule")
	}
	return nil
}

func (plan TokenConditionPlan) Digest() (string, error) {
	if err := plan.Validate(); err != nil {
		return "", err
	}
	return plan.PlanSHA256, nil
}

func (plan TokenConditionPlan) JSON() ([]byte, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(plan)
}

func (plan TokenConditionPlan) Clone() TokenConditionPlan {
	dup := plan
	dup.Cases = append([]TokenConditionCase(nil), plan.Cases...)
	return dup
}

// DecodeTokenConditionPlan accepts one canonical plan and rejects a v1 token
// plan, a duplicate key, or a schedule that does not match its digest.
func DecodeTokenConditionPlan(data []byte) (TokenConditionPlan, error) {
	var plan TokenConditionPlan
	if err := decodeExact(data, MaxConditionPlanBytes, &plan); err != nil {
		return TokenConditionPlan{}, err
	}
	if err := plan.Validate(); err != nil {
		return TokenConditionPlan{}, err
	}
	return plan, nil
}

func sealConditionPlan(plan TokenConditionPlan) (TokenConditionPlan, error) {
	if err := validateConditionShape(plan); err != nil {
		return TokenConditionPlan{}, err
	}
	sealed := plan
	sealed.Schema = TokenConditionSchema
	sealed.Generator = ConditionGenerator
	sealed.PlanSHA256 = ""
	sealed.Cases = make([]TokenConditionCase, len(plan.Cases))
	for i, item := range plan.Cases {
		pack, err := ConditionPack(item.Condition, plan.RetrievalPolicySHA256)
		if err != nil {
			return TokenConditionPlan{}, err
		}
		content := hashBytes(ConditionGenerator, []byte(pack))
		cell := TokenConditionCase{
			Sequence: i + 1, Family: item.Family, Position: item.Position, Seed: item.Seed,
			Condition: item.Condition, ContentSHA256: content,
		}
		id, err := conditionCaseID(plan.TargetUsableInputTokens, cell)
		if err != nil {
			return TokenConditionPlan{}, err
		}
		cell.ID = id
		sealed.Cases[i] = cell
	}
	sum, err := hashValue(TokenConditionSchema, sealed)
	if err != nil {
		return TokenConditionPlan{}, err
	}
	sealed.PlanSHA256 = sum
	return sealed, nil
}

func validateConditionShape(plan TokenConditionPlan) error {
	if plan.Semantics != SemanticsUsableInput {
		return errors.New("unsupported token condition semantics")
	}
	if plan.TargetUsableInputTokens < 1 || plan.TargetUsableInputTokens > MaxOperatingWindowTokens {
		return errors.New("usable-input target is outside the sealed integer range")
	}
	if err := validateTokenIdentity(plan.Identity); err != nil {
		return err
	}
	if plan.RetrievalPolicySHA256 != "" && !tokenDigest.MatchString(plan.RetrievalPolicySHA256) {
		return errors.New("retrieval policy digest is missing or not lowercase sha256")
	}
	names := expectedConditions(plan.RetrievalPolicySHA256)
	if len(plan.Cases) != len(names) {
		return errors.New("token condition schedule does not match the matched pack")
	}
	first := plan.Cases[0]
	if !knownTokenFamily(first.Family) || !knownTokenPosition(first.Position) {
		return errors.New("token condition case has an unknown family or position")
	}
	for i, name := range names {
		item := plan.Cases[i]
		if item.Condition != name || item.Family != first.Family || item.Position != first.Position || item.Seed != first.Seed {
			return errors.New("token condition schedule does not match the matched pack")
		}
	}
	return nil
}

func expectedConditions(retrieval string) []string {
	names := append([]string{}, conditionOrder...)
	if retrieval != "" {
		names = append(names, ConditionRetrieval)
	}
	return names
}

func conditionCaseID(target int, item TokenConditionCase) (string, error) {
	return hashValue("fitr.context-token-account.condition.v1", struct {
		Target        int      `json:"target_usable_input_tokens"`
		Sequence      int      `json:"sequence"`
		Family        Family   `json:"family"`
		Position      Position `json:"position"`
		Seed          uint64   `json:"seed"`
		Condition     string   `json:"condition"`
		ContentSHA256 string   `json:"content_sha256"`
		Generator     string   `json:"generator"`
	}{target, item.Sequence, item.Family, item.Position, item.Seed, item.Condition, item.ContentSHA256, ConditionGenerator})
}

func sameConditionCases(left, right []TokenConditionCase) bool {
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
