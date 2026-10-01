package render

import (
	"fmt"
	"io"

	"github.com/blisspixel/fitr/internal/analysis"
)

// WriteSourceProjection composes the arithmetic owner's facts to the current
// terminal width, including transport observations when projection is absent.
func WriteSourceProjection(report io.Writer, projection *analysis.SourceFitReport, headers []analysis.SourceHeaderRead) {
	width := Width()
	for _, header := range headers {
		detail := fmt.Sprintf("%s: %s; %d bytes from %s", header.Path, header.Observation.Outcome,
			header.Observation.Bytes, header.Observation.Host)
		Field(report, "  prefix", 15, detail, width)
	}
	if projection == nil {
		return
	}

	if projection.RuntimeProfile != "" {
		runtimeDetail := projection.RuntimeProfile + ": " + projection.RuntimeStatus
		if projection.RuntimeReason != "" {
			runtimeDetail += "; " + projection.RuntimeReason
		}
		Field(report, "  runtime", 15, runtimeDetail, width)
	}
	Field(report, "  architecture", 15, projection.ArchitectureReason, width)
	if projection.ComponentPlan != nil {
		planDetail := fmt.Sprintf("%s: %.3f GiB required", projection.ComponentPlan.Status, float64(projection.ComponentPlan.TotalRequiredBytes)/float64(1<<30))
		if projection.ComponentPlan.TotalOptionalBytes > 0 {
			planDetail += fmt.Sprintf(" (%.3f GiB optional)", float64(projection.ComponentPlan.TotalOptionalBytes)/float64(1<<30))
		}
		if projection.ComponentPlan.Reason != "" {
			planDetail += "; " + projection.ComponentPlan.Reason
		}
		Field(report, "  plan", 15, planDetail, width)
	}
	Field(report, "  components", 15, projection.ProjectionReason, width)
	if projection.CapacitySource != "" {
		Field(report, "  ceiling", 15, projection.CapacitySource, width)
	}
	if projection.ComponentBytes != nil && projection.CapacityBytes != nil {
		detail := fmt.Sprintf("%.3f GiB weights + cache / %.3f GiB ceiling at %d context",
			float64(*projection.ComponentBytes)/float64(1<<30), float64(*projection.CapacityBytes)/float64(1<<30), projection.Context)
		Field(report, "  projection", 15, detail, width)
	}
	for _, gap := range projection.Gaps {
		Field(report, "  gap", 15, gap, width)
	}
}
