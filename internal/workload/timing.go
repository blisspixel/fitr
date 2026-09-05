package workload

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
	TimeToValidMillis       *int64 `json:"time_to_valid_ms,omitempty"`
}

func analyzeTrial(trial Trial) TrialAnalysis {
	analysis := TrialAnalysis{
		TrialID: trial.TrialID, Proof: trial.Verifier.EvidenceClass,
		TimingStatus: "unavailable", Retries: "not_permitted",
		HumanWait: "unsupported", Escalation: "unsupported",
	}
	if _, err := validateEvents(trial.Events, trial.Outcome, trial.Verifier); err != nil {
		return analysis
	}
	events := trial.Events
	end := len(events) - 1
	timing := TrialTiming{
		ReleaseToTerminalMillis: events[end].ElapsedMillis - events[0].ElapsedMillis,
		WorkerMillis:            events[end-4].ElapsedMillis - events[1].ElapsedMillis,
		VerifierQueueMillis:     events[end-2].ElapsedMillis - events[end-3].ElapsedMillis,
		VerifierMillis:          events[end-1].ElapsedMillis - events[end-2].ElapsedMillis,
	}
	for index, event := range events {
		switch event.Type {
		case EventModelCompleted:
			timing.ModelMillis += event.ElapsedMillis - events[index-1].ElapsedMillis
		case EventToolCompleted:
			timing.ToolMillis += event.ElapsedMillis - events[index-1].ElapsedMillis
		}
	}
	timing.WorkerOverheadMillis = timing.WorkerMillis - timing.ModelMillis - timing.ToolMillis
	timing.HarnessOverheadMillis = timing.ReleaseToTerminalMillis - timing.WorkerMillis -
		timing.VerifierQueueMillis - timing.VerifierMillis
	if trial.Outcome == OutcomeAccepted {
		elapsed := timing.ReleaseToTerminalMillis
		timing.TimeToValidMillis = &elapsed
	}
	analysis.Timing, analysis.TimingStatus = &timing, "harness_observed"
	return analysis
}
