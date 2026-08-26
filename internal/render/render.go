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
	"math"
	"os"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

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
	// Windows Terminal is UTF-8. Legacy conhost is a code page; stay ASCII.
	return os.Getenv("WT_SESSION") != "" || os.Getenv("WT_PROFILE_ID") != ""
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
//
//nolint:gocritic // [ -/] is the CSI parameter/intermediate byte range (0x20-0x2F) from ECMA-48, not a typo
var ansiRe = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b[@-Z\\-_]|[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)

func Sanitize(s string) string { return ansiRe.ReplaceAllString(s, "") }

var c1EscapeReplacer = strings.NewReplacer(
	"\u0090", "\x1bP",
	"\u0098", "\x1bX",
	"\u009b", "\x1b[",
	"\u009c", "\x1b\\",
	"\u009d", "\x1b]",
	"\u009e", "\x1b^",
	"\u009f", "\x1b_",
)

// SingleLine turns untrusted text into one terminal-safe presentation line.
// It consumes terminal control sequences, removes control and format runes,
// and collapses layout whitespace while preserving ordinary Unicode text.
func SingleLine(s string) string {
	s = c1EscapeReplacer.Replace(s)
	s = strings.ToValidUTF8(s, "\uFFFD")

	var out strings.Builder
	space := false
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = consumeEscape(s, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if unicode.IsSpace(r) {
			if out.Len() > 0 {
				space = true
			}
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if space {
			out.WriteByte(' ')
			space = false
		}
		out.WriteRune(r)
	}
	return strings.TrimSpace(out.String())
}

func consumeEscape(s string, start int) int {
	i := start + 1
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[':
		return consumeCSI(s, i+1)
	case ']':
		return consumeStringControl(s, i+1, true)
	case 'P', 'X', '^', '_':
		return consumeStringControl(s, i+1, false)
	case '\\':
		return i + 1
	}
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
		i++
	}
	if i < len(s) {
		i++
	}
	return i
}

func consumeCSI(s string, i int) int {
	for i < len(s) {
		b := s[i]
		i++
		if b >= 0x40 && b <= 0x7e {
			break
		}
	}
	return i
}

func consumeStringControl(s string, i int, bellTerminates bool) int {
	for i < len(s) {
		if bellTerminates && s[i] == '\a' {
			return i + 1
		}
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
			return i + 2
		}
		i++
	}
	return i
}

func fit(s string, n int, ell string) string {
	s = SingleLine(s)
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
	EffectiveCtx             int
	ContextState             string
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
	MDEDiffpp                float64
	ShowsIntervals           bool
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
	fmt.Fprintf(d.err, "  %-12s %s\n", d.pal.wrap(d.pal.Accent, SingleLine(name)), d.pal.wrap(d.pal.Muted, SingleLine(detail)))
}

func (d *textDisplay) Note(msg, level string) {
	style := d.pal.Muted
	if level == "warn" {
		style = d.pal.Warn
	}
	fmt.Fprintf(d.err, "  %s\n", d.pal.wrap(style, "! "+SingleLine(msg)))
}

func (d *textDisplay) Done(name string, seconds float64) {
	fmt.Fprintf(d.err, "  %-12s %s\n", "", d.pal.wrap(d.pal.Muted,
		fmt.Sprintf("%s done in %.1fs", SingleLine(name), seconds)))
}

func (d *textDisplay) stateStyle(s score.State) string {
	switch s {
	case score.Pass:
		return d.pal.Pass
	case score.Fail:
		return d.pal.Fail
	case score.Blocked:
		return d.pal.Blocked
	case score.Inconclusive:
		// Deliberately NOT amber. Amber is measured to read as weak-green
		// rather than "unknown" (weight 0.15 against green's 0.25), only ~24%
		// of readers agree on what an amber indicator means, and it is the
		// worst colour for the most common form of colour blindness: +85%
		// response time for deuteranopes, against roughly none for green.
		// Amber is also already carrying BLKD here, and one channel cannot
		// mean three things. An undecided row makes no claim, so it is muted
		// rather than coloured: an uncertain value should not be able to
		// shout.
		return d.pal.Muted
	default:
		return d.pal.Skip
	}
}

