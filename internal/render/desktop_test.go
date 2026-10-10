package render

import (
	"bytes"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/desktop"
)

func TestDesktopStatusPrintsUnmeasuredWithoutReplacingIt(t *testing.T) {
	var buf bytes.Buffer
	WriteDesktopStatus(&buf, desktop.Status{
		State: desktop.StateEmpty,
		Rows: []desktop.Row{
			{ID: "estimated_fit", Label: "Estimated fit", Value: "unmeasured", State: "unmeasured"},
		},
		Limits: []string{"This status reads sealed local evidence."},
	}, "plain")
	output := buf.String()
	if !strings.Contains(output, "unmeasured (unmeasured)") || strings.Contains(output, "compatible") {
		t.Fatalf("%s", output)
	}
}
