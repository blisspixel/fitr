package advise

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/blisspixel/fitr/internal/device"
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
	Name           string
	Size           int64
	Quant          string
	Path           string // GGUF path when the runtime exposed one; never crawled
	ArtifactDigest string // verified runtime-bound SHA-256; empty is unmeasured
	Arch           Arch   // zero unless the caller already has architecture
	ResidentB      int64  // live PS allocation when the process is loaded; 0 unknown
}

// InventoryEvidence is the saved-run facts inventory needs. Callers map
// records; this package does not import the store.
type InventoryEvidence struct {
	Model           string
	ArtifactDigest  string
	Device          device.Fingerprint
	DeviceKey       string // Fingerprint.Key: same box, not a ranking key
	ComparableIssue string // missing or invalid v2 context/placement receipt
	Level           string
	IntegrityIssue  string
	Contaminated    bool
	Arch            Arch
	WeightsB        int64
	NumCtx          int
	Repeats         int
	ToolsBlocked    bool
}

// InventoryQuery is everything Join needs. Tags are the runtime's list, not
// a disk crawl and not a catalog.
type InventoryQuery struct {
	Tags       []InstalledModel
	Loaded     []string
	Evidence   []InventoryEvidence
	Current    device.Fingerprint
	CurrentKey string
	HaveGB     float64
	HaveSrc    string
	Serving    map[string]int // observed PS context_length; missing/0 = unknown
}

// InventoryRow is one installed model joined to current evidence.
type InventoryRow struct {
	Model        string
	State        string
	SizeB        int64
	Quant        string
	Loaded       bool
	Fit          string // compatible / low_memory / incompatible / skip; empty if unknown
	Next         string
	Note         string
	Ctx          string // compact measured, or measured/serving when they differ
	Windows      string // compact context-fit graph; empty if architecture unknown
	MeasuredCtx  int
	ServingCtx   int
	ServingKnown bool
}

// InventoryTable is the joined, sorted, capped inventory.
type InventoryTable struct {
	Rows   []InventoryRow
	Hidden int // installed models omitted by the cap; 0 means complete
}

// Join lists what is already serving. Unmeasured is a candidate, never a
// recommendation. Architecture, when already known, attaches a fit tier and
// a compact context-fit graph. Serving context comes only from a live PS
// observation, never from a guessed default.
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
	ev := matchEvidence(tag, evidence, q)
	sharedCapacity := automaticSharedMemoryCapacity(inventoryMemoryInput(q))
	weightsExceed := memoryBudgetTrusted(q.HaveSrc) && !sharedCapacity && q.HaveGB > 0 && tag.Size > 0 &&
		float64(tag.Size) > q.HaveGB*GiB

	serving, servingKnown := servingOf(tag.Name, q.Serving)
	row.ServingCtx = serving
	row.ServingKnown = servingKnown
	switch {
	case ev != nil && evidenceUsable(tag, *ev, q):
		row.State = StateMeasured
		row.MeasuredCtx = ev.NumCtx
		row.Next = MeasuredNext(tag.Name, ev.Repeats, ev.NumCtx, ev.Level, ev.ToolsBlocked, serving, servingKnown)
		if ev.Level == "quick" || ev.Level == "checks-only" {
			row.Note = "measured at --" + ev.Level + "; default battery still unproven"
		} else if note := applyNote(ev.NumCtx, serving, servingKnown); note != "" {
			row.Note = note
		}
	case ev != nil:
		row.State = StateStale
		row.Next = "fitr run " + shellModel(tag.Name)
		row.Note = staleNote(tag, *ev, q)
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
	fitEvidence := ev
	if fitEvidence != nil && !evidenceUsable(tag, *fitEvidence, q) {
		fitEvidence = nil
	}
	attachFit(&row, tag, fitEvidence, q)
	row.Ctx = compactCtxPair(row.MeasuredCtx, row.ServingCtx, row.ServingKnown)
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
	if ev == nil && tag.ResidentB > 0 && !row.ServingKnown && !arch.KVReady() && !arch.Hybrid {
		ctx = 0
	}
	if !memoryBudgetTrusted(q.HaveSrc) || q.HaveGB <= 0 {
		return
	}
	// A live allocation can answer whether the current process fits even when
	// the runtime does not expose weights or architecture metadata. Projection
	// to other context windows still requires both.
	if tag.ResidentB <= 0 && (weights <= 0 || (!arch.KVReady() && !arch.Hybrid)) {
		return
	}
	in := inventoryMemoryInput(q)
	in.WeightsB = weights
	in.Ctx = ctx
	in.Arch = arch
	in.ResidentB = tag.ResidentB
	if tag.ResidentB > 0 {
		in.ResidentSrc = "runtime PS"
		if row.ServingKnown {
			in.ResidentCtx = row.ServingCtx
		}
	}
	r := evaluateCore(in)
	row.Fit = r.Tier
	// A running process beats a file-size budget. Incompatible from weights
	// alone is not a loaded-process verdict.
	if row.Loaded && r.Tier == Incompatible {
		row.Fit = Skip
	}
	row.Windows = CompactWindows(ContextFit(in))
	if row.State != StateUnproven {
		return
	}
	if next := AdviseNext(row.Model, row.Fit, r.FlagValue); next != "" {
		row.Next = next
	}
}

