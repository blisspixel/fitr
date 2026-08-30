package render

import (
	"fmt"
	"io"
	"math"
	"strings"
)

// Board column plan.
//
// The rule used to say 104 while a row carrying a full serves list reached 123,
// so the boundary was crossed by the table it was drawn around, and 123 wraps
// on any ordinary terminal. Build and serves moved to a continuation line --
// neither is a number you scan a column of -- which buys the numeric row an
// exact fit at DefaultWidth with every column intact.
const (
	boardModelWidth = 24
	boardHeaderFmt  = "  %-*s %-10s %7s %6s %-8s %8s %6s %2s"
	boardRowFmt     = "  %s %s %7.2f %6s %s %8.1f %6.2f %s\n"
	// 2 gutter + model + bar(10) + tok/s(7) + sd(6) + runs(8) + prefill(8) +
	// GB(6) + k(2), one space between each.
	boardWidth = 2 + boardModelWidth + 1 + 10 + 1 + 7 + 1 + 6 + 1 + 8 + 1 + 8 + 1 + 6 + 1 + 2
)

// Board is the presentation model for the saved-result overview. It contains
// only values needed by the terminal, which keeps layout concerns out of the
// measurement and scoring code.
// The JSON tags matter: this presentation model is what `--display json`
// emits. Encoding the sealed result records instead produced 23 KB for two
// models, which grows past what a caller can hold in one read at three, and
// buries the handful of fields the board actually reports.
type Board struct {
	Groups  []BoardGroup `json:"groups"`
	Results int          `json:"results"`
}

type BoardGroup struct {
	GPU          string     `json:"gpu"`
	Driver       string     `json:"gpu_driver,omitempty"`
	KV           string     `json:"kv_cache_type,omitempty"`
	Note         string     `json:"note,omitempty"`
	NumCtx       int        `json:"num_ctx,omitempty"`
	EffectiveCtx int        `json:"effective_ctx,omitempty"`
	ContextState string     `json:"context_state,omitempty"`
	Rows         []BoardRow `json:"rows"`
}

type BoardRow struct {
	Model        string    `json:"model"`
	ParamSize    string    `json:"parameter_size,omitempty"`
	Quant        string    `json:"quant,omitempty"`
	DecodeMean   float64   `json:"decode_tps"`
	DecodeSD     float64   `json:"decode_sd,omitempty"`
	PrefillMean  float64   `json:"prefill_tps,omitempty"`
	ResidentGB   float64   `json:"resident_gb,omitempty"`
	DecodeSeries []float64 `json:"decode_series,omitempty"`
	Repeats      int       `json:"repeats"`
	Serves       []string  `json:"serves,omitempty"`
}

// WriteBoard renders a data-dense overview without taking over the terminal.
// The same presentation model can later feed an interactive TUI. Plain mode
// remains stable ASCII for pipes, logs, and CI.
func WriteBoard(w io.Writer, board Board, mode string) {
	resolved := Resolve(mode)
	if resolved == "none" {
		return
	}
	p, g, unicode := boardStyle(resolved == "rich")

	// The board's columns are fixed, so its rule matches them rather than the
	// terminal: a rule wider than the content is as wrong as one narrower.
	title := fmt.Sprintf("FITR BOARD  %d result(s)  %d device/config block(s)", board.Results, len(board.Groups))
	fmt.Fprintln(w, p.wrap(p.Head, title))
	fmt.Fprintln(w, p.wrap(p.Muted, strings.Repeat("-", boardWidth)))
	for i, group := range board.Groups {
		if i > 0 {
			fmt.Fprintln(w)
		}
		writeBoardGroup(w, group, p, g, unicode)
	}
	if len(board.Groups) > 1 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, p.wrap(p.Warn, "blocks are never ranked across hardware or request configuration"))
	}
	fmt.Fprintln(w, p.wrap(p.Muted, "k = repeats; k<3 is a smoke test, not a rankable result"))
}

func boardStyle(rich bool) (palette, glyphs, bool) {
	p := palette{}
	g := glyphs{" | ", "-", "+/-", "..."}
	if !rich {
		return p, g, false
	}
	return pickPalette(!noColor()), pickGlyphs(), unicodeOK()
}

func writeBoardGroup(w io.Writer, group BoardGroup, p palette, g glyphs, unicode bool) {
	writeBoardGroupHeader(w, group, p, g)
	maxDecode := boardMaxDecode(group.Rows)
	fmt.Fprintf(w, "%s\n", fmt.Sprintf(boardHeaderFmt,
		boardModelWidth, "model", "decode", "tok/s", "sd", "runs", "prefill", "GB", "k"))
	for _, row := range group.Rows {
		writeBoardRow(w, row, maxDecode, p, g, unicode)
	}
	fmt.Fprintln(w, p.wrap(p.Muted, "  decode bars are relative only within this device/config block"))
	fmt.Fprintln(w, p.wrap(p.Muted, "  runs is repeat shape oldest to newest; flat means repeats did not move"))
}

func writeBoardGroupHeader(w io.Writer, group BoardGroup, p palette, g glyphs) {
	kv := group.KV
	if kv == "" {
		kv = "default"
	}
	// The GPU name comes from the machine, so this line has no bound of its
	// own. Wrap it rather than trusting the hardware to be politely named.
	header := fmt.Sprintf("%s%sdriver %s%sKV %s%s%s",
		SingleLine(group.GPU), g.Dot, SingleLine(group.Driver), g.Dot,
		SingleLine(kv), g.Dot, SingleLine(boardContext(group)))
	for i, line := range wrap(header, boardWidth-2) {
		if i > 0 {
			line = "  " + line
		}
		fmt.Fprintln(w, p.wrap(p.Head, line))
	}
	noteStyle := p.Muted
	if strings.Contains(group.Note, "not comparable") {
		noteStyle = p.Warn
	}
	fmt.Fprintf(w, "  %s\n", p.wrap(noteStyle, SingleLine(group.Note)))
}

