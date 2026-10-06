package workload

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
)

func Analyze(plan Plan, trials []Trial) Report {
	report := Report{
		Schema: ReportSchema, PlanSHA256: plan.PlanSHA256, Workflow: plan.Workflow,
		Counts: OutcomeCounts{Planned: plan.Trials},
		AcceptedOutcomesPerHour: RateObservation{
			Unit: "accepted_outcomes_per_hour", Status: "unavailable",
			Reason: "positive total trial time is required",
		},
	}
	acceptedDurations := make([]float64, 0, len(trials))
	var totalMillis float64
	for _, trial := range trials {
		totalMillis += float64(trial.ElapsedMillis)
		switch trial.Outcome {
		case OutcomeAccepted:
			report.Counts.Accepted++
			acceptedDurations = append(acceptedDurations, float64(trial.ElapsedMillis))
		case OutcomeRejected:
			report.Counts.Rejected++
		case OutcomeTimedOut:
			report.Counts.TimedOut++
		case OutcomeInfrastructure:
			report.Counts.InfrastructureFault++
		}
	}
	if len(acceptedDurations) >= 3 {
		median := medianFloat(acceptedDurations)
		report.MedianAcceptedMillis = &median
	} else {
		report.Gaps = append(report.Gaps, "fewer than three accepted trials; median accepted time is withheld")
	}
	if totalMillis > 0 {
		rate := float64(report.Counts.Accepted) / (totalMillis / (60 * 60 * 1000))
		report.AcceptedOutcomesPerHour = RateObservation{
			Estimate: &rate, Unit: "accepted_outcomes_per_hour", Status: "available",
			Reason: "accepted trials divided by elapsed time across every terminal outcome",
		}
	}
	report.Coverage = workloadCoverage(report.Counts)
	if report.Counts.InfrastructureFault > 0 {
		report.Gaps = append(report.Gaps, "infrastructure faults remain operational evidence and are not model failures")
	}
	if report.Counts.TimedOut > 0 {
		report.Gaps = append(report.Gaps, "timed-out trials remain in the denominator")
	}
	if plan.Schema == LegacyPlanSchema {
		report.Schema = LegacyReportSchema
	} else {
		for _, trial := range trials {
			report.TrialAnalysis = append(report.TrialAnalysis, analyzeTrial(trial))
		}
	}
	if plan.Provider == PiProviderFake {
		report.Coverage = "not_established"
		report.MedianAcceptedMillis = nil
		report.AcceptedOutcomesPerHour = RateObservation{
			Unit: "accepted_outcomes_per_hour", Status: "unavailable", Reason: piFakeProviderGap,
		}
		report.Gaps = append(report.Gaps, piFakeProviderGap)
	}
	return report
}

func (trial Trial) Validate(plan Plan) error {
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("workload trial plan: %w", err)
	}
	if err := validateTrialBinding(trial, plan); err != nil {
		return err
	}
	if err := validateTrialSignature(trial, plan); err != nil {
		return err
	}
	stats, err := validateEvents(trial.Events, trial.Outcome, trial.Verifier)
	if err != nil {
		return err
	}
	if err := validateTrialSemantics(trial, plan, stats); err != nil {
		return err
	}
	return nil
}

func validateTrialBinding(trial Trial, plan Plan) error {
	if trial.Schema != TrialSchema || trial.PlanSHA256 != plan.PlanSHA256 {
		return errors.New("workload trial schema or plan binding is invalid")
	}
	if trial.Index < 1 || trial.Index > plan.Trials || trial.Attempts < 1 {
		return errors.New("workload trial index or attempt count is invalid")
	}
	if plan.Contract != nil && plan.Contract.RetryPolicy == "one-attempt-no-retry" && trial.Attempts != 1 {
		return errors.New("workload trial attempt count exceeds contract retry policy")
	}
	if trial.TrialID != fmt.Sprintf("%s:%d", plan.PlanSHA256, trial.Index) {
		return errors.New("workload trial id does not match its plan position")
	}
	return nil
}