func inventoryMemoryInput(q InventoryQuery) Input {
	return Input{
		HaveGB:              q.HaveGB,
		HaveSrc:             q.HaveSrc,
		NVIDIAUnifiedMemory: device.IsNVIDIAUnifiedMemoryGPU(q.Current.GPU),
	}
}

func servingOf(name string, serving map[string]int) (int, bool) {
	if n, ok := serving[name]; ok {
		return n, n > 0
	}
	n, found := 0, false
	for have, v := range serving {
		if v <= 0 || !sameInventoryModel(name, have) {
			continue
		}
		if found && v != n {
			return 0, false
		}
		n, found = v, true
	}
	return n, found
}

func applyNote(measured, serving int, known bool) string {
	if measured <= 0 {
		return ""
	}
	if known && serving > 0 && serving != measured {
		return fmt.Sprintf("measured ctx=%d; serving ctx=%d", measured, serving)
	}
	if known && serving == measured && measured != defaultRunCtx {
		return fmt.Sprintf("serving the measured ctx=%d", measured)
	}
	if !known && measured != defaultRunCtx {
		return fmt.Sprintf("measured ctx=%d; persist so the next load keeps it", measured)
	}
	return ""
}

// SameModel is the inventory name match, including :latest aliases.
func SameModel(a, b string) bool { return sameInventoryModel(a, b) }

func evidenceUsable(tag InstalledModel, ev InventoryEvidence, q InventoryQuery) bool {
	if ev.IntegrityIssue != "" || ev.ComparableIssue != "" || ev.Contaminated {
		return false
	}
	if q.CurrentKey == "" || ev.DeviceKey == "" || ev.DeviceKey != q.CurrentKey {
		return false
	}
	if len(performanceRelevantDiff(ev.Device, q.Current)) != 0 {
		return false
	}
	currentDigest, currentOK := normalizedArtifactDigest(tag.ArtifactDigest)
	savedDigest, savedOK := normalizedArtifactDigest(ev.ArtifactDigest)
	return currentOK && savedOK && currentDigest == savedDigest
}

// EvidenceReusable is the single fail-closed rule for attaching saved
// measurements to a currently served artifact. Presentation surfaces must not
// reimplement a looser model-name or legacy-device-key join.
func EvidenceReusable(tag InstalledModel, ev InventoryEvidence, q InventoryQuery) bool {
	return evidenceUsable(tag, ev, q)
}

func staleNote(tag InstalledModel, ev InventoryEvidence, q InventoryQuery) string {
	currentDigest, currentOK := normalizedArtifactDigest(tag.ArtifactDigest)
	savedDigest, savedOK := normalizedArtifactDigest(ev.ArtifactDigest)
	switch {
	case ev.IntegrityIssue != "":
		return ev.IntegrityIssue
	case ev.ComparableIssue != "":
		return ev.ComparableIssue
	case ev.Contaminated:
		return "prior run was contaminated by a resident model"
	case !savedOK:
		return "prior run has no verified runtime-bound model artifact digest"
	case !currentOK:
		return "runtime did not provide a verified model artifact digest"
	case savedDigest != currentDigest:
		return "model artifact changed since the last measurement"
	case ev.DeviceKey != q.CurrentKey:
		if fields := fingerprintChanges(ev.Device, q.Current); fields != "" {
			return fields + " changed since the last measurement"
		}
		return "device or runtime changed since the last measurement"
	default:
		return "saved evidence cannot be used"
	}
}