func boardContext(group BoardGroup) string {
	switch {
	case group.EffectiveCtx > 0 && group.EffectiveCtx != group.NumCtx:
		return fmt.Sprintf("ctx %d -> %d effective", group.NumCtx, group.EffectiveCtx)
	case group.EffectiveCtx > 0:
		return fmt.Sprintf("ctx %d verified", group.EffectiveCtx)
	default:
		return fmt.Sprintf("ctx %d", group.NumCtx)
	}
}

func boardMaxDecode(rows []BoardRow) float64 {
	maxDecode := 0.0
	for _, row := range rows {
		if row.DecodeMean > maxDecode {
			maxDecode = row.DecodeMean
		}
	}
	return maxDecode
}

func writeBoardRow(w io.Writer, row BoardRow, maxDecode float64, p palette, g glyphs, unicode bool) {
	model := p.wrap(p.Head, pad(row.Model, boardModelWidth, g.Ell))
	bar := p.wrap(p.Accent, valueBar(row.DecodeMean, maxDecode, 8, unicode))
	trend := p.wrap(p.Muted, pad(boardSpark(row, unicode), 8, ""))
	sd := "-"
	if row.Repeats > 1 && row.DecodeSD > 0 {
		sd = fmt.Sprintf("%.2f", row.DecodeSD)
	}
	k := fmt.Sprintf("%2d", row.Repeats)
	if row.Repeats < 3 {
		k = p.wrap(p.Warn, k)
	}
	fmt.Fprintf(w, boardRowFmt, model, bar, row.DecodeMean, sd, trend,
		row.PrefillMean, row.ResidentGB, k)
	writeBoardRowTail(w, row, p, g)
}

func boardSpark(row BoardRow, unicode bool) string {
	if spark, informative := sparkline(row.DecodeSeries, 7, unicode); informative {
		return spark
	}
	if len(row.DecodeSeries) > 1 && flatSeries(row.DecodeSeries) {
		return "flat"
	}
	return "-"
}

func writeBoardRowTail(w io.Writer, row BoardRow, p palette, g glyphs) {
	var tail []string
	if build := strings.TrimSpace(row.ParamSize + " " + row.Quant); build != "" {
		tail = append(tail, build)
	}
	if len(row.Serves) > 0 {
		tail = append(tail, "serves "+strings.Join(row.Serves, ", "))
	}
	if len(tail) == 0 {
		return
	}
	// The visible separator survives wrapping; whitespace runs do not.
	for _, line := range wrap(SingleLine(strings.Join(tail, g.Dot)), boardWidth-4) {
		fmt.Fprintf(w, "    %s\n", p.wrap(p.Muted, line))
	}
}

func valueBar(value, ceiling float64, width int, unicode bool) string {
	if width < 1 {
		return ""
	}
	filled := 0
	if ceiling > 0 && value > 0 {
		filled = int(math.Round(float64(width) * value / ceiling))
		filled = min(max(filled, 1), width)
	}
	full, empty := "#", "."
	if unicode {
		full, empty = "█", "░"
	}
	return "[" + strings.Repeat(full, filled) + strings.Repeat(empty, width-filled) + "]"
}

// flatFloor is the relative spread below which a sparkline is refused.
//
// The glyph row is normalised to the series min and max, so a run that varied
// by 0.4% renders the same dramatic zigzag as one that varied by tenfold. On a
// tool that will not print "+/- 0.00" for a single sample, drawing a trend out
// of noise is the same error with a nicer face. Below this the numbers on the
// row already say everything the picture could.
const flatFloor = 0.05

// flatSeries reports whether a series varies too little for a drawn shape to be
// honest about it.
func flatSeries(values []float64) bool {
	lo, hi, ok := seriesRange(values)
	if !ok {
		return true
	}
	sum, n := 0.0, 0
	for _, v := range values {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			sum += v
			n++
		}
	}
	if n == 0 {
		return true
	}
	mean := sum / float64(n)
	return mean == 0 || (hi-lo)/math.Abs(mean) < flatFloor
}

// sparkline returns the glyph row and whether it carries information.
//
// It is refused outright without Unicode. The ASCII ramp it used to fall back
// to -- .:-=+*#@ -- is not a perceptual ramp: `#` and `@` are dense but not
// tall, `-` sits mid-cell, and readers cannot order those by height, which is
// the only channel a sparkline has. `@.**@#=*=#` is noise wearing a chart's
// clothes, and a chart nobody can read is worse than no chart.
func sparkline(values []float64, limit int, unicode bool) (string, bool) {
	if limit < 1 || len(values) < 2 || !unicode {
		return "", false
	}
	clean := make([]float64, 0, len(values))
	for _, value := range values {
		if !math.IsNaN(value) && !math.IsInf(value, 0) {
			clean = append(clean, value)
		}
	}
	if len(clean) < 2 {
		return "", false
	}
	if len(clean) > limit {
		if limit == 1 {
			clean = clean[len(clean)-1:]
		} else {
			sampled := make([]float64, limit)
			for i := range sampled {
				index := int(math.Round(float64(i) * float64(len(clean)-1) / float64(limit-1)))
				sampled[i] = clean[index]
			}
			clean = sampled
		}
	}
	if flatSeries(clean) {
		return "", false
	}
	levels := []rune("▁▂▃▄▅▆▇█")
	lo, hi, _ := seriesRange(clean)
	var out strings.Builder
	for _, value := range clean {
		out.WriteRune(levels[int(math.Round(float64(len(levels)-1)*(value-lo)/(hi-lo)))])
	}
	return out.String(), true
}
