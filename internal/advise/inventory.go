package advise

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Inventory states are plain text. Color must never carry them alone.
const (
	StateMeasured     = "measured"
	StateUnproven     = "unproven"
	StateIncompatible = "incompatible"
	StateStale        = "stale"
)

const maxInventoryRows = 100

// InstalledModel is one entry from the serving runtime's installed list.
// Size 0 means the runtime did not report a file size.
type InstalledModel struct {
	Name  string
	Size  int64
	Quant string
	Path  string // GGUF path when the runtime exposed one; never crawled
	Arch  Arch   // zero unless the caller already has architecture
}

// InventoryEvidence is the saved-run facts inventory needs. Callers map
// records; this package does not import the store.
type InventoryEvidence struct {
	Model          string
	DeviceKey      string // Fingerprint.Key: same box, not a ranking key
	Level          string
	IntegrityIssue string
	Contaminated   bool
	Arch           Arch
	WeightsB       int64
	NumCtx         int
}

// InventoryQuery is everything Join needs. Tags are the runtime's list, not
// a disk crawl and not a catalog.
type InventoryQuery struct {
	Tags       []InstalledModel
	Loaded     []string
	Evidence   []InventoryEvidence
	CurrentKey string
	HaveGB     float64
	HaveSrc    string
}

// InventoryRow is one installed model joined to current evidence.
type InventoryRow struct {
	Model  string
	State  string
	SizeB  int64
	Quant  string
	Loaded bool
	Fit    string // compatible / low_memory / incompatible / skip; empty if unknown
	Next   string
	Note   string
}

// InventoryTable is the joined, sorted, capped inventory.
type InventoryTable struct {
	Rows   []InventoryRow
	Hidden int // installed models omitted by the cap; 0 means complete
}

// Join lists what is already serving. Unmeasured is a candidate, never a
// recommendation. Weights-versus-budget is the only fit check: architecture
// KV sizing stays on `fitr advise <model>`.
func Join(q InventoryQuery) InventoryTable {
	loaded := map[string]bool{}
	for _, name := range q.Loaded {
		if name = strings.TrimSpace(name); name != "" {
			loaded[name] = true
		}
	}
	evidence := q.Evidence

	seen := map[string]bool{}
	var rows []InventoryRow
	for _, tag := range q.Tags {
		name := strings.TrimSpace(tag.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		rows = append(rows, classify(tag, loaded[name] || loadedMatch(loaded, name), evidence, q))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if oi, oj := stateOrder(rows[i].State), stateOrder(rows[j].State); oi != oj {
			return oi < oj
		}
		if rows[i].Loaded != rows[j].Loaded {
			return rows[i].Loaded
		}
		return strings.ToLower(rows[i].Model) < strings.ToLower(rows[j].Model)
	})
	hidden := 0
	if len(rows) > maxInventoryRows {
		hidden = len(rows) - maxInventoryRows
		rows = rows[:maxInventoryRows]
	}
	return InventoryTable{Rows: rows, Hidden: hidden}
}

func classify(tag InstalledModel, isLoaded bool, evidence []InventoryEvidence, q InventoryQuery) InventoryRow {
	row := InventoryRow{
		Model:  tag.Name,
		SizeB:  tag.Size,
		Quant:  tag.Quant,
		Loaded: isLoaded,
	}
	ev := matchEvidence(tag.Name, evidence)
	weightsExceed := memoryBudgetTrusted(q.HaveSrc) && q.HaveGB > 0 && tag.Size > 0 &&
		float64(tag.Size) > q.HaveGB*GiB

	switch {
	case ev != nil && evidenceUsable(*ev, q.CurrentKey):
		row.State = StateMeasured
		if ev.Level == "quick" || ev.Level == "checks-only" {
			row.Next = "fitr run " + shellModel(tag.Name)
			row.Note = "measured at --" + ev.Level + "; default battery still unproven"
		} else {
			row.Next = "fitr view " + shellModel(tag.Name)
		}
	case ev != nil:
		row.State = StateStale
		row.Next = "fitr run " + shellModel(tag.Name)
		row.Note = staleNote(*ev, q.CurrentKey)
	case isLoaded && weightsExceed:
		// Same rule as advise: a running process beats a budget reading.
		row.State = StateUnproven
		row.Next = "fitr advise " + shellModel(tag.Name)
		row.Note = "weights exceed the memory reading; the process is running, so the budget is the suspect number"
	case weightsExceed:
		row.State = StateIncompatible
		row.Note = fmt.Sprintf("weights %.1f GB exceed %s", float64(tag.Size)/GiB, memoryBudget(q.HaveGB, q.HaveSrc))
		row.Next = "try a smaller quant"
	default:
		row.State = StateUnproven
		row.Next = "fitr advise " + shellModel(tag.Name)
	}
	attachFit(&row, tag, ev, q)
	return row
}