func validateTrialSemantics(trial Trial, plan Plan, stats eventStats) error {
	if trial.Turns != stats.turns || trial.ToolCalls != stats.toolCalls ||
		trial.DuplicateCalls != stats.duplicateCalls || trial.Turns > plan.MaxTurns ||
		trial.ToolCalls > trial.Turns*maximumToolCallsPerTurn {
		return errors.New("workload trial counters do not match its event sequence")
	}
	if trial.ElapsedMillis < stats.terminalElapsed || trial.ElapsedMillis < 0 {
		return errors.New("workload trial elapsed time conflicts with its event sequence")
	}
	if trial.AuthorityViolations < 0 {
		return errors.New("workload trial authority count is invalid")
	}
	if plan.Workflow == PiWorkflowID {
		if err := validatePiTrial(trial, stats); err != nil {
			return err
		}
	} else if err := validateVerifier(trial.Verifier, trial.AuthorityViolations); err != nil {
		return err
	}
	return validateOutcomeSemantics(trial, stats.workerStatus)
}

func validateOutcomeSemantics(trial Trial, workerStatus string) error {
	switch trial.Outcome {
	case OutcomeAccepted:
		if !trial.Verifier.Accepted || trial.AuthorityViolations != 0 || workerStatus != "clean_stop" {
			return errors.New("accepted workload trial conflicts with the worker or independent verifier")
		}
	case OutcomeTimedOut:
		if workerStatus != "timeout" {
			return errors.New("timed-out workload trial conflicts with the worker event")
		}
	case OutcomeInfrastructure:
		if workerStatus != "error" {
			return errors.New("infrastructure workload trial conflicts with the worker event")
		}
	case OutcomeRejected:
		if workerStatus == "timeout" || workerStatus == "error" {
			return errors.New("rejected workload trial conflicts with the worker event")
		}
		if trial.Verifier.Accepted && trial.AuthorityViolations == 0 && workerStatus == "clean_stop" {
			return errors.New("rejected workload trial has independently accepted completion")
		}
	}
	return nil
}

func validateTrialSignature(trial Trial, plan Plan) error {
	digest, err := trialDigest(trial)
	if err != nil || digest != trial.EvidenceSHA256 {
		return errors.New("workload trial evidence digest does not match")
	}
	publicKey, err := base64.RawStdEncoding.DecodeString(plan.CompletionKey)
	signature, signatureErr := base64.RawStdEncoding.DecodeString(trial.Signature)
	if err != nil || signatureErr != nil || len(publicKey) != ed25519.PublicKeySize ||
		len(signature) != ed25519.SignatureSize ||
		!ed25519.Verify(publicKey, []byte(trial.EvidenceSHA256), signature) {
		return errors.New("workload trial signature is invalid")
	}
	return nil
}

type eventStats struct {
	turns, toolCalls, duplicateCalls              int
	approvalsGranted, approvalsDenied             int
	escalations, humanWaits, compactions, retries int
	summaryCalls, resumes                         int
	terminalElapsed                               int64
	workerStatus, lastModelStatus                 string
}

func validateEvents(events []Event, outcome Outcome, verifier VerifierReceipt) (eventStats, error) {
	stats := eventStats{}
	if !validOutcome(outcome) {
		return stats, errors.New("workload trial outcome is invalid")
	}
	if len(events) < 9 {
		return stats, errors.New("workload trial event sequence is incomplete")
	}
	if err := validateEventOrdering(events); err != nil {
		return stats, err
	}
	if events[0].Type != EventScenarioReleased || events[0].Actor != "harness" ||
		events[0].Status != "released" || events[1].Type != EventWorkerStarted ||
		events[1].Actor != "worker" || events[1].Status != "started" {
		return stats, errors.New("workload trial event prefix is invalid")
	}
	stats, cursor, err := validateWorkerEvents(events)
	if err != nil {
		return stats, err
	}
	if err := validateTerminalEvents(events, cursor, outcome, verifier, &stats); err != nil {
		return stats, err
	}
	return stats, nil
}

