package role

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/experiment"
	"github.com/blisspixel/fitr/internal/record"
)

const ReviewSchema = "fitr.role.review.v1"

type Candidate struct {
	ID         string               `json:"id"`
	RunID      string               `json:"run_id"`
	Model      string               `json:"model,omitempty"`
	State      string               `json:"state"`
	Reasons    []string             `json:"reasons,omitempty"`
	Evaluation *decision.Evaluation `json:"evaluation,omitempty"`
	Preference *PreferenceResult    `json:"preference,omitempty"`
}

type ReviewReport struct {
	Schema      string                 `json:"schema"`
	Role        string                 `json:"role"`
	Revision    string                 `json:"revision"`
	Scope       string                 `json:"scope"`
	State       string                 `json:"state"`
	Candidates  []Candidate            `json:"candidates"`
	Comparison  *experiment.Comparison `json:"comparison,omitempty"`
	Lead        string                 `json:"exploration_lead,omitempty"`
	Gaps        []string               `json:"gaps,omitempty"`
	Next        string                 `json:"next"`
	EvaluatedAt string                 `json:"evaluated_at"`
}

// Review reloads canonical evidence on every call. Attachments pin evidence,
// not mutable model aliases; an overwritten or removed result stays visible
// as stale instead of silently changing the candidate set.
func Review(library Library, records record.Store, now time.Time) (ReviewReport, error) {
	if err := library.Validate(); err != nil {
		return ReviewReport{}, err
	}
	if now.IsZero() {
		return ReviewReport{}, errors.New("role review requires a current time")
	}
	spec, err := library.CurrentSpec()
	if err != nil {
		return ReviewReport{}, err
	}
	report := ReviewReport{
		Schema: ReviewSchema, Role: library.Name, Revision: library.CurrentRevision,
		Scope: "battery_screening", State: "empty", Candidates: []Candidate{},
		Next:        "Attach canonical run evidence; a source claim cannot qualify a model.",
		EvaluatedAt: now.UTC().Format(time.RFC3339),
	}
	var qualified []*record.Record
	for _, attachment := range library.Candidates {
		candidate, result := reviewAttachment(attachment, spec, records, now)
		report.Candidates = append(report.Candidates, candidate)
		if candidate.State == "eligible" {
			qualified = append(qualified, result)
		}
	}
	selectCandidates(&report, spec, qualified)
	return report, nil
}

func reviewAttachment(attachment Attachment, spec Spec, records record.Store, now time.Time) (Candidate, *record.Record) {
	candidate := Candidate{ID: attachment.EvidenceSHA256, RunID: attachment.RunID, State: "unresolved"}
	result, _, err := readCanonicalRoleRecord(attachment.Path, records)
	if err != nil {
		candidate.Reasons = []string{"Pinned evidence is missing or invalid; reattach a canonical run."}
		return candidate, nil
	}
	candidate.Model = result.Model
	if result.Completion == nil || result.Completion.EvidenceSHA256 != attachment.EvidenceSHA256 ||
		result.StableRunID() != attachment.RunID {
		candidate.State = "stale"
		candidate.Reasons = []string{"The source no longer matches the attached evidence identity."}
		return candidate, nil
	}
	if issue := result.EvidenceIntegrityIssue(); issue != "" {
		candidate.Reasons = []string{issue}
		return candidate, nil
	}
	started, err := time.Parse(time.RFC3339, result.StartedAt)
	if err != nil || started.After(now.Add(5*time.Minute)) {
		candidate.Reasons = []string{"The evidence timestamp cannot establish freshness."}
		return candidate, nil
	}
	if now.Sub(started) > time.Duration(spec.MaxAgeDays)*24*time.Hour {
		candidate.State = "stale"
		candidate.Reasons = []string{"Evidence exceeds this role's maximum age; measure again before choosing."}
		return candidate, nil
	}
	evaluation, err := decision.Evaluate(result, spec.Decision)
	if err != nil {
		candidate.Reasons = []string{"The sealed run cannot be evaluated against this role."}
		return candidate, nil
	}
	candidate.Evaluation = &evaluation
	if evaluation.Subject.ArtifactBinding != record.IdentityBindingRuntime ||
		evaluation.Subject.ArtifactDigest == "" || evaluation.Subject.ComparabilityKey == "" {
		candidate.Reasons = []string{"Runtime-bound artifact and device configuration are required."}
		return candidate, nil
	}
	candidate.State = string(evaluation.Eligibility)
	for _, requirement := range evaluation.Requirements {
		if requirement.State != decision.RequirementEstablished {
			candidate.Reasons = append(candidate.Reasons, requirement.ID+": "+requirement.Reason)
		}
	}
	if candidate.State != "eligible" {
		return candidate, nil
	}
	candidate.Preference, candidate.Reasons = preferenceResult(spec, evaluation.Requirements)
	return candidate, result
}

