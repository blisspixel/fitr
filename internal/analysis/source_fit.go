package analysis

import "github.com/blisspixel/fitr/internal/source"

// SourceArchitectureShape contains the architecture facts and cache figures
// derived by the arithmetic owner. Presentation never recomputes these facts.
type SourceArchitectureShape = source.ArchitectureShape

// SourceFitReport describes declared weights plus modeled cache under one
// explicit component ceiling. It never establishes runtime support or quality.
type SourceFitReport struct {
	Schema             string                   `json:"schema"`
	SourceSHA256       string                   `json:"source_sha256,omitempty"`
	ArchitectureStatus string                   `json:"architecture_status"`
	ArchitectureReason string                   `json:"architecture_reason"`
	ProjectionStatus   string                   `json:"projection_status"`
	ProjectionReason   string                   `json:"projection_reason"`
	RuntimeStatus      string                   `json:"runtime_status"`
	RuntimeReason      string                   `json:"runtime_reason,omitempty"`
	RuntimeProfile     string                   `json:"runtime_profile,omitempty"`
	Files              []string                 `json:"files"`
	Context            int                      `json:"context,omitempty"`
	CapacityBytes      *int64                   `json:"capacity_bytes,omitempty"`
	CapacitySource     string                   `json:"capacity_source,omitempty"`
	WeightsBytes       *int64                   `json:"weights_bytes,omitempty"`
	CacheBytes         *int64                   `json:"cache_bytes,omitempty"`
	ComponentBytes     *int64                   `json:"component_bytes,omitempty"`
	Shape              *SourceArchitectureShape `json:"architecture_shape,omitempty"`
	ComponentPlan      *source.ComponentPlan    `json:"component_plan,omitempty"`
	Gaps               []string                 `json:"gaps"`
}

// SourceHeaderRead retains the selected filename, prefix digest and transport
// provenance without raw model bytes or a signed download location.
type SourceHeaderRead = source.HeaderRead