// Scorecard column plan. Everything is composed to a resolved width, so the
// rule and the rows agree: a rule narrower than the content it bounds reads as
// a rendering fault, and the previous fixed 78 was violated by 7 of 10 rows.
const (
	gutter = 2
	// tagWidth covers "[PASS]". The brackets stay: the tag is the one field a
	// reader greps for, and they make it unmistakably a marker rather than the
	// first word of the sentence beside it.
	tagWidth = 6
	colGap   = 2
	// measureWidth holds the widest real measure, "10/11 passes [0.62-0.98]".
	// Truncating here would cut the interval, which is the half of the number
	// that decides whether the verdict means anything.
	measureWidth = 24
	// contIndent puts continuation lines under the label column, so the tag
	// gutter stays a single uninterrupted strip the eye can scan for FAIL.
	contIndent = gutter + tagWidth + colGap
	// minTailWidth is the narrowest useful gate column. Below it the measure
	// and gate move to a continuation line rather than being truncated.
	minTailWidth = 10
	// evidenceHang aligns the closing notes under the header block's value
	// column, so the whole report has two left edges and not five.
	evidenceHang = 9
)

// verdictRow renders one need: a scannable primary line, then its receipts.
//
// The primary line is fixed-width by construction, so every tag, label, and
// measure lands in the same screen column on every row and in every run. The
// variable-length text -- the family breakdown and the prose caveat -- is
// wrapped underneath rather than allowed to run off the right edge, which is
// what produced a 224-character row against a 78-column rule.
func (d *textDisplay) verdictRow(w io.Writer, label string, v score.Verdict, width int) {
	measure, gate, detail, note := v.Parts()
	// On a narrow terminal the label yields before anything else. Truncating a
	// need's name costs a word; truncating its measure costs the interval.
	labelWidth := min(score.LabelWidth, max(width-contIndent-minTailWidth, 8))
	// The tag pads inside the brackets, not outside, so the closing bracket
	// lands in the same column on every row: "[n/a ]" beside "[PASS]".
	line := strings.Repeat(" ", gutter) +
		d.pal.wrap(d.stateStyle(v.State), "["+pad(stateTag(v.State), tagWidth-2, "")+"]") +
		strings.Repeat(" ", colGap) + pad(label, labelWidth, d.g.Ell)

	tailStart := contIndent + labelWidth + colGap
	tailWidth := width - tailStart
	gateWidth := tailWidth - measureWidth - colGap

	switch {
	case tailWidth < minTailWidth:
		// Too narrow for a tail at all. Measure and gate become the first
		// continuation line rather than being cut down to nothing.
		fmt.Fprintln(w, strings.TrimRight(line, " "))
		if head := strings.TrimSpace(measure + " " + gate); head != "" {
			d.continuation(w, head, width, "")
		}
	case gateWidth < minTailWidth:
		fmt.Fprintln(w, strings.TrimRight(line+strings.Repeat(" ", colGap)+
			fit(strings.TrimSpace(measure+" "+gate), tailWidth, d.g.Ell), " "))
	default:
		// A verdict with no quantity and no threshold has only prose. Promote a
		// short note into the tail so an unmeasured need costs one line, not two.
		if measure == "" && gate == "" && note != "" && len([]rune(note)) <= tailWidth {
			measure, note = note, ""
			line += strings.Repeat(" ", colGap) + d.pal.wrap(d.pal.Muted, measure)
			fmt.Fprintln(w, strings.TrimRight(line, " "))
			break
		}
		line += strings.Repeat(" ", colGap) + pad(measure, measureWidth, d.g.Ell)
		line += strings.Repeat(" ", colGap) + d.pal.wrap(d.pal.Muted, fit(gate, gateWidth, d.g.Ell))
		fmt.Fprintln(w, strings.TrimRight(line, " "))
	}

	if len(detail) > 0 {
		d.continuation(w, strings.Join(detail, ", "), width, d.pal.Muted)
	}
	if note != "" {
		d.continuation(w, note, width, d.pal.Muted)
	}
}

func (d *textDisplay) continuation(w io.Writer, text string, width int, style string) {
	for _, l := range wrap(SingleLine(text), max(width-contIndent, MinWidth-contIndent)) {
		fmt.Fprintf(w, "%s%s\n", strings.Repeat(" ", contIndent), d.pal.wrap(style, l))
	}
}

