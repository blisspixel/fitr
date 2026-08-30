package record

import (
	"strings"

	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

const scoredMemoryProbeCtx = 32768

// Measured reconstructs the scorer input exclusively from raw observations.
// Keeping this translation beside Record lets completion and read validation
// prove that a persisted scorecard is exactly reproducible from sealed facts.
func (r *Record) Measured() score.Measured {
	if r == nil {
		return score.Measured{}
	}
	m := r.baseMeasured()
	r.addSpeedMeasurements(&m)
	r.addMemoryMeasurement(&m)
	r.addCodeMeasurements(&m)
	r.addCheckMeasurements(&m)
	r.addRefusalMeasurements(&m)
	r.addToolMeasurements(&m)
	r.addAgenticMeasurements(&m)
	r.addWithdrawalMeasurements(&m)
	r.addPlumbingMeasurements(&m)
	return m
}

func (r *Record) baseMeasured() score.Measured {
	return score.Measured{
		Model: r.Model, Capabilities: r.ModelMeta.Capabilities, Contamination: r.Contamination,
		Rep: r.Rep, CodePlanned: r.TaskPlan.CodeTrials > 0,
		AgenticPlanned: r.TaskPlan.AgenticTrials > 0,
	}
}

func (r *Record) addSpeedMeasurements(m *score.Measured) {
	if r.DecodeSum.N > 0 {
		m.SpeedKnown = true
		m.DecodeTPS, m.TTFT, m.PrefillTPS = r.DecodeSum.Mean, r.TTFTSum.Mean, r.PrefillSum.Mean
		m.TTFTCacheKnown = len(r.Speed) > 0
		m.PrefillCacheKnown = len(r.Speed) > 0
		for _, sample := range r.Speed {
			if sample.ColdTTFT > 0 && m.TTFTCold == 0 {
				m.TTFTCold = sample.ColdTTFT
			}
			if sample.WarmTTFT > 0 && m.TTFTWarm == 0 {
				m.TTFTWarm = sample.WarmTTFT
			}
			if sample.GatedTTFTContaminated() {
				m.TTFTCacheContaminated = true
			}
			if sample.PrefillContaminated() {
				m.PrefillCacheContaminated = true
			}
			if !sample.GatedCacheReceiptValid() {
				m.TTFTCacheKnown = false
			}
			if !sample.PrefillCacheReceiptValid() {
				m.PrefillCacheKnown = false
			}
			if sample.ClientDerived {
				m.TimingsClientDerived = true
			}
		}
	}
}

func (r *Record) addMemoryMeasurement(m *score.Measured) {
	if resident, verified := r.Memory.VerifiedAt(scoredMemoryProbeCtx); verified {
		m.MemoryKnown, m.ResidentGB32K = true, resident
	}
}

func (r *Record) addCodeMeasurements(m *score.Measured) {
	if len(r.CodeWrite)+len(r.CodeFix) > 0 {
		var writeOK, fixOK []bool
		for _, result := range r.CodeWrite {
			if pass, measured := eval.MeasuredOutcome(result.Outcome, result.Pass); measured {
				writeOK = append(writeOK, pass)
			}
		}
		for _, result := range r.CodeFix {
			if pass, measured := eval.MeasuredOutcome(result.Outcome, result.Pass); measured {
				fixOK = append(fixOK, pass)
			}
		}
		expected := r.TaskPlan.CodeTrials
		if expected == 0 && r.SchemaVersion < 5 {
			expected = r.Repeats * 2
		}
		m.CodeKnown = expected > 0 && len(writeOK)+len(fixOK) == expected
		writeFlakes, fixFlakes := stats.Flakiness(writeOK), stats.Flakiness(fixOK)
		m.CodeWritePass = writeFlakes.Passes*2 > writeFlakes.N
		m.CodeFixPass = fixFlakes.Passes*2 > fixFlakes.N
		m.CodeFlaky = writeFlakes.Flaky || fixFlakes.Flaky
		m.CodePasses = writeFlakes.Passes + fixFlakes.Passes
		m.CodeRepeats = writeFlakes.N + fixFlakes.N
	}
}

func (r *Record) addCheckMeasurements(m *score.Measured) {
	for _, check := range r.Checks {
		pool := measuredPool(m, check.Need)
		pass, measured := eval.MeasuredOutcome(check.Outcome, check.Pass)
		if !measured {
			pool.AddUnscorable(check.Family)
			if strings.Contains(check.Detail, "does not support tools") {
				m.ToolsUnsupported = true
			}
			continue
		}
		pool.Add(check.Family, pass)
	}
}

func (r *Record) addRefusalMeasurements(m *score.Measured) {
	if r.Refusal != nil {
		expected := r.TaskPlan.RefusalTrials
		if expected == 0 && r.SchemaVersion < 5 {
			expected = len(r.Refusal)
		}
		complete, refused := expected > 0 && len(r.Refusal) == expected, 0
		for _, result := range r.Refusal {
			outcome := result.Outcome
			if outcome == "" {
				switch result.Verdict {
				case "answered":
					outcome = eval.OutcomePass
				case "partial", "refused", "empty":
					outcome = eval.OutcomeFail
				default:
					complete = false
				}
			}
			pass, measured := eval.MeasuredOutcome(outcome, false)
			if !measured {
				complete = false
			} else if !pass {
				refused++
			}
		}
		m.RefusalKnown, m.RefusedCount = complete, refused
	}
}

func (r *Record) addToolMeasurements(m *score.Measured) {
	if len(r.Tools) > 0 {
		passes, measured := 0, 0
		for _, result := range r.Tools {
			if pass, ok := eval.MeasuredOutcome(result.Outcome, result.Pass); ok {
				measured++
				if pass {
					passes++
				}
			}
		}
		expected := r.TaskPlan.ToolTrials
		if expected == 0 && r.SchemaVersion < 5 {
			expected = r.Repeats
		}
		m.ToolsRan = measured > 0 && measured == expected
		m.ToolsPass = m.ToolsRan && passes*2 > measured
	}
}

func (r *Record) addAgenticMeasurements(m *score.Measured) {
	if r.Agentic != nil {
		if pass, measured := eval.MeasuredOutcome(r.Agentic.Outcome, r.Agentic.Pass); measured {
			m.AgenticRan, m.AgenticPass = true, pass
		}
		m.AgenticMalformed = r.Agentic.Malformed
		m.AgenticTurns = r.Agentic.Turns
		m.AgenticCtxCeiling = r.Agentic.CtxCeiling
		m.AgenticMaxPrompt = r.Agentic.MaxPromptTok
		m.AgenticCompacted = r.Agentic.Compacted
	}
}

func (r *Record) addWithdrawalMeasurements(m *score.Measured) {
	if r.Withdrawal != nil {
		if _, measured := eval.MeasuredOutcome(r.Withdrawal.Outcome, r.Withdrawal.Pass); measured {
			m.WithdrawRan = true
			m.WithdrawDeadCalls = r.Withdrawal.DeadCalls
			m.WithdrawClean = r.Withdrawal.Ended == "clean_stop"
		}
	}
}

func (r *Record) addPlumbingMeasurements(m *score.Measured) {
	if r.Plumbing != nil {
		m.PlumbingRan = r.Plumbing.Outcome != eval.OutcomeSkipped && r.Plumbing.Outcome != eval.OutcomeError
		if r.Plumbing.Outcome == "" {
			m.PlumbingRan = true
		}
		m.PlumbingHealthy = m.PlumbingRan && r.Plumbing.Healthy
		m.PlumbingVerdict = r.Plumbing.Verdict
		if rung, ok := r.Plumbing.Rungs["5_irrelevance"]; ok && m.PlumbingRan {
			m.IrrelevanceRan, m.IrrelevancePass = true, rung.Pass
			if !rung.Pass {
				m.SpuriousCalls = 1
			}
		}
	}
}

func measuredPool(measured *score.Measured, need string) *score.Pool {
	switch need {
	case "structured_output":
		return &measured.Structured
	case "instruction_precision":
		return &measured.Precision
	case "reasoning":
		return &measured.Reasoning
	case "tool_calling":
		return &measured.ToolCalling
	case "tool_restraint":
		return &measured.ToolRestraintPool
	default:
		return &measured.User
	}
}
