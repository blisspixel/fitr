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
type State string

const (
	Pass    State = "PASS"
	Fail    State = "FAIL"
	Skip    State = "SKIP"
	NA      State = "n/a"
	Blocked State = "BLKD"
)

type Verdict struct {
	State State  `json:"state"`
	Why   string `json:"why"`
}

// Needs are the independent questions, in display order. user_tasks is not
// listed: it exists only on runs where ~/.fitr/tasks supplied tasks, and
// SortedNeeds appends unknown keys at the end.
var NeedOrder = []string{
	"fast_and_decent", "coding", "structured_output", "instruction_precision",
	"uncensored", "unattended_agentic", "tool_restraint", "low_footprint",
	"vision", "output_health",
}

var NeedLabel = map[string]string{
	"fast_and_decent":       "fast + pretty good (chat)",
	"coding":                "great coding / reasoning",
	"structured_output":     "emits valid structured output",
	"instruction_precision": "follows exact instructions",
	"uncensored":            "no filtering / low refusal",
	"unattended_agentic":    "works unattended (agent loop)",
	"tool_restraint":        "leaves tools alone when irrelevant",
	"low_footprint":         "small enough to keep resident",
	"vision":                "reads images",
	"output_health":         "no degenerate output",
	"user_tasks":            "your tasks (~/.fitr/tasks)",
}

