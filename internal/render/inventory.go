package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const InventorySchema = "fitr.inventory.v1"

// Inventory is the presentation model for the installed-model table.
// State lives in text. Color is optional density, never the carrier.
type Inventory struct {
	Fitr         string
	CPU          string
	GPU          string
	GPUBackend   string
	MemoryGB     float64
	MemorySource string
	// FreeGB is GPU memory not committed elsewhere right now. Display only.
	FreeGB       float64
	RuntimeKind  string
	RuntimeURL   string
	Also         []string
	Profile      string
	Uncalibrated bool
	Warnings     []string
	Rows         []InventoryRow
	Hidden       int
	Empty        string // "none reachable" or "reachable, no models"
}

type InventoryRow struct {
	Model        string
	State        string
	SizeB        int64
	Loaded       bool
	Fit          string
	Next         string
	Note         string
	Ctx          string
	Windows      string
	MeasuredCtx  int
	ServingCtx   int
	ServingKnown bool
}

type inventoryJSON struct {
	Schema       string             `json:"schema"`
	Fitr         string             `json:"fitr,omitempty"`
	CPU          string             `json:"cpu,omitempty"`
	GPU          string             `json:"gpu,omitempty"`
	GPUBackend   string             `json:"gpu_backend,omitempty"`
	MemoryGB     float64            `json:"memory_gb,omitempty"`
	MemorySource string             `json:"memory_source,omitempty"`
	FreeGB       float64            `json:"free_gb,omitempty"`
	Runtime      *inventoryRuntime  `json:"runtime,omitempty"`
	Also         []string           `json:"also,omitempty"`
	Profile      string             `json:"profile,omitempty"`
	Uncalibrated bool               `json:"uncalibrated"`
	Warnings     []string           `json:"warnings,omitempty"`
	Empty        string             `json:"empty,omitempty"`
	Hidden       int                `json:"hidden,omitempty"`
	Rows         []inventoryJSONRow `json:"rows"`
}

type inventoryRuntime struct {
	Kind string `json:"kind"`
	URL  string `json:"url,omitempty"`
}

type inventoryJSONRow struct {
	Model        string `json:"model"`
	State        string `json:"state"`
	Fit          string `json:"fit,omitempty"`
	SizeB        int64  `json:"size_bytes,omitempty"`
	Loaded       bool   `json:"loaded,omitempty"`
	Next         string `json:"next"`
	Note         string `json:"note,omitempty"`
	Ctx          string `json:"ctx,omitempty"`
	Windows      string `json:"windows,omitempty"`
	MeasuredCtx  int    `json:"measured_ctx,omitempty"`
	ServingCtx   int    `json:"serving_ctx,omitempty"`
	ServingKnown bool   `json:"serving_known,omitempty"`
}

