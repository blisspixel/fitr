package contextquality

import "errors"

// Analyze re-verifies the finite answer set in plan order. Missing or
// unavailable observations suppress VerifiedPrefixUTF8Bytes across the entire
// phase, including tiers above an already failed tier. Diagnostic bounds are
// not preferences, token capacities, statistical intervals or runtime proof.
func Analyze(plan Plan, observations []Observation) (Report, error) {
	if err := plan.Validate(); err != nil {
		return Report{}, err
	}
	indexed, err := indexObservations(plan, observations)
	if err != nil {
		return Report{}, err
	}
	report := Report{Schema: ReportSchema, PlanSHA256: plan.PlanSHA256, Scope: "finite_context_task_set",
		BoundKind: "finite_task_set", Runtime: "unbound", NativeTokenAccounting: "unknown",
		Counts: Counts{Planned: len(plan.Cells)}, Cells: []Verification{}, Tiers: []Tier{}}
	for index, cell := range plan.Cells {
		observation, exists := indexed[cell.ID]
		result, err := analyzeCell(plan, index+1, observation, exists)
		if err != nil {
			return Report{}, err
		}
		report.Cells = append(report.Cells, result)
		if index%CellsPerTier == 0 {
			report.Tiers = append(report.Tiers, Tier{PayloadUTF8Bytes: cell.PayloadUTF8Bytes, Counts: Counts{Planned: CellsPerTier}})
		}
		addOutcome(&report.Counts, result.Outcome)
		addOutcome(&report.Tiers[len(report.Tiers)-1].Counts, result.Outcome)
	}
	for index := range report.Tiers {
		report.Tiers[index].Outcome = countOutcome(report.Tiers[index].Counts)
	}
	report.Outcome = countOutcome(report.Counts)
	report.Complete = report.Counts.Unavailable == 0
	derivePrefix(&report)
	return report, nil
}

func indexObservations(plan Plan, observations []Observation) (map[string]Observation, error) {
	if len(observations) > len(plan.Cells) {
		return nil, errors.New("context task observation count exceeds the plan")
	}
	cells := make(map[string]Cell, len(plan.Cells))
	for _, cell := range plan.Cells {
		cells[cell.ID] = cell
	}
	indexed := make(map[string]Observation, len(observations))
	for _, observation := range observations {
		if len(observation.CellID) != 71 {
			return nil, errors.New("context observation has an invalid cell identity")
		}
		cell, exists := cells[observation.CellID]
		_, duplicate := indexed[observation.CellID]
		if !exists || duplicate || observation.PayloadSHA256 != cell.PayloadSHA256 || observation.PromptSHA256 != cell.PromptSHA256 {
			return nil, errors.New("context observation is unknown, duplicated or bound to different content")
		}
		if err := validateDisposition(observation); err != nil {
			return nil, err
		}
		indexed[cell.ID] = observation
	}
	return indexed, nil
}

func validateDisposition(observation Observation) error {
	if observation.Disposition != NotAvailable && observation.UnavailableReason != "" {
		return errors.New("available observation cannot claim an unavailable reason")
	}
	switch observation.Disposition {
	case Answered:
		return nil // Oversized answers fail the task without copying their bytes.
	case OutputLimit:
		if len(observation.Answer) > MaxAnswerBytes {
			return errors.New("partial answer exceeds its retained byte limit")
		}
	case ContextLimit:
		if observation.Answer != "" {
			return errors.New("refused context input cannot also contain an answer")
		}
	case NotAvailable:
		if observation.Answer != "" {
			return errors.New("unavailable task cannot retain a completed answer")
		}
		switch observation.UnavailableReason {
		case "cancelled", "transport_error", "accounting_unknown", "runtime_unverified", "not_attempted":
			return nil
		default:
			return errors.New("unavailable task requires a supported reason")
		}
	default:
		return errors.New("unsupported context task disposition")
	}
	return nil
}

func analyzeCell(plan Plan, sequence int, observation Observation, exists bool) (Verification, error) {
	result := Verification{Schema: VerificationSchema, CellID: plan.Cells[sequence-1].ID, Outcome: Unavailable, Reason: "missing_observation"}
	if !exists {
		return result, nil
	}
	switch observation.Disposition {
	case NotAvailable:
		result.Reason = observation.UnavailableReason
	case OutputLimit, ContextLimit:
		result.Outcome, result.Reason = Fail, string(observation.Disposition)
	case Answered:
		if len(observation.Answer) > MaxAnswerBytes {
			result.Outcome, result.Reason = Fail, "answer_too_large"
			return result, nil
		}
		task, err := plannedTask(plan, sequence)
		if err != nil {
			return Verification{}, err
		}
		return verifyTask(task, []byte(observation.Answer))
	}
	return result, nil
}

func addOutcome(counts *Counts, outcome Outcome) {
	switch outcome {
	case Pass:
		counts.Pass++
	case Fail:
		counts.Fail++
	case Unavailable:
		counts.Unavailable++
	}
}

func countOutcome(counts Counts) Outcome {
	if counts.Unavailable > 0 {
		return Unavailable
	}
	if counts.Fail > 0 {
		return Fail
	}
	return Pass
}

func derivePrefix(report *Report) {
	known, possible := true, true
	for _, tier := range report.Tiers {
		known = known && tier.Outcome == Pass
		possible = possible && tier.Counts.Fail == 0
		if known {
			value := tier.PayloadUTF8Bytes
			report.KnownPrefixUTF8Bytes = &value
		}
		if possible {
			value := tier.PayloadUTF8Bytes
			report.PossiblePrefixUTF8Bytes = &value
		}
	}
	if report.Complete && report.KnownPrefixUTF8Bytes != nil {
		value := *report.KnownPrefixUTF8Bytes
		report.VerifiedPrefixUTF8Bytes = &value
		report.AtLeastLargestTested = value == report.Tiers[len(report.Tiers)-1].PayloadUTF8Bytes
	}
}