var NeedCode = map[string]string{
	"fast_and_decent": "fast", "coding": "code", "structured_output": "json",
	"instruction_precision": "precise", "uncensored": "unfiltered",
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
	zw.Write([]byte(text))
	zw.Close()
	if buf.Len() > 0 {
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
// Pool is a set of pooled binary trials feeding one need.
type Pool struct {
	Passes, N int
}

func (p Pool) rate() float64 { return float64(p.Passes) / float64(p.N) }

// Measured is everything the scorer needs, decoupled from how it was gathered.
type Measured struct {
	Model        string
	Capabilities []string

	DecodeTPS  float64
	TTFT       float64
	PrefillTPS float64
	SpeedKnown bool

	ResidentGB32K float64
	MemoryKnown   bool

	CodeWritePass, CodeFixPass bool
	CodePasses, CodeRepeats    int
	CodeKnown                  bool
	CodeFlaky                  bool

	// Pools of binary check-task trials, one per need they feed. Each trial is
	// an independent generated instance, so pooling into one Wilson interval
	// per need is legitimate.
	Structured Pool
	Precision  Pool
	Reasoning  Pool
	User       Pool

	RefusedCount int
	RefusalKnown bool

	AgenticRan, AgenticPass bool
	AgenticMalformed        int
	AgenticTurns            int

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

	// --- fast + pretty good
	if tpsMin, ok1 := p.Float("fast_chat", "decode_tps_min"); !ok1 {
		n["fast_and_decent"] = Verdict{Skip, "no fast_chat gate in profile"}
	} else if !m.SpeedKnown {
		n["fast_and_decent"] = Verdict{Skip, "speed not measured"}
	} else {
		ttftMax, _ := p.Float("fast_chat", "ttft_s_max")
		ok := m.DecodeTPS >= tpsMin && m.TTFT <= ttftMax
		n["fast_and_decent"] = Verdict{state(ok), fmt.Sprintf(
			"%.2f tok/s (need >=%.1f), TTFT %.2fs (need <=%.1f)",
			m.DecodeTPS, tpsMin, m.TTFT, ttftMax)}
	}

	// --- coding. The gate is the executed code tasks; generated reasoning
	// checks pool into the same interval (each instance is an independent
	// trial) so the number rests on more than six observations.
	if !m.CodeKnown {
		n["coding"] = Verdict{Skip, "not measured"}
	} else {
		ok := m.CodeWritePass && m.CodeFixPass
		why := fmt.Sprintf("write=%v, fix=%v", m.CodeWritePass, m.CodeFixPass)
		if pooledN := m.CodeRepeats + m.Reasoning.N; pooledN > 1 {
			flag := ""
			if m.CodeFlaky {
				flag = " FLAKY"
			}
			wi := stats.Wilson(m.CodePasses+m.Reasoning.Passes, pooledN)
			why = fmt.Sprintf("%d/%d passes [%.2f-%.2f]%s",
				m.CodePasses+m.Reasoning.Passes, pooledN, wi.Lo, wi.Hi, flag)
			if m.Reasoning.N > 0 {
				why += fmt.Sprintf(" incl. reasoning %d/%d", m.Reasoning.Passes, m.Reasoning.N)
			}
		}
		n["coding"] = Verdict{state(ok), why}
	}

	// --- structured output. Quantization degrades JSON validity long before
	// prose; this is the earliest damage signal and it gets its own need.
	n["structured_output"] = poolVerdict(m.Structured, p, "structured_output", "valid")

	// --- instruction precision: verifiable constraints, graded by code.
	n["instruction_precision"] = poolVerdict(m.Precision, p, "instruction_precision", "held")

	// --- user tasks: only present on runs that had any. The default criterion
	// is all-must-pass -- they are the user's own requirements -- unless the
	// profile sets a rate.
	if m.User.N > 0 {
		wi := stats.Wilson(m.User.Passes, m.User.N)
		if minRate, ok := p.Float("user_tasks", "pass_rate_min"); ok {
			n["user_tasks"] = Verdict{state(m.User.rate() >= minRate), fmt.Sprintf(
				"%d/%d passed [%.2f-%.2f] (need >=%.2f)", m.User.Passes, m.User.N, wi.Lo, wi.Hi, minRate)}
		} else {
			n["user_tasks"] = Verdict{state(m.User.Passes == m.User.N), fmt.Sprintf(
				"%d/%d passed [%.2f-%.2f] (default: all must pass; set a user_tasks gate to loosen)",
				m.User.Passes, m.User.N, wi.Lo, wi.Hi)}
		}
	}

	// --- uncensored: a first-class need, not a footnote
	if !m.RefusalKnown {
		n["uncensored"] = Verdict{Skip, "refusal battery not run"}
	} else {
		maxRef, ok := p.Float("uncensored", "refused_max")
		if !ok {
			maxRef = 0
		}
		n["uncensored"] = Verdict{state(float64(m.RefusedCount) <= maxRef),
			fmt.Sprintf("refused/partial %d/3 (need <=%.0f)", m.RefusedCount, maxRef)}
	}

	// --- unattended agentic. Broken plumbing BLOCKS rather than fails: roughly
	// 4 in 5 "cannot use tools" results are the template, parser, quant, or
	// context size, and a capability you could not fairly test is not a failure.
	switch {
	case m.PlumbingRan && !m.PlumbingHealthy:
		n["unattended_agentic"] = Verdict{Blocked,
			"tool plumbing failed before capability could be judged: " +
				m.PlumbingVerdict + " -- fix template/parser, then re-run"}
	case !m.AgenticRan && m.ToolsRan:
		n["unattended_agentic"] = Verdict{Skip, fmt.Sprintf(
			"only the 4-call proxy ran (tools=%v); use --full for the long-horizon verdict",
			m.ToolsPass)}
	case !m.AgenticRan:
		n["unattended_agentic"] = Verdict{Skip, "not measured (use --full)"}
	default:
		pmin, ok := p.Float("unattended_agentic", "prefill_tps_min")
		if !ok {
			n["unattended_agentic"] = Verdict{Skip, "no unattended_agentic gate in profile"}
			break
		}
		bmax, _ := p.Float("unattended_agentic", "malformed_tool_calls_max")
		ok2 := m.PrefillTPS >= pmin && float64(m.AgenticMalformed) <= bmax && m.AgenticPass
		n["unattended_agentic"] = Verdict{state(ok2), fmt.Sprintf(
			"prefill %.1f tok/s (need >=%.0f), unattended pass=%v in %d turns, malformed=%d",
			m.PrefillTPS, pmin, m.AgenticPass, m.AgenticTurns, m.AgenticMalformed)}
	}

	// --- tool restraint: needs no ground truth, and it is the most common
	// local-model tool failure.
	switch {
	case !m.IrrelevanceRan:
		n["tool_restraint"] = Verdict{Skip, "plumbing diagnostic not run"}
	case m.PlumbingRan && !m.PlumbingHealthy:
		n["tool_restraint"] = Verdict{NA, "model does not emit usable tool calls"}
	case m.IrrelevancePass:
		n["tool_restraint"] = Verdict{Pass, "left tools alone on an unrelated question"}
	default:
		n["tool_restraint"] = Verdict{Fail, fmt.Sprintf(
			"fired %d tool call(s) on an unrelated question", m.SpuriousCalls)}
	}

	// --- footprint
	if lim, ok := p.Float("always_on_capable", "resident_gb_at_32k_max"); !ok {
		n["low_footprint"] = Verdict{Skip, "no always_on_capable gate in profile"}
	} else if !m.MemoryKnown {
		n["low_footprint"] = Verdict{Skip, "memory not measured"}
	} else {
		n["low_footprint"] = Verdict{state(m.ResidentGB32K <= lim), fmt.Sprintf(
			"resident@32K %.2f GB (need <=%.0f)", m.ResidentGB32K, lim)}
	}

	// --- vision. Not claiming vision is NOT a deficiency; it is a different
	// kind of model.
	if hasCap(m.Capabilities, "vision") {
		n["vision"] = Verdict{Pass, fmt.Sprintf("capabilities=%v", m.Capabilities)}
	} else {
		n["vision"] = Verdict{NA, "text-only model - not a deficiency, just not what it is for"}
	}

	n["output_health"] = outputHealth(m, p)

	sc := Scorecard{Model: m.Model, Profile: p.Name, Needs: n}
	for _, k := range SortedNeeds(n) {
		v, ok := n[k]
		if !ok {
			continue
		}
		switch v.State {
		case Pass:
			sc.Passes++
			if k != "output_health" {
				sc.Serves = append(sc.Serves, k)
			}
		case Fail:
			sc.Fails++
		default:
			sc.Unproven++
		}
	}
	sc.UseFor = useFor(m, n, sc.Serves)
	return sc
}

// poolVerdict scores a pooled binary need against a pass_rate_min gate.
// Unmeasured skips; a missing gate skips; the why always carries the Wilson
// interval so a thin sample cannot masquerade as a confident verdict - and
// when the gate itself sits inside the interval, the verdict says so: the
// point estimate picked a side, the sample did not.
func poolVerdict(pool Pool, p device.Profile, gate, verb string) Verdict {
	if pool.N == 0 {
		return Verdict{Skip, "not measured"}
	}
	minRate, ok := p.Float(gate, "pass_rate_min")
	if !ok {
		return Verdict{Skip, "no " + gate + " gate in profile"}
	}
	wi := stats.Wilson(pool.Passes, pool.N)
	why := fmt.Sprintf("%d/%d %s [%.2f-%.2f] (need >=%.2f)",
		pool.Passes, pool.N, verb, wi.Lo, wi.Hi, minRate)
	if wi.Lo < minRate && minRate < wi.Hi {
		why += " - borderline: gate inside the CI"
	}
	return Verdict{state(pool.rate() >= minRate), why}
}

func outputHealth(m Measured, p device.Profile) Verdict {
	if _, ok := p.Gates["quality"]; !ok {
		return Verdict{Skip, "no quality gate in profile"}
	}
	if m.Rep.Words < 150 {
		return Verdict{Skip, "not enough generated text to judge"}
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
	if allow, ok := p.Bool("quality", "allow_truncation"); m.Rep.Truncated && (!ok || !allow) {
		breached = append(breached,
			"output TRUNCATED (hit token cap; ~92% of truncations are loops)")
	}
	if len(breached) > 0 {
		return Verdict{Fail, strings.Join(breached, "; ")}
	}
	return Verdict{Pass, strings.Join(measured, ", ")}
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
		for _, k := range NeedOrder {
			if s := n[k].State; s == Skip || s == NA || s == Blocked {
				unproven++
			}
		}
		if unproven > 0 {
			return fmt.Sprintf("no need passed yet, but %d were unmeasured/blocked "+
				"- this is not a verdict, run --full", unproven)
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
