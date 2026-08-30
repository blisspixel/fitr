// Package score turns raw measurements into a device-specific verdict.
//
// Three ideas:
//
//  1. SCORE AGAINST NEEDS, NOT ONE ROLE. "Is it good" is unanswerable. "Do I
//     need great coding", "must it never refuse", "must it run unattended" are
//     separate, legitimate needs, and a model can serve one brilliantly and
//     another not at all. A single score cannot say that.
//
//  2. GATES ARE DEVICE-SPECIFIC and live in profiles, not code.
//
//  3. DEGENERACY IS MEASURED. A local model once produced a 104 KB report that
//     passed every structural check while 25% of its paragraphs were exact
//     duplicates -- it had looped one table 11 times. Length correlated
//     NEGATIVELY with quality.
package score

import (
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/stats"
)

// State is a verdict. SKIP means "not measured" and must NEVER read as failure;
// NA means the model never claimed the capability, which is not a deficiency.
// INCONCLUSIVE means a measurement exists but an integrity problem excludes it
// from PASS and FAIL claims.
type State string

const (
	Pass         State = "PASS"
	Fail         State = "FAIL"
	Skip         State = "SKIP"
	NA           State = "n/a"
	Blocked      State = "BLKD"
	Inconclusive State = "INCONCLUSIVE"
)

// Verdict is a state plus the evidence for it, kept in parts.
//
// It used to be a state and one string, assembled here with "; " and ", "
// joins. That put layout in the scorer: the renderer received a sentence it
// could only print or truncate, and the longest one reached 224 characters
// against a 78-column rule. A renderer cannot lay out a string it did not
// structure, so the structure lives here and the composition is derived.
type Verdict struct {
	State State `json:"state"`
	// Why is the one-line composition of the parts below. It is the persisted
	// contract and what machine readers have always consumed, so it stays.
	// Derive it with newVerdict; do not assign it beside the parts.
	Why string `json:"why"`
	// Measure is the quantity the verdict rests on, short enough for a column.
	Measure string `json:"measure,omitempty"`
	// Gate is the threshold Measure was judged against.
	Gate string `json:"gate,omitempty"`
	// Detail is the breakdown behind Measure, one fragment per element.
	Detail []string `json:"detail,omitempty"`
	// Note is prose: caveats, exclusions, and the readings to head off.
	Note string `json:"note,omitempty"`
}

func newVerdict(s State, measure, gate string, detail []string, note string) Verdict {
	v := Verdict{State: s, Measure: measure, Gate: gate, Detail: detail, Note: note}
	v.Why = v.compose()
	return v
}

// skipped and blocked are verdicts whose whole content is prose: nothing was
// measured, so there is no quantity and no threshold to show.
func skipped(note string) Verdict { return newVerdict(Skip, "", "", nil, note) }
func blocked(note string) Verdict { return newVerdict(Blocked, "", "", nil, note) }

func (v Verdict) compose() string {
	var bits []string
	head := v.Measure
	switch {
	case head != "" && v.Gate != "":
		head += " (" + v.Gate + ")"
	case head == "":
		head = v.Gate
	}
	if head != "" {
		bits = append(bits, head)
	}
	if len(v.Detail) > 0 {
		bits = append(bits, strings.Join(v.Detail, ", "))
	}
	if v.Note != "" {
		bits = append(bits, v.Note)
	}
	return strings.Join(bits, "; ")
}

// withNote appends a caveat and recomposes. Used by the exclusion paths, which
// qualify a verdict that has already been built.
func (v Verdict) withNote(extra string) Verdict {
	switch {
	case extra == "":
	case v.Note == "":
		v.Note = extra
	default:
		v.Note += "; " + extra
	}
	v.Why = v.compose()
	return v
}

// Parts returns the display parts. Results saved before the parts existed carry
// only Why, so it falls back to treating that whole string as prose rather than
// rendering an empty row for a verdict that has an explanation.
func (v Verdict) Parts() (measure, gate string, detail []string, note string) {
	if v.Measure == "" && v.Gate == "" && len(v.Detail) == 0 && v.Note == "" {
		return "", "", nil, v.Why
	}
	return v.Measure, v.Gate, v.Detail, v.Note
}

// Needs are the independent questions, in display order. user_tasks is not
// listed: it exists only on runs where ~/.fitr/tasks supplied tasks, and
// SortedNeeds appends unknown keys at the end.
var NeedOrder = []string{
	"fast_and_decent", "coding", "structured_output", "instruction_precision",
	"uncensored", "tool_calling", "unattended_agentic", "tool_restraint",
	"low_footprint", "vision", "output_health",
}

// LabelWidth is the scorecard's label column. Labels are capped rather than
// truncated at render time because the cap is a writing constraint, not a
// display accident: "leaves tools alone when they don't apply" was 39 columns
// against a 34-column field, so it ran into the text beside it with no
// separator and the row read as one run-on phrase.
const LabelWidth = 26

// MeasureWidth is the scorecard's measure column, and therefore a writing
// constraint on every Measure this package composes. It lives here rather than
// only in the renderer because the scorer is what can overflow it: a verb one
// word too long silently truncated an interval, and the interval is the half of
// the number that decides whether a verdict means anything.
// 26 and not 24: "41/48 correct [0.73-0.93]" is 25, and a two-digit pool over a
// two-digit denominator is ordinary once a family runs at -k 10. The columns
// were rebalanced against the gate column rather than the verbs being
// abbreviated, because "correct" and "held" are what the numbers mean.
const MeasureWidth = 26

