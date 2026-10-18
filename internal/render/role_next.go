package render

import (
	"strings"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/role"
)

// roleReviewNext prints the command the evaluation already chose. It does not
// invent fitr role confirm, a download, or a smaller model. Exploration-lead
// reads only the lead: a sibling command would be a switch this report does
// not authorize.
func roleReviewNext(report role.ReviewReport) (string, string) {
	if report.State == "exploration-lead" {
		return explorationReviewNext(report)
	}
	action, model, nameIt := strongestReviewAction(report)
	if action == nil {
		return unmeasuredReviewCommand(report), ""
	}
	return analysis.FormatArgv(action.Argv, model), reviewActionWhy(report, action, model, nameIt)
}

func explorationReviewNext(report role.ReviewReport) (string, string) {
	for _, candidate := range report.Candidates {
		if candidate.ID != report.Lead || candidate.Evaluation == nil || candidate.Evaluation.NextAction == nil {
			continue
		}
		action := candidate.Evaluation.NextAction
		if len(action.Argv) == 0 {
			break
		}
		return analysis.FormatArgv(action.Argv, candidate.Model), strings.TrimSpace(action.Reason)
	}
	return unmeasuredReviewCommand(report), ""
}

func strongestReviewAction(report role.ReviewReport) (*decision.Action, string, bool) {
	var best *decision.Action
	model := ""
	unresolved := 0
	for _, candidate := range report.Candidates {
		if candidate.State != "eligible" && candidate.State != "ineligible" {
			unresolved++
		}
		next := candidateNextAction(candidate)
		if !decision.StrongerNextAction(best, next) {
			continue
		}
		best = next
		model = candidate.Model
	}
	return best, model, unresolved > 1
}

func candidateNextAction(candidate role.Candidate) *decision.Action {
	if candidate.Evaluation == nil {
		return nil
	}
	return candidate.Evaluation.NextAction
}

func reviewActionWhy(report role.ReviewReport, action *decision.Action, model string, nameIt bool) string {
	why := strings.TrimSpace(action.Reason)
	if nameIt {
		name := strings.TrimSpace(model)
		if name == "" {
			name = "name is unmeasured"
		}
		why = "candidate " + name + ": " + why
	}
	limit := strings.TrimSpace(report.Next)
	if limit == "" || strings.Contains(why, limit) {
		return why
	}
	if why == "" {
		return limit
	}
	if strings.HasSuffix(why, ".") {
		return why + " " + limit
	}
	return why + ". " + limit
}

func unmeasuredReviewCommand(report role.ReviewReport) string {
	limit := strings.TrimSpace(report.Next)
	if limit == "" {
		return "next command is unmeasured"
	}
	return "next command is unmeasured: " + limit
}