func (d *textDisplay) Result(sc score.Scorecard, m Meta) {
	w := d.out
	width := Width()
	rule := strings.Repeat("-", width)
	fmt.Fprintln(w, rule)
	fmt.Fprintf(w, "model    %s\n", SingleLine(sc.Model))
	if size := strings.TrimSpace(fmt.Sprintf("%s  %s  %s",
		SingleLine(m.ParamSize), SingleLine(m.Quant), SingleLine(m.Family))); size != "" {
		fmt.Fprintf(w, "size     %s\n", size)
	}
	if m.NumCtx > 0 {
		switch {
		case m.EffectiveCtx > 0 && m.EffectiveCtx != m.NumCtx:
			fmt.Fprintf(w, "ctx      %d requested -> %d effective (%s)\n", m.NumCtx, m.EffectiveCtx, SingleLine(m.ContextState))
		case m.EffectiveCtx > 0:
			fmt.Fprintf(w, "ctx      %d effective (%s)\n", m.EffectiveCtx, SingleLine(m.ContextState))
		case m.ContextState != "":
			fmt.Fprintf(w, "ctx      %d requested; effective %s\n", m.NumCtx, SingleLine(m.ContextState))
		default:
			fmt.Fprintf(w, "ctx      %d\n", m.NumCtx)
		}
	}
	if m.Level != "" {
		run := SingleLine(m.Level)
		if m.Repeats > 0 {
			run += fmt.Sprintf("%sk=%d", d.g.Dot, m.Repeats)
		}
		if m.WallSeconds > 0 {
			run += fmt.Sprintf("%s%.1fs", d.g.Dot, m.WallSeconds)
		}
		if m.StartedAt != "" {
			run += d.g.Dot + SingleLine(m.StartedAt)
		}
		fmt.Fprintf(w, "run      %s\n", run)
	}
	useFor := sc.UseFor
	if m.Calibration {
		useFor = "calibration evidence only - not a standalone product verdict"
	}
	d.headerField(w, "use for", useFor, width, d.pal.Accent)
	d.headerField(w, "device", fmt.Sprintf("%s%sdriver %s%s%s%sprofile %s",
		SingleLine(m.GPU), d.g.Dot, SingleLine(m.Driver), d.g.Dot,
		SingleLine(m.Device), d.g.Dot, SingleLine(m.Profile)), width, "")
	fmt.Fprintln(w, rule)
	for _, k := range score.SortedNeeds(sc.Needs) {
		v, ok := sc.Needs[k]
		if !ok {
			continue
		}
		d.verdictRow(w, score.NeedLabel[k], v, width)
	}

	if m.DecodeN > 0 || m.PrefillN > 0 || m.TTFTN > 0 || m.ResidentGB > 0 {
		fmt.Fprintf(w, "\n%s\n", d.pal.wrap(d.pal.Head, "performance"))
		if m.DecodeN > 0 {
			d.perfRow(w, "decode", "tok/s", m.DecodeMean, m.DecodeSD, m.DecodeN,
				m.DecodeSeries, m.DecodeMin, m.DecodeMax, width)
		}
		if m.PrefillN > 0 {
			d.perfRow(w, "prefill", "tok/s", m.PrefillMean, m.PrefillSD, m.PrefillN,
				m.PrefillSeries, 0, 0, width)
		}
		if m.TTFTN > 0 {
			d.perfRow(w, "TTFT", "s", m.TTFTMean, m.TTFTSD, m.TTFTN, m.TTFTSeries, 0, 0, width)
		}
		if m.ResidentGB > 0 {
			fmt.Fprintf(w, "  %-8s %.2f GB resident\n", "memory", m.ResidentGB)
		}
		if m.FirstRunSlow {
			d.footer(w, fmt.Sprintf("! first decode repeat was %.1fx slower than the settled repeats",
				m.FirstRunRatio), width, 2, 4, d.pal.Warn)
		}
	}
	// Say what this sample CANNOT resolve. An honest gap beats implied precision.
	if m.MDEpp > 0 {
		// Two figures, because fitr makes two kinds of claim. The gate figure
		// is a one-sample calculation and was printed on its own beside the
		// words "not good from slightly better", which is a model-versus-model
		// sentence quoting a number that cannot support it. A difference of
		// two rates carries both variances, so it resolves worse by sqrt(2).
		line := fmt.Sprintf("evidence  %s %s %d trials, resolves ~%s against a gate",
			resolutionBand(m.MDEpp), d.g.Dash, m.Trials, resolutionText(m.MDEpp))
		if m.MDEDiffpp > 0 {
			line += ", " + resolutionText(m.MDEDiffpp) + " between two models"
		}
		// The plain-English version of the same fact, for the reader who is
		// not going to convert percentage points into a decision.
		line += ". Separates broken from working, not good from slightly better"
		fmt.Fprintln(w)
		d.footer(w, line, width, 0, evidenceHang, d.pal.Muted)
		// Name what the ranges are, and both things they are not. Readers
		// reliably take an interval for the spread of scores they will see, or
		// for the tool's confidence in the model, and neither is what it says.
		if m.ShowsIntervals {
			d.footer(w, "ranges are what this run pins the true rate to. They are not the spread of "+
				"scores you will see, and not how sure fitr is that the model is good",
				width, evidenceHang, evidenceHang, d.pal.Muted)
		}
	}
	if m.Repeats < 3 {
		warning := "! single-sample run - identical configs vary 10-20pp between runs; " +
			"re-run with -k 3 before ranking against a close model"
		if m.Calibration {
			warning = "! single calibration instance - use -k 5 for a workflow pass and -k 10 for decision evidence"
		}
		fmt.Fprintln(w)
		d.footer(w, warning, width, 0, 2, d.pal.Warn)
	}
	fmt.Fprintln(w, rule)
}