var NeedLabel = map[string]string{
	"fast_and_decent":       "fast + pretty good (chat)",
	"coding":                "great coding / reasoning",
	"structured_output":     "valid structured output",
	"instruction_precision": "follows exact instructions",
	"uncensored":            "no filtering / low refusal",
	"tool_calling":          "calls tools correctly",
	"unattended_agentic":    "works unattended (agent)",
	"tool_restraint":        "leaves unused tools alone",
	"low_footprint":         "keeps a small footprint",
	"vision":                "reads images",
	"output_health":         "no degenerate output",
	"user_tasks":            "your tasks (~/.fitr/tasks)",
}

var NeedCode = map[string]string{
	"fast_and_decent": "fast", "coding": "code", "structured_output": "json",
	"instruction_precision": "precise", "uncensored": "unfiltered",
	"tool_calling":       "tools",
	"unattended_agentic": "agentic", "tool_restraint": "restraint",
	"low_footprint": "small", "vision": "vision", "user_tasks": "user",
}

// ---------------------------------------------------------------- degeneracy
type Repetition struct {
	DupParagraphRatio float64 `json:"dup_paragraph_ratio"`
	DupSentenceRatio  float64 `json:"dup_sentence_ratio"`
	DupLineRatio      float64 `json:"dup_line_ratio"`
	Top2GramCharFrac  float64 `json:"top_2gram_char_frac"`
	Top4GramCharFrac  float64 `json:"top_4gram_char_frac"`
	GzipRatio         float64 `json:"gzip_compression_ratio"`
	RepetitionScore   float64 `json:"repetition_score"`
	Distinct4Gram     float64 `json:"distinct_4gram"`
	Words             int     `json:"words"`
	Truncated         bool    `json:"truncated"`
}

var (
	wordRe  = regexp.MustCompile(`[A-Za-z']+`)
	paraRe  = regexp.MustCompile(`\n\s*\n`)
	sentRe  = regexp.MustCompile(`(?m)([.!?])\s+`)
	spaceRe = regexp.MustCompile(`\s+`)
	numRe   = regexp.MustCompile(`\$?\d[\d,]*\.?\d*%?`)
)

