package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/blisspixel/fitr/internal/role"
)

// AutoStatus is a presentation of a validated session, not a quality score.
// Limits describe admission allowances, never completed work or billing.
type AutoStatus struct {
	SessionID             string             `json:"session_id"`
	Role                  string             `json:"role"`
	State                 string             `json:"state"`
	Label                 string             `json:"label"`
	Incumbent             string             `json:"incumbent"`
	Candidate             string             `json:"candidate,omitempty"`
	Choice                string             `json:"preselected_choice,omitempty"`
	Gap                   string             `json:"gap,omitempty"`
	Next                  string             `json:"next,omitempty"`
	ExplorationPoints     int                `json:"exploration_points"`
	ConfirmationPoints    int                `json:"confirmation_points"`
	CandidateCount        int                `json:"candidate_count"`
	Requests              int64              `json:"requests"`
	RequestLimit          int64              `json:"request_limit"`
	OutputTokens          int64              `json:"requested_output_tokens"`
	OutputTokenLimit      int64              `json:"requested_output_token_limit"`
	ProtectedRequests     int64              `json:"protected_confirmation_requests"`
	ProtectedOutputTokens int64              `json:"protected_confirmation_output_tokens"`
	ExpiresAt             string             `json:"expires_at"`
	Review                *role.ReviewReport `json:"exploration_review,omitempty"`
}

func WriteAutoStatus(w io.Writer, status AutoStatus, mode string) {
	p, _ := inventoryStyle(Resolve(mode) == "rich")
	width := Width()
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Head, "fitr / auto"))
	Field(w, "  role", 15, status.Role, width)
	style := p.Warn
	if status.State == "adopted" {
		style = p.Pass
	}
	if status.Gap != "" {
		style = p.Fail
	}
	for _, line := range wrap(SingleLine(status.Label), width-4) {
		fmt.Fprintf(w, "  %s\n", p.wrap(style, line))
	}
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Muted, strings.Repeat("-", width-4)))
	Field(w, "  selected", 15, status.Incumbent, width)
	if status.Candidate != "" {
		Field(w, "  candidate", 15, status.Candidate, width)
	}
	if status.Choice != "" {
		Field(w, "  preselected", 15, status.Choice, width)
	}
	if status.Gap != "" {
		Field(w, "  gap", 15, status.Gap, width)
	}
	fmt.Fprintln(w)
	Field(w, "  evidence", 15, fmt.Sprintf("%d/%d exploration; %d/%d confirmation points complete", status.ExplorationPoints, status.CandidateCount, status.ConfirmationPoints, status.CandidateCount), width)
	if status.Review != nil {
		writeAutoExplorationReview(w, *status.Review, p, width)
	}
	Field(w, "  budget used", 15, fmt.Sprintf("%d/%d requests; %d/%d requested output tokens", status.Requests, status.RequestLimit, status.OutputTokens, status.OutputTokenLimit), width)
	if status.ProtectedRequests > 0 {
		Field(w, "  protected", 15, fmt.Sprintf("Confirmation: %d requests; %d requested output tokens", status.ProtectedRequests, status.ProtectedOutputTokens), width)
	}
	Field(w, "  expires", 15, status.ExpiresAt, width)
	if status.Next != "" {
		fmt.Fprintln(w)
		Field(w, "  next", 15, status.Next, width)
	}
	fmt.Fprintln(w)
	Field(w, "  session", 15, status.SessionID, width)
	Field(w, "  ", 2, "Quality floors come before preferences. Token allowances are requested caps, not measured usage.", width)
}

func writeAutoExplorationReview(w io.Writer, review role.ReviewReport, p palette, width int) {
	fmt.Fprintln(w)
	Field(w, "  review", 15, "Exploration rechecked at "+review.EvaluatedAt, width)
	Field(w, "  ", 15, "Recorded session outcome unchanged.", width)
	for _, candidate := range review.Candidates {
		Field(w, "  model", 15, candidate.Model, width)
		style := p.Warn
		switch candidate.State {
		case "eligible":
			style = p.Pass
		case "ineligible":
			style = p.Fail
		}
		for _, line := range wrap(SingleLine(strings.ToUpper(candidate.State)), width-15) {
			fmt.Fprintf(w, "  state        %s\n", p.wrap(style, line))
		}
		if candidate.Preference != nil {
			Field(w, "  preference", 15, fmt.Sprintf("%.3f | bounds %.3f to %.3f", candidate.Preference.Estimate, candidate.Preference.Low, candidate.Preference.High), width)
		}
		for _, reason := range candidate.Reasons {
			Field(w, "  gap", 15, reason, width)
		}
	}
	for _, gap := range review.Gaps {
		Field(w, "  gap", 15, gap, width)
	}
	fmt.Fprintln(w)
}