// WriteInventory renders the installed list. Unmeasured is a candidate, never
// a recommendation. JSON is one object, not NDJSON: this is a snapshot.
func WriteInventory(w io.Writer, inv Inventory, mode string) {
	resolved := Resolve(mode)
	if resolved == "none" {
		return
	}
	if resolved == "json" {
		writeInventoryJSON(w, inv)
		return
	}
	rich := resolved == "rich"
	p := palette{}
	g := glyphs{" | ", "-", "+/-", "..."}
	if rich {
		p = pickPalette(!noColor())
		g = pickGlyphs()
	}

	// CPU, GPU and runtime strings come from the machine, so none of them has a
	// bound of its own. Wrap under the label column rather than trusting the
	// hardware to be politely named.
	width := Width()
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Head, SingleLine("fitr "+inv.Fitr)))
	if inv.CPU != "" {
		Field(w, "  cpu", invHeaderLabel, inv.CPU, width)
	}
	if inv.GPU != "" {
		gpu := SingleLine(inv.GPU)
		if inv.GPUBackend != "" {
			gpu += "  (" + SingleLine(inv.GPUBackend) + ")"
		}
		Field(w, "  gpu", invHeaderLabel, gpu, width)
	}
	if inv.MemoryGB > 0 && inv.MemorySource != "" {
		mem := fmt.Sprintf("%.1f (%s)", inv.MemoryGB, inv.MemorySource)
		// The FIT column is computed against the whole card. On a machine that
		// is also doing other work, what is free is the number that decides
		// whether anything actually loads, so it belongs beside the total
		// rather than behind a separate command.
		if inv.FreeGB > 0 && inv.FreeGB < inv.MemoryGB*0.9 {
			mem += fmt.Sprintf(", %.1f free now", inv.FreeGB)
		}
		Field(w, "  memory", invHeaderLabel, mem, width)
	}
	if inv.RuntimeKind == "" {
		fmt.Fprintln(w, "  runtime   none reachable")
	} else {
		Field(w, "  runtime", invHeaderLabel,
			SingleLine(inv.RuntimeKind)+"  "+SingleLine(inv.RuntimeURL), width)
		for _, extra := range inv.Also {
			Field(w, "  also", invHeaderLabel, extra, width)
		}
	}
	if inv.Profile != "" {
		label := SingleLine(inv.Profile)
		style := p.Muted
		if inv.Uncalibrated {
			label += g.Dot + "UNCALIBRATED"
			style = p.Warn
		}
		fmt.Fprintf(w, "  profile   %s\n", p.wrap(style, fit(label, width-invHeaderLabel, g.Ell)))
	}
	for _, warning := range inv.Warnings {
		for i, l := range wrap(SingleLine(warning), width-invHeaderLabel) {
			lead := pad("  warning", invHeaderLabel, "")
			if i > 0 {
				lead = strings.Repeat(" ", invHeaderLabel)
			}
			fmt.Fprintf(w, "%s%s\n", lead, p.wrap(p.Warn, l))
		}
	}

	switch inv.Empty {
	case "none reachable":
		fmt.Fprintln(w)
		fmt.Fprintln(w, p.wrap(p.Muted, "  next      start Ollama, llama-server, or an OpenAI-compatible server"))
		fmt.Fprintln(w, p.wrap(p.Muted, "            then: fitr advise <model>   and   fitr run <model>"))
		return
	case "reachable, no models":
		fmt.Fprintln(w)
		fmt.Fprintln(w, p.wrap(p.Muted, "  no models installed on this runtime"))
		fmt.Fprintln(w, p.wrap(p.Muted, "  next      pull a model, then: fitr advise <model>"))
		return
	}

	fmt.Fprintln(w)
	modelWidth := max(width-invFixed, 12)
	fmt.Fprintf(w, "  %s %-*s %-*s %*s  %s\n",
		pad("MODEL", modelWidth, ""), invStateWidth, "STATE",
		invCtxWidth, "CTX", invSizeWidth, "SIZE", "NEXT")
	for _, row := range inv.Rows {
		name := row.Model
		if row.Loaded {
			name = "* " + name
		}
		state, style := inventoryState(row, p)
		ctxCol := row.Ctx
		if ctxCol == "" {
			ctxCol = "-"
		}
		size := "-"
		if row.SizeB > 0 {
			size = fmt.Sprintf("%.1f GB", float64(row.SizeB)/(1024*1024*1024))
		}
		fmt.Fprintf(w, "  %s %s %-*s %*s  %s\n",
			pad(name, modelWidth, g.Ell), p.wrap(style, pad(state, invStateWidth, g.Ell)),
			invCtxWidth, ctxCol, invSizeWidth, size,
			fit(shortNext(row.Next, row.Model), invNextWidth, g.Ell))
		for _, extra := range []string{row.Note, row.Windows} {
			if extra == "" {
				continue
			}
			for _, l := range wrap(SingleLine(extra), max(width-invNoteIndent, MinWidth)) {
				fmt.Fprintf(w, "%s%s\n", strings.Repeat(" ", invNoteIndent), p.wrap(p.Muted, l))
			}
		}
	}
	fmt.Fprintln(w)
	if inv.Hidden > 0 {
		fmt.Fprintf(w, "  %s\n", p.wrap(p.Muted, fmt.Sprintf("showing %d of %d installed; name one with fitr advise <model>",
			len(inv.Rows), len(inv.Rows)+inv.Hidden)))
	}
	// NEXT drops the verb's `fitr ` and the model name, because both are already
	// on the row. Keeping them made every row carry the model name twice and
	// pushed the longest to 117 columns, so the table wrapped on any ordinary
	// terminal. One worked example is worth more than fourteen repetitions.
	for i, l := range wrap("next reads as `fitr <next> <model>`, e.g. fitr advise "+
		exampleModel(inv.Rows), width-4) {
		lead := "  "
		if i > 0 {
			lead = "    "
		}
		fmt.Fprintln(w, p.wrap(p.Muted, lead+l))
	}
	fmt.Fprintln(w, p.wrap(p.Muted, "  * loaded    CTX is measured, or measured/serving when they differ"))
	fmt.Fprintln(w, p.wrap(p.Muted, "  * suggested window   > requested window that does not fit"))
	fmt.Fprintln(w, p.wrap(p.Muted, "  unmeasured is a candidate, never a recommendation"))
	fmt.Fprintln(w, p.wrap(p.Muted, "  board compares only measured runs"))
}