// RepetitionMetrics computes five independent degeneration signals.
//
// Five and not one because a single metric has blind spots: on a short looping
// sample dup_paragraph_ratio reads 0.0 (its paragraph filter skips short
// blocks) while dup_line_ratio reads 0.91 and the gzip ratio 9.2.
func RepetitionMetrics(text string) Repetition {
	if len(text) < 200 {
		return Repetition{Distinct4Gram: 1}
	}
	r := Repetition{}

	var paras []string
	for _, p := range paraRe.Split(text, -1) {
		if p = strings.TrimSpace(p); len(p) > 120 {
			paras = append(paras, p)
		}
	}
	r.DupParagraphRatio = dupRatio(paras)

	var sents []string
	for _, s := range sentRe.Split(text, -1) {
		if s = strings.TrimSpace(s); len(s) > 60 {
			sents = append(sents, s)
		}
	}
	r.DupSentenceRatio = dupRatio(sents)

	var lines []string
	for l := range strings.SplitSeq(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	r.DupLineRatio = dupRatio(lines)

	words := wordRe.FindAllString(strings.ToLower(text), -1)
	r.Words = len(words)
	if len(words) > 3 {
		seen := map[string]bool{}
		total := 0
		for i := 0; i+4 <= len(words); i++ {
			seen[strings.Join(words[i:i+4], " ")] = true
			total++
		}
		if total > 0 {
			r.Distinct4Gram = round(float64(len(seen))/float64(total), 4)
			r.RepetitionScore = round(1-r.Distinct4Gram, 4)
		}
	}
	r.Top2GramCharFrac = topNGramCharFrac(words, 2, len(text))
	r.Top4GramCharFrac = topNGramCharFrac(words, 4, len(text))

	// gzip ratio. Human prose ~2.28, model output 2.36-2.72; >4 is plainly
	// degenerate. Length-sensitive, so a signal rather than a hard gate.
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, writeErr := zw.Write([]byte(text))
	closeErr := zw.Close()
	// Close flushes the final block, so an unchecked failure here yields a
	// short buffer, an inflated ratio, and a text wrongly called degenerate.
	// Writing to a bytes.Buffer cannot fail today; leaving the ratio unset is
	// still the right answer if that ever changes.
	if writeErr == nil && closeErr == nil && buf.Len() > 0 {
		r.GzipRatio = round(float64(len(text))/float64(buf.Len()), 3)
	}
	return r
}

func dupRatio(items []string) float64 {
	if len(items) == 0 {
		return 0
	}
	counts := map[[16]byte]int{}
	for _, it := range items {
		counts[md5.Sum([]byte(strings.ToLower(spaceRe.ReplaceAllString(it, " "))))]++
	}
	dups := 0
	for _, c := range counts {
		if c > 1 {
			dups += c - 1
		}
	}
	return round(float64(dups)/float64(len(items)), 4)
}

// topNGramCharFrac is the Gopher-family signal: the character share held by the
// single most frequent n-gram. Catches one phrase dominating even when the
// surrounding paragraphs differ.
func topNGramCharFrac(words []string, n, textLen int) float64 {
	if len(words) < n || textLen == 0 {
		return 0
	}
	counts := map[string]int{}
	for i := 0; i+n <= len(words); i++ {
		counts[strings.Join(words[i:i+n], " ")]++
	}
	best, bestCount := "", 0
	for g, c := range counts {
		if c > bestCount || (c == bestCount && g > best) {
			best, bestCount = g, c
		}
	}
	return round(float64(len(best)*bestCount)/float64(textLen), 4)
}

type Density struct {
	DistinctNumerics int     `json:"distinct_numerics"`
	PerThousandWords float64 `json:"numerics_per_1k_words"`
}

// InformationDensity is a padding detector: length rising while distinct facts
// stay flat is filler, which is exactly how a looping report reads.
func InformationDensity(text string) Density {
	words := len(wordRe.FindAllString(text, -1))
	seen := map[string]bool{}
	for _, m := range numRe.FindAllString(text, -1) {
		seen[m] = true
	}
	d := Density{DistinctNumerics: len(seen)}
	if words > 0 {
		d.PerThousandWords = round(1000*float64(len(seen))/float64(words), 2)
	}
	return d
}

// ---------------------------------------------------------------- inputs
// FamilyPool is one generated-check family's trials inside a need.
type FamilyPool struct {
	Family     string `json:"family"`
	Passes     int    `json:"passes"`
	N          int    `json:"n"`
	Planned    int    `json:"planned,omitempty"`
	Unscorable int    `json:"unscorable,omitempty"`
}

// Pool is a set of pooled binary trials feeding one need.
type Pool struct {
	Passes     int          `json:"passes"`
	N          int          `json:"n"`
	Planned    int          `json:"planned,omitempty"`
	Unscorable int          `json:"unscorable,omitempty"`
	Families   []FamilyPool `json:"families,omitempty"`
}

func (p Pool) rate() float64 { return float64(p.Passes) / float64(p.N) }

func (p *Pool) Add(family string, pass bool) {
	if p == nil {
		return
	}
	p.N++
	p.Planned++
	if pass {
		p.Passes++
	}
	family = strings.TrimSpace(family)
	if family == "" {
		family = "unspecified"
	}
	for i := range p.Families {
		if p.Families[i].Family == family {
			p.Families[i].N++
			p.Families[i].Planned++
			if pass {
				p.Families[i].Passes++
			}
			return
		}
	}
	fp := FamilyPool{Family: family, N: 1, Planned: 1}
	if pass {
		fp.Passes = 1
	}
	p.Families = append(p.Families, fp)
}

// AddUnscorable records a planned terminal observation that cannot enter a
// numerator or denominator. Keeping it in the pool prevents informative
// missingness from turning an easy observed subset into PASS.
func (p *Pool) AddUnscorable(family string) {
	if p == nil {
		return
	}
	p.Planned++
	p.Unscorable++
	family = strings.TrimSpace(family)
	if family == "" {
		family = "unspecified"
	}
	for i := range p.Families {
		if p.Families[i].Family == family {
			p.Families[i].Planned++
			p.Families[i].Unscorable++
			return
		}
	}
	p.Families = append(p.Families, FamilyPool{Family: family, Planned: 1, Unscorable: 1})
}

func (p Pool) clusters() []stats.Cluster {
	if len(p.Families) == 0 {
		if p.N == 0 {
			return nil
		}
		return []stats.Cluster{{Passes: p.Passes, N: p.N}}
	}
	out := make([]stats.Cluster, 0, len(p.Families))
	for _, f := range p.Families {
		out = append(out, stats.Cluster{Passes: f.Passes, N: f.N})
	}
	return out
}

// Measured is everything the scorer needs, decoupled from how it was gathered.
type Measured struct {
	Model        string
	Capabilities []string
	// Contamination names models that remained resident when the runtime was
	// asked to unload them. A non-empty list invalidates score claims from this
	// run even when the raw measurements look plausible.
	Contamination []string

	DecodeTPS  float64
	TTFT       float64 // loaded, prompt uncached - what the gate judges
	TTFTCold   float64 // first question of the day: load + prefill + first token
	TTFTWarm   float64 // same prompt again, prefix cache hit
	PrefillTPS float64
	SpeedKnown bool
	// TTFTCacheContaminated is true when the gated TTFT prompt was mostly
	// served from cache - the number would be a warm-prefix figure.
	TTFTCacheContaminated bool
	// TTFTCacheKnown distinguishes a verified cache miss from a backend that
	// cannot report cache state. Unknown cache state cannot establish the
	// loaded/uncached latency gate.
	TTFTCacheKnown bool
	// PrefillCacheKnown requires a valid cache receipt for every long-prompt
	// sample. PrefillCacheContaminated records any positive cache hit. Unknown
	// or contaminated timings stay visible but cannot prove an uncached-prefill
	// dependent need.
	PrefillCacheKnown        bool
	PrefillCacheContaminated bool
	// TimingsClientDerived is true when decode/TTFT came from wall-clock on
	// the client (OpenAI-compat). The number is still gated; the label is
	// the honesty (design rule 6).
	TimingsClientDerived bool

	ResidentGB32K float64
	MemoryKnown   bool

	CodeWritePass, CodeFixPass bool
	CodePasses, CodeRepeats    int
	CodePlanned                bool
	CodeKnown                  bool
	CodeFlaky                  bool

	// Pools of binary check-task trials, one per need they feed. Repeats of one
	// family are fresh generated instances, not independent evidence; families
	// are clusters.
	// Need-level intervals use Rao-Scott Wilson, not iid Wilson on the pool.
	Structured Pool
	Precision  Pool
	Reasoning  Pool
	User       Pool
	// ToolCalling and ToolRestraintPool run through the real tool channel:
	// tools are handed to the model and the assistant message is graded, not
	// the text. They are separate from Structured because they fail
	// separately -- a model can emit flawless JSON on demand and still never
	// put a call in the tool channel, which is the most-reported local failure
	// and the one a text-graded prompt cannot see.
	ToolCalling       Pool
	ToolRestraintPool Pool
	// ToolsUnsupported is set when the runtime itself refused the tool request
	// because this model declares no tool support. That is a capability fact
	// like vision, not a failure and not an unmeasured gap, and saying so is
	// more useful than "not measured" to anyone asking whether a model can
	// drive an agent.
	ToolsUnsupported bool

	RefusedCount int
	RefusalKnown bool

	AgenticPlanned          bool
	AgenticRan, AgenticPass bool
	AgenticMalformed        int
	AgenticTurns            int
	AgenticCtxCeiling       bool
	AgenticMaxPrompt        int
	AgenticCompacted        bool

	// Tool withdrawal: restraint under change. DeadCalls counts calls to a
	// tool after it vanished from the tools list; one grace call is tolerated
	// (discovering the removal), persisting past the error is not.
	WithdrawRan       bool
	WithdrawDeadCalls int
	WithdrawClean     bool

	ToolsRan  bool
	ToolsPass bool

	PlumbingRan     bool
	PlumbingHealthy bool
	PlumbingVerdict string
	IrrelevanceRan  bool
	IrrelevancePass bool
	SpuriousCalls   int

	Rep Repetition
}

type Scorecard struct {
	Model    string             `json:"model"`
	Profile  string             `json:"profile"`
	Needs    map[string]Verdict `json:"needs"`
	Serves   []string           `json:"serves"`
	UseFor   string             `json:"use_it_for"`
	Passes   int                `json:"passes"`
	Fails    int                `json:"fails"`
	Unproven int                `json:"unproven"`
}

func Score(m Measured, p device.Profile) Scorecard {
	n := map[string]Verdict{}
	n["fast_and_decent"] = scoreFastAndDecent(m, p)

	n["coding"] = scoreCoding(m)

	// --- structured output. Quantization degrades JSON validity long before
	// prose; this is the earliest damage signal and it gets its own need.
	n["structured_output"] = poolVerdict(m.Structured, p, "structured_output", "valid")

	// --- instruction precision: verifiable constraints, graded by code.
	n["instruction_precision"] = poolVerdict(m.Precision, p, "instruction_precision", "held")

	n["tool_calling"] = scoreToolCalling(m, p)

	// --- user tasks: only present on runs that had any. The default criterion
	// is all-must-pass -- they are the user's own requirements -- unless the
	// profile sets a rate.
	if v, measured := scoreUserTasks(m, p); measured {
		n["user_tasks"] = v
	}

	n["uncensored"] = scoreUncensored(m, p)

	// --- unattended agentic. Broken plumbing BLOCKS rather than fails: most
	// "cannot use tools" results are the template, parser, quant, or context
	// size, and a capability you could not fairly test is not a failure.
	//
	// "Most" rather than a ratio on purpose. This carried "roughly 4 in 5" for
	// a long time and that figure had no source; the mechanisms are real and
	// nameable -- lazy-grammar trigger strings, a missing PARSER directive on a
	// side-loaded GGUF, a newline baked into a Jinja terminator -- and several
	// reproduce byte-identically at temperature 0. The claim did not need a
	// number to be true, and the number was not one we could stand behind.
	n["unattended_agentic"] = scoreUnattendedAgentic(m, p)

	// --- tool restraint: needs no ground truth, and it is the most common
	// local-model tool failure. Two halves: restraint at REST (no calls on an
	// irrelevant question) and restraint under CHANGE (a tool vanishes
	// mid-loop; one grace call to discover that, then stop).
	n["tool_restraint"] = scoreToolRestraint(m, p)

	n["low_footprint"] = scoreFootprint(m, p)

	// --- vision. Not claiming vision is NOT a deficiency; it is a different
	// kind of model.
	n["vision"] = scoreVision(m)

	n["output_health"] = outputHealth(m, p)
	sc := summarizeNeeds(m, p, n)
	sc.UseFor = useFor(m, n, sc.Serves)
	return ExcludeContamination(sc, m.Contamination)
}

func scoreFastAndDecent(m Measured, p device.Profile) Verdict {
	tpsMin, configured := p.Float("fast_chat", "decode_tps_min")
	if !configured {
		return skipped("no fast_chat gate in profile")
	}
	if !m.SpeedKnown {
		return skipped("speed not measured")
	}
	ttftMax, _ := p.Float("fast_chat", "ttft_s_max")
	// A cache-hit TTFT is not a new-question measurement. Judging it
	// would let a warm-prefix figure wear a cold-prompt badge (usually
	// a false PASS). Exclude it from the gate and say so.
	decodeOK := m.DecodeTPS >= tpsMin
	verdictState := state(decodeOK && m.TTFT <= ttftMax)
	detail := []string{fmt.Sprintf("TTFT %.2fs loaded/cache state unknown (need <=%.1f)", m.TTFT, ttftMax)}
	note := ""
	switch {
	case !m.TTFTCacheKnown:
		note = "backend did not provide a cache receipt; loaded/uncached TTFT is unproven"
		if decodeOK {
			verdictState = Inconclusive
		}
	case m.TTFTCacheContaminated:
		detail[0] = fmt.Sprintf("TTFT %.2fs loaded/partial cache hit (need <=%.1f)", m.TTFT, ttftMax)
		note = "gated TTFT included cached prompt tokens - not an explicit miss, excluded from the gate"
		if decodeOK {
			verdictState = Inconclusive
		}
	default:
		detail[0] = fmt.Sprintf("TTFT %.2fs loaded/uncached (need <=%.1f)", m.TTFT, ttftMax)
	}
	if m.TTFTCold > 0 {
		detail = append(detail, fmt.Sprintf("cold start %.1fs", m.TTFTCold))
	}
	if m.TTFTWarm > 0 {
		detail = append(detail, fmt.Sprintf("cached prefix %.2fs", m.TTFTWarm))
	}
	if m.TimingsClientDerived {
		note = joinNote(note, "client-derived wall-clock (not server timings)")
	}
	return newVerdict(verdictState, fmt.Sprintf("%.2f tok/s", m.DecodeTPS),
		fmt.Sprintf("need >=%.1f", tpsMin), detail, note)
}

// scoreCoding keeps executable evidence separate from generated reasoning.
// The latter has clustered families and cannot manufacture precision for the
// executable write/fix contract.
func scoreCoding(m Measured) Verdict {
	if !m.CodeKnown {
		v := skipped("executable coding not measured")
		if m.CodePlanned {
			v = skipped("scheduled coding task was skipped; isolated execution is unavailable")
		}
		if m.Reasoning.N == 0 {
			return v
		}
		wi := stats.ClusteredWilson(m.Reasoning.clusters())
		note := fmt.Sprintf("reasoning checks %d/%d [%.2f-%.2f] observed, not a coding verdict",
			m.Reasoning.Passes, m.Reasoning.N, wi.Lo, wi.Hi)
		if m.CodePlanned {
			note = joinNote("scheduled coding task was skipped because isolated execution is unavailable", note)
		}
		return newVerdict(Skip, "not measured", "", nil, note)
	}

	measure := "write+fix"
	if m.CodeRepeats > 0 {
		measure = fmt.Sprintf("%d/%d executable", m.CodePasses, m.CodeRepeats)
	}
	detail := []string{fmt.Sprintf("write=%v, fix=%v", m.CodeWritePass, m.CodeFixPass)}
	if m.Reasoning.N > 0 {
		wi := stats.ClusteredWilson(m.Reasoning.clusters())
		detail = append(detail, fmt.Sprintf("reasoning %d/%d [%.2f-%.2f] observed separately",
			m.Reasoning.Passes, m.Reasoning.N, wi.Lo, wi.Hi))
	}
	note := ""
	if m.CodeFlaky {
		note = "FLAKY - the same task passed on some repeats and failed on others"
	}
	return newVerdict(state(m.CodeWritePass && m.CodeFixPass), measure, "", detail, note)
}

func scoreToolCalling(m Measured, p device.Profile) Verdict {
	switch {
	case m.ToolsUnsupported:
		return newVerdict(NA, "no tool support", "", nil,
			"the runtime reports this model does not accept tools, so there is no call to judge. "+
				"An agent harness cannot drive it")
	case m.PlumbingRan && !m.PlumbingHealthy:
		return blocked("tool plumbing did not establish a usable protocol; call fidelity was not judged")
	default:
		return poolVerdict(m.ToolCalling, p, "tool_calling", "correct")
	}
}

func scoreUserTasks(m Measured, p device.Profile) (Verdict, bool) {
	if m.User.N == 0 && m.User.Planned == 0 {
		return Verdict{}, false
	}
	if _, configuredRate := p.Float("user_tasks", "pass_rate_min"); configuredRate {
		return poolVerdict(m.User, p, "user_tasks", "passed"), true
	}
	if m.User.N == 0 {
		return newVerdict(Inconclusive, fmt.Sprintf("0/%d scorable", m.User.Planned),
			"all must pass", familyBreakdown(m.User.Families),
			"planned user-task evidence was unavailable; no outcome enters a denominator"), true
	}
	wi := stats.ClusteredWilson(m.User.clusters())
	measure := fmt.Sprintf("%d/%d passed [%.2f-%.2f]", m.User.Passes, m.User.N, wi.Lo, wi.Hi)
	v := newVerdict(state(m.User.Passes == m.User.N), measure,
		"all must pass", familyBreakdown(m.User.Families),
		"default criterion: these are your own requirements; set a user_tasks gate to loosen")
	if m.User.Planned > m.User.N {
		v.State = Inconclusive
		v.Note = joinNote(v.Note, fmt.Sprintf(
			"%d of %d planned user-task observations were not scorable", m.User.Planned-m.User.N, m.User.Planned))
		v.Why = v.compose()
	}
	return v, true
}

func scoreUncensored(m Measured, p device.Profile) Verdict {
	if !m.RefusalKnown {
		return skipped("refusal battery not run")
	}
	maxRef, ok := p.Float("uncensored", "refused_max")
	if !ok {
		maxRef = 0
	}
	return newVerdict(state(float64(m.RefusedCount) <= maxRef),
		fmt.Sprintf("refused/partial %d/3", m.RefusedCount),
		fmt.Sprintf("need <=%.0f", maxRef), nil, "")
}

func scoreUnattendedAgentic(m Measured, p device.Profile) Verdict {
	switch {
	case m.PlumbingRan && !m.PlumbingHealthy:
		return blocked("tool plumbing failed before capability could be judged: " +
			m.PlumbingVerdict + " -- fix template/parser, then re-run")
	case !m.AgenticRan && m.AgenticPlanned:
		return skipped("scheduled long-horizon task was skipped; isolated execution is unavailable")
	case !m.AgenticRan && m.ToolsRan:
		return skipped(fmt.Sprintf(
			"only the 4-call proxy ran (tools=%v); use --full for the long-horizon verdict", m.ToolsPass))
	case !m.AgenticRan:
		return skipped("not measured (use --full)")
	}

	pmin, ok := p.Float("unattended_agentic", "prefill_tps_min")
	if !ok {
		return skipped("no unattended_agentic gate in profile")
	}
	bmax, _ := p.Float("unattended_agentic", "malformed_tool_calls_max")
	behaviorOK := float64(m.AgenticMalformed) <= bmax && m.AgenticPass
	detail := []string{
		fmt.Sprintf("unattended pass=%v in %d turns", m.AgenticPass, m.AgenticTurns),
		fmt.Sprintf("malformed=%d", m.AgenticMalformed),
	}
	note := ""
	if m.AgenticCtxCeiling && !m.AgenticCompacted {
		behaviorOK = false
		note = fmt.Sprintf("transcript peaked at %d tokens and never shrank - filled the window with no compaction",
			m.AgenticMaxPrompt)
	} else if m.AgenticCtxCeiling {
		note = fmt.Sprintf("transcript peaked at %d tokens then compacted", m.AgenticMaxPrompt)
	}
	verdictState := state(behaviorOK && m.PrefillTPS >= pmin)
	if (!m.PrefillCacheKnown || m.PrefillCacheContaminated) && behaviorOK {
		verdictState = Inconclusive
		if !m.PrefillCacheKnown {
			note = joinNote(note, "prefill cache state was not proven; the uncached prompt-processing gate is unproven")
		} else {
			note = joinNote(note, "prefill probe observed cached tokens; the uncached prompt-processing gate is unproven")
		}
	}
	return newVerdict(verdictState, fmt.Sprintf("prefill %.1f tok/s", m.PrefillTPS),
		fmt.Sprintf("need >=%.0f", pmin), detail, note)
}

func scoreToolRestraint(m Measured, p device.Profile) Verdict {
	switch {
	case m.PlumbingRan && !m.PlumbingHealthy:
		return blocked("tool plumbing did not establish a usable protocol; restraint was not judged")
	case !m.IrrelevanceRan && !m.WithdrawRan && m.ToolRestraintPool.N == 0:
		return skipped("plumbing diagnostic not run")
	case m.ToolRestraintPool.N > 0:
		return scorePooledToolRestraint(m, p)
	default:
		return scoreLegacyToolRestraint(m)
	}
}

func scorePooledToolRestraint(m Measured, p device.Profile) Verdict {
	// The pooled family is the measurement once it exists. Restraint under
	// change remains a separate binary so a good at-rest rate cannot hide calls
	// to a withdrawn tool.
	v := poolVerdict(m.ToolRestraintPool, p, "tool_restraint", "clean")
	if !m.WithdrawRan {
		return v
	}
	ok, detail := withdrawalAssessment(m)
	if !ok {
		v.State = Fail
	}
	v.Detail = append(v.Detail, detail)
	v.Why = v.compose()
	return v
}

func scoreLegacyToolRestraint(m Measured) Verdict {
	checks, clean := 0, 0
	var detail []string
	if m.IrrelevanceRan {
		checks++
		if m.IrrelevancePass {
			clean++
			detail = append(detail, "left tools alone on an unrelated question")
		} else {
			detail = append(detail, fmt.Sprintf("fired %d tool call(s) on an unrelated question", m.SpuriousCalls))
		}
	}
	if m.WithdrawRan {
		checks++
		ok, assessment := withdrawalAssessment(m)
		if ok {
			clean++
		}
		detail = append(detail, assessment)
	}
	return newVerdict(state(clean == checks), fmt.Sprintf("%d/%d clean", clean, checks),
		fmt.Sprintf("need %d/%d", checks, checks), detail, "")
}

func withdrawalAssessment(m Measured) (bool, string) {
	withdrawOK := m.WithdrawDeadCalls <= 1 && m.WithdrawClean
	switch {
	case withdrawOK && m.WithdrawDeadCalls == 0:
		return true, "never called a withdrawn tool"
	case withdrawOK:
		return true, "one grace call to a withdrawn tool, then stopped cleanly"
	case m.WithdrawDeadCalls > 1:
		return false, fmt.Sprintf("kept calling a withdrawn tool (%d dead calls)", m.WithdrawDeadCalls)
	default:
		return false, "did not stop cleanly after a tool was withdrawn"
	}
}

func scoreFootprint(m Measured, p device.Profile) Verdict {
	lim, ok := p.Float("always_on_capable", "resident_gb_at_32k_max")
	if !ok {
		return skipped("no always_on_capable gate in profile")
	}
	if !m.MemoryKnown {
		return skipped("memory not measured")
	}
	return newVerdict(state(m.ResidentGB32K <= lim),
		fmt.Sprintf("resident@32K %.2f GB", m.ResidentGB32K),
		fmt.Sprintf("need <=%.0f", lim), nil, "")
}

func scoreVision(m Measured) Verdict {
	if hasCap(m.Capabilities, "vision") {
		return newVerdict(Pass, "declared", "", nil,
			fmt.Sprintf("capabilities=%v", m.Capabilities))
	}
	return newVerdict(NA, "text-only", "", nil,
		"not a deficiency, just not what this model is for")
}

func summarizeNeeds(m Measured, p device.Profile, needs map[string]Verdict) Scorecard {
	sc := Scorecard{Model: m.Model, Profile: p.Name, Needs: needs}
	for _, need := range SortedNeeds(needs) {
		verdict, ok := needs[need]
		if !ok {
			continue
		}
		switch verdict.State {
		case Pass:
			sc.Passes++
			if need != "output_health" {
				sc.Serves = append(sc.Serves, need)
			}
		case Fail:
			sc.Fails++
		default:
			sc.Unproven++
		}
	}
	return sc
}

// ExcludeEvidence converts every measured PASS or FAIL into an explicit
// INCONCLUSIVE observation without mutating the stored scorecard. Readers use
// it whenever the evidence contract does not support ranking claims.
func ExcludeEvidence(sc Scorecard, reason string) Scorecard {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "evidence contract is unavailable"
	}
	// Distinct from a thin sample on purpose. A thin sample is fixed by running
	// more trials; this is fixed by changing the environment and running again.
	// One word covers both states today, so the text has to carry the
	// difference: a reader who thinks contamination means "measure harder" will
	// measure the same contaminated thing harder.
	detail := reason + "; this run does not count - not a fail, and not fixed by more trials"

	needs := make(map[string]Verdict, len(sc.Needs))
	for need, verdict := range sc.Needs {
		if verdict.State != Skip && verdict.State != NA {
			verdict.State = Inconclusive
			verdict = verdict.withNote(detail)
		}
		needs[need] = verdict
	}
	sc.Needs = needs
	sc.Serves = nil
	sc.Passes = 0
	sc.Fails = 0
	sc.Unproven = len(needs)
	sc.UseFor = "INCONCLUSIVE - " + reason
	return sc
}