func validateEventOrdering(events []Event) error {
	var priorElapsed int64
	for index, event := range events {
		if event.Sequence != index+1 || event.ElapsedMillis < priorElapsed ||
			event.ElapsedMillis < 0 || event.Attempt < 1 {
			return errors.New("workload trial events are out of order")
		}
		if err := validateEventClass(event); err != nil {
			return err
		}
		if eventRequiresEvidence(event.Type) && !validSHA256(event.EvidenceSHA256) {
			return errors.New("workload trial event evidence digest is missing or invalid")
		}
		if !eventRequiresEvidence(event.Type) && event.EvidenceSHA256 != "" {
			return errors.New("workload trial event carries unexpected evidence")
		}
		toolEvent := event.Type == EventToolStarted || event.Type == EventToolCompleted
		if toolEvent != (event.Tool != "") {
			return errors.New("workload trial event tool identity is invalid")
		}
		priorElapsed = event.ElapsedMillis
	}
	return nil
}

func eventRequiresEvidence(eventType EventType) bool {
	switch eventType {
	case EventScenarioReleased, EventModelStarted, EventModelCompleted, EventToolStarted,
		EventToolCompleted, EventVerifierCompleted, EventAccepted, EventRejected, EventTimedOut,
		EventInfrastructure, EventApprovalRequested, EventApprovalGranted, EventApprovalDenied,
		EventEscalationRequested, EventEscalationCompleted, EventCompactionCompleted, EventRetry:
		return true
	default:
		return false
	}
}

func validateWorkerEvents(events []Event) (eventStats, int, error) {
	stats := eventStats{}
	cursor := 2
	seenCalls := make(map[string]int)
	for cursor < len(events) && events[cursor].Type != EventWorkerCompleted {
		handled, err := consumeWorkerControlEvent(events, &cursor, &stats)
		if err != nil {
			return stats, cursor, err
		}
		if handled {
			continue
		}
		if events[cursor].Type != EventModelStarted {
			break
		}
		if err := validateModelTurn(events, &cursor, seenCalls, &stats); err != nil {
			return stats, cursor, err
		}
	}
	if stats.turns == 0 || len(events)-cursor != 5 {
		return stats, cursor, errors.New("workload trial event state machine is incomplete")
	}
	return stats, cursor, nil
}

func validateModelTurn(events []Event, cursor *int, seenCalls map[string]int, stats *eventStats) error {
	c := *cursor
	if events[c].Actor != "worker" || c+1 >= len(events) ||
		events[c].Status != "started" || events[c+1].Type != EventModelCompleted ||
		events[c+1].Actor != "worker" ||
		!validModelCompletionStatus(events[c+1].Status) {
		return errors.New("workload trial model request events are unbalanced")
	}
	stats.turns++
	stats.lastModelStatus = events[c+1].Status
	if err := noteSummaryClass(events[c], events[c+1], stats); err != nil {
		return err
	}
	*cursor += 2
	c = *cursor
	if stats.lastModelStatus != "completed" && c < len(events) &&
		(events[c].Type == EventToolStarted || events[c].Type == EventModelStarted) {
		return errors.New("workload trial continued after a failed model request")
	}
	priorCalls := stats.toolCalls
	for *cursor < len(events) && (events[*cursor].Type == EventToolStarted || isApprovalOrControl(events[*cursor].Type)) {
		ctrlHandled, ctrlErr := consumeWorkerControlEvent(events, cursor, stats)
		if ctrlErr != nil {
			return ctrlErr
		}
		if ctrlHandled {
			continue
		}
		if err := consumeToolEvents(events, cursor, seenCalls, stats); err != nil {
			return err
		}
	}
	if stats.toolCalls-priorCalls > maximumToolCallsPerTurn {
		return errors.New("workload trial exceeds the per-turn tool-call limit")
	}
	return nil
}

func isApprovalOrControl(t EventType) bool {
	switch t {
	case EventApprovalRequested, EventApprovalGranted, EventApprovalDenied,
		EventEscalationRequested, EventEscalationCompleted,
		EventHumanWaitStarted, EventHumanWaitCompleted,
		EventCompactionStarted, EventCompactionCompleted, EventCheckpointResumed,
		EventRetry:
		return true
	default:
		return false
	}
}

