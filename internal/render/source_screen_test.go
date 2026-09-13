package render

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/source"

	"github.com/clipperhouse/displaywidth"
)

func TestSourceScreenAndProjectionComposeToWidth(t *testing.T) {
	for _, width := range []int{40, 80, 140} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			t.Setenv("FITR_WIDTH", strconv.Itoa(width))
			var output bytes.Buffer
			report := analysis.SourceScreen{State: "unresolved", Next: "Read a complete header before projecting this candidate.",
				Gates: []analysis.SourceScreenGate{{Name: "architecture", State: "unresolved", Reason: "incomplete metadata"},
					{Name: "fit", State: "not_checked", Reason: "Resolve the architecture gate first."}}}
			WriteSourceScreen(&output, report)
			components, ceiling := int64(3<<30), int64(4<<30)
			projection := analysis.SourceFitReport{ArchitectureReason: "architecture declaration", ProjectionReason: "component projection",
				ComponentBytes: &components, CapacityBytes: &ceiling, CapacitySource: "operator component ceiling", Context: 4096,
				Gaps: []string{"resident allocation remains unmeasured"}}
			reads := []analysis.SourceHeaderRead{{Path: strings.Repeat("model-", 40) + "\x1b[2J.gguf",
				Observation: source.PrefixObservation{Outcome: "resolved", Host: "public.example", Bytes: 32768}}}
			WriteSourceProjection(&output, &projection, reads)
			WriteSourceProjection(&output, nil, nil)
			text := output.String()
			for _, word := range []string{"UNRESOLVED", "not_checked", "public.example", "3.000 GiB", "4.000 GiB", "4096", "unmeasured"} {
				if !strings.Contains(text, word) {
					t.Fatalf("missing %q in %s", word, text)
				}
			}
			if strings.Contains(text, "\x1b") {
				t.Fatal("terminal controls survived source projection")
			}
			for _, line := range strings.Split(text, "\n") {
				if displaywidth.String(line) > width {
					t.Fatalf("projection overflowed width%d: %q", width, line)
				}
			}
		})
	}
}