func selectCandidates(report *ReviewReport, spec Spec, qualified []*record.Record) {
	if len(report.Candidates) == 0 {
		return
	}
	report.State = "unresolved"
	report.Next = "Resolve missing, stale or uncertain evidence before choosing a model."
	for _, candidate := range report.Candidates {
		if candidate.State != "eligible" && candidate.State != "ineligible" {
			report.Gaps = append(report.Gaps, "At least one declared candidate has unresolved quality or resource evidence.")
			return
		}
	}
	if len(qualified) == 0 {
		report.State = "no-qualified-candidate"
		report.Next = "Try another configuration or revise the role explicitly. A smaller model must earn its own quality evidence."
		return
	}
	if len(qualified) == 1 {
		report.State = "single-qualified"
		report.Next = "One candidate clears the declared screening floors. This does not establish general workflow competence or a comparative winner."
		return
	}
	compareCandidates(report, spec, qualified)
}

func compareCandidates(report *ReviewReport, spec Spec, qualified []*record.Record) {
	comparison, err := experiment.AnalyzeQuant(qualified, spec.Decision, nil)
	if err != nil {
		report.Gaps = append(report.Gaps, "Candidate comparison could not be validated.")
		return
	}
	report.Comparison = &comparison.Comparison
	if !comparison.Comparison.Ready {
		report.Gaps = append(report.Gaps, comparison.Comparison.Missing...)
		report.Next = "Align the qualified candidates' runtime, device, context and measurement protocol."
		return
	}
	for _, candidate := range report.Candidates {
		if candidate.State == "eligible" && candidate.Preference == nil {
			report.Gaps = append(report.Gaps, "A qualified candidate is missing preference measurements or uncertainty bounds.")
			return
		}
	}
	report.State = "tradeoff"
	report.Next = "Preference bounds overlap or weight sensitivity changes the choice; collect evidence that resolves the tradeoff."
	for _, candidate := range report.Candidates {
		if candidate.State != "eligible" {
			continue
		}
		leads := true
		for _, other := range report.Candidates {
			if other.ID != candidate.ID && other.State == "eligible" && !robustlyLeads(*candidate.Preference, *other.Preference, spec.Preferences) {
				leads = false
				break
			}
		}
		if leads {
			report.State, report.Lead = "exploration-lead", candidate.ID
			report.Next = "Collect fresh confirmation under a sealed role preference policy before adoption. Exploration does not authorize a model switch."
			return
		}
	}
}

// AttachRecord checks storage reconciliation before persisting the reference.
// It does not require passing quality: failed candidates belong in the review.
func AttachRecord(path string, records record.Store) (Attachment, error) {
	result, canonical, err := readCanonicalRoleRecord(path, records)
	if err != nil {
		return Attachment{}, err
	}
	if issue := result.EvidenceIntegrityIssue(); issue != "" {
		return Attachment{}, fmt.Errorf("attach canonical evidence: %s", issue)
	}
	if result.Completion == nil {
		return Attachment{}, errors.New("attach requires a sealed completion receipt")
	}
	return Attachment{Path: canonical, EvidenceSHA256: result.Completion.EvidenceSHA256, RunID: result.StableRunID()}, nil
}

func readCanonicalRoleRecord(path string, records record.Store) (*record.Record, string, error) {
	if err := rejectRoleSymlink(path); err != nil {
		return nil, "", err
	}
	result, err := records.Read(path)
	if err != nil {
		return nil, "", err
	}
	canonical, err := canonicalRecordPath(path, records, result.Model)
	if err != nil {
		return nil, "", err
	}
	return result, canonical, nil
}

func canonicalRecordPath(path string, records record.Store, model string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.Abs(records.CanonicalPath(model))
	if err != nil {
		return "", err
	}
	if absolute != canonical && (runtime.GOOS != "windows" || !strings.EqualFold(absolute, canonical)) {
		return "", errors.New("attach requires the model's canonical current result path")
	}
	return canonical, nil
}