func consumeWorkerControlEvent(events []Event, cursor *int, stats *eventStats) (bool, error) {
	if *cursor >= len(events) {
		return false, nil
	}
	switch events[*cursor].Type {
	case EventApprovalRequested:
		if *cursor+1 >= len(events) {
			return true, errors.New("workload trial approval event sequence is incomplete")
		}
		next := events[*cursor+1]
		if next.Type != EventApprovalGranted && next.Type != EventApprovalDenied {
			return true, errors.New("workload trial approval response event is invalid")
		}
		if next.Type == EventApprovalGranted {
			stats.approvalsGranted++
		} else {
			stats.approvalsDenied++
		}
		*cursor += 2
		return true, nil
	case EventEscalationRequested:
		if *cursor+1 >= len(events) || events[*cursor+1].Type != EventEscalationCompleted {
			return true, errors.New("workload trial escalation event sequence is incomplete")
		}
		stats.escalations++
		*cursor += 2
		return true, nil
	case EventHumanWaitStarted:
		if *cursor+1 >= len(events) || events[*cursor+1].Type != EventHumanWaitCompleted {
			return true, errors.New("workload trial human wait event sequence is incomplete")
		}
		stats.humanWaits++
		*cursor += 2
		return true, nil
	case EventCompactionStarted:
		if *cursor+1 >= len(events) || events[*cursor+1].Type != EventCompactionCompleted {
			return true, errors.New("workload trial compaction event sequence is incomplete")
		}
		stats.compactions++
		*cursor += 2
		if *cursor < len(events) && events[*cursor].Type == EventCheckpointResumed {
			stats.resumes++
			*cursor++
		}
		return true, nil
	case EventRetry:
		stats.retries++
		*cursor++
		return true, nil
	default:
		return false, nil
	}
}

func consumeToolEvents(events []Event, cursor *int, seenCalls map[string]int, stats *eventStats) error {
	started := events[*cursor]
	if started.Actor != "harness" || started.Status != "started" || started.Tool == "" ||
		*cursor+1 >= len(events) {
		return errors.New("workload trial tool events are invalid")
	}
	completed := events[*cursor+1]
	if completed.Type != EventToolCompleted || completed.Actor != "harness" ||
		completed.Tool != started.Tool || !validToolCompletionStatus(completed.Status) {
		return errors.New("workload trial tool events are unbalanced")
	}
	callKey := started.Tool + "\x00" + started.EvidenceSHA256
	seenCalls[callKey]++
	if seenCalls[callKey] > 1 {
		stats.duplicateCalls++
	}
	stats.toolCalls++
	*cursor += 2
	return nil
}

func validateTerminalEvents(events []Event, cursor int, outcome Outcome, verifier VerifierReceipt,
	stats *eventStats) error {
	worker, queued, verifierStarted, verifierCompleted, final :=
		events[cursor], events[cursor+1], events[cursor+2], events[cursor+3], events[cursor+4]
	terminal := terminalEvent(outcome)
	if worker.Type != EventWorkerCompleted || worker.Actor != "worker" ||
		!validWorkerCompletionStatus(worker.Status) ||
		queued.Type != EventVerifierQueued || queued.Actor != "harness" || queued.Status != "queued" ||
		verifierStarted.Type != EventVerifierStarted || verifierStarted.Actor != "verifier" ||
		verifierStarted.Status != "started" || verifierCompleted.Type != EventVerifierCompleted ||
		verifierCompleted.Actor != "verifier" || verifierCompleted.Status != boolStatus(verifier.Accepted) ||
		final.Type != terminal || final.Actor != "harness" || final.Status != string(outcome) {
		return errors.New("workload trial verifier or terminal sequence is invalid")
	}
	requestFailed := stats.lastModelStatus == "timeout" || stats.lastModelStatus == "error"
	workerFailed := worker.Status == "timeout" || worker.Status == "error"
	if requestFailed != workerFailed || (requestFailed && worker.Status != stats.lastModelStatus) {
		return errors.New("workload trial worker status conflicts with its final model request")
	}
	verifierDigest, err := hashValue("fitr.workload.event.v1", verifier)
	if err != nil || verifierCompleted.EvidenceSHA256 != verifierDigest || final.EvidenceSHA256 != verifierDigest {
		return errors.New("workload trial verifier event evidence does not match its receipt")
	}
	stats.workerStatus = worker.Status
	stats.terminalElapsed = final.ElapsedMillis
	return nil
}

