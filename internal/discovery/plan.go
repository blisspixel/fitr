package discovery

type Step struct {
	Code string   `json:"code"`
	Text string   `json:"text"`
	Argv []string `json:"argv,omitempty"`
}

type Proposal struct {
	IdeaID string `json:"idea_id"`
	Steps  []Step `json:"steps"`
}

// Plan produces inert steps. Each later stage depends on resolving the earlier
// evidence; a proposal never claims a source, artifact or harness was verified.
func Plan(idea Idea) Proposal {
	steps := []Step{{Code: "resolve", Text: "Verify source, artifact revision, quant, shards, license and runtime support."}}
	if idea.Model != "" {
		steps = append(steps, Step{Code: "fit", Text: "Check this exact artifact at the intended context and resource budget.",
			Argv: []string{"fitr", "advise", idea.Model}})
	}
	steps = append(steps,
		Step{Code: "requirements", Text: "Declare minimum task quality, reliability, authority and resource limits before preference weights."},
	)
	if idea.Harness != "" {
		steps = append(steps, Step{Code: "harness", Text: "Pin the harness, tools, permissions and model routing before testing the full workflow."})
	}
	steps = append(steps,
		Step{Code: "measure", Text: "Measure verified outcomes, retries and human corrections on the same role tasks; speed alone cannot qualify."},
		Step{Code: "confirm", Text: "Confirm any exploratory choice with fresh paired evidence."},
		Step{Code: "review", Text: "Keep a qualified incumbent unless the challenger earns this role. If none meets the quality floor, leave the role unfilled."},
	)
	return Proposal{IdeaID: idea.ID, Steps: steps}
}
