package analysis

import (
	"strconv"

	"github.com/blisspixel/fitr/internal/contextquality"
	"github.com/blisspixel/fitr/internal/record"
)

const (
	TokenToneAccepted = "accepted"
	TokenToneGap      = "gap"
)

// TokenAccountCase is one declared case. Outcome is the observation's
// disposition. It is not a rate.
type TokenAccountCase struct {
	Family   string `json:"family"`
	Position string `json:"position"`
	Seed     uint64 `json:"seed"`
	Outcome  string `json:"outcome"`
}

// TokenAccount is the projection of a derived token receipt.
//
// Every sentence and label is copied from the report the record re-derived.
// A renderer must not turn the counts into a rate or read a byte prefix as
// this target. Status descriptive-only means the record cannot certify the
// target, whatever the stored state said.
type TokenAccount struct {
	Status        ObservationStatus  `json:"status"`
	TargetLabel   string             `json:"target_label"`
	Semantics     string             `json:"semantics"`
	State         string             `json:"state"`
	Tone          string             `json:"tone"`
	PayloadLabel  string             `json:"payload_label"`
	OverheadLabel string             `json:"overhead_label"`
	ReserveLabel  string             `json:"reserve_label"`
	WindowLabel   string             `json:"window_label"`
	Passed        int                `json:"passed"`
	Failed        int                `json:"failed"`
	Unresolved    int                `json:"unresolved"`
	Seeds         int                `json:"seeds"`
	Uncertainty   string             `json:"uncertainty"`
	Reason        string             `json:"reason"`
	FreeMemory    string             `json:"free_memory"`
	NextAction    string             `json:"next_action"`
	Cases         []TokenAccountCase `json:"cases"`
}

func tokenAccountFrom(result *record.Record, unclaimable bool) *TokenAccount {
	if result == nil || result.ContextTokenAccount == nil {
		return nil
	}
	report := result.ContextTokenAccount.Report
	status := StatusAvailable
	state := report.State
	reason := report.Reason
	next := report.NextAction
	if unclaimable {
		status = StatusDescriptiveOnly
		state = "descriptive_only"
		reason = "descriptive only: this record cannot certify a usable-input token target"
		next = "repair the record identity before reading the token account"
	}
	view := &TokenAccount{
		Status: status, TargetLabel: report.TargetLabel, Semantics: report.Semantics,
		State: state, Tone: tokenTone(state), PayloadLabel: tokenCountLabel(report.ObservedPayloadTokens),
		OverheadLabel: tokenCountLabel(report.ObservedOverheadTokens), ReserveLabel: tokenReserveLabel(report),
		WindowLabel: tokenWindowLabel(report), Passed: report.Passed, Failed: report.Failed,
		Unresolved: report.Unresolved, Seeds: report.Seeds, Uncertainty: report.Uncertainty,
		Reason: reason, FreeMemory: report.FreeMemory, NextAction: next,
	}
	for _, item := range report.Cases {
		view.Cases = append(view.Cases, TokenAccountCase{
			Family: string(item.Family), Position: string(item.Position), Seed: item.Seed, Outcome: item.Outcome,
		})
	}
	return view
}

func tokenTone(state string) string {
	if state == contextquality.TokenQualified {
		return TokenToneAccepted
	}
	return TokenToneGap
}

func tokenCountLabel(value *int) string {
	if value == nil {
		return "unmeasured"
	}
	return strconv.Itoa(*value)
}

func tokenReserveLabel(report contextquality.TokenReport) string {
	label := strconv.Itoa(report.GenerationReserveTokens)
	if report.ReasoningReserveTokens > 0 {
		label += "; reasoning " + strconv.Itoa(report.ReasoningReserveTokens)
	}
	return label
}

func tokenWindowLabel(report contextquality.TokenReport) string {
	requested := strconv.Itoa(report.RequestedWindowTokens)
	if report.EnforcedWindowTokens == nil {
		return "enforced window unmeasured; requested " + requested
	}
	return "enforced " + strconv.Itoa(*report.EnforcedWindowTokens) + "; requested " + requested
}
