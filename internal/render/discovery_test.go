package render

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/clipperhouse/displaywidth"
)

func TestDiscoveryCardsRespectWidthAndKeepStatesInPlainText(t *testing.T) {
	for _, width := range []int{40, 60, 80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			t.Setenv("FITR_WIDTH", strconv.Itoa(width))
			t.Setenv("NO_COLOR", "1")
			var output bytes.Buffer
			WriteDiscovery(&output, []DiscoveryCard{{
				Role: "coding", Model: "candidate " + strings.Repeat("模型e\u0301", 30),
				Harness: "Pi version pinned", Claim: "hostile \x1b[2J text and " + strings.Repeat("long", 50),
				Next:  "fitr discover plan --role coding",
				Steps: []DiscoveryStep{{Label: "fit", Text: "verify exact context", Command: "fitr advise candidate"}},
			}}, "plain")
			text := output.String()
			if !strings.Contains(text, "[UNMEASURED]") || strings.Contains(text, "\x1b") {
				t.Fatalf("state or terminal safety lost: %q", text)
			}
			for _, line := range strings.Split(text, "\n") {
				if displaywidth.String(line) > width {
					t.Fatalf("line exceeds %d columns: %s", width, line)
				}
			}
		})
	}
}

func TestDiscoveryEmptyAndUnresolvedCards(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	for _, cards := range [][]DiscoveryCard{nil, {{Role: "daily-driver"}}} {
		var output bytes.Buffer
		WriteDiscovery(&output, cards, "rich")
		if !strings.Contains(output.String(), "\x1b[") || !strings.Contains(output.String(), "discovery") {
			t.Fatal("rich discovery lost its hierarchy")
		}
	}
}
