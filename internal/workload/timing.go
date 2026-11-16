package workload

import (
	"fmt"
	"strings"
)

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
	// Process is derived from a validated event sequence. Nil means the
	// sequence was not validated, which is not a count of zero.
	Process *ToolProcess `json:"tool_process,omitempty"`
}

// ToolProcess counts tool results already stored on the trial. It is not a
// quality score. Repeated counts the same tool and argument digest again.
type ToolProcess struct {
	Status       string   `json:"status"`
	Malformed    int      `json:"malformed"`
	Denied       int      `json:"denied"`
	Failed       int      `json:"failed"`
	Repeated     int      `json:"repeated"`
	Observations []string `json:"observations,omitempty"`
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
	analysis.Process = processFromStats(stats)
	return analysis
}

func processFromStats(stats eventStats) *ToolProcess {
	var notes []string
	if len(stats.toolNotes) > 0 {
		notes = append([]string(nil), stats.toolNotes...)
	}
	return &ToolProcess{
		Status: "observed", Malformed: stats.malformed, Denied: stats.denied,
		Failed: stats.failed, Repeated: stats.duplicateCalls, Observations: notes,
	}
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

// ProcessText projects the derived tool-process observation. Unavailable is
// the absence of a validated sequence, not a count of zero. The counts are
// observations. The wording stays free of a quality judgement.
func (analysis TrialAnalysis) ProcessText() string {
	process := analysis.Process
	if process == nil {
		return "unavailable"
	}
	parts := []string{fmt.Sprintf(
		"observed malformed %d denied %d failed %d repeated %d",
		process.Malformed, process.Denied, process.Failed, process.Repeated,
	)}
	parts = append(parts, process.Observations...)
	parts = append(parts, "same tool and argument digest", "a zero count is an observation")
	return strings.Join(parts, "; ")
}
