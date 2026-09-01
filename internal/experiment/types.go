// Package experiment derives typed multi-run experiment reports from sealed
// fitr records. Experiment reports are analysis and are never persisted back
// into their source records as evidence.
package experiment

import "github.com/blisspixel/fitr/internal/analysis"

const ComparisonSchema = "fitr.experiment.comparison.v1"

type Stage string

const (
	StageExplore Stage = "explore"
	StageConfirm Stage = "confirm"
)

type FactorRole string

const (
	FactorTreatment     FactorRole = "treatment"
	FactorRequiredEqual FactorRole = "required_equal"
	FactorNuisance      FactorRole = "nuisance_observation"
)

type FactorState string

const (
	FactorEqual     FactorState = "equal"
	FactorDifferent FactorState = "different"
	FactorMissing   FactorState = "missing"
)

type FactorObservation struct {
	RunID       string `json:"run_id"`
	ValueSHA256 string `json:"value_sha256,omitempty"`
	Present     bool   `json:"present"`
}

type FactorComparison struct {
	Code         string              `json:"code"`
	Role         FactorRole          `json:"role"`
	State        FactorState         `json:"state"`
	Observations []FactorObservation `json:"observations"`
	Reason       string              `json:"reason"`
}

type Comparison struct {
	Schema    string             `json:"schema"`
	Treatment string             `json:"treatment"`
	Ready     bool               `json:"ready"`
	Factors   []FactorComparison `json:"factors"`
	Missing   []string           `json:"missing,omitempty"`
}

type Action struct {
	Code   string   `json:"code"`
	Argv   []string `json:"argv,omitempty"`
	Reason string   `json:"reason"`
}

type SourceReference struct {
	RunID          string          `json:"run_id"`
	EvidenceSHA256 string          `json:"evidence_sha256"`
	Source         analysis.Source `json:"source"`
}