// ExcludeContamination excludes observations made while another model was
// resident. Contaminated results remain visible as history, but not claims.
func ExcludeContamination(sc Scorecard, models []string) Scorecard {
	if len(models) == 0 {
		return sc
	}
	unique := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model = strings.TrimSpace(model); model != "" {
			unique[model] = struct{}{}
		}
	}
	names := make([]string, 0, len(unique))
	for model := range unique {
		names = append(names, model)
	}
	sort.Strings(names)
	reason := "resident model contamination detected"
	if len(names) > 0 {
		reason += ": " + strings.Join(names, ", ")
	}
	sc = ExcludeEvidence(sc, reason)
	sc.UseFor = "INCONCLUSIVE - resident model contamination; unload all models and re-run"
	return sc
}

// poolVerdict scores a pooled binary need against a pass_rate_min gate.
// Unmeasured skips; a missing gate skips; the why always carries the interval
// so a thin sample cannot masquerade as a confident verdict. Distinct check
// families are clustered: iid Wilson overstates n and can hide a dead family.
// When the gate sits inside the interval, the verdict says so.
func poolVerdict(pool Pool, p device.Profile, gate, verb string) Verdict {
	planned := pool.Planned
	if planned == 0 {
		planned = pool.N
	}
	if planned == 0 {
		return skipped("not measured")
	}
	if pool.N == 0 {
		return newVerdict(Inconclusive, fmt.Sprintf("0/%d scorable", planned), "",
			familyBreakdown(pool.Families),
			"planned evidence was unavailable; no outcome enters a denominator")
	}
	minRate, ok := p.Float(gate, "pass_rate_min")
	if !ok {
		return skipped("no " + gate + " gate in profile")
	}
	wi := stats.ClusteredWilson(pool.clusters())
	note := ""
	verdictState := state(pool.rate() >= minRate)
	if gateInsideInterval(wi.Lo, wi.Hi, minRate) {
		verdictState = Inconclusive
		note = undecidedWhy(wi.Lo, wi.Hi, minRate)
	}
	if dead := establishedFamilyBelowGate(pool.Families, minRate); dead != "" && verdictState == Pass {
		verdictState = Inconclusive
		note = joinNote(note, "undecided: family "+dead+" is established below the bar, so the "+
			"pooled rate is hiding it. Not thin evidence: look at that family")
	}
	if planned > pool.N {
		verdictState = Inconclusive
		note = joinNote(note, fmt.Sprintf(
			"%d of %d planned observations were not scorable; missing evidence cannot establish this need",
			planned-pool.N, planned))
	}
	if countPlannedFamilies(pool.Families) < 2 && planned > 1 {
		verdictState = Inconclusive
		note = joinNote(note,
			"one check family establishes only within-family behavior, not the broader need")
	}
	return newVerdict(verdictState,
		fmt.Sprintf("%d/%d %s [%.2f-%.2f]", pool.Passes, pool.N, verb, wi.Lo, wi.Hi),
		fmt.Sprintf("need >=%.2f", minRate), familyBreakdown(pool.Families), note)
}

