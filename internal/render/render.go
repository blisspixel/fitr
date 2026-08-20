// Package render is the display layer: one renderer, several thin drivers.
//
// Modes
//
//	auto   rich if stdout is a TTY, else plain   (default)
//	rich   colour + progress on stderr
//	plain  line-oriented, no ANSI                (CI, pipes, logs)
//	json   NDJSON on stdout, nothing else        (machine-readable)
//	none   silent
//
// Stream discipline: progress to STDERR, results to STDOUT, so
// `fitr run m > out.txt` stays clean and pipeable.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/blisspixel/fitr/internal/score"
)

// ---------------------------------------------------------------- environment
// NO_COLOR: present AND NOT EMPTY disables colour (no-color.org). An empty
// value means UNSET -- the classic off-by-one in this spec.
func noColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return true
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return false
	}
	return !isTTY(os.Stdout)
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// Unicode is only safe on a UTF stream. Encodability is NOT renderability:
// cp1252 happily encodes the typographic glyphs, but a console on codepage 437
// draws entirely different characters for those bytes.
func unicodeOK() bool {
	if os.Getenv("FITR_ASCII") != "" {
		return false
	}
	if os.Getenv("FITR_UNICODE") != "" {
		return true
	}
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := strings.ToLower(os.Getenv(k)); strings.Contains(v, "utf") {
			return true
		}
	}
	// Windows consoles are cp-something unless told otherwise; default to ASCII.
	return os.Getenv("WT_SESSION") != "" && os.Getenv("OS") == ""
}

type glyphs struct{ Dot, Dash, PM, Ell string }

func pickGlyphs() glyphs {
	if unicodeOK() {
		return glyphs{" · ", "-", "±", "…"}
	}
	return glyphs{" | ", "-", "+/-", "..."}
}

// ---------------------------------------------------------------- colour
type palette struct{ Pass, Fail, Skip, NA, Blocked, Warn, Muted, Head, Accent string }

const reset = "\x1b[0m"

func pickPalette(color bool) palette {
	if !color {
		return palette{}
	}
	return palette{
		Pass: "\x1b[32m", Fail: "\x1b[31m", Skip: "\x1b[90m", NA: "\x1b[90m",
		Blocked: "\x1b[33m", Warn: "\x1b[33m", Muted: "\x1b[90m",
		Head: "\x1b[1;34m", Accent: "\x1b[35m",
	}
}

func (p palette) wrap(style, s string) string {
	if style == "" {
		return s
	}
	return style + s + reset
}

// Model output is UNTRUSTED input to the terminal: it can spoof a prompt,
// rewrite the screen, or hide text with ANSI.
var ansiRe = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b[@-Z\\-_]|[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)

func Sanitize(s string) string { return ansiRe.ReplaceAllString(s, "") }

func fit(s string, n int, ell string) string {
	s = Sanitize(s)
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	ellRunes := []rune(ell)
	if len(ellRunes) >= n {
		return string(ellRunes[:n])
	}
	keep := max(n-len(ellRunes), 1)
	cut := runes[:keep]
	lastSpace := -1
	for i, r := range cut {
		if r == ' ' {
			lastSpace = i
		}
	}
	if lastSpace > keep*3/5 {
		cut = cut[:lastSpace]
	}
	return strings.TrimRight(string(cut), " ,;") + ell
}

// ---------------------------------------------------------------- display
type Display interface {
	Phase(name, detail string)
	Note(msg, level string)
	Done(name string, seconds float64)
	Result(sc score.Scorecard, meta Meta)
	Emit(v any)
	Close()
}

// Meta is the context a scorecard needs to be interpretable at all.
type Meta struct {
	ParamSize, Quant, Family string
	GPU, Driver, Device      string
	Profile                  string
	StartedAt, Level         string
	WallSeconds              float64
	NumCtx                   int
	Repeats                  int
	DecodeMean, DecodeSD     float64
	DecodeMin, DecodeMax     float64
	DecodeN                  int
	PrefillMean, PrefillSD   float64
	PrefillN                 int
	TTFTMean, TTFTSD         float64
	TTFTN                    int
	ResidentGB               float64
	DecodeSeries             []float64
	PrefillSeries            []float64
	TTFTSeries               []float64
	FirstRunSlow             bool
	FirstRunRatio            float64
	Trials                   int
	MDEpp                    float64
	SavedPath                string
	Calibration              bool
}

func Resolve(mode string) string {
	if mode != "" && mode != "auto" {
		return mode
	}
	if !isTTY(os.Stdout) {
		return "plain"
	}
	if os.Getenv("TERM") == "dumb" {
		return "plain"
	}
	return "rich"
}

func ValidMode(mode string) bool {
	switch mode {
	case "", "auto", "rich", "plain", "json", "none":
		return true
	default:
		return false
	}
}

