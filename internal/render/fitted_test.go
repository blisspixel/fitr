package render

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"

	"github.com/clipperhouse/displaywidth"
)

func TestFittedLineUsesRoleStatusTimestamps(t *testing.T) {
	qualified := fittedStatus("qualified")
	qualified.EvaluatedAt = "2026-01-01T00:00:00Z"
	qualified.Selection.EvaluatedAt = "2026-02-02T00:00:00Z"
	word, body := SelectionSignature(qualified)
	if word != "fitted" || body != "coding: qwen3:8b, measured 2026-10-17, review by 2026-11-16" {
		t.Fatalf("signature = %q %q", word, body)
	}
	raw, err := json.Marshal(qualified)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "fitted") || strings.Contains(string(raw), "16K") {
		t.Fatalf("status JSON gained a display claim: %s", raw)
	}

	noStart := fittedStatus("qualified")
	noStart.Selection.Selected.StartedAt = ""
	_, body = SelectionSignature(noStart)
	if !strings.Contains(body, "measured 2026-02-02") || strings.Contains(body, "2026-01-01") {
		t.Fatalf("measured date = %q", body)
	}
	blank := fittedStatus("qualified")
	blank.Selection.Selected.Model.Resolved = " "
	if word, body := SelectionSignature(blank); word != "" || body != "" {
		t.Fatalf("blank model became a signature %q %q", word, body)
	}
	hostile := fittedStatus("qualified")
	hostile.Role = "coding\x1b[2J"
	hostile.Selection.Selected.Model.Resolved = "qwen3:8b\x1b[2J"
	if _, body := SelectionSignature(hostile); strings.Contains(body, "\x1b") || !strings.Contains(body, "coding: qwen3:8b") {
		t.Fatalf("hostile signature: %q", body)
	}
}

func TestFittedLineRefusesUnmeasuredStates(t *testing.T) {
	for _, state := range []string{"stale", "unselected", "failed", "unresolved"} {
		status := fittedStatus(state)
		if state == "unselected" {
			status.Selection = nil
		}
		got, text := SelectionSignature(status)
		if state == "stale" {
			if got != "refit" || strings.Contains(text, "fitted") {
				t.Fatalf("stale signature = %q %q", got, text)
			}
			continue
		}
		if got != "" || text != "" {
			t.Fatalf("%s invented a signature %q %q", state, got, text)
		}
	}
	challenger := fittedStatus("qualified")
	challenger.LastAttempt = &role.LifecycleAttemptStatus{Action: "failed", At: "2026-10-18T00:00:00Z"}
	if word, _ := SelectionSignature(challenger); word != "fitted" {
		t.Fatalf("failed challenger hid the incumbent: %q", word)
	}
	unmeasured := fittedStatus("unselected")
	unmeasured.Selection = nil
	unmeasured.LastAttempt = challenger.LastAttempt
	if word, body := SelectionSignature(unmeasured); word != "" || body != "" {
		t.Fatalf("unmeasured failure became a signature %q %q", word, body)
	}
}

func TestFittedLineMatchesOnlyTheNamedRun(t *testing.T) {
	qualified := fittedStatus("qualified")
	qualified.Selection.Selected.Attachment.RunID = "run-1"
	matched, text, ok := MatchingRunSignature(qualified, "run-1")
	if !ok || matched != "fitted" || !strings.Contains(text, "qwen3:8b") {
		t.Fatalf("matching run = %q %q %v", matched, text, ok)
	}
	if _, _, ok := MatchingRunSignature(qualified, "other"); ok {
		t.Fatal("a different run inherited the qualification")
	}
	if _, _, ok := MatchingRunSignature(qualified, ""); ok {
		t.Fatal("an empty run id matched")
	}
}

func TestFittedLineKeepsWidthColorAndChallenger(t *testing.T) {
	for _, width := range []int{40, 60} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			t.Setenv("FITR_WIDTH", strconv.Itoa(width))
			t.Setenv("NO_COLOR", "1")
			var output bytes.Buffer
			status := fittedStatus("qualified")
			status.Selection.Selected.Model.Resolved = strings.Repeat("model-", 12)
			WriteRoleSelection(&output, status, "plain")
			text := output.String()
			flat := strings.Join(strings.Fields(text), " ")
			if !strings.Contains(flat, "fitted") || strings.Contains(text, "\x1b") {
				t.Fatalf("plain fitted line: %s", text)
			}
			lines := strings.Split(text, "\n")
			hung := false
			for i, line := range lines {
				if displaywidth.String(line) > width {
					t.Fatalf("line exceeds %d: %q", width, line)
				}
				if !strings.Contains(line, "fitted") || i+1 >= len(lines) {
					continue
				}
				hung = strings.HasPrefix(lines[i+1], strings.Repeat(" ", 13)) && strings.TrimSpace(lines[i+1]) != ""
			}
			if !hung {
				t.Fatalf("fitted line did not hang under its gutter:\n%s", text)
			}
		})
	}

	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("FITR_WIDTH", "80")
	var rich bytes.Buffer
	WriteRoleSelection(&rich, fittedStatus("qualified"), "rich")
	text := rich.String()
	if !strings.Contains(text, "\x1b[32mfitted") || !strings.Contains(text, "\x1b[90m2026-10-17") || !strings.Contains(text, "\x1b[90m2026-11-16") {
		t.Fatalf("fitted ink: %s", text)
	}
	if strings.Contains(text, "\x1b[32mqwen3") || strings.Contains(text, "\x1b[36m") {
		t.Fatalf("model took the qualification color: %s", text)
	}

	var stale bytes.Buffer
	WriteRoleSelection(&stale, fittedStatus("stale"), "rich")
	staleText := stale.String()
	if !strings.Contains(staleText, "\x1b[33mrefit") || strings.Contains(staleText, "fitted") || !strings.Contains(staleText, "fitr role review coding") {
		t.Fatalf("stale line: %s", staleText)
	}

	var challenger bytes.Buffer
	failed := fittedStatus("qualified")
	failed.LastAttempt = &role.LifecycleAttemptStatus{Action: "failed", At: "2026-10-18T00:00:00Z"}
	failed.Attempts = 2
	WriteRoleSelection(&challenger, failed, "plain")
	challengerText := challenger.String()
	flat := strings.Join(strings.Fields(challengerText), " ")
	if !strings.Contains(flat, "fitted") || !strings.Contains(flat, "QUALIFIED") || !strings.Contains(flat, "failed") || strings.Contains(flat, "fitr role review") {
		t.Fatalf("failed challenger: %s", challengerText)
	}
}

func fittedStatus(state string) role.SelectionStatus {
	return role.SelectionStatus{
		Role: "coding", State: state, EvaluatedAt: "2026-01-01T00:00:00Z",
		Selection: &role.SelectionReceipt{
			Selected: role.ConfirmationPoint{
				Model:     record.ModelIdentity{Resolved: "qwen3:8b"},
				StartedAt: "2026-10-17T15:04:05Z",
			},
			ExpiresAt:   "2026-11-16T00:00:00Z",
			EvaluatedAt: "2026-02-02T03:04:05Z",
		},
	}
}