func familyBreakdown(families []FamilyPool) []string {
	if len(families) == 0 {
		return nil
	}
	names := append([]FamilyPool(nil), families...)
	sort.Slice(names, func(i, j int) bool { return names[i].Family < names[j].Family })
	parts := make([]string, 0, len(names))
	for _, f := range names {
		part := fmt.Sprintf("%s %d/%d", f.Family, f.Passes, f.N)
		if f.Unscorable > 0 {
			part += fmt.Sprintf(" (%d unavailable)", f.Unscorable)
		}
		parts = append(parts, part)
	}
	return parts
}

func countPlannedFamilies(families []FamilyPool) int {
	n := 0
	for _, family := range families {
		if family.Planned > 0 || family.N > 0 {
			n++
		}
	}
	return n
}

func joinNote(existing, extra string) string {
	switch {
	case extra == "":
		return existing
	case existing == "":
		return extra
	default:
		return existing + "; " + extra
	}
}

func establishedFamilyBelowGate(families []FamilyPool, gate float64) string {
	for _, f := range families {
		if f.N < 3 {
			continue
		}
		if stats.Wilson(f.Passes, f.N).Hi < gate {
			return f.Family
		}
	}
	return ""
}

func gateInsideInterval(lo, hi, gate float64) bool {
	return lo <= gate && gate <= hi
}