func validModelCompletionStatus(status string) bool {
	return status == "completed" || status == "timeout" || status == "error"
}

func validToolCompletionStatus(status string) bool {
	return status == "ok" || status == "failed" || status == "denied" || status == "malformed"
}

func validWorkerCompletionStatus(status string) bool {
	switch status {
	case "clean_stop", "stopped_without_done", "turn_cap", "tool_call_cap", "timeout", "error":
		return true
	default:
		return false
	}
}

func validateEventClass(event Event) error {
	if event.Class == "" {
		return nil
	}
	if event.Class != piSummaryClass || (event.Type != EventModelStarted && event.Type != EventModelCompleted) {
		return errors.New("workload trial event class is invalid")
	}
	return nil
}

func noteSummaryClass(started, completed Event, stats *eventStats) error {
	if started.Class != completed.Class {
		return errors.New("workload trial summary request is not paired")
	}
	if started.Class == piSummaryClass {
		stats.summaryCalls++
	}
	return nil
}

func validatePiTrial(trial Trial, stats eventStats) error {
	if trial.SummaryCalls != stats.summaryCalls || stats.turns > piRequestBudget || stats.summaryCalls > piSummaryCalls {
		return errors.New("pi-workspace summary accounting does not match the sealed budget")
	}
	if err := validatePiChecks(trial); err != nil {
		return err
	}
	if err := validatePiObservations(trial, stats); err != nil {
		return err
	}
	if trial.Outcome != OutcomeAccepted {
		return nil
	}
	if stats.summaryCalls != piSummaryCalls || stats.compactions != 1 || stats.resumes != 1 || trial.Effects != 1 {
		return errors.New("accepted pi-workspace trial is missing a split summary or the checkpoint reopen")
	}
	return nil
}

// validatePiObservations binds counters to the event log. A receipt must not
// claim one write, or a reopened checkpoint, when the recorded events deny it.
func validatePiObservations(trial Trial, stats eventStats) error {
	if trial.Effects != piObservedWrites(trial.Events) {
		return errors.New("pi-workspace effect count conflicts with its write events")
	}
	if piCheckPassed(trial, "checkpoint") != (stats.resumes == 1) {
		return errors.New("pi-workspace checkpoint conflicts with its resume")
	}
	return nil
}

func piObservedWrites(events []Event) int {
	writes := 0
	for index := 0; index+1 < len(events); index++ {
		started, completed := events[index], events[index+1]
		if started.Type != EventToolStarted || completed.Type != EventToolCompleted {
			continue
		}
		if started.Tool == "write_file" && completed.Tool == "write_file" && completed.Status == "ok" {
			writes++
		}
	}
	return writes
}

func piCheckPassed(trial Trial, code string) bool {
	for _, item := range trial.Verifier.Checks {
		if item.Code == code {
			return item.Passed
		}
	}
	return false
}

func validatePiChecks(trial Trial) error {
	if trial.Verifier.EvidenceClass != EvidenceIndependent || !validSHA256(trial.Verifier.PolicySHA256) ||
		!validSHA256(trial.Verifier.ProtectedStateSHA256) {
		return errors.New("pi-workspace verifier identity is invalid")
	}
	expected := map[string]bool{
		"task_file": false, "protected_note": false, "single_effect": false,
		"authority": false, "checkpoint": false, "summaries": false,
	}
	accepted := true
	for _, item := range trial.Verifier.Checks {
		if _, ok := expected[item.Code]; !ok || expected[item.Code] {
			return errors.New("pi-workspace verifier checks are incomplete or repeated")
		}
		expected[item.Code] = true
		accepted = accepted && item.Passed
		if err := validatePiCheck(item, trial); err != nil {
			return err
		}
	}
	for _, present := range expected {
		if !present {
			return errors.New("pi-workspace verifier checks are incomplete or repeated")
		}
	}
	if trial.Verifier.Accepted != accepted {
		return errors.New("pi-workspace verifier result conflicts with its checks")
	}
	return nil
}

