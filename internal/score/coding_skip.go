package score

import "strings"

// isolationSkipSentence keeps the stable "worker never started" clause and
// appends the reason the trial recorded. An empty or generic detail stays
// the clause alone. The scorer does not ask the live sandbox probe: a card
// reopened on another machine has to say what that run recorded.
func isolationSkipSentence(prefix, detail string) string {
	detail = strings.TrimSpace(detail)
	detail = strings.TrimPrefix(detail, "isolated execution is unavailable:")
	detail = strings.TrimSpace(detail)
	if detail == "" || detail == "isolated execution is unavailable" {
		return prefix
	}
	if advice := isolationSkipAdvice(detail); advice != "" {
		return prefix + ": " + advice
	}
	return prefix + ": " + detail
}

// isolationSkipAdvice is the operator sentence for the two probe reasons
// that otherwise read as a forgotten flag. Other recorded reasons are
// appended as themselves.
func isolationSkipAdvice(detail string) string {
	switch {
	case strings.Contains(detail, "bubblewrap (bwrap) is not installed"):
		return "bubblewrap (bwrap) is not installed; install bubblewrap to score coding locally (fitr doctor shows sandbox status)"
	case strings.Contains(detail, "needs bubblewrap on Linux"):
		return "scoring generated code needs bubblewrap on Linux; other results are unaffected"
	default:
		return ""
	}
}
