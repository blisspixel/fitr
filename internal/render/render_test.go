package render

import (
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/blisspixel/fitr/internal/score"
)

func TestNoColorEmptyStringMeansUnset(t *testing.T) {
	// no-color.org: present AND NOT EMPTY disables colour. NO_COLOR="" must NOT
	// disable it -- the classic off-by-one in this spec.
	t.Setenv("NO_COLOR", "1")
	if !noColor() {
		t.Fatal(`NO_COLOR=1 must disable colour`)
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	if noColor() {
		t.Fatal(`NO_COLOR="" means UNSET; FORCE_COLOR should win`)
	}
}

func TestNoColorZeroStillDisables(t *testing.T) {
	// "0" is non-empty, so per spec it DISABLES. This surprises people coming
	// from npm/chalk where FORCE_COLOR=0 means off -- documented, not fixed.
	t.Setenv("NO_COLOR", "0")
	if !noColor() {
		t.Fatal(`NO_COLOR=0 is non-empty and must disable colour`)
	}
}

func TestUnicodeFallsBackWhenNotUTF(t *testing.T) {
	t.Setenv("FITR_ASCII", "")
	t.Setenv("FITR_UNICODE", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "C")
	t.Setenv("WT_SESSION", "")
	if unicodeOK() {
		t.Fatal("a non-UTF locale must fall back to ASCII glyphs")
	}
	t.Setenv("LANG", "en_US.UTF-8")
	if !unicodeOK() {
		t.Fatal("a UTF locale should allow unicode glyphs")
	}
}

func TestAsciiOverrideAlwaysWins(t *testing.T) {
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("FITR_ASCII", "1")
	if unicodeOK() {
		t.Fatal("FITR_ASCII must force ASCII even on a UTF locale")
	}
	g := glyphs{" | ", "-", "+/-", "..."}
	if strings.ContainsAny(g.PM+g.Dot+g.Dash, "\u00b1\u00b7\u2014") {
		t.Fatal("ASCII glyph set must contain no multi-byte characters")
	}
}

func TestSanitizeStripsTerminalEscapes(t *testing.T) {
	// Model output is untrusted input to the terminal: ANSI can clear the
	// screen, reposition the cursor, or spoof a prompt.
	nasty := "hello\x1b[2J\x1b[1;1Hspoofed\x07 and \x1b[31mred"
	got := Sanitize(nasty)
	if strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, 0x07) {
		t.Fatalf("escapes survived sanitisation: %q", got)
	}
	if !strings.Contains(got, "hello") || !strings.Contains(got, "spoofed") {
		t.Fatalf("sanitiser destroyed legitimate text: %q", got)
	}
}

func TestStatNeverPrintsFabricatedZeroSigma(t *testing.T) {
	g := glyphs{" | ", "-", "+/-", "..."}
	// A single observation is not an estimate. hyperfine relabels rather than
	// printing "+/- 0.00", which would be a claim of precision we do not have.
	single := stat(23.16, 0, 1, 0, 0, g)
	if strings.Contains(single, "+/-") {
		t.Fatalf("n=1 must not carry an error bar, got %q", single)
	}
	if !strings.Contains(single, "abs") {
		t.Fatalf("n=1 should be labelled, got %q", single)
	}
	multi := stat(23.16, 0.44, 3, 22.7, 23.6, g)
	if !strings.Contains(multi, "+/-0.44") {
		t.Fatalf("n=3 should carry its spread, got %q", multi)
	}
}

func TestStatHandlesNoData(t *testing.T) {
	if got := stat(0, 0, 0, 0, 0, glyphs{}); got != "n/a" {
		t.Fatalf("got %q, want n/a", got)
	}
}

func TestFitTruncatesOnWordBoundary(t *testing.T) {
	got := fit("daily driver coding and agents plus more", 24, "...")
	if len(got) > 24 {
		t.Fatalf("fit overflowed: %q (%d)", got, len(got))
	}
	if strings.Contains(got, " ...") {
		t.Fatalf("trailing space before ellipsis reads as a bug: %q", got)
	}
}

func TestFitNeverSplitsUTF8(t *testing.T) {
	got := fit("模型名称很长", 5, "…")
	if !utf8.ValidString(got) {
		t.Fatalf("fit produced invalid UTF-8: %q", got)
	}
	if utf8.RuneCountInString(got) > 5 {
		t.Fatalf("fit overflowed rune budget: %q", got)
	}
	if got := fit("long", 1, "..."); got != "." {
		t.Fatalf("one-cell fit = %q", got)
	}
	if got := fit("long", 0, "..."); got != "" {
		t.Fatalf("zero-cell fit = %q", got)
	}
}