// headerField prints one label/value pair from the header block, wrapping the
// value under its own column. The device line and a long use-for both exceed
// the rule on ordinary runs.
func (d *textDisplay) headerField(w io.Writer, label, value string, width int, style string) {
	lines := wrap(SingleLine(value), max(width-evidenceHang, MinWidth))
	if len(lines) == 0 {
		return
	}
	for i, l := range lines {
		lead := pad(label, evidenceHang, "")
		if i > 0 {
			lead = strings.Repeat(" ", evidenceHang)
		}
		fmt.Fprintf(w, "%s%s\n", lead, d.pal.wrap(style, l))
	}
}

// footer wraps a closing note to the rule, hanging continuations under the
// first line's text so a wrapped sentence still reads as one block.
func (d *textDisplay) footer(w io.Writer, text string, width, lead, hang int, style string) {
	for i, l := range wrap(SingleLine(text), max(width-hang, MinWidth)) {
		indent := lead
		if i > 0 {
			indent = hang
		}
		fmt.Fprintf(w, "%s%s\n", strings.Repeat(" ", indent), d.pal.wrap(style, l))
	}
}

// perfRow prints one measured series: the estimate, then the spread.
//
// The tail is either the drawn shape between its own endpoints or the endpoints
// stated in words -- the same information, one of them a picture. Putting the
// endpoints on the row is what makes the glyphs readable: a sparkline is
// normalised to its own min and max, so without them "▁▄█" could be a 0.4%
// wobble or a tenfold swing, and the caption it used to carry ("graphs show
// repeat shape, oldest to newest") named the axis without ever giving a scale.
func (d *textDisplay) perfRow(w io.Writer, name, unit string, mean, sd float64, n int,
	series []float64, knownMin, knownMax float64, width int,
) {
	head := fmt.Sprintf("  %-8s %s %s", name, stat(mean, sd, n, d.g), unit)
	lo, hi, ok := seriesRange(series)
	if !ok && knownMax > knownMin {
		lo, hi, ok = knownMin, knownMax, true
	}
	if !ok {
		fmt.Fprintln(w, head)
		return
	}
	tail := fmt.Sprintf("min %.2f, max %.2f", lo, hi)
	if spark, informative := sparkline(series, 12, d.rich && unicodeOK()); informative {
		tail = fmt.Sprintf("%.2f %s %.2f", lo, d.pal.wrap(d.pal.Accent, spark), hi)
	}
	// Pad to a shared column so the tails line up down the block, but never
	// past the width: alignment is worth a few spaces, not a wrapped line.
	gap := 2
	if pad := 38 - len([]rune(head)); pad > gap {
		gap = pad
	}
	if len([]rune(head))+gap+len([]rune(SingleLine(tail))) > width {
		gap = 2
	}
	fmt.Fprintf(w, "%s%s%s\n", head, strings.Repeat(" ", gap), d.pal.wrap(d.pal.Muted, tail))
}