func outputHealth(m Measured, p device.Profile) Verdict {
	if _, ok := p.Gates["quality"]; !ok {
		return skipped("no quality gate in profile")
	}
	if m.Rep.Words < 150 {
		return skipped("not enough generated text to judge")
	}
	type chk struct {
		name string
		val  float64
		key  string
	}
	checks := []chk{
		{"dup_paragraphs", m.Rep.DupParagraphRatio, "dup_paragraph_ratio_max"},
		{"dup_lines", m.Rep.DupLineRatio, "dup_line_ratio_max"},
		{"top_4gram_share", m.Rep.Top4GramCharFrac, "top_4gram_char_frac_max"},
		{"gzip_ratio", m.Rep.GzipRatio, "gzip_compression_ratio_max"},
		{"repetition", m.Rep.RepetitionScore, "repetition_score_max"},
	}
	var breached, measured []string
	for _, c := range checks {
		lim, ok := p.Float("quality", c.key)
		if !ok {
			continue
		}
		measured = append(measured, fmt.Sprintf("%s %.3g", c.name, c.val))
		if c.val > lim {
			breached = append(breached, fmt.Sprintf("%s %.3g > %.3g", c.name, c.val, lim))
		}
	}
	// Truncation is a separate finding, not one of the value checks: counting it
	// in the same denominator would let a fully breached card report "-1/5".
	held := len(measured) - len(breached)
	if allow, ok := p.Bool("quality", "allow_truncation"); m.Rep.Truncated && (!ok || !allow) {
		breached = append(breached,
			"output TRUNCATED (hit token cap; ~92% of truncations are loops)")
	}
	measure := fmt.Sprintf("%d/%d checks", held, len(measured))
	if len(breached) > 0 {
		return newVerdict(Fail, measure, "all must hold", breached, "")
	}
	return newVerdict(Pass, measure, "all must hold", measured, "")
}

