package role

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/experiment"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/strictjson"
)

const (
	ConfirmationBundleSchema       = "fitr.role.confirmation.bundle.v1"
	ConfirmationReportSchema       = "fitr.role.confirmation.report.v1"
	maximumConfirmationBundleBytes = 64 << 20
)

type ConfirmationReport struct {
	Schema               string                 `json:"schema"`
	Scope                string                 `json:"scope"`
	State                string                 `json:"state"`
	PlanSHA256           string                 `json:"plan_sha256"`
	ChosenEvidenceSHA256 string                 `json:"chosen_evidence_sha256"`
	WinnerEvidenceSHA256 string                 `json:"winner_evidence_sha256,omitempty"`
	Candidates           []Candidate            `json:"candidates"`
	Comparison           *experiment.Comparison `json:"comparison,omitempty"`
	Gaps                 []string               `json:"gaps,omitempty"`
	EvaluatedAt          string                 `json:"evaluated_at"`
}

type ConfirmationBundle struct {
	Schema       string             `json:"schema"`
	Plan         ConfirmationPlan   `json:"plan"`
	PointRecords []*record.Record   `json:"point_records"`
	Report       ConfirmationReport `json:"report"`
}

// AnalyzeConfirmation validates fresh evidence against its issued plan before
// evaluating any preference. Missing positions produce incomplete, never a
// smaller candidate set. Invalid or substituted evidence is an error.
func AnalyzeConfirmation(plan ConfirmationPlan, points []*record.Record, now time.Time) (ConfirmationReport, error) {
	if err := plan.Validate(); err != nil {
		return ConfirmationReport{}, err
	}
	if now.IsZero() || len(points) > len(plan.Candidates) {
		return ConfirmationReport{}, errors.New("invalid confirmation time or point count")
	}
	created, _ := confirmationTime(plan.CreatedAt)
	expires, _ := confirmationTime(plan.ExpiresAt)
	if now.Before(created) || now.After(expires) {
		return ConfirmationReport{}, errors.New("confirmation is outside its issuance window")
	}
	seen := make(map[string]bool)
	for index, result := range points {
		if result == nil {
			continue
		}
		if err := validateConfirmationPoint(plan, index, result, now); err != nil {
			return ConfirmationReport{}, fmt.Errorf("role confirmation point %d: %w", index+1, err)
		}
		for _, key := range []string{result.StableRunID(), result.Completion.EvidenceSHA256} {
			if seen[key] {
				return ConfirmationReport{}, errors.New("confirmation points reuse a run or evidence identity")
			}
			seen[key] = true
		}
	}
	return evaluateConfirmationRecords(plan, points, now)
}

func validateConfirmationPoint(plan ConfirmationPlan, index int, result *record.Record, now time.Time) error {
	if issue := result.EvidenceIntegrityIssue(); issue != "" {
		return errors.New(issue)
	}
	if result.Manifest.Provenance == nil {
		return errors.New("confirmation provenance is missing")
	}
	if err := ValidatePreparedConfirmationPoint(plan, index+1, result, result.Manifest.Model, *result.Manifest.Provenance); err != nil {
		return err
	}
	if err := ValidateConfirmationCapacityPoint(plan, index+1, result); err != nil {
		return err
	}
	if err := ValidateConfirmationContextPoint(plan, result); err != nil {
		return err
	}
	started, err := confirmationTime(result.StartedAt)
	created, _ := confirmationTime(plan.CreatedAt)
	// Current run timestamps have second precision. Fresh seed and binding
	// prevent same-second exploration reuse despite this serialization margin.
	if err != nil || started.Before(created.Truncate(time.Second)) || started.After(now.Add(time.Second)) {
		return errors.New("confirmation run does not establish fresh timing")
	}
	if !roleFinite(result.WallSeconds) || result.WallSeconds < 0 || result.WallSeconds > 24*60*60 ||
		started.Add(time.Duration(result.WallSeconds*float64(time.Second))).After(now.Add(time.Second)) {
		return errors.New("confirmation completion timing is invalid")
	}
	if err := confirmationCapacityWindow(result.CapacityPlan, started, now.Add(time.Second)); err != nil {
		return err
	}
	for _, candidate := range plan.Candidates {
		if candidate.RunID == result.StableRunID() || candidate.EvidenceSHA256 == result.Completion.EvidenceSHA256 {
			return errors.New("exploration identity cannot become fresh confirmation")
		}
	}
	return nil
}

func evaluateConfirmationRecords(plan ConfirmationPlan, points []*record.Record, now time.Time) (ConfirmationReport, error) {
	report := ConfirmationReport{
		Schema: ConfirmationReportSchema, Scope: ConfirmationScope, State: "incomplete", PlanSHA256: plan.PlanSHA256,
		ChosenEvidenceSHA256: plan.ChosenEvidenceSHA256, Candidates: []Candidate{}, EvaluatedAt: now.UTC().Format(time.RFC3339Nano),
	}
	complete := len(points) == len(plan.Candidates)
	for index := range plan.Candidates {
		if index >= len(points) || points[index] == nil {
			report.Candidates = append(report.Candidates, Candidate{State: "missing"})
			complete = false
			continue
		}
		candidate, err := confirmationEvaluation(plan.Spec, points[index])
		if err != nil {
			return ConfirmationReport{}, err
		}
		report.Candidates = append(report.Candidates, candidate)
	}
	if !complete {
		report.Gaps = []string{"All predeclared candidate positions must have fresh terminal evidence."}
		return report, nil
	}
	comparison, err := experiment.AnalyzeQuant(points, plan.Spec.Decision, nil)
	if err != nil {
		return ConfirmationReport{}, err
	}
	report.Comparison = &comparison.Comparison
	if !comparison.Comparison.Ready {
		report.State, report.Gaps = "incompatible", append([]string(nil), comparison.Comparison.Missing...)
		return report, nil
	}
	if failure := confirmationObservationState(points); failure != "" {
		report.State, report.Gaps = failure, []string{"Planned observations include unavailable evidence; keep the incumbent."}
		return report, nil
	}
	selectConfirmationWinner(&report, plan)
	return report, nil
}

