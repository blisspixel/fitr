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
		return glyphs{" · ", "—", "±", "…"}
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
	if len(s) <= n {
		return s
	}
	keep := max(n-len(ell), 1)
	cut := s[:keep]
	if i := strings.LastIndex(cut, " "); i > keep*3/5 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;") + ell
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
	Repeats                  int
	DecodeMean, DecodeSD     float64
	DecodeMin, DecodeMax     float64
	DecodeN                  int
	PrefillMean, PrefillSD   float64
	PrefillN                 int
	FirstRunSlow             bool
	FirstRunRatio            float64
	SavedPath                string
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
	fmt.Fprintf(w, "size     %s  %s  %s\n", m.ParamSize, m.Quant, m.Family)
	fmt.Fprintf(w, "use for  %s\n", d.pal.wrap(d.pal.Accent, Sanitize(sc.UseFor)))
	fmt.Fprintf(w, "device   %s%sdriver %s%s%s%sprofile %s\n",
		m.GPU, d.g.Dot, m.Driver, d.g.Dot, m.Device, d.g.Dot, m.Profile)
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

	if m.DecodeN > 0 {
		fmt.Fprintf(w, "\n%s\n", d.pal.wrap(d.pal.Muted, fmt.Sprintf(
			"over %d repeats   decode %s   prefill %s", m.Repeats,
			stat(m.DecodeMean, m.DecodeSD, m.DecodeN, m.DecodeMin, m.DecodeMax, d.g),
			stat(m.PrefillMean, m.PrefillSD, m.PrefillN, 0, 0, d.g))))
	}
	if m.Repeats < 3 {
		fmt.Fprintf(w, "\n%s\n", d.pal.wrap(d.pal.Warn,
			"! single-sample run - identical configs vary 10-20pp between runs; "+
				"re-run with -k 3 before ranking against a close model"))
	}
	fmt.Fprintln(w, rule)
}

// stat never prints "+/- 0.00". A single observation is not an estimate;
// hyperfine relabels it rather than inventing a zero sigma.
func stat(mean, sd float64, n int, mn, mx float64, g glyphs) string {
	if n == 0 {
		return "n/a"
	}
	if n < 2 || sd == 0 {
		return fmt.Sprintf("%.2f (abs, n=1)", mean)
	}
	if mn != 0 || mx != 0 {
		return fmt.Sprintf("%.2f %s%.2f (min %.2f, max %.2f)", mean, g.PM, sd, mn, mx)
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
	d.emit(map[string]any{
		"event": "result", "model": sc.Model, "profile": sc.Profile,
		"use_for": sc.UseFor, "serves": sc.Serves, "needs": states,
		"passes": sc.Passes, "fails": sc.Fails, "unproven": sc.Unproven,
		"repeats": m.Repeats, "saved": m.SavedPath,
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