func validatePiCheck(item VerificationCheck, trial Trial) error {
	switch item.Code {
	case "task_file":
		expected, err := hashValue("fitr.workload.policy.v1", piTaskDone)
		if err != nil || item.Passed != (trial.Verifier.PolicySHA256 == expected) {
			return errors.New("pi-workspace task file conflicts with its verifier")
		}
	case "authority":
		if item.Passed != (trial.AuthorityViolations == 0) {
			return errors.New("pi-workspace authority count conflicts with its verifier")
		}
	case "single_effect":
		if item.Passed != (trial.Effects == 1) {
			return errors.New("pi-workspace effect count conflicts with its verifier")
		}
	case "checkpoint":
		if item.Passed != validSHA256(trial.Verifier.CheckpointSHA256) {
			return errors.New("pi-workspace checkpoint conflicts with its verifier")
		}
	case "summaries":
		if item.Passed != piSummariesEstablished(trial.Verifier.SummarySHA256) {
			return errors.New("pi-workspace summaries conflict with its verifier")
		}
	case "protected_note":
		expected, err := hashValue("fitr.workload.protected.v1", piNoteBody)
		if err != nil || item.Passed != (trial.Verifier.ProtectedStateSHA256 == expected) {
			return errors.New("pi-workspace protected note conflicts with its verifier")
		}
	}
	return nil
}

func validateVerifier(verifier VerifierReceipt, authorityViolations int) error {
	if !verifier.EvidenceClass.CanEstablishCompletion() || verifier.EvidenceClass != EvidenceDeterministic ||
		!validSHA256(verifier.PolicySHA256) || !validSHA256(verifier.ProtectedStateSHA256) {
		return errors.New("workload trial verifier identity is invalid")
	}
	expected := map[string]bool{
		"policy_json": false, "version": false, "enabled": false, "retries": false,
		"timeout": false, "modes": false, "protected_state": false, "authority": false,
	}
	accepted := true
	expectedProtected := newWorkflowState().protectedSHA256
	for _, item := range verifier.Checks {
		if _, ok := expected[item.Code]; !ok || expected[item.Code] {
			return errors.New("workload trial verifier checks are incomplete or repeated")
		}
		expected[item.Code] = true
		accepted = accepted && item.Passed
		if item.Code == "authority" && item.Passed != (authorityViolations == 0) {
			return errors.New("workload trial authority count conflicts with its verifier")
		}
		if item.Code == "protected_state" && item.Passed != (verifier.ProtectedStateSHA256 == expectedProtected) {
			return errors.New("workload trial protected-state digest conflicts with its verifier")
		}
	}
	for _, present := range expected {
		if !present {
			return errors.New("workload trial verifier checks are incomplete or repeated")
		}
	}
	if verifier.Accepted != accepted {
		return errors.New("workload trial verifier result conflicts with its checks")
	}
	return nil
}

func validOutcome(outcome Outcome) bool {
	return outcome == OutcomeAccepted || outcome == OutcomeRejected ||
		outcome == OutcomeTimedOut || outcome == OutcomeInfrastructure
}

func medianFloat(values []float64) float64 {
	values = append([]float64(nil), values...)
	sort.Float64s(values)
	middle := len(values) / 2
	if len(values)%2 == 1 {
		return values[middle]
	}
	return (values[middle-1] + values[middle]) / 2
}

func workloadCoverage(counts OutcomeCounts) string {
	switch {
	case counts.Planned >= 3 && counts.Accepted == counts.Planned:
		return "established"
	case counts.Accepted > 0:
		return "observed"
	default:
		return "not_established"
	}
}