func New(mode string) Display {
	switch Resolve(mode) {
	case "json":
		return &jsonDisplay{out: os.Stdout}
	case "none":
		return &noneDisplay{}
	case "rich":
		return &textDisplay{out: os.Stdout, err: os.Stderr,
			pal: pickPalette(!noColor()), g: pickGlyphs(), rich: true}
	default:
		return &textDisplay{out: os.Stdout, err: os.Stderr,
			pal: palette{}, g: glyphs{" | ", "-", "+/-", "..."}}
	}
}

// ---------------------------------------------------------------- text driver
type textDisplay struct {
	out, err io.Writer
	pal      palette
	g        glyphs
	rich     bool
}

func (d *textDisplay) Phase(name, detail string) {
	fmt.Fprintf(d.err, "  %-12s %s\n", d.pal.wrap(d.pal.Accent, name), d.pal.wrap(d.pal.Muted, detail))
}

func (d *textDisplay) Note(msg, level string) {
	style := d.pal.Muted
	if level == "warn" {
		style = d.pal.Warn
	}
	fmt.Fprintf(d.err, "  %s\n", d.pal.wrap(style, "! "+Sanitize(msg)))
}

func (d *textDisplay) Done(name string, seconds float64) {
	fmt.Fprintf(d.err, "  %-12s %s\n", "", d.pal.wrap(d.pal.Muted,
		fmt.Sprintf("%s done in %.1fs", name, seconds)))
}

func (d *textDisplay) stateStyle(s score.State) string {
	switch s {
	case score.Pass:
		return d.pal.Pass
	case score.Fail:
		return d.pal.Fail
	case score.Blocked:
		return d.pal.Blocked
	default:
		return d.pal.Skip
	}
}

func (d *textDisplay) Result(sc score.Scorecard, m Meta) {
	w := d.out
	rule := strings.Repeat("-", 78)
	fmt.Fprintln(w, rule)
	fmt.Fprintf(w, "model    %s\n", Sanitize(sc.Model))
	fmt.Fprintf(w, "size     %s  %s  %s\n", Sanitize(m.ParamSize), Sanitize(m.Quant), Sanitize(m.Family))
	if m.NumCtx > 0 {
		fmt.Fprintf(w, "ctx      %d\n", m.NumCtx)
	}
	if m.Level != "" {
		run := Sanitize(m.Level)
		if m.Repeats > 0 {
			run += fmt.Sprintf("%sk=%d", d.g.Dot, m.Repeats)
		}
		if m.WallSeconds > 0 {
			run += fmt.Sprintf("%s%.1fs", d.g.Dot, m.WallSeconds)
		}
		if m.StartedAt != "" {
			run += d.g.Dot + Sanitize(m.StartedAt)
		}
		fmt.Fprintf(w, "run      %s\n", run)
	}
	useFor := sc.UseFor
	if m.Calibration {
		useFor = "calibration evidence only - not a standalone product verdict"
	}
	fmt.Fprintf(w, "use for  %s\n", d.pal.wrap(d.pal.Accent, Sanitize(useFor)))
	fmt.Fprintf(w, "device   %s%sdriver %s%s%s%sprofile %s\n",
		Sanitize(m.GPU), d.g.Dot, Sanitize(m.Driver), d.g.Dot, Sanitize(m.Device), d.g.Dot, Sanitize(m.Profile))
	fmt.Fprintln(w, rule)
	for _, k := range score.SortedNeeds(sc.Needs) {
		v, ok := sc.Needs[k]
		if !ok {
			continue
		}
		fmt.Fprintf(w, "[%s] %-34s %s\n",
			d.pal.wrap(d.stateStyle(v.State), fmt.Sprintf("%-4s", v.State)),
			score.NeedLabel[k], Sanitize(v.Why))
	}

	if m.DecodeN > 0 || m.PrefillN > 0 || m.TTFTN > 0 || m.ResidentGB > 0 {
		fmt.Fprintf(w, "\n%s\n", d.pal.wrap(d.pal.Head, "performance"))
		if m.DecodeN > 0 {
			fmt.Fprintf(w, "  %-8s %s tok/s", "decode", stat(m.DecodeMean, m.DecodeSD, m.DecodeN, m.DecodeMin, m.DecodeMax, d.g))
			if len(m.DecodeSeries) > 0 {
				fmt.Fprintf(w, "  %s", d.pal.wrap(d.pal.Accent, sparkline(m.DecodeSeries, 12, d.rich && unicodeOK())))
			}
			fmt.Fprintln(w)
		}
		if m.PrefillN > 0 {
			fmt.Fprintf(w, "  %-8s %s tok/s", "prefill", stat(m.PrefillMean, m.PrefillSD, m.PrefillN, 0, 0, d.g))
			if len(m.PrefillSeries) > 0 {
				fmt.Fprintf(w, "  %s", d.pal.wrap(d.pal.Accent, sparkline(m.PrefillSeries, 12, d.rich && unicodeOK())))
			}
			fmt.Fprintln(w)
		}
		if m.TTFTN > 0 {
			fmt.Fprintf(w, "  %-8s %s s", "TTFT", stat(m.TTFTMean, m.TTFTSD, m.TTFTN, 0, 0, d.g))
			if len(m.TTFTSeries) > 0 {
				fmt.Fprintf(w, "  %s", d.pal.wrap(d.pal.Accent, sparkline(m.TTFTSeries, 12, d.rich && unicodeOK())))
			}
			fmt.Fprintln(w)
		}
		if m.ResidentGB > 0 {
			fmt.Fprintf(w, "  %-8s %.2f GB resident\n", "memory", m.ResidentGB)
		}
		if len(m.DecodeSeries) > 1 || len(m.PrefillSeries) > 1 || len(m.TTFTSeries) > 1 {
			fmt.Fprintf(w, "  %s\n", d.pal.wrap(d.pal.Muted, "graphs show repeat shape, oldest to newest"))
		}
		if m.FirstRunSlow {
			fmt.Fprintf(w, "  %s\n", d.pal.wrap(d.pal.Warn, fmt.Sprintf("! first decode repeat was %.1fx slower than the settled repeats", m.FirstRunRatio)))
		}
	}
	// Say what this sample CANNOT resolve. An honest gap beats implied precision.
	if m.MDEpp > 0 {
		fmt.Fprintf(w, "%s\n", d.pal.wrap(d.pal.Muted, fmt.Sprintf(
			"%d binary trials %s min detectable effect ~%.0fpp - separates broken from working, "+
				"not good from slightly better", m.Trials, d.g.Dash, m.MDEpp)))
	}
	if m.Repeats < 3 {
		warning := "! single-sample run - identical configs vary 10-20pp between runs; " +
			"re-run with -k 3 before ranking against a close model"
		if m.Calibration {
			warning = "! single calibration instance - use -k 5 for a workflow pass and -k 10 for decision evidence"
		}
		fmt.Fprintf(w, "\n%s\n", d.pal.wrap(d.pal.Warn, warning))
	}
	fmt.Fprintln(w, rule)
}

