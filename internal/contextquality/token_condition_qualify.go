package contextquality

import "errors"

// QualifyConditions derives the claim from the sealed plan and the
// observations. A passing cell does not erase a required failure. A pass
// with a missing payload or overhead count is missing accounting, not a
// qualified cell. This does not submit the schedule to a runtime.
func QualifyConditions(plan TokenConditionPlan, observed ConditionObservationSet) (ConditionReport, error) {
	if err := plan.Validate(); err != nil {
		return ConditionReport{}, err
	}
	byID, err := conditionObservations(plan, observed)
	if err != nil {
		return ConditionReport{}, err
	}
	label, err := UsableInputLabel(plan.TargetUsableInputTokens)
	if err != nil {
		return ConditionReport{}, err
	}
	report := ConditionReport{
		Schema: TokenConditionReport, PlanSHA256: plan.PlanSHA256,
		TargetUsableInputTokens: plan.TargetUsableInputTokens, TargetLabel: label,
		Semantics: plan.Semantics, Uncertainty: conditionUncertainty,
	}
	for _, item := range plan.Cases {
		outcome := conditionOutcome(byID[item.ID])
		report.Cases = append(report.Cases, ConditionCaseResult{
			CaseID: item.ID, Sequence: item.Sequence, Family: item.Family, Position: item.Position,
			Seed: item.Seed, Condition: item.Condition, Outcome: outcome,
		})
		switch outcome {
		case CasePass:
			report.Passed++
		case CaseQualityFailed:
			report.Failed++
		default:
			report.Unresolved++
		}
	}
	report.State, report.Reason, report.NextAction = decideConditions(report, byID, plan.TargetUsableInputTokens)
	return report, nil
}

func conditionObservations(plan TokenConditionPlan, observed ConditionObservationSet) (map[string]ConditionObservation, error) {
	if len(observed.Cases) != len(plan.Cases) {
		return nil, errors.New("token condition observations do not match the sealed schedule")
	}
	byID := map[string]ConditionObservation{}
	for _, item := range observed.Cases {
		if _, ok := byID[item.CaseID]; ok {
			return nil, errors.New("token condition observations repeat a sealed cell")
		}
		byID[item.CaseID] = item
	}
	for _, item := range plan.Cases {
		if _, ok := byID[item.ID]; !ok {
			return nil, errors.New("token condition observations do not match the sealed schedule")
		}
	}
	return byID, nil
}

func conditionOutcome(item ConditionObservation) string {
	if item.PayloadTokens == nil || item.OverheadTokens == nil {
		if item.Outcome == CaseQualityFailed {
			return CaseQualityFailed
		}
		return CaseMissingAccounting
	}
	switch item.Outcome {
	case CasePass, CaseQualityFailed, CaseCapacityLimited, CaseMissingAccounting, CaseTransportFailed, CaseNotTested:
		return item.Outcome
	default:
		return CaseNotTested
	}
}

func decideConditions(report ConditionReport, observed map[string]ConditionObservation, target int) (string, string, string) {
	if report.Failed > 0 {
		return TokenQualityFailed, "a required condition failed on quality, and a passing condition does not erase it",
			"do not adopt this target from another condition or a higher tier"
	}
	if conditionState(report, CaseMissingAccounting) {
		return TokenMissingAccounting, "a required condition is missing payload or overhead tokens",
			"record payload tokens and template overhead before claiming this target"
	}
	if payloadBelowTarget(report, observed, target) {
		return TokenRefused, "the observed payload is below the sealed target",
			"do not shrink the target to earn a pass"
	}
	if conditionState(report, CaseCapacityLimited) {
		return TokenCapacityLimited, "a required condition was refused for capacity",
			"do not shrink the target, window or reserve to earn a pass"
	}
	if conditionState(report, CaseTransportFailed) {
		return TokenTransportFailed, "a transport fault is not a model-quality result",
			"repeat the sealed plan; this fault is not a failed answer"
	}
	if conditionState(report, CaseNotTested) || report.Unresolved > 0 {
		return TokenNotTested, "a declared condition was not run",
			"run the sealed schedule before reading a result for this target"
	}
	return TokenQualified, "the sealed conditions passed as a fixed battery",
		"larger targets and untested positions remain unmeasured"
}

func payloadBelowTarget(report ConditionReport, observed map[string]ConditionObservation, target int) bool {
	for _, item := range report.Cases {
		if item.Outcome != CasePass {
			continue
		}
		payload := observed[item.CaseID].PayloadTokens
		if payload == nil || *payload < target {
			return true
		}
	}
	return false
}

func conditionState(report ConditionReport, outcome string) bool {
	for _, item := range report.Cases {
		if item.Outcome == outcome {
			return true
		}
	}
	return false
}

// SameConditionCell reports whether two sealed cells are the same measured
// comparison. Family, position and seed are not sufficient.
func SameConditionCell(left, right TokenConditionCase) bool {
	return left.ID != "" && left == right
}

func (set ConditionObservationSet) Clone() ConditionObservationSet {
	dup := set
	dup.Cases = make([]ConditionObservation, len(set.Cases))
	for i := range set.Cases {
		dup.Cases[i] = set.Cases[i]
		dup.Cases[i].PayloadTokens = copyInt(set.Cases[i].PayloadTokens)
		dup.Cases[i].OverheadTokens = copyInt(set.Cases[i].OverheadTokens)
	}
	return dup
}

func (report ConditionReport) Clone() ConditionReport {
	dup := report
	dup.Cases = append([]ConditionCaseResult(nil), report.Cases...)
	return dup
}