func confirmationEvaluation(spec Spec, result *record.Record) (Candidate, error) {
	evaluation, err := decision.Evaluate(result, spec.Decision)
	if err != nil {
		return Candidate{}, err
	}
	candidate := Candidate{ID: result.Completion.EvidenceSHA256, RunID: result.StableRunID(), Model: result.Model,
		State: string(evaluation.Eligibility), Evaluation: &evaluation}
	for _, requirement := range evaluation.Requirements {
		if requirement.State != decision.RequirementEstablished {
			candidate.Reasons = append(candidate.Reasons, requirement.ID+": "+requirement.Reason)
		}
	}
	if evaluation.Eligibility == decision.DecisionEligible {
		candidate.Preference, candidate.Reasons = preferenceResult(spec, evaluation.Requirements)
	}
	return candidate, nil
}

func confirmationObservationState(points []*record.Record) string {
	state := ""
	for _, result := range points {
		for _, counts := range result.EvidenceCounts {
			if counts.Errors > 0 {
				return "failed"
			}
			if counts.Observed < counts.Expected {
				state = "incomplete"
			}
		}
	}
	return state
}

func selectConfirmationWinner(report *ConfirmationReport, plan ConfirmationPlan) {
	qualified := 0
	for _, candidate := range report.Candidates {
		if candidate.State != "eligible" && candidate.State != "ineligible" {
			report.State, report.Gaps = "unresolved", []string{"A declared quality or resource floor is unresolved."}
			return
		}
		if candidate.State == "eligible" {
			qualified++
			if candidate.Preference == nil {
				report.State, report.Gaps = "unresolved", []string{"An eligible candidate has missing preference bounds."}
				return
			}
		}
	}
	if qualified == 0 {
		report.State = "no-qualified-candidate"
		return
	}
	report.State = "overlap"
	for index, candidate := range report.Candidates {
		if candidate.State != "eligible" || !confirmationLeads(candidate, report.Candidates, plan.Spec.Preferences) {
			continue
		}
		report.WinnerEvidenceSHA256 = plan.Candidates[index].EvidenceSHA256
		report.State = "unexpected-winner"
		if report.WinnerEvidenceSHA256 == plan.ChosenEvidenceSHA256 {
			report.State = "confirmed"
		}
		return
	}
	report.Gaps = []string{"Preference bounds overlap or the choice changes within the sealed weight sensitivity."}
}

func confirmationLeads(candidate Candidate, others []Candidate, preferences []Preference) bool {
	for _, other := range others {
		if other.ID != candidate.ID && other.State == "eligible" && !robustlyLeads(*candidate.Preference, *other.Preference, preferences) {
			return false
		}
	}
	return true
}

func NewConfirmationBundle(plan ConfirmationPlan, points []*record.Record, now time.Time) (ConfirmationBundle, error) {
	report, err := AnalyzeConfirmation(plan, points, now)
	if err != nil {
		return ConfirmationBundle{}, err
	}
	bundle := ConfirmationBundle{Schema: ConfirmationBundleSchema, Plan: plan, PointRecords: points, Report: report}
	data, err := json.Marshal(bundle)
	if err != nil {
		return ConfirmationBundle{}, err
	}
	return decodeConfirmationBundle(data)
}

// Validate recomputes the report at its recorded evaluation time. Historical
// bundles remain auditable after expiry; adoption must separately check the
// current time, role revision, issued-plan state and canonical current twins.
func (bundle ConfirmationBundle) Validate() (ConfirmationReport, error) {
	if bundle.Schema != ConfirmationBundleSchema {
		return ConfirmationReport{}, errors.New("unsupported role confirmation bundle schema")
	}
	now, err := confirmationTime(bundle.Report.EvaluatedAt)
	if err != nil {
		return ConfirmationReport{}, err
	}
	rebuilt, err := AnalyzeConfirmation(bundle.Plan, bundle.PointRecords, now)
	if err != nil {
		return ConfirmationReport{}, err
	}
	if !confirmationEqual(bundle.Report, rebuilt) {
		return ConfirmationReport{}, errors.New("confirmation report differs from its sealed evidence")
	}
	return rebuilt, nil
}

func (bundle ConfirmationBundle) JSON() ([]byte, error) {
	if _, err := bundle.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(data)+1 > maximumConfirmationBundleBytes {
		return nil, errors.New("role confirmation bundle exceeds 64 MiB")
	}
	return append(data, '\n'), nil
}

func LoadConfirmationBundle(path string) (ConfirmationBundle, error) {
	if err := rejectRoleSymlink(path); err != nil {
		return ConfirmationBundle{}, err
	}
	data, err := boundedio.ReadFile(path, maximumConfirmationBundleBytes)
	if err != nil {
		return ConfirmationBundle{}, err
	}
	return decodeConfirmationBundle(data)
}

func decodeConfirmationBundle(data []byte) (ConfirmationBundle, error) {
	if len(data) > maximumConfirmationBundleBytes {
		return ConfirmationBundle{}, errors.New("role confirmation bundle exceeds 64 MiB")
	}
	if err := strictjson.Validate(data); err != nil {
		return ConfirmationBundle{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var bundle ConfirmationBundle
	if err := decoder.Decode(&bundle); err != nil {
		return ConfirmationBundle{}, err
	}
	if _, err := bundle.Validate(); err != nil {
		return ConfirmationBundle{}, err
	}
	return bundle, nil
}
