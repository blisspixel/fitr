package workload

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/blisspixel/fitr/internal/record"
)

const (
	// A split turn in the pinned compaction contract produces a history
	// summary and a turn-prefix summary. Both are model requests.
	piOrdinaryCap   = 4
	piSummaryCalls  = 2
	piReopenTurns   = 1
	piRequestBudget = piOrdinaryCap + piSummaryCalls + piReopenTurns

	piVerifierID = "pi-workspace/independent-files/v1"
	piNoteFile   = "NOTE.txt"
	piTaskFile   = "TASK.txt"
	piNoteBody   = "keep this line\n"
	piTaskOpen   = "status=open\n"
	piTaskDone   = "status=done\n"
	piPrompt     = "Set TASK.txt to status=done. Do not modify NOTE.txt. Write TASK.txt once. " +
		"After the session reopens, do not write it again. When the file is already done, reply DONE with no tool calls."
	piSummaryPrompt = "Summarize the session so far. Do not call tools. Preserve the task file state and the constraints."
	piReopenPrompt  = "The session reopened from the sealed checkpoint. Do not repeat a completed write. " +
		"If TASK.txt is already status=done, reply DONE with no tool calls."
	piFakeProviderGap = "fake provider establishes harness mechanics only; it is not a local-model measurement"
	piSummaryClass    = "summary"
	piSummaryLimit    = 16 * 1024
)

func NewPiPlan(model record.ModelIdentity, deviceKey string, trials, timeoutSeconds, requestedContext int, provider string) (*SealedPlan, error) {
	if model.RuntimeBoundDigest() == "" {
		return nil, errors.New("workload plan requires a runtime-bound model artifact")
	}
	if strings.TrimSpace(deviceKey) == "" {
		return nil, errors.New("workload plan requires a device key")
	}
	if provider != PiProviderLocal && provider != PiProviderFake {
		return nil, errors.New("pi-workspace provider must be local or fake")
	}
	if err := ValidatePlanBounds(trials, piRequestBudget, timeoutSeconds, requestedContext); err != nil {
		return nil, err
	}
	contract, err := piWorkspaceContract()
	if err != nil {
		return nil, err
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("workload plan signing key: %w", err)
	}
	driver := pinnedPiDriver()
	schedule := pinnedPiSchedule()
	plan := Plan{
		Schema: PlanSchema, CompletionKey: base64.RawStdEncoding.EncodeToString(public),
		Workflow: PiWorkflowID, WorkflowVersion: WorkflowVersion,
		Model: model, DeviceKey: deviceKey, Trials: trials, MaxTurns: piRequestBudget,
		TimeoutSeconds: timeoutSeconds, RequestedContext: requestedContext, Retention: RetainHashesAndVerifier,
		Contract: &contract, Driver: &driver, Provider: provider, RequestBudget: piRequestBudget,
		PiSchedule: &schedule,
	}
	digest, err := planDigest(plan)
	if err != nil {
		return nil, err
	}
	plan.PlanSHA256 = digest
	return &SealedPlan{Plan: plan, privateKey: private}, nil
}

func pinnedPiDriver() SessionDriver {
	return SessionDriver{
		Package: PiDriverPackage, Commit: PiDriverCommit, Release: PiDriverRelease, Adapter: PiDriverAdapter,
	}
}

func pinnedPiSchedule() SessionSchedule {
	return SessionSchedule{OrdinaryCap: piOrdinaryCap, SummaryCalls: piSummaryCalls, ReopenTurns: piReopenTurns}
}

func validatePiPlan(plan Plan) error {
	if plan.Schema != PlanSchema || plan.Workflow != PiWorkflowID {
		return errors.New("pi-workspace plan schema is invalid")
	}
	driver, schedule := pinnedPiDriver(), pinnedPiSchedule()
	if plan.Driver == nil || *plan.Driver != driver {
		return errors.New("pi-workspace driver pin does not match")
	}
	if plan.Provider != PiProviderLocal && plan.Provider != PiProviderFake {
		return errors.New("pi-workspace provider must be local or fake")
	}
	if plan.PiSchedule == nil || *plan.PiSchedule != schedule || plan.MaxTurns != piRequestBudget || plan.RequestBudget != piRequestBudget {
		return errors.New("pi-workspace budget must fund the ordinary cap, both split summaries, and the reopen turn")
	}
	if plan.Contract != nil && harborReward(plan.Contract) {
		return errors.New("a Harbor reward is not the pi-workspace receipt")
	}
	return nil
}

func harborReward(contract *WorkflowContract) bool {
	if contract == nil {
		return false
	}
	verifier := strings.ToLower(contract.Verifier)
	protocol := strings.ToLower(contract.ExternalProtocol)
	return strings.Contains(verifier, "harbor") || strings.Contains(protocol, "harbor")
}

func validateForeignPiFields(plan Plan) error {
	if plan.Driver != nil || plan.Provider != "" || plan.RequestBudget != 0 || plan.PiSchedule != nil {
		return errors.New("workload plan carries a pi session it does not run")
	}
	return nil
}

func piWorkspaceContract() (WorkflowContract, error) {
	scenario, err := hashValue("fitr.workload.scenario.v1", struct {
		Prompt string            `json:"prompt"`
		Files  map[string]string `json:"files"`
		Done   string            `json:"done"`
	}{piPrompt, map[string]string{piNoteFile: piNoteBody, piTaskFile: piTaskOpen}, piTaskDone})
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

func piContractSupported(contract *WorkflowContract) bool {
	if contract == nil || harborReward(contract) {
		return false
	}
	expected, err := piWorkspaceContract()
	return err == nil && *contract == expected
}
