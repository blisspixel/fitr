package render

import (
	"encoding/json"
	"fmt"
	"io"
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
	RuntimeKind  string
	RuntimeURL   string
	Also         []string
	Profile      string
	Uncalibrated bool
	Rows         []InventoryRow
	Hidden       int
	Empty        string // "none reachable" or "reachable, no models"
}

type InventoryRow struct {
	Model  string
	State  string
	SizeB  int64
	Loaded bool
	Fit    string
	Next   string
	Note   string
}

type inventoryJSON struct {
	Schema       string             `json:"schema"`
	Fitr         string             `json:"fitr,omitempty"`
	CPU          string             `json:"cpu,omitempty"`
	GPU          string             `json:"gpu,omitempty"`
	GPUBackend   string             `json:"gpu_backend,omitempty"`
	MemoryGB     float64            `json:"memory_gb,omitempty"`
	MemorySource string             `json:"memory_source,omitempty"`
	Runtime      *inventoryRuntime  `json:"runtime,omitempty"`
	Also         []string           `json:"also,omitempty"`
	Profile      string             `json:"profile,omitempty"`
	Uncalibrated bool               `json:"uncalibrated"`
	Empty        string             `json:"empty,omitempty"`
	Hidden       int                `json:"hidden,omitempty"`
	Rows         []inventoryJSONRow `json:"rows"`
}

type inventoryRuntime struct {
	Kind string `json:"kind"`
	URL  string `json:"url,omitempty"`
}

type inventoryJSONRow struct {
	Model  string `json:"model"`
	State  string `json:"state"`
	Fit    string `json:"fit,omitempty"`
	SizeB  int64  `json:"size_bytes,omitempty"`
	Loaded bool   `json:"loaded,omitempty"`
	Next   string `json:"next"`
	Note   string `json:"note,omitempty"`
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

	fmt.Fprintf(w, "  %s\n", p.wrap(p.Head, SingleLine("fitr "+inv.Fitr)))
	if inv.CPU != "" {
		fmt.Fprintf(w, "  cpu       %s\n", SingleLine(inv.CPU))
	}
	if inv.GPU != "" {
		gpu := SingleLine(inv.GPU)
		if inv.GPUBackend != "" {
			gpu += "  (" + SingleLine(inv.GPUBackend) + ")"
		}
		fmt.Fprintf(w, "  gpu       %s\n", gpu)
	}
	if inv.MemoryGB > 0 && inv.MemorySource != "" {
		fmt.Fprintf(w, "  memory    %s\n", SingleLine(fmt.Sprintf("%.1f (%s)", inv.MemoryGB, inv.MemorySource)))
	}
	if inv.RuntimeKind == "" {
		fmt.Fprintln(w, "  runtime   none reachable")
	} else {
		fmt.Fprintf(w, "  runtime   %s  %s\n", SingleLine(inv.RuntimeKind), SingleLine(inv.RuntimeURL))
		for _, extra := range inv.Also {
			fmt.Fprintf(w, "  also      %s\n", SingleLine(extra))
		}
	}
	if inv.Profile != "" {
		label := SingleLine(inv.Profile)
		style := p.Muted
		if inv.Uncalibrated {
			label += g.Dot + "UNCALIBRATED"
			style = p.Warn
		}
		fmt.Fprintf(w, "  profile   %s\n", p.wrap(style, label))
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
	fmt.Fprintf(w, "  %-24s %-12s %-12s %8s  %s\n", "MODEL", "STATE", "FIT", "SIZE", "NEXT")
	for _, row := range inv.Rows {
		name := row.Model
		if row.Loaded {
			name = "* " + name
		}
		state := p.wrap(stateColor(p, row.State), fmt.Sprintf("%-12s", row.State))
		fitLabel := row.Fit
		if fitLabel == "" {
			fitLabel = "-"
		}
		if fitLabel == "low_memory" {
			fitLabel = "low mem"
		}
		fitCol := p.wrap(fitTierColor(p, row.Fit), fmt.Sprintf("%-12s", fitLabel))
		size := "-"
		if row.SizeB > 0 {
			size = fmt.Sprintf("%.1f GB", float64(row.SizeB)/(1024*1024*1024))
		}
		fmt.Fprintf(w, "  %-24s %s %s %8s  %s\n",
			fit(name, 24, g.Ell), state, fitCol, size, SingleLine(row.Next))
		if row.Note != "" {
			fmt.Fprintf(w, "  %-24s %s\n", "", p.wrap(p.Muted, SingleLine(row.Note)))
		}
	}
	fmt.Fprintln(w)
	if inv.Hidden > 0 {
		fmt.Fprintf(w, "  %s\n", p.wrap(p.Muted, fmt.Sprintf("showing %d of %d installed; name one with fitr advise <model>",
			len(inv.Rows), len(inv.Rows)+inv.Hidden)))
	}
	fmt.Fprintln(w, p.wrap(p.Muted, "  * loaded    unmeasured is a candidate, never a recommendation"))
	fmt.Fprintln(w, p.wrap(p.Muted, "  next is one command per row; board compares only measured runs"))
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
		Profile:      inv.Profile,
		Uncalibrated: inv.Uncalibrated,
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
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}