// useFor never prints a bare dismissal. If nothing passed it says how much went
// unmeasured, because that is the honest statement.
func useFor(m Measured, n map[string]Verdict, serves []string) string {
	if hasCap(m.Capabilities, "embedding") {
		return "embeddings / local search"
	}
	if n["output_health"].State == Fail {
		return "AVOID - produces degenerate/looping output"
	}
	has := map[string]bool{}
	for _, s := range serves {
		has[s] = true
	}
	var bits []string
	switch {
	case has["coding"] && has["unattended_agentic"]:
		bits = append(bits, "daily driver (coding + agents)")
	case has["coding"]:
		bits = append(bits, "coding")
	case has["unattended_agentic"]:
		bits = append(bits, "unattended agent")
	}
	if has["structured_output"] {
		bits = append(bits, "JSON/structured pipelines")
	}
	if has["uncensored"] {
		bits = append(bits, "no-filter writing/chat")
	}
	if has["fast_and_decent"] && len(bits) == 0 {
		bits = append(bits, "quick chat")
	}
	if has["vision"] {
		bits = append(bits, "vision one-shot")
	}
	if len(bits) == 0 {
		unproven := 0
		for _, verdict := range n {
			if s := verdict.State; s == Skip || s == NA || s == Blocked || s == Inconclusive {
				unproven++
			}
		}
		if unproven > 0 {
			return fmt.Sprintf("no need passed yet, but %d were unmeasured/blocked "+
				"- this is not a verdict; coding stays unproven until an isolated worker exists", unproven)
		}
		return "none of the measured needs on this device"
	}
	if has["low_footprint"] {
		bits = append(bits, "(small footprint)")
	}
	return strings.Join(bits, ", ")
}

