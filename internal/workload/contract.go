package workload

// EvidenceClass describes the source of a claim, independently of whether the
// observation passed. Protocol success and worker self-report are not proof.
type EvidenceClass string

const (
	EvidenceDeterministic EvidenceClass = "deterministic_assertion"
	EvidenceExternalState EvidenceClass = "external_system_state"
	EvidenceIndependent   EvidenceClass = "independent_verifier"
	EvidenceHarness       EvidenceClass = "harness_state_machine"
	EvidenceHeuristic     EvidenceClass = "heuristic"
	EvidenceModelJudged   EvidenceClass = "model_judged"
	EvidenceSelfReported  EvidenceClass = "self_reported"
	EvidenceNone          EvidenceClass = "none"
)

func (class EvidenceClass) Valid() bool {
	switch class {
	case EvidenceDeterministic, EvidenceExternalState, EvidenceIndependent,
		EvidenceHarness, EvidenceHeuristic, EvidenceModelJudged, EvidenceSelfReported, EvidenceNone:
		return true
	default:
		return false
	}
}

// CanEstablishCompletion is necessary but not sufficient: each workflow must
// also authorize the particular verifier and bind its independently checked state.
func (class EvidenceClass) CanEstablishCompletion() bool {
	return class == EvidenceDeterministic || class == EvidenceExternalState || class == EvidenceIndependent
}

// WorkflowContract records the fixed authority and proof boundary before any
// model generation. It is a declaration, not permission to execute extensions.
type WorkflowContract struct {
	Schema         string        `json:"schema"`
	ScenarioSHA256 string        `json:"scenario_sha256"`
	ToolsSHA256    string        `json:"tools_sha256"`
	Verifier       string        `json:"verifier"`
	Proof          EvidenceClass `json:"proof"`
	Authority      string        `json:"authority"`
	Isolation      string        `json:"isolation"`
	RetryPolicy    string        `json:"retry_policy"`
	ApprovalPolicy string        `json:"approval_policy"`
	ContextPolicy  string        `json:"context_policy"`
}

func policyRepairContract() WorkflowContract {
	state := newWorkflowState()
	scenario, _ := hashValue("fitr.workload.scenario.v1", struct {
		Prompt string            `json:"prompt"`
		Files  map[string]string `json:"files"`
	}{workflowPrompt, state.files})
	tools, _ := hashValue("fitr.workload.tools.v1", workflowTools())
	return WorkflowContract{
		Schema: "fitr.workload.contract.v1", ScenarioSHA256: scenario, ToolsSHA256: tools,
		Verifier: "policy-repair/deterministic/v1", Proof: EvidenceDeterministic,
		Authority: "virtual-policy-json-only", Isolation: "harness-capability-boundary",
		RetryPolicy: "one-attempt-no-retry", ApprovalPolicy: "unsupported",
		ContextPolicy: "requested-only",
	}
}
