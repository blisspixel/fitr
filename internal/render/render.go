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

	"github.com/blisspixel/fitr/internal/analysis"
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
	Analysis                 *analysis.Report `json:"-"`
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
	ResidentContext          int `json:"-"`
	DecodeSeries             []float64
	PrefillSeries            []float64
	TTFTSeries               []float64
	FirstRunSlow             bool
	FirstRunRatio            float64
	ShowsIntervals           bool
	SavedPath                string
	Calibration              bool
}

// resolvedMeta keeps legacy presentation fields readable while making a
// validated current report the sole owner of performance, context, and
// resident-capacity facts. Analysis is derived after the sealed record passes
// its evidence contract; renderers only project its values.
func resolvedMeta(meta Meta) Meta {
	if meta.Analysis == nil {
		return meta
	}
	report := meta.Analysis
	meta.NumCtx = report.Context.Requested
	meta.EffectiveCtx = 0
	if report.Context.Effective != nil {
		meta.EffectiveCtx = *report.Context.Effective
	}
	meta.ContextState = report.Context.State
	applyPerformanceObservation(&meta.DecodeMean, &meta.DecodeSD, &meta.DecodeMin,
		&meta.DecodeMax, &meta.DecodeN, &meta.DecodeSeries, report.Performance.DecodeTPS)
	applyPerformanceObservation(&meta.PrefillMean, &meta.PrefillSD, nil,
		nil, &meta.PrefillN, &meta.PrefillSeries, report.Performance.PrefillTPS)
	applyPerformanceObservation(&meta.TTFTMean, &meta.TTFTSD, nil,
		nil, &meta.TTFTN, &meta.TTFTSeries, report.Performance.TTFTSeconds)
	meta.FirstRunSlow = report.Performance.DecodeTPS.FirstRunSlow
	meta.FirstRunRatio = report.Performance.DecodeTPS.FirstRunRatio
	meta.ResidentGB, meta.ResidentContext = 0, 0
	if resident := report.Capacity.Resident; resident != nil && resident.Estimate != nil {
		meta.ResidentGB = float64(*resident.Estimate) / (1024 * 1024 * 1024)
		meta.ResidentContext = resident.RequestedContext
	}
	return meta
}

// ResolvedMeta projects a derived analysis into the compatibility fields used
// by existing clients. The Analysis pointer remains attached and is omitted
// from the frozen presentation JSON shape.
func ResolvedMeta(meta Meta) Meta { return resolvedMeta(meta) }

