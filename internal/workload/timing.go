package workload

import "fmt"

// TrialAnalysis is reconstructed from a validated trial's harness events.
// Worker includes model and tool time; those components must not be added to it.
type TrialAnalysis struct {
	TrialID      string        `json:"trial_id"`
	Proof        EvidenceClass `json:"proof"`
	Timing       *TrialTiming  `json:"timing,omitempty"`
	TimingStatus string        `json:"timing_status"`
	Retries      string        `json:"retries"`
	HumanWait    string        `json:"human_wait"`
	Escalation   string        `json:"escalation"`
	Approvals    string        `json:"approvals,omitempty"`
	Compaction   string        `json:"compaction,omitempty"`
}

type TrialTiming struct {
	ReleaseToTerminalMillis int64  `json:"release_to_terminal_ms"`
	WorkerMillis            int64  `json:"worker_ms"`
	ModelMillis             int64  `json:"model_ms"`
	ToolMillis              int64  `json:"tool_ms"`
	WorkerOverheadMillis    int64  `json:"worker_overhead_ms"`
	VerifierQueueMillis     int64  `json:"verifier_queue_ms"`
	VerifierMillis          int64  `json:"verifier_ms"`
	HarnessOverheadMillis   int64  `json:"harness_overhead_ms"`
	HumanWaitMillis         int64  `json:"human_wait_ms,omitempty"`
	EscalationMillis        int64  `json:"escalation_ms,omitempty"`
	CompactionMillis        int64  `json:"compaction_ms,omitempty"`
	TimeToValidMillis       *int64 `json:"time_to_valid_ms,omitempty"`
}

func analyzeTrial(trial Trial) TrialAnalysis {
	analysis := TrialAnalysis{
		TrialID: trial.TrialID, Proof: trial.Verifier.EvidenceClass,
		TimingStatus: "unavailable", Retries: "not_permitted",
		HumanWait: "unsupported", Escalation: "unsupported",
	}
	stats, err := validateEvents(trial.Events, trial.Outcome, trial.Verifier)
	if err != nil {
		return analysis
	}
	timing := calculateTiming(trial.Events, trial.Outcome)
	populateAnalysisFields(&analysis, trial, stats, &timing)
	analysis.Timing, analysis.TimingStatus = &timing, "harness_observed"
	return analysis
}

func calculateTiming(events []Event, outcome Outcome) TrialTiming {
	end := len(events) - 1
	timing := TrialTiming{
		ReleaseToTerminalMillis: events[end].ElapsedMillis - events[0].ElapsedMillis,
		WorkerMillis:            events[end-4].ElapsedMillis - events[1].ElapsedMillis,
		VerifierQueueMillis:     events[end-2].ElapsedMillis - events[end-3].ElapsedMillis,
		VerifierMillis:          events[end-1].ElapsedMillis - events[end-2].ElapsedMillis,
	}
	for index, event := range events {
		if index == 0 {
			continue
		}
		duration := event.ElapsedMillis - events[index-1].ElapsedMillis
		switch event.Type {
		case EventModelCompleted:
			timing.ModelMillis += duration
		case EventToolCompleted:
			timing.ToolMillis += duration
		case EventHumanWaitCompleted:
			timing.HumanWaitMillis += duration
		case EventEscalationCompleted:
			timing.EscalationMillis += duration
		case EventCompactionCompleted:
			timing.CompactionMillis += duration
		}
	}
	timing.WorkerOverheadMillis = timing.WorkerMillis - timing.ModelMillis - timing.ToolMillis -
		timing.HumanWaitMillis - timing.EscalationMillis - timing.CompactionMillis
	timing.HarnessOverheadMillis = timing.ReleaseToTerminalMillis - timing.WorkerMillis -
		timing.VerifierQueueMillis - timing.VerifierMillis
	if outcome == OutcomeAccepted {
		elapsed := timing.ReleaseToTerminalMillis
		timing.TimeToValidMillis = &elapsed
	}
	return timing
}

func populateAnalysisFields(analysis *TrialAnalysis, trial Trial, stats eventStats, timing *TrialTiming) {
	if trial.Attempts > 1 {
		analysis.Retries = fmt.Sprintf("%d_attempts", trial.Attempts)
	} else if stats.retries > 0 {
		analysis.Retries = "attempted"
	}
	if timing.HumanWaitMillis > 0 || stats.humanWaits > 0 {
		analysis.HumanWait = "observed"
	}
	if timing.EscalationMillis > 0 || stats.escalations > 0 {
		analysis.Escalation = "observed"
	}
	if stats.approvalsDenied > 0 {
		analysis.Approvals = "denied"
	} else if stats.approvalsGranted > 0 {
		analysis.Approvals = "granted"
	}
	if stats.compactions > 0 || timing.CompactionMillis > 0 {
		analysis.Compaction = "observed"
	}
}
