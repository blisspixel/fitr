package workload

import (
	"errors"
	"strings"
)

var errPiPreservation = errors.New("pi-workspace preservation schedule does not match its contract")

// Fact preservation is sealed into a new pi-workspace plan. The verifier
// reads the workspace files, not the summary text. A historical plan has no
// preservation schedule and keeps the original six checks, so a receipt
// sealed before this schedule still validates.
const (
	piPreservationSchema   = "fitr.pi-preservation.v1"
	piCorrectionFile       = "CORRECTION.txt"
	piCurrentPortLine      = "port=8443"
	piStalePortLine        = "port=8080"
	piRecallLine           = "recall=unresolved"
	piContextActionSummary = "model_generated_summary"
	piTaskPreservedOpen    = "status=open\nport=8080\nrecall=unresolved\n"
	piTaskPreserved        = "status=done\nport=8443\nrecall=unresolved\n"
	piCorrectionBody       = "port=8443\nrecall=unresolved\n"

	// The prompts name the source file. They do not carry the port or the
	// recall value. Those stay in CORRECTION.txt and in the verifier.
	piPreservedPrompt = "Read CORRECTION.txt. Write TASK.txt once with status=done, the current port from that file, " +
		"and recall left unresolved. Do not modify NOTE.txt. A summary is not the task file. " +
		"After the session reopens, do not write TASK.txt again. When the file is already corrected, reply DONE with no tool calls."
	piPreservedSummaryPrompt = "Summarize the session so far. Do not call tools. Do not invent a port or close a failed recall."
	piPreservedReopenPrompt  = "The session reopened from the sealed checkpoint. Do not repeat a completed write. " +
		"If TASK.txt already matches CORRECTION.txt, reply DONE with no tool calls."
)

// PreservationSchedule is the one fact set a new pi-workspace plan seals.
// Reductions is the pinned split, two summaries and one reopen, not a second
// cycle. A longer sequence is not this receipt.
type PreservationSchedule struct {
	Schema      string `json:"schema"`
	CurrentPort string `json:"current_port"`
	StalePort   string `json:"stale_port"`
	Recall      string `json:"recall"`
	Reductions  int    `json:"reductions"`
}

func pinnedPreservation() PreservationSchedule {
	return PreservationSchedule{
		Schema: piPreservationSchema, CurrentPort: piCurrentPortLine,
		StalePort: piStalePortLine, Recall: piRecallLine, Reductions: 1,
	}
}

func (plan Plan) PreservesFacts() bool {
	return plan.Preservation != nil
}

func piSessionPrompts(plan Plan) (string, string, string) {
	if plan.PreservesFacts() {
		return piPreservedPrompt, piPreservedSummaryPrompt, piPreservedReopenPrompt
	}
	return piPrompt, piSummaryPrompt, piReopenPrompt
}

func piDoneBody(plan Plan) string {
	if plan.PreservesFacts() {
		return piTaskPreserved
	}
	return piTaskDone
}

func piPreservedContract() (WorkflowContract, error) {
	return piScenarioContract(piPreservedPrompt, map[string]string{
		piNoteFile: piNoteBody, piTaskFile: piTaskPreservedOpen, piCorrectionFile: piCorrectionBody,
	}, piTaskPreserved)
}

func piHistoricalContract() (WorkflowContract, error) {
	return piScenarioContract(piPrompt, map[string]string{
		piNoteFile: piNoteBody, piTaskFile: piTaskOpen,
	}, piTaskDone)
}

func piScenarioContract(prompt string, files map[string]string, done string) (WorkflowContract, error) {
	scenario, err := hashValue("fitr.workload.scenario.v1", struct {
		Prompt string            `json:"prompt"`
		Files  map[string]string `json:"files"`
		Done   string            `json:"done"`
	}{prompt, files, done})
	if err != nil {
		return WorkflowContract{}, err
	}
	tools, err := hashValue("fitr.workload.tools.v1", piWorkspaceTools())
	if err != nil {
		return WorkflowContract{}, err
	}
	return WorkflowContract{
		Schema: "fitr.workload.contract.v1", ScenarioSHA256: scenario, ToolsSHA256: tools,
		Verifier: piVerifierID, Proof: EvidenceIndependent, Authority: "virtual-workspace-files-only",
		Isolation: "harness-capability-boundary", RetryPolicy: "one-attempt-no-retry",
		ApprovalPolicy: "unsupported", ContextPolicy: "requested-only",
		CompactionPolicy: "forced-split-summary",
	}, nil
}

func validatePiPreservation(plan Plan) error {
	preserved, err := piPreservedContract()
	if err != nil {
		return err
	}
	historical, err := piHistoricalContract()
	if err != nil {
		return err
	}
	switch {
	case plan.Contract != nil && *plan.Contract == preserved:
		if plan.Preservation == nil || *plan.Preservation != pinnedPreservation() {
			return errPiPreservation
		}
	case plan.Contract != nil && *plan.Contract == historical:
		if plan.Preservation != nil {
			return errPiPreservation
		}
	default:
		return errPiPreservation
	}
	return nil
}

func prefixedLine(content, prefix string) (string, bool) {
	found := ""
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		if found != "" {
			return "", false
		}
		found = line
	}
	if found == "" {
		return "", false
	}
	return found, true
}

func lineDigest(domain, line string, ok bool) string {
	if !ok || line == "" {
		return ""
	}
	digest, err := hashValue(domain, line)
	if err != nil {
		return ""
	}
	return digest
}