func applyPerformanceObservation(mean, sd, minValue, maxValue *float64, n *int,
	series *[]float64, observation analysis.PerformanceObservation) {
	*mean, *sd, *n = 0, 0, observation.SampleCount
	*series = append((*series)[:0], observation.Samples...)
	if observation.Estimate != nil {
		*mean = *observation.Estimate
	}
	if observation.SD != nil {
		*sd = *observation.SD
	}
	if minValue != nil {
		*minValue = 0
		if observation.Min != nil {
			*minValue = *observation.Min
		}
	}
	if maxValue != nil {
		*maxValue = 0
		if observation.Max != nil {
			*maxValue = *observation.Max
		}
	}
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
	// that decides whether the verdict means anything. The scorer owns the
	// constant because the scorer is what can overflow it.
	measureWidth = score.MeasureWidth
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
func (d *textDisplay) verdictRow(w io.Writer, need, label string, v score.Verdict, width int) {
	measure, gate, detail, note := PresentVerdictParts(need, v)
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
	m = resolvedMeta(m)
	w := d.out
	width := Width()
	rule := strings.Repeat("-", width)
	d.resultHeader(w, sc, m, width, rule)
	d.resultVerdicts(w, sc, width)
	d.resultPerformance(w, m, width)
	d.resultCapacity(w, m, width)
	d.resultAnalysis(w, m, width)
	d.resultEvidenceNotes(w, m, width)
	fmt.Fprintln(w, rule)
}

func (d *textDisplay) resultHeader(w io.Writer, sc score.Scorecard, m Meta, width int, rule string) {
	fmt.Fprintln(w, rule)
	fmt.Fprintf(w, "model    %s\n", SingleLine(sc.Model))
	if size := strings.TrimSpace(fmt.Sprintf("%s  %s  %s",
		SingleLine(m.ParamSize), SingleLine(m.Quant), SingleLine(m.Family))); size != "" {
		fmt.Fprintf(w, "size     %s\n", size)
	}
	writeResultContext(w, m)
	if m.Level != "" {
		fmt.Fprintf(w, "run      %s\n", resultRunLine(m, d.g))
	}
	useFor := UseForLabel(sc.UseFor)
	if m.Calibration {
		useFor = "calibration evidence only - not a standalone product verdict"
	}
	d.headerField(w, "use for", useFor, width, d.pal.Accent)
	deviceParts := make([]string, 0, 3)
	if gpu := SingleLine(m.GPU); gpu != "" {
		deviceParts = append(deviceParts, gpu)
	}
	if driver := SingleLine(m.Driver); driver != "" {
		deviceParts = append(deviceParts, "driver "+driver)
	}
	if placement := SingleLine(m.Device); placement != "" {
		deviceParts = append(deviceParts, placement)
	}
	d.headerField(w, "device", strings.Join(deviceParts, d.g.Dot), width, "")
	d.headerField(w, "profile", m.Profile, width, "")
	fmt.Fprintln(w, rule)
}

// UseForLabel improves the grammar of a legacy scorecard phrase without
// changing the sealed scorecard or scoring-policy hash.
func UseForLabel(value string) string {
	const suffix = ", (small footprint)"
	if strings.HasSuffix(value, suffix) {
		return strings.TrimSuffix(value, suffix) + "; small footprint"
	}
	return value
}

// PresentVerdictParts resolves display-only wording where one independent
// scenario can override a pooled rate. The sealed verdict remains unchanged.
func PresentVerdictParts(need string, verdict score.Verdict) (string, string, []string, string) {
	measure, gate, detail, note := verdict.Parts()
	if need != "tool_restraint" || verdict.State != score.Fail || !hasWithdrawalFailure(detail) {
		return measure, gate, detail, note
	}
	note = strings.Replace(note, "undecided:", "ordinary irrelevance checks are undecided:", 1)
	note = strings.Replace(note, "Not a fail", "that interval alone is not a fail", 1)
	note = strings.TrimSpace(note)
	if note != "" {
		note += "; "
	}
	note += "overall FAIL comes from the separate withdrawal scenario above"
	return measure, gate, detail, note
}

// PresentVerdictWhy composes the display-only parts for clients that render a
// single evidence string.
func PresentVerdictWhy(need string, verdict score.Verdict) string {
	measure, gate, detail, note := PresentVerdictParts(need, verdict)
	var parts []string
	head := measure
	if head != "" && gate != "" {
		head += " (" + gate + ")"
	} else if head == "" {
		head = gate
	}
	if head != "" {
		parts = append(parts, head)
	}
	if len(detail) > 0 {
		parts = append(parts, strings.Join(detail, ", "))
	}
	if note != "" {
		parts = append(parts, note)
	}
	return strings.Join(parts, "; ")
}

func hasWithdrawalFailure(detail []string) bool {
	for _, item := range detail {
		if strings.Contains(item, "withdrawn tool") || strings.Contains(item, "stop cleanly after a tool was withdrawn") {
			return true
		}
	}
	return false
}

func writeResultContext(w io.Writer, m Meta) {
	if m.NumCtx <= 0 {
		return
	}
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

func resultRunLine(m Meta, g glyphs) string {
	run := SingleLine(m.Level)
	if m.Repeats > 0 {
		run += fmt.Sprintf("%sk=%d", g.Dot, m.Repeats)
	}
	if m.WallSeconds > 0 {
		run += fmt.Sprintf("%s%.1fs", g.Dot, m.WallSeconds)
	}
	if m.StartedAt != "" {
		run += g.Dot + SingleLine(m.StartedAt)
	}
	return run
}

func (d *textDisplay) resultVerdicts(w io.Writer, sc score.Scorecard, width int) {
	for _, k := range score.SortedNeeds(sc.Needs) {
		v, ok := sc.Needs[k]
		if !ok {
			continue
		}
		d.verdictRow(w, k, score.NeedLabel[k], v, width)
	}
}

func (d *textDisplay) resultPerformance(w io.Writer, m Meta, width int) {
	if m.DecodeN <= 0 && m.PrefillN <= 0 && m.TTFTN <= 0 && !analysisLatencyAvailable(m.Analysis) {
		return
	}
	fmt.Fprintf(w, "\n%s\n", d.pal.wrap(d.pal.Head, "performance"))
	d.resultPrimaryPerformance(w, m, width)
	d.resultAuxiliaryLatency(w, m.Analysis, width)
	if m.FirstRunSlow {
		d.footer(w, fmt.Sprintf("! first decode repeat was %.1fx slower than the settled repeats",
			m.FirstRunRatio), width, 2, 4, d.pal.Warn)
	}
}

func (d *textDisplay) resultPrimaryPerformance(w io.Writer, m Meta, width int) {
	if m.DecodeN > 0 {
		d.perfRow(w, "decode", "tok/s", m.DecodeMean, m.DecodeSD, m.DecodeN,
			m.DecodeSeries, m.DecodeMin, m.DecodeMax, width)
		if m.Analysis != nil {
			d.descriptiveReceipt(w, m.Analysis.Performance.DecodeTPS, width)
		}
	}
	if m.PrefillN > 0 {
		d.perfRow(w, "prefill", "tok/s", m.PrefillMean, m.PrefillSD, m.PrefillN,
			m.PrefillSeries, 0, 0, width)
		if m.Analysis != nil {
			d.descriptiveReceipt(w, m.Analysis.Performance.PrefillTPS, width)
		}
	}
	if m.TTFTN > 0 {
		d.perfRow(w, primaryTTFTLabel(m.Analysis), "s", m.TTFTMean, m.TTFTSD, m.TTFTN,
			m.TTFTSeries, 0, 0, width)
		if m.Analysis != nil {
			d.descriptiveReceipt(w, m.Analysis.Performance.TTFTSeconds, width)
		}
	}
}

func primaryTTFTLabel(report *analysis.Report) string {
	if report != nil {
		return analysis.TTFTLabel(report.Performance.TTFTSeconds)
	}
	return "TTFT"
}

func (d *textDisplay) resultAuxiliaryLatency(w io.Writer, report *analysis.Report, width int) {
	if report == nil {
		return
	}
	d.analysisPerfRow(w, "loaded cache-hit TTFT", report.Performance.LoadedCacheHitTTFTSeconds, width)
	d.analysisPerfRow(w, "runtime-unloaded TTFT", report.Performance.RuntimeUnloadedTTFTSeconds, width)
	d.analysisPerfRow(w, "runtime load", report.Performance.RuntimeLoadSeconds, width)
}

func analysisLatencyAvailable(report *analysis.Report) bool {
	if report == nil {
		return false
	}
	return report.Performance.RuntimeUnloadedTTFTSeconds.Estimate != nil ||
		report.Performance.RuntimeLoadSeconds.Estimate != nil ||
		report.Performance.LoadedCacheHitTTFTSeconds.Estimate != nil
}

func (d *textDisplay) analysisPerfRow(w io.Writer, label string,
	observation analysis.PerformanceObservation, width int) {
	if observation.Estimate == nil {
		return
	}
	sd, minValue, maxValue := 0.0, 0.0, 0.0
	if observation.SD != nil {
		sd = *observation.SD
	}
	if observation.Min != nil {
		minValue = *observation.Min
	}
	if observation.Max != nil {
		maxValue = *observation.Max
	}
	d.perfRow(w, label, "s", *observation.Estimate, sd, observation.SampleCount,
		observation.Samples, minValue, maxValue, width)
	d.descriptiveReceipt(w, observation, width)
}

func (d *textDisplay) descriptiveReceipt(w io.Writer, observation analysis.PerformanceObservation, width int) {
	if observation.Status != analysis.StatusDescriptiveOnly {
		return
	}
	d.footer(w, "descriptive only; source "+analysis.AcquisitionLabel(observation.Acquisition),
		width, 2, 4, d.pal.Muted)
}

func (d *textDisplay) resultCapacity(w io.Writer, m Meta, width int) {
	if m.ResidentGB > 0 {
		fmt.Fprintf(w, "\n%s\n", d.pal.wrap(d.pal.Head, "capacity"))
		fmt.Fprintf(w, "  %-8s %.2f GB after requested %s load probe\n", "resident", m.ResidentGB,
			residentContextLabel(m.ResidentContext))
		if m.Analysis != nil && m.Analysis.Capacity.Resident != nil &&
			m.Analysis.Capacity.Resident.Status == analysis.StatusDescriptiveOnly {
			d.footer(w, "descriptive only; source "+analysis.AcquisitionLabel(
				m.Analysis.Capacity.Resident.Acquisition), width, 2, 4, d.pal.Muted)
		}
		if m.Analysis != nil && m.Analysis.Capacity.Placement != nil {
			placement := m.Analysis.Capacity.Placement
			const gib = 1024 * 1024 * 1024
			fmt.Fprintf(w, "  %-18s %.2f GB (%.1f%% of runtime allocation)\n", "runtime accelerator",
				float64(placement.AcceleratorBytes)/gib, placement.AcceleratorPercent)
			fmt.Fprintf(w, "  %-18s %.2f GB (derived remainder)\n", "non-accelerator",
				float64(placement.NonAcceleratorBytes)/gib)
			d.footer(w, placement.Boundary, width, 2, 4, d.pal.Muted)
		}
	}
}

func (d *textDisplay) resultAnalysis(w io.Writer, m Meta, width int) {
	if m.Analysis == nil || len(m.Analysis.Diagnoses) == 0 && len(m.Analysis.Gaps) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s\n", d.pal.wrap(d.pal.Head, "evidence"))
	for _, diagnosis := range m.Analysis.Diagnoses {
		d.headerField(w, "observed", analysis.DiagnosisLabel(diagnosis.Code)+": "+diagnosis.Statement, width, "")
	}
	for _, gap := range m.Analysis.Gaps {
		d.headerField(w, "limit", analysis.GapLabel(gap.Code)+": "+gap.Message, width, d.pal.Muted)
	}
}

func residentContextLabel(ctx int) string {
	if ctx <= 0 || ctx == 32768 {
		return "32K"
	}
	return fmt.Sprintf("%d-token", ctx)
}

func (d *textDisplay) resultEvidenceNotes(w io.Writer, m Meta, width int) {
	// Each need carries its own clustered interval. Combining heterogeneous
	// endpoints into one global MDE would invent a denominator no verdict uses.
	if m.ShowsIntervals {
		fmt.Fprintln(w)
		d.footer(w, "ranges belong to each need. fitr never combines unrelated outcomes into one score or resolution claim",
			width, 0, evidenceHang, d.pal.Muted)
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
	if n < 2 {
		return fmt.Sprintf("%.2f (abs, n=1)", mean)
	}
	if sd == 0 {
		return fmt.Sprintf("%.2f (identical, n=%d)", mean, n)
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
	m = resolvedMeta(m)
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

// stateTag is the fixed-width form of a verdict for the scorecard column.
//
// The stored state stays "INCONCLUSIVE", because it is persisted in saved
// results and in the JSON contract. It is 12 characters, so a row carrying it
// rendered 14 columns wide against every other row's 6 and pushed the label
// column out of alignment. A row that does not line up reads as an exception,
// and an exception reads as something going wrong, which is the opposite of
// what an undecided row means.
//
// "INCL" is the display form: four characters, fits the column, and remains a
// recognizable abbreviation of the full state shown in detail and JSON.
func stateTag(s score.State) string {
	if s == score.Inconclusive {
		return "INCL"
	}
	return string(s)
}
