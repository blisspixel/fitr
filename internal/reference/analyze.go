package reference

import "github.com/blisspixel/fitr/internal/stats"

// Analyze derives every count and interval from the observations.
// An agent claim does not change the verdict the verifier recorded.
func Analyze(plan Plan, conditions RunConditions, rows []Observation) (Result, error) {
	if err := Preflight(plan, conditions); err != nil {
		return Result{}, err
	}
	ordered, err := align(plan, rows)
	if err != nil {
		return Result{}, err
	}
	tasks := deriveTasks(plan, ordered)
	result := Result{
		Schema: ResultSchema, EvidenceClass: EvidenceClass,
		Fresh: false, RecordedFixture: true, Plan: plan, Conditions: conditions,
		Observations: ordered, Tasks: tasks, Note: ResultNote,
	}
	result.Clean, result.Contaminated, result.Unmeasured = splitCounts(tasks)
	result.CleanRate = rateFrom(result.Clean.Resolved, result.Clean.Unresolved)
	result.ContaminatedRate = rateFrom(result.Contaminated.Resolved, result.Contaminated.Unresolved)
	result.TotalWallMillis = totalWall(ordered)
	result.EnergyJoules = sumOptional(energies(ordered))
	result.APICostUSD = sumOptional(costs(ordered))
	return result, nil
}

func deriveTasks(plan Plan, rows []Observation) []TaskResult {
	created := make(map[string]string, len(plan.Instances))
	for _, instance := range plan.Instances {
		created[instance.ID] = instance.Created
	}
	tasks := make([]TaskResult, 0, len(rows))
	for _, row := range rows {
		tasks = append(tasks, taskFrom(row, created[row.InstanceID], plan.ArtifactRelease))
	}
	return tasks
}

func taskFrom(row Observation, created, release string) TaskResult {
	return TaskResult{
		InstanceID: row.InstanceID, Repeat: row.Repeat, Created: created,
		Outcome: row.Verdict, Contamination: contamination(created, release),
		Steps: row.Steps, PromptTokens: row.PromptTokens, CompletionTokens: row.CompletionTokens,
		WallMillis: row.WallMillis, VerifierLogSHA256: row.VerifierLogSHA256,
		AgentClaimIgnored: row.AgentClaim != "", CacheHitTokens: row.CacheHitTokens,
	}
}

func splitCounts(tasks []TaskResult) (clean, contaminated, unmeasured Counts) {
	for _, task := range tasks {
		switch task.Contamination {
		case contaminationClean:
			addCount(&clean, task.Outcome)
		case ContaminationMaybe:
			addCount(&contaminated, task.Outcome)
			contaminated.PossiblyContaminated++
		default:
			addCount(&unmeasured, task.Outcome)
			unmeasured.ContaminationUnmeasured++
		}
	}
	return clean, contaminated, unmeasured
}

func addCount(counts *Counts, outcome string) {
	switch outcome {
	case OutcomeResolved:
		counts.Resolved++
	case OutcomeUnresolved:
		counts.Unresolved++
	case OutcomeHarness:
		counts.HarnessError++
	case OutcomeEnvironment:
		counts.EnvironmentError++
	case OutcomeTimeout:
		counts.Timeout++
	}
}

func rateFrom(resolved, unresolved int) Rate {
	denominator := resolved + unresolved
	if denominator == 0 {
		return Rate{Status: rateUnmeasured, Reason: "no scorable outcomes"}
	}
	interval := stats.Wilson(resolved, denominator)
	point, lo, hi := interval.Point, interval.Lo, interval.Hi
	return Rate{
		Resolved: resolved, Denominator: denominator,
		Estimate: &point, Lo: &lo, Hi: &hi, Status: rateMeasured,
	}
}

func totalWall(rows []Observation) int64 {
	var total int64
	for _, row := range rows {
		total += row.WallMillis
	}
	return total
}

func energies(rows []Observation) []*float64 {
	values := make([]*float64, len(rows))
	for i := range rows {
		values[i] = rows[i].EnergyJoules
	}
	return values
}

func costs(rows []Observation) []*float64 {
	values := make([]*float64, len(rows))
	for i := range rows {
		values[i] = rows[i].APICostUSD
	}
	return values
}

func sumOptional(values []*float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	var total float64
	for _, value := range values {
		if value == nil {
			return nil
		}
		total += *value
	}
	return &total
}