// stat never prints "+/- 0.00". A single observation is not an estimate;
// hyperfine relabels it rather than inventing a zero sigma. When there is a
// real spread, the coefficient of variation rides along: it is the one
// dimensionless stability number that compares across devices.
func stat(mean, sd float64, n int, mn, mx float64, g glyphs) string {
	if n == 0 {
		return "n/a"
	}
	if n < 2 || sd == 0 {
		return fmt.Sprintf("%.2f (abs, n=1)", mean)
	}
	cv := ""
	if mean != 0 {
		cv = fmt.Sprintf(", CV %.1f%%", 100*sd/mean)
	}
	if mn != 0 || mx != 0 {
		return fmt.Sprintf("%.2f %s%.2f (min %.2f, max %.2f%s)", mean, g.PM, sd, mn, mx, cv)
	}
	return fmt.Sprintf("%.2f %s%.2f", mean, g.PM, sd)
}

func (d *textDisplay) Emit(v any) {}
func (d *textDisplay) Close()     {}

// ---------------------------------------------------------------- json driver
type jsonDisplay struct{ out io.Writer }

func (d *jsonDisplay) emit(v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintln(d.out, string(b))
}
func (d *jsonDisplay) Phase(name, detail string) {
	d.emit(map[string]any{"event": "phase", "name": name, "detail": detail})
}

// Notes go to STDERR as plain text even in JSON mode; nobody wraps diagnostics
// in JSON, and stdout must stay a clean record stream.
func (d *jsonDisplay) Note(msg, level string) {
	fmt.Fprintln(os.Stderr, Sanitize(msg))
}
func (d *jsonDisplay) Done(name string, seconds float64) {
	d.emit(map[string]any{"event": "phase_done", "name": name, "seconds": seconds})
}
func (d *jsonDisplay) Result(sc score.Scorecard, m Meta) {
	states := map[string]string{}
	for k, v := range sc.Needs {
		states[k] = string(v.State)
	}
	useFor := sc.UseFor
	if m.Calibration {
		useFor = "calibration evidence only - not a standalone product verdict"
	}
	d.emit(map[string]any{
		"event": "result", "model": sc.Model, "profile": sc.Profile,
		"use_for": useFor, "serves": sc.Serves, "needs": states,
		"passes": sc.Passes, "fails": sc.Fails, "unproven": sc.Unproven,
		"repeats": m.Repeats, "num_ctx": m.NumCtx, "saved": m.SavedPath,
	})
}
func (d *jsonDisplay) Emit(v any) { d.emit(v) }
func (d *jsonDisplay) Close()     {}

// ---------------------------------------------------------------- none
type noneDisplay struct{}

func (noneDisplay) Phase(string, string)         {}
func (noneDisplay) Note(string, string)          {}
func (noneDisplay) Done(string, float64)         {}
func (noneDisplay) Result(score.Scorecard, Meta) {}
func (noneDisplay) Emit(any)                     {}
func (noneDisplay) Close()                       {}