func TestResolveModeFallsBackForNonTTY(t *testing.T) {
	if got := Resolve("json"); got != "json" {
		t.Fatalf("explicit mode must win, got %q", got)
	}
	// Test binaries never have a TTY on stdout, so auto must resolve to plain.
	if got := Resolve("auto"); got != "plain" {
		t.Fatalf("auto on a non-TTY = %q, want plain", got)
	}
}

func TestValidModeRejectsTypos(t *testing.T) {
	for _, mode := range []string{"", "auto", "rich", "plain", "json", "none"} {
		if !ValidMode(mode) {
			t.Fatalf("valid mode %q was rejected", mode)
		}
	}
	if ValidMode("colourful") {
		t.Fatal("unknown display mode was accepted")
	}
}

func TestJSONModeEmitsOnlyRecordsOnStdout(t *testing.T) {
	// Diagnostics must go to stderr even under --json; nobody wraps errors in
	// JSON, and stdout has to stay a clean record stream.
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	d := &jsonDisplay{out: w}
	d.Note("this is a diagnostic", "warn")
	d.Result(score.Scorecard{Model: "m", Needs: map[string]score.Verdict{}}, Meta{})
	w.Close()
	os.Stdout = old

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	out := string(buf[:n])
	if strings.Contains(out, "this is a diagnostic") {
		t.Fatalf("a note leaked into the JSON stdout stream: %q", out)
	}
	if !strings.Contains(out, `"event":"result"`) {
		t.Fatalf("result record missing from stdout: %q", out)
	}
}

func TestNoneDisplayIsSilent(t *testing.T) {
	d := New("none")
	d.Phase("x", "y")
	d.Note("z", "warn")
	d.Result(score.Scorecard{}, Meta{})
	d.Close() // must not panic
}

func TestScorecardPrintsNumCtx(t *testing.T) {
	var buf strings.Builder
	d := &textDisplay{out: &buf, err: &buf, pal: palette{}, g: glyphs{" | ", "-", "+/-", "..."}}
	d.Result(score.Scorecard{Model: "qwen3:30b", UseFor: "chat"}, Meta{
		ParamSize: "30.5B", Quant: "Q4_K_M", Family: "qwen3moe",
		GPU: "demo-gpu", Driver: "1", Device: "GPU", Profile: "lappy",
		NumCtx: 4096,
	})
	got := buf.String()
	if !strings.Contains(got, "ctx      4096") {
		t.Fatalf("scorecard must print the request context:\n%s", got)
	}
}

func TestScorecardSanitizesMetadata(t *testing.T) {
	var buf strings.Builder
	d := &textDisplay{out: &buf, err: &buf, pal: palette{}, g: glyphs{" | ", "-", "+/-", "..."}}
	d.Result(score.Scorecard{Model: "m", Needs: map[string]score.Verdict{}}, Meta{
		ParamSize: "8B\x1b[2J", GPU: "gpu\x07", Driver: "driver\x1b[31m", Profile: "profile\x1b[H",
	})
	got := buf.String()
	if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\x07') {
		t.Fatalf("scorecard leaked terminal control bytes: %q", got)
	}
}

func TestCalibrationScorecardDoesNotPretendToBeAProductVerdict(t *testing.T) {
	var buf strings.Builder
	d := &textDisplay{out: &buf, err: &buf, pal: palette{}, g: glyphs{" | ", "-", "+/-", "..."}}
	d.Result(score.Scorecard{Model: "m", UseFor: "no need passed yet"}, Meta{
		GPU: "gpu", Driver: "driver", Device: "GPU", Profile: "default",
		Repeats: 1, Calibration: true,
	})
	got := buf.String()
	if !strings.Contains(got, "calibration evidence only") || !strings.Contains(got, "-k 10 for decision evidence") {
		t.Fatalf("calibration framing missing:\n%s", got)
	}
	if strings.Contains(got, "before ranking") {
		t.Fatalf("calibration output gave product-ranking advice:\n%s", got)
	}
}

func TestJSONResultIncludesNumCtx(t *testing.T) {
	var buf strings.Builder
	d := &jsonDisplay{out: &buf}
	d.Result(score.Scorecard{Model: "m", Needs: map[string]score.Verdict{}}, Meta{NumCtx: 4096})
	got := buf.String()
	if !strings.Contains(got, `"num_ctx":4096`) {
		t.Fatalf("json result missing num_ctx:\n%s", got)
	}
}