func attachFit(row *InventoryRow, tag InstalledModel, ev *InventoryEvidence, q InventoryQuery) {
	arch := tag.Arch
	weights := tag.Size
	ctx := defaultRunCtx
	if ev != nil {
		if ev.Arch.KVReady() {
			arch = ev.Arch
		}
		if ev.WeightsB > 0 {
			weights = ev.WeightsB
		}
		if ev.NumCtx > 0 {
			ctx = ev.NumCtx
		}
	}
	if weights <= 0 || !memoryBudgetTrusted(q.HaveSrc) || q.HaveGB <= 0 {
		return
	}
	if !arch.KVReady() && !arch.Hybrid {
		return
	}
	r := evaluateCore(Input{
		WeightsB: weights, HaveGB: q.HaveGB, HaveSrc: q.HaveSrc,
		Ctx: ctx, Arch: arch,
	})
	row.Fit = r.Tier
	if row.State != StateUnproven {
		return
	}
	if next := AdviseNext(row.Model, r.Tier, r.FlagValue); next != "" {
		row.Next = next
	}
}

func evidenceUsable(ev InventoryEvidence, currentKey string) bool {
	if ev.IntegrityIssue != "" || ev.Contaminated {
		return false
	}
	if currentKey == "" || ev.DeviceKey == "" {
		return false
	}
	return ev.DeviceKey == currentKey
}

func staleNote(ev InventoryEvidence, currentKey string) string {
	switch {
	case ev.IntegrityIssue != "":
		return ev.IntegrityIssue
	case ev.Contaminated:
		return "prior run was contaminated by a resident model"
	case ev.DeviceKey != currentKey:
		return "device or runtime changed since the last measurement"
	default:
		return "saved evidence cannot be used"
	}
}

func matchEvidence(name string, evidence []InventoryEvidence) *InventoryEvidence {
	for i := range evidence {
		if sameInventoryModel(name, evidence[i].Model) {
			return &evidence[i]
		}
	}
	return nil
}

func loadedMatch(loaded map[string]bool, name string) bool {
	if loaded[name] {
		return true
	}
	for have := range loaded {
		if sameInventoryModel(name, have) {
			return true
		}
	}
	return false
}

func sameInventoryModel(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == b {
		return true
	}
	trim := func(s string) string {
		s = strings.TrimSuffix(s, ":latest")
		return strings.TrimSuffix(s, ":LATEST")
	}
	return trim(a) == trim(b)
}

func stateOrder(state string) int {
	switch state {
	case StateMeasured:
		return 0
	case StateStale:
		return 1
	case StateUnproven:
		return 2
	case StateIncompatible:
		return 3
	default:
		return 9
	}
}

func memoryBudgetTrusted(src string) bool {
	switch src {
	case "nvidia-smi", "unified memory (system RAM)", "drm sysfs":
		return true
	}
	return strings.HasPrefix(src, "--vram-gb")
}

func memoryBudget(gb float64, src string) string {
	if gb <= 0 {
		return "unknown (not measured)"
	}
	s := fmt.Sprintf("%.1f GB", gb)
	if src != "" {
		s += " (" + src + ")"
	}
	return s
}

func shellModel(name string) string {
	if strings.ContainsAny(name, " \t\"'\\") {
		return strconv.Quote(name)
	}
	return name
}