func matchEvidence(tag InstalledModel, evidence []InventoryEvidence, q InventoryQuery) *InventoryEvidence {
	// A reusable receipt always wins. Selecting a stale candidate first and
	// rejecting it later can hide a valid run that appears later in history.
	for i := range evidence {
		if sameInventoryModel(tag.Name, evidence[i].Model) && evidenceUsable(tag, evidence[i], q) {
			return &evidence[i]
		}
	}
	// Nothing is reusable. Pick the most relevant stale receipt only so the
	// row can explain why it must be remeasured; it must not feed projections.
	best, bestScore := -1, -1
	for i := range evidence {
		if !sameInventoryModel(tag.Name, evidence[i].Model) {
			continue
		}
		score := 0
		currentDigest, currentOK := normalizedArtifactDigest(tag.ArtifactDigest)
		savedDigest, savedOK := normalizedArtifactDigest(evidence[i].ArtifactDigest)
		if currentOK && savedOK && currentDigest == savedDigest {
			score += 4
		}
		if q.CurrentKey != "" && evidence[i].DeviceKey == q.CurrentKey {
			score += 2
		}
		if evidence[i].IntegrityIssue == "" && evidence[i].ComparableIssue == "" && !evidence[i].Contaminated {
			score++
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	if best < 0 {
		return nil
	}
	return &evidence[best]
}

var artifactDigestPattern = regexp.MustCompile(`(?i)^sha256:[0-9a-f]{64}$`)

func normalizedArtifactDigest(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) == 64 {
		value = "sha256:" + value
	}
	return value, artifactDigestPattern.MatchString(value)
}

func fingerprintChanges(saved, current device.Fingerprint) string {
	if saved.Key() == "||||||" || current.Key() == "||||||" {
		return ""
	}
	diffs := performanceRelevantDiff(saved, current)
	if len(diffs) == 0 {
		return ""
	}
	fields := make([]string, 0, len(diffs))
	for _, diff := range diffs {
		name := strings.ReplaceAll(diff[0], "_", " ")
		name = strings.TrimPrefix(name, "config.")
		fields = append(fields, name)
	}
	return strings.Join(fields, ", ")
}

func performanceRelevantDiff(saved, current device.Fingerprint) [][3]string {
	diffs := saved.Diff(current)
	out := diffs[:0]
	for _, diff := range diffs {
		if diff[0] == "inference_device" && equivalentPlacement(diff[1], diff[2]) {
			continue
		}
		out = append(out, diff)
	}
	return out
}

func equivalentPlacement(a, b string) bool {
	classA, classB := placementClass(a), placementClass(b)
	return classA != "" && classA == classB
}

func placementClass(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	if match := gpuPercentPattern.FindStringSubmatch(value); len(match) == 2 {
		percent, err := strconv.ParseFloat(match[1], 64)
		if err == nil && percent < 99.95 {
			return "mixed"
		}
	}
	hasCPU := strings.Contains(value, "cpu")
	hasAccelerator := strings.Contains(value, "gpu") || strings.Contains(value, "cuda") ||
		strings.Contains(value, "metal") || strings.Contains(value, "rocm") || strings.Contains(value, "vulkan")
	switch {
	case hasCPU && hasAccelerator:
		return "mixed"
	case hasCPU:
		return "cpu"
	case hasAccelerator:
		return "accelerator"
	default:
		return ""
	}
}

var gpuPercentPattern = regexp.MustCompile(`gpu[^0-9]{0,8}([0-9]+(?:\.[0-9]+)?)%`)

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
	case "nvidia-smi", "drm sysfs", device.NVIDIAUnifiedMemorySource, device.NVIDIAUnifiedProbeSource,
		// Apple Silicon. The explicit kernel setting is a measurement. The
		// derived share is an assumption, and it is trusted anyway because it
		// replaced a worse one: reporting installed RAM as GPU-available memory
		// overstated a 128 GB machine's budget by tens of gigabytes. It errs
		// low, so it declines to certify rather than certifying something that
		// cannot load, and the source string carries the word "assumed"
		// everywhere the budget is printed.
		"iogpu.wired_limit_mb", "unified memory (assumed GPU share of system RAM)",
		// Retained so results saved before the budget was corrected still read
		// as measured rather than silently becoming unproven.
		"unified memory (system RAM)":
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
