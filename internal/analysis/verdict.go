package analysis

import (
	"strconv"
	"strings"

	"github.com/blisspixel/fitr/internal/score"
)

// VerdictPresentation projects the sealed verdict without rescoring it. The
// deciding family is present only when the recorded breakdown identifies it;
// an unknown historical explanation remains descriptive rather than guessed.
type VerdictPresentation struct {
	State          score.State `json:"state"`
	Measure        string      `json:"measure,omitempty"`
	Gate           string      `json:"gate,omitempty"`
	Detail         []string    `json:"detail,omitempty"`
	Note           string      `json:"note,omitempty"`
	Why            string      `json:"why"`
	DecidingFamily string      `json:"deciding_family,omitempty"`
}

// PresentVerdict keeps an independent binary failure ahead of a pooled rate
// that does not decide the overall verdict. This is derived display wording,
// never a replacement for the scorecard in a signed record or --full export.
func PresentVerdict(need string, verdict score.Verdict) VerdictPresentation {
	measure, gate, detail, note := verdict.Parts()
	view := VerdictPresentation{
		State: verdict.State, Measure: measure, Gate: gate,
		Detail: append([]string(nil), detail...), Note: note,
	}
	if need == "tool_restraint" && verdict.State == score.Fail {
		for i, item := range detail {
			if withdrawalFailed(item) {
				view = presentWithdrawalFailure(view, i)
				break
			}
		}
	}
	view.Why = verdictWhy(view)
	return view
}

func presentWithdrawalFailure(view VerdictPresentation, index int) VerdictPresentation {
	detail := view.Detail
	view.DecidingFamily = "tool_withdrawal"
	view.Detail = []string{detail[index]}
	if head := verdictHead(view.Measure, view.Gate); head != "" {
		view.Detail = append(view.Detail, restraintMeasurementLabel(detail)+head)
	}
	for j, fragment := range detail {
		if j != index {
			view.Detail = append(view.Detail, fragment)
		}
	}
	view.Measure, view.Gate = "withdrawal", ""
	view.Note = strings.Replace(view.Note, "undecided:", "ordinary irrelevance checks are undecided:", 1)
	view.Note = strings.TrimSpace(strings.Replace(view.Note, "Not a fail", "that interval alone is not a fail", 1))
	if view.Note != "" {
		view.Note += "; "
	}
	view.Note += "overall FAIL comes from the separate withdrawal scenario above"
	return view
}

func restraintMeasurementLabel(detail []string) string {
	// Older policies measured irrelevance and withdrawal together. Their
	// combined denominator cannot be relabeled as ordinary irrelevance.
	for _, fragment := range detail {
		if strings.HasPrefix(fragment, "tool_irrelevance ") {
			return "ordinary irrelevance: "
		}
	}
	return "combined checks: "
}

func verdictWhy(view VerdictPresentation) string {
	var parts []string
	if head := verdictHead(view.Measure, view.Gate); head != "" {
		parts = append(parts, head)
	}
	if len(view.Detail) > 0 {
		parts = append(parts, strings.Join(view.Detail, ", "))
	}
	if view.Note != "" {
		parts = append(parts, view.Note)
	}
	return strings.Join(parts, "; ")
}

// PresentVerdicts is the additive result JSON projection. The existing needs
// state map and canonical full record retain their contracts.
func PresentVerdicts(needs map[string]score.Verdict) map[string]VerdictPresentation {
	views := make(map[string]VerdictPresentation, len(needs))
	for need, verdict := range needs {
		views[need] = PresentVerdict(need, verdict)
	}
	return views
}

// DecidingVerdicts supplements a View export that already contains the full
// canonical scorecard. Carry only the independent-family overrides so the
// compact view does not duplicate every unchanged explanation and breakdown.
func DecidingVerdicts(needs map[string]score.Verdict) map[string]VerdictPresentation {
	views := make(map[string]VerdictPresentation)
	for need, verdict := range needs {
		view := PresentVerdict(need, verdict)
		if view.DecidingFamily != "" {
			views[need] = view
		}
	}
	return views
}

func verdictHead(measure, gate string) string {
	if measure != "" && gate != "" {
		return measure + " (" + gate + ")"
	}
	return measure + gate
}

func withdrawalFailed(detail string) bool {
	if detail == "did not stop cleanly after a tool was withdrawn" {
		return true
	}
	const prefix, suffix = "kept calling a withdrawn tool (", " dead calls)"
	if !strings.HasPrefix(detail, prefix) || !strings.HasSuffix(detail, suffix) {
		return false
	}
	count, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(detail, prefix), suffix))
	return err == nil && count > 1
}