func seriesRange(series []float64) (lo, hi float64, ok bool) {
	for _, v := range series {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		if !ok {
			lo, hi, ok = v, v, true
			continue
		}
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	return lo, hi, ok && hi > lo
}

// stat never prints "+/- 0.00". A single observation is not an estimate;
// hyperfine relabels it rather than inventing a zero sigma. When there is a
// real spread, the coefficient of variation rides along: it is the one
// dimensionless stability number that compares across devices.
func stat(mean, sd float64, n int, g glyphs) string {
	if n == 0 {
		return "n/a"
	}
	if n < 2 || sd == 0 {
		return fmt.Sprintf("%.2f (abs, n=1)", mean)
	}
	if mean != 0 {
		return fmt.Sprintf("%.2f %s%.2f (CV %.1f%%, n=%d)", mean, g.PM, sd, 100*sd/mean, n)
	}
	return fmt.Sprintf("%.2f %s%.2f (n=%d)", mean, g.PM, sd, n)
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
	fmt.Fprintln(os.Stderr, SingleLine(msg))
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
	payload := map[string]any{
		"event": "result", "model": sc.Model, "profile": sc.Profile,
		"use_for": useFor, "serves": sc.Serves, "needs": states,
		"passes": sc.Passes, "fails": sc.Fails, "unproven": sc.Unproven,
		"repeats": m.Repeats, "num_ctx": m.NumCtx, "saved": m.SavedPath,
	}
	if m.ContextState != "" {
		payload["context_state"] = m.ContextState
	}
	if m.EffectiveCtx > 0 {
		payload["effective_ctx"] = m.EffectiveCtx
	}
	d.emit(payload)
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

// resolutionText renders a minimum detectable effect. Percentage points cannot
// exceed 100, so a calculation that lands above it has not found a wide
// threshold; it has found that this sample resolves nothing. Printing "~114pp"
// states an impossible quantity where the honest answer is a sentence.
func resolutionText(pp float64) string {
	if pp >= 100 {
		return "nothing"
	}
	return fmt.Sprintf("%.0fpp", pp)
}

// stateTag is the fixed-width form of a verdict for the scorecard column.
//
// The stored state stays "INCONCLUSIVE", because it is persisted in saved
// results and in the JSON contract. It is 12 characters, so a row carrying it
// rendered 14 columns wide against every other row's 6 and pushed the label
// column out of alignment. A row that does not line up reads as an exception,
// and an exception reads as something going wrong, which is the opposite of
// what an undecided row means.
//
// "????" is the display form: four characters, fits the column, and cannot be
// misread as a verdict word the way a truncated "INCON" could.
func stateTag(s score.State) string {
	if s == score.Inconclusive {
		return "????"
	}
	return string(s)
}

// resolutionBand names how much this run can resolve, so the reader gets an
// at-a-glance signal without having to interpret a percentage.
//
// The word never appears without its number on the same line, which is the
// whole design constraint: a verbal probability alone transmits its intended
// meaning to about 40% of readers, a word beside its number to about 72%, and
// the number alone to about 75%. The word is the convenience; the number is
// the message. If one of them has to go, the word goes.
//
// The scale is GRADE's certainty ladder rather than an invented one, and it
// describes the RUN, not the model. That distinction is load-bearing: a
// qualitative low-confidence label attached to a subject gets absorbed as bad
// news about that subject, which is precisely how "we could not tell" ends up
// reading as "it failed".
func resolutionBand(mdePP float64) string {
	switch {
	case mdePP <= 0:
		return "unknown"
	case mdePP <= 10:
		return "high"
	case mdePP <= 20:
		return "moderate"
	case mdePP <= 40:
		return "low"
	default:
		return "very low"
	}
}
