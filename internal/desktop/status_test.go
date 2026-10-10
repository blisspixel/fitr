package desktop

import (
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"
)

func TestProjectEmptyStoreAsksForARoleList(t *testing.T) {
	status := Project(Evidence{})
	if status.Schema != Schema || !status.ReadOnly || status.State != StateEmpty || status.Model != "" {
		t.Fatalf("%+v", status)
	}
	if len(status.Rows) != 7 || status.Next.Effect != EffectRead || strings.Join(status.Next.Argv, " ") != "fitr role list" {
		t.Fatalf("rows %d next %+v", len(status.Rows), status.Next)
	}
	for _, row := range status.Rows {
		if row.Value == "" || strings.Contains(row.Value, "compatible") {
			t.Fatalf("row invented a claim: %+v", row)
		}
	}
}

func TestProjectRefusesToChooseAmongRoles(t *testing.T) {
	status := Project(Evidence{Libraries: []role.Library{{Name: "zeta"}, {Name: "alpha"}}})
	if status.State != StateUnresolved || status.Model != "" || len(status.Roles) != 2 || status.Roles[0] != "alpha" || status.Roles[1] != "zeta" {
		t.Fatalf("%+v", status)
	}
	if status.Next.Effect != EffectRead || len(status.Next.Argv) != 0 {
		t.Fatalf("choosing a role became a command: %+v", status.Next)
	}
}

func TestProjectKeepsStaleFitUnmeasured(t *testing.T) {
	status := Project(Evidence{
		RoleName: "coding",
		Selection: &role.SelectionStatus{
			State: "stale", Reason: "evidence expired",
			Selection: &role.SelectionReceipt{Selected: role.ConfirmationPoint{Model: record.ModelIdentity{Resolved: "qwen3:8b"}}},
		},
	})
	if status.State != StateStale || status.Model != "qwen3:8b" {
		t.Fatalf("%+v", status)
	}
	if row := findRow(t, status, "estimated_fit"); row.Value != "unmeasured" || row.State != "unmeasured" {
		t.Fatalf("stale evidence gained an estimate: %+v", row)
	}
	if row := findRow(t, status, "measured_fit"); row.Value != "unmeasured" {
		t.Fatalf("stale evidence gained a measurement: %+v", row)
	}
	if row := findRow(t, status, "freshness"); row.State != "stale" || !strings.Contains(row.Value, "evidence expired") {
		t.Fatalf("freshness: %+v", row)
	}
}

func TestNilPredictionStaysUnmeasured(t *testing.T) {
	status := Project(localEvidence(analysis.Report{Context: analysis.Context{Requested: 4096}}))
	row := findRow(t, status, "estimated_fit")
	if row.Value != "unmeasured" || row.State != "unmeasured" {
		t.Fatalf("nil prediction became %q (%s)", row.Value, row.State)
	}
}

func TestDescriptiveBudgetIsNotObservedFit(t *testing.T) {
	report := analysis.Report{Capacity: analysis.Capacity{Budget: &analysis.CapacityBudgetObservation{
		Status: analysis.StatusDescriptiveOnly, State: analysis.CapacityBudgetFit, BudgetBytes: 100,
	}}}
	row := findRow(t, Project(localEvidence(report)), "measured_fit")
	if row.Value != "descriptive only" || row.State != string(analysis.StatusDescriptiveOnly) || row.State == string(analysis.CapacityBudgetFit) {
		t.Fatalf("descriptive budget became %+v", row)
	}
}

func TestRequestedContextZeroStaysUnmeasured(t *testing.T) {
	report := analysis.Report{Context: analysis.Context{Requested: 0, State: ""}}
	row := findRow(t, Project(localEvidence(report)), "requested_context")
	if row.Value != "unmeasured" || row.State != "unmeasured" {
		t.Fatalf("requested zero became %+v", row)
	}
}

func TestAvailablePredictionWithoutBytesStaysUnmeasured(t *testing.T) {
	report := analysis.Report{Capacity: analysis.Capacity{Prediction: &analysis.CapacityPredictionObservation{
		Status: analysis.StatusAvailable,
	}}}
	row := findRow(t, Project(localEvidence(report)), "estimated_fit")
	if row.Value != "unmeasured" || row.State != "unmeasured" {
		t.Fatalf("empty projection became %+v", row)
	}
}

func TestUnresolvedRequirementsComeFromTheMatchedCandidate(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	other := "sha256:" + strings.Repeat("a", 64)
	status := Project(Evidence{
		RoleName: "coding",
		Review: &role.ReviewReport{Candidates: []role.Candidate{
			{ID: other, State: "unresolved", Evaluation: &decision.Evaluation{Requirements: []decision.RequirementResult{
				{ID: "other-need", State: decision.RequirementUnresolved, Reason: "other open"},
			}}},
			{ID: digest, State: "eligible", Evaluation: &decision.Evaluation{Requirements: []decision.RequirementResult{
				{ID: "quality", State: decision.RequirementEstablished, Reason: "met"},
				{ID: "memory", State: decision.RequirementBlocked, Reason: "blocked"},
			}}},
		}},
		Selection: &role.SelectionStatus{State: "qualified", Selection: &role.SelectionReceipt{Selected: role.ConfirmationPoint{
			Attachment: role.Attachment{EvidenceSHA256: digest, Path: `C:\evidence\run.json`},
			Model:      record.ModelIdentity{Resolved: "qwen3:8b"},
		}}},
	})
	if status.State != StateQualified || status.Model != "qwen3:8b" || status.UnresolvedState != "listed" || len(status.Unresolved) != 1 {
		t.Fatalf("state %s unresolved %+v", status.State, status.Unresolved)
	}
	if status.Unresolved[0].ID != "memory" || status.Unresolved[0].State != string(decision.RequirementBlocked) {
		t.Fatalf("matched the wrong requirement: %+v", status.Unresolved)
	}
	if strings.Contains(status.Next.Reason, `C:\`) || strings.Contains(strings.Join(status.Sources, " "), "sha256:") {
		t.Fatalf("status leaked a path or digest: %+v", status)
	}
}

func TestProjectHidesPathsInStoreErrors(t *testing.T) {
	status := Project(Evidence{ReviewErr: errPath("open C:\\fitr\\roles\\coding.json: sha256:abcd")})
	if status.State != StateUnavailable || strings.Contains(status.Next.Reason, `\`) || strings.Contains(status.Next.Reason, "sha256:") {
		t.Fatalf("%+v", status.Next)
	}
}

func TestLoadEmptyDirectoryIsEmpty(t *testing.T) {
	directory := t.TempDir()
	status := Load(role.Store{Dir: directory}, record.Store{Dir: directory}, "", time.Now(), nil)
	if status.State != StateEmpty || status.Next.Effect != EffectRead {
		t.Fatalf("%+v", status)
	}
}

func TestLoadRejectsAnInvalidRoleName(t *testing.T) {
	directory := t.TempDir()
	status := Load(role.Store{Dir: directory}, record.Store{Dir: directory}, "Not A Role", time.Now(), nil)
	if status.State != StateUnsupported || status.Model != "" {
		t.Fatalf("%+v", status)
	}
}

func localEvidence(report analysis.Report) Evidence {
	return Evidence{RoleName: "coding", Report: &report, Review: &role.ReviewReport{Next: "measure again"}}
}

func findRow(t *testing.T, status Status, id string) Row {
	t.Helper()
	for _, row := range status.Rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("missing row %s", id)
	return Row{}
}

type errPath string

func (e errPath) Error() string { return string(e) }