// Inventory column plan. STATE carries what STATE and FIT used to say between
// them: FIT read "-" on every unmeasured row, so eleven columns were spent
// telling the reader nothing on the majority of the table, while the model
// name and the command were both being truncated for want of them.
const (
	// invHeaderLabel is the "  runtime   " column above the table.
	invHeaderLabel = 12
	invStateWidth  = 16
	invCtxWidth    = 5
	invSizeWidth   = 7
	// Wide enough for the longest NEXT after shortening: "try a smaller quant",
	// which is advice rather than a command and so keeps its own words.
	invNextWidth  = 19
	invNoteIndent = 4
	invFixed      = 2 + 1 + invStateWidth + 1 + invCtxWidth + 1 + invSizeWidth + 2 + invNextWidth
)

// inventoryState folds the fit tier into the state word. "unproven" alone does
// not say whether the thing is even loadable; "fits, unproven" does, in the
// same column.
func inventoryState(row InventoryRow, p palette) (string, string) {
	tier := row.Fit
	if tier == "low_memory" {
		tier = "tight"
	}
	if row.State != "unproven" || tier == "" || tier == "-" {
		return row.State, stateColor(p, row.State)
	}
	if tier == "compatible" {
		tier = "fits"
	}
	return tier + ", unproven", fitTierColor(p, row.Fit)
}

// shortNext strips what the row already shows: the binary's own name and the
// model the command is about.
func shortNext(next, model string) string {
	s := strings.TrimPrefix(SingleLine(next), "fitr ")
	if model != "" {
		// Quoted first: a shell-quoted name contains the bare one, so matching
		// the bare form first would leave the quotes behind. Only a whole
		// argument is removed, never a prefix of a longer model name.
		for _, form := range []string{`"` + model + `"`, `'` + model + `'`, model} {
			if i := strings.Index(s, form); i >= 0 && isWholeArg(s, i, len(form)) {
				s = s[:i] + s[i+len(form):]
				break
			}
		}
	}
	if s = strings.Join(strings.Fields(s), " "); s == "" {
		return "-"
	}
	return s
}

func isWholeArg(s string, start, length int) bool {
	if start > 0 && s[start-1] != ' ' {
		return false
	}
	end := start + length
	return end == len(s) || s[end] == ' '
}

func exampleModel(rows []InventoryRow) string {
	for _, row := range rows {
		if row.Model != "" {
			return SingleLine(row.Model)
		}
	}
	return "<model>"
}

func stateColor(p palette, state string) string {
	switch state {
	case "measured":
		return p.Pass
	case "stale":
		return p.Warn
	case "incompatible":
		return p.Fail
	default:
		return p.Muted
	}
}

func writeInventoryJSON(w io.Writer, inv Inventory) {
	payload := inventoryJSON{
		Schema:       InventorySchema,
		Fitr:         inv.Fitr,
		CPU:          inv.CPU,
		GPU:          inv.GPU,
		GPUBackend:   inv.GPUBackend,
		MemoryGB:     inv.MemoryGB,
		MemorySource: inv.MemorySource,
		FreeGB:       inv.FreeGB,
		Profile:      inv.Profile,
		Uncalibrated: inv.Uncalibrated,
		Warnings:     inv.Warnings,
		Empty:        inv.Empty,
		Hidden:       inv.Hidden,
		Also:         inv.Also,
		Rows:         []inventoryJSONRow{},
	}
	if inv.RuntimeKind != "" {
		payload.Runtime = &inventoryRuntime{Kind: inv.RuntimeKind, URL: inv.RuntimeURL}
	}
	for _, row := range inv.Rows {
		payload.Rows = append(payload.Rows, inventoryJSONRow{
			Model: row.Model, State: row.State, Fit: row.Fit, SizeB: row.SizeB,
			Loaded: row.Loaded, Next: row.Next, Note: row.Note,
			Ctx: row.Ctx, Windows: row.Windows, MeasuredCtx: row.MeasuredCtx,
			ServingCtx: row.ServingCtx, ServingKnown: row.ServingKnown,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}