func state(ok bool) State {
	if ok {
		return Pass
	}
	return Fail
}

func hasCap(caps []string, want string) bool {
	return slices.Contains(caps, want)
}

func round(v float64, places int) float64 {
	p := 1.0
	for range places {
		p *= 10
	}
	if v < 0 {
		return -float64(int64(-v*p+0.5)) / p
	}
	return float64(int64(v*p+0.5)) / p
}

// SortedNeeds returns needs in stable display order.
func SortedNeeds(n map[string]Verdict) []string {
	out := append([]string(nil), NeedOrder...)
	seen := map[string]bool{}
	for _, k := range out {
		seen[k] = true
	}
	var extra []string
	for k := range n {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

// undecidedWhy explains a rate the run could not place against its bar.
//
// Three things are deliberate. The subject is the RUN, not the model: readers
// absorb a qualitative low-confidence label as bad news about whatever it is
// attached to, so "this run fits both" says the sample is thin without saying
// the model is weak. The wrong reading is negated outright, because "no
// evidence of an effect" is read as "evidence of no effect" even by people
// trained to know better. And the phrase "95% confidence interval" is avoided:
// researchers asked six true/false questions about one endorse three and a
// half false statements on average, no better than untrained undergraduates.
func undecidedWhy(lo, hi, gate float64) string {
	return fmt.Sprintf("undecided: %.0f%% sits inside %.0f-%.0f%%, so this run fits both "+
		"clearing and missing the bar. Not a fail", 100*gate, 100*lo, 100*hi)
}
