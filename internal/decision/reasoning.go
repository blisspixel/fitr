package decision

import (
	"errors"
	"strconv"
	"strings"
)

const ReasoningAccountSchema = "fitr.reasoning-account.v1"

// ReasoningRequest is the effort the operator asked for. An empty Effort is
// omitted. It is not filled with a model-specific default.
type ReasoningRequest struct {
	Effort string `json:"effort,omitempty"`
}

// ReasoningAccount keeps a requested effort apart from an applied mode.
// Application stays unknown until a protocol records that the mode was
// applied. A capability flag, API acceptance, elapsed time, output length,
// and a self-report are not that protocol.
type ReasoningAccount struct {
	Schema      string `json:"schema"`
	Requested   string `json:"requested,omitempty"`
	Application string `json:"application"`
	Note        string `json:"note"`
}

func (r *ReasoningRequest) validate() error {
	if r == nil {
		return nil
	}
	if strings.TrimSpace(r.Effort) != r.Effort {
		return errors.New("reasoning effort cannot have leading or trailing whitespace")
	}
	if strings.ContainsAny(r.Effort, " \t") {
		return errors.New("reasoning effort must be one token")
	}
	return nil
}

func reasoningRequirement(spec DecisionSpec) (RequirementResult, ReasoningAccount, bool) {
	if spec.Reasoning == nil {
		return RequirementResult{}, ReasoningAccount{}, false
	}
	account := reasoningAccount(spec.Reasoning.Effort)
	return RequirementResult{
		ID:      reasoningRequirementID(spec.Requirements),
		State:   RequirementUnresolved,
		Reason:  account.Note,
		Missing: []string{"an observation that the requested effort was applied"},
		NextAction: &Action{
			Code:   "record_reasoning_application",
			Reason: "record whether the requested effort was applied. a capability flag is not that observation",
		},
	}, account, true
}

func reasoningRequirementID(requirements []Requirement) string {
	used := make(map[string]bool, len(requirements))
	for _, requirement := range requirements {
		used[requirement.ID] = true
	}
	id := "reasoning_application"
	for suffix := 2; used[id]; suffix++ {
		id = "reasoning_application_" + strconv.Itoa(suffix)
	}
	return id
}

func reasoningAccount(requested string) ReasoningAccount {
	note := "application was not observed. a capability declaration, api acceptance, elapsed time, output length, and self-report are not an applied mode."
	if requested == "" {
		note += " an omitted effort is not filled with a default."
	}
	return ReasoningAccount{
		Schema: ReasoningAccountSchema, Requested: requested,
		Application: "unknown", Note: note,
	}
}

// CertifyEffortComparison refuses every pair. No accepted provenance records
// an applied mode, so two unknown runs cannot be treated as the same effort
// and a hand-labeled observation cannot certify one either.
func CertifyEffortComparison(_, _ ReasoningAccount) error {
	return errors.New("effort comparison is not certified: application is unknown. no observation protocol records an applied mode")
}
