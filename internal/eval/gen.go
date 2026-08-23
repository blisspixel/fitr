// Check-task generation: parameterized task families with computed ground truth.
//
// Why generated and not static: a static answer string in a public repo is
// future training data, and a memorized answer measures recall, not capability
// (GSM-Symbolic showed models crater when names and numbers vary). So a check
// task is a FAMILY - a template whose names, numbers, and dates are drawn from
// a seeded RNG at run time, with the correct answer COMPUTED by the harness.
// No canonical instantiation is stored anywhere. Every repeat is a genuinely
// independent trial, which is what the Wilson intervals want.
//
// Why these families: structured output breaks under quantization before prose
// does - JSON validity and tool-argument fidelity are the earliest damage
// signals - so the battery is deliberately weighted toward schema-shaped
// output, with verifiable-constraint instruction tasks and computed-answer
// reasoning tasks behind them.
package eval

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/strictjson"
)

// Instance is one generated occurrence of a check task. Canon is a known
// correct answer used by self-tests (grade(Canon) must pass); it is never sent
// to a model and never stored in a result.
type Instance struct {
	Prompt string
	Canon  string
	Grade  func(text string) (bool, string)
}

type genFunc func(params map[string]any, rng *rand.Rand) Instance

var families = map[string]genFunc{
	"json_object":  genJSONObject,
	"json_extract": genJSONExtract,
	"json_schema":  genJSONSchema,
	"csv_strict":   genCSV,
	"tool_args":    genToolArgs,
	"format":       genFormat,
	"line_rules":   genLineRules,
	"keywords":     genKeywords,
	"math_chain":   genMathChain,
	"date_math":    genDateMath,
	"state_track":  genStateTrack,
	"list_ops":     genListOps,
	"static":       genStatic,
}

// FamilyKnown reports whether a family name has a generator.
func FamilyKnown(name string) bool { _, ok := families[name]; return ok }

// ---------------------------------------------------------------- pools
var (
	poolNames    = []string{"Maya", "Tomas", "Priya", "Jonas", "Amara", "Felix", "Ingrid", "Marcus", "Leilani", "Dmitri"}
	poolCities   = []string{"Lisbon", "Osaka", "Tromso", "Cusco", "Windhoek", "Tallinn", "Galway", "Valparaiso"}
	poolProducts = []string{"ceramic mug", "brass hinge", "linen apron", "copper kettle", "oak stool", "wool blanket", "maple shelf", "walnut tray"}
	poolTerms    = []string{"kestrel", "basalt", "lantern", "estuary", "flywheel", "gimbal", "scree", "bowsprit"}
	poolTopics   = []string{"a harbor at dawn", "an old observatory", "a mountain rail line", "a tidal flat at low tide"}
	poolFields   = []string{"batch", "label", "codes", "active", "shard", "tier", "slots", "flags"}
	poolStatus   = []string{"active", "paused", "closed"}
)

func one[T any](rng *rand.Rand, pool []T) T { return pool[rng.IntN(len(pool))] }

func pick[T any](rng *rand.Rand, pool []T, k int) []T {
	idx := rng.Perm(len(pool))[:k]
	out := make([]T, k)
	for i, j := range idx {
		out[i] = pool[j]
	}
	return out
}

func randDate(rng *rand.Rand) time.Time {
	base := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	return base.AddDate(0, 0, rng.IntN(1100))
}

func money(cents int) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) }

// ---------------------------------------------------------------- grading helpers
// stripFence unwraps a reply that is exactly one fenced code block. A single
// fence is accepted because every real consumer strips it; JSON buried in
// prose is not, because no consumer can.
func stripFence(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return t
	}
	m := fenceRe.FindStringSubmatchIndex(t)
	if m == nil || m[0] != 0 || strings.TrimSpace(t[m[1]:]) != "" {
		return t
	}
	return strings.TrimSpace(t[m[2]:m[3]])
}

// strictJSON decodes exactly one JSON value with nothing but whitespace after
// it. "Mostly JSON" is a parse failure in every real pipeline, so it is one here.
func strictJSON(s string) (any, error) {
	if err := strictjson.Validate([]byte(s)); err != nil {
		return nil, err
	}
	r := strings.NewReader(s)
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	rest, _ := io.ReadAll(io.MultiReader(dec.Buffered(), r))
	if strings.TrimSpace(string(rest)) != "" {
		return nil, fmt.Errorf("content after the JSON value")
	}
	return v, nil
}

// jsonEqual compares parsed JSON against an expected Go value: exact keys (no
// extras), exact types, exact values. Floats compare to the cent (0.005),
// which is the tolerance money needs and identity for everything else used here.
func jsonEqual(got, want any) (bool, string) {
	switch w := want.(type) {
	case int:
		n, ok := got.(json.Number)
		if !ok {
			return false, fmt.Sprintf("want integer %d, got %T", w, got)
		}
		f, err := n.Float64()
		if err != nil || f != float64(w) {
			return false, fmt.Sprintf("want %d, got %s", w, n)
		}
	case float64:
		n, ok := got.(json.Number)
		if !ok {
			return false, fmt.Sprintf("want number %.2f, got %T", w, got)
		}
		f, err := n.Float64()
		if err != nil || f-w > 0.005 || w-f > 0.005 {
			return false, fmt.Sprintf("want %.2f, got %s", w, n)
		}
	case string:
		s, ok := got.(string)
		if !ok || s != w {
			return false, fmt.Sprintf("want %q, got %v", w, got)
		}
	case bool:
		b, ok := got.(bool)
		if !ok || b != w {
			return false, fmt.Sprintf("want %v, got %v", w, got)
		}
	case []any:
		a, ok := got.([]any)
		if !ok {
			return false, fmt.Sprintf("want array, got %T", got)
		}
		if len(a) != len(w) {
			return false, fmt.Sprintf("want %d elements, got %d", len(w), len(a))
		}
		for i := range w {
			if ok, why := jsonEqual(a[i], w[i]); !ok {
				return false, fmt.Sprintf("index %d: %s", i, why)
			}
		}
	case map[string]any:
		m, ok := got.(map[string]any)
		if !ok {
			return false, fmt.Sprintf("want object, got %T", got)
		}
		for k := range m {
			if _, ok := w[k]; !ok {
				return false, fmt.Sprintf("unexpected field %q", k)
			}
		}
		for k, wv := range w {
			gv, ok := m[k]
			if !ok {
				return false, fmt.Sprintf("missing field %q", k)
			}
			if ok, why := jsonEqual(gv, wv); !ok {
				return false, fmt.Sprintf("field %q: %s", k, why)
			}
		}
	default:
		return false, fmt.Sprintf("unsupported expected type %T", want)
	}
	return true, ""
}

func gradeJSONObject(want map[string]any) func(string) (bool, string) {
	return func(text string) (bool, string) {
		v, err := strictJSON(stripFence(text))
		if err != nil {
			return false, "not a single JSON value: " + err.Error()
		}
		if ok, why := jsonEqual(v, any(want)); !ok {
			return false, why
		}
		return true, "valid JSON, exact match"
	}
}

// lastAnswer pulls the text after the final "Answer:" marker. Reasoning tasks
// are graded on the answer, not on output discipline - the instruction needs
// have their own tasks - so extraction here is deliberately lenient.
func lastAnswer(text string) (string, bool) {
	low := strings.ToLower(text)
	i := strings.LastIndex(low, "answer:")
	if i < 0 {
		return "", false
	}
	rest := text[i+len("answer:"):]
	if j := strings.IndexByte(rest, '\n'); j >= 0 {
		rest = rest[:j]
	}
	return strings.Trim(rest, " \t*`_.$"), true
}

func parseCents(s string) (int, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(strings.TrimPrefix(s, "$"), ",", ""))
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	if f < 0 {
		return int(f*100 - 0.5), true
	}
	return int(f*100 + 0.5), true
}

// ---------------------------------------------------------------- families
func genJSONObject(params map[string]any, rng *rand.Rand) Instance {
	nested := pBool(params, "nested")
	f := pick(rng, poolFields, 4)
	n := 10 + rng.IntN(90)
	word := one(rng, poolTerms)
	flag := rng.IntN(2) == 0
	arr := []any{rng.IntN(50), rng.IntN(50), rng.IntN(50)}

	want := map[string]any{f[0]: n, f[1]: word, f[2]: flag, f[3]: arr}
	var sb strings.Builder
	sb.WriteString("Return ONLY a JSON object - no commentary before or after.\n")
	sb.WriteString("It must have exactly these fields and values:\n")
	fmt.Fprintf(&sb, "- %q: the integer %d\n", f[0], n)
	fmt.Fprintf(&sb, "- %q: the string %q\n", f[1], word)
	fmt.Fprintf(&sb, "- %q: the boolean %v\n", f[2], flag)
	fmt.Fprintf(&sb, "- %q: an array of exactly these three integers, in order: %v, %v, %v\n",
		f[3], arr[0], arr[1], arr[2])
	if nested {
		count := 2 + rng.IntN(7)
		note := one(rng, poolTerms)
		want["meta"] = map[string]any{"count": count, "note": note}
		fmt.Fprintf(&sb, "- \"meta\": an object with exactly two fields: %q (the integer %d) and %q (the string %q)\n",
			"count", count, "note", note)
	}
	sb.WriteString("No other fields.")
	canon, _ := json.Marshal(want)
	return Instance{Prompt: sb.String(), Canon: string(canon), Grade: gradeJSONObject(want)}
}

func genJSONExtract(params map[string]any, rng *rand.Rand) Instance {
	distract := pBool(params, "distractors")
	name, city := one(rng, poolNames), one(rng, poolCities)
	product := one(rng, poolProducts)
	qty := 2 + rng.IntN(38)
	cents := 199 + rng.IntN(4800)
	date := randDate(rng)

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s placed an order in %s on %s: %d units of the %s at $%s per unit.",
		name, city, date.Format("January 2, 2006"), qty, product, money(cents))
	if distract {
		fmt.Fprintf(&sb, " The invoice number was INV-%04d. The shop has %d aisles and a $%s gift-card display near the register.",
			1000+rng.IntN(9000), 3+rng.IntN(20), money(2500))
	}
	prose := sb.String()

	want := map[string]any{
		"name": name, "city": city, "quantity": qty,
		"unit_price": float64(cents) / 100, "date": date.Format("2006-01-02"),
	}
	prompt := "Read the note below and return ONLY a JSON object with exactly these keys:\n" +
		"name (string), city (string), quantity (integer), unit_price (number), date (string, format YYYY-MM-DD).\n" +
		"No other keys, no commentary.\n\nNote: " + prose
	canon, _ := json.Marshal(want)
	return Instance{Prompt: prompt, Canon: string(canon), Grade: gradeJSONObject(want)}
}

func genJSONSchema(params map[string]any, rng *rand.Rand) Instance {
	id := 1000 + rng.IntN(9000)
	status := one(rng, poolStatus)
	tags := pick(rng, poolTerms, 3)
	priority := 1 + rng.IntN(5)
	archived := rng.IntN(2) == 0

	tagAny := []any{tags[0], tags[1], tags[2]}
	want := map[string]any{
		"id": id, "status": status, "tags": tagAny,
		"priority": priority, "archived": archived,
	}
	arch := "is not"
	if archived {
		arch = "is"
	}
	prompt := fmt.Sprintf(`Produce ONLY a JSON object that conforms to this schema - exact keys, exact types, no extras:
  id        integer
  status    string, one of "active" | "paused" | "closed"
  tags      array of strings
  priority  integer
  archived  boolean

Facts: record %d is currently %s with priority %d. It is tagged %q, %q and %q, and it %s archived.`,
		id, status, priority, tags[0], tags[1], tags[2], arch)
	canon, _ := json.Marshal(want)
	return Instance{Prompt: prompt, Canon: string(canon), Grade: gradeJSONObject(want)}
}

func genCSV(params map[string]any, rng *rand.Rand) Instance {
	rows := pInt(params, "rows", 4)
	prods := pick(rng, poolProducts, rows)
	type rec struct {
		p     string
		qty   int
		cents int
	}
	recs := make([]rec, rows)
	var facts, canon strings.Builder
	canon.WriteString("product,qty,unit_price\n")
	for i := range recs {
		recs[i] = rec{prods[i], 1 + rng.IntN(20), 150 + rng.IntN(3000)}
		fmt.Fprintf(&facts, "- %s: quantity %d, unit price $%s\n", recs[i].p, recs[i].qty, money(recs[i].cents))
		fmt.Fprintf(&canon, "%s,%d,%s\n", recs[i].p, recs[i].qty, money(recs[i].cents))
	}
	prompt := fmt.Sprintf(`Return ONLY CSV text. The header row must be exactly:
product,qty,unit_price
Then exactly %d data rows, one per item below, in the order given. Prices as plain numbers without a currency sign. No commentary.

%s`, rows, facts.String())
	grade := func(text string) (bool, string) {
		r := csv.NewReader(strings.NewReader(stripFence(text)))
		r.TrimLeadingSpace = true
		all, err := r.ReadAll()
		if err != nil {
			return false, "not parseable CSV: " + err.Error()
		}
		if len(all) == 0 || strings.Join(all[0], ",") != "product,qty,unit_price" {
			return false, "header row is wrong"
		}
		if len(all)-1 != rows {
			return false, fmt.Sprintf("want %d data rows, got %d", rows, len(all)-1)
		}
		for i, rc := range recs {
			row := all[i+1]
			if len(row) != 3 {
				return false, fmt.Sprintf("row %d: want 3 cells, got %d", i+1, len(row))
			}
			if strings.TrimSpace(row[0]) != rc.p {
				return false, fmt.Sprintf("row %d: want product %q, got %q", i+1, rc.p, row[0])
			}
			if q, err := strconv.Atoi(strings.TrimSpace(row[1])); err != nil || q != rc.qty {
				return false, fmt.Sprintf("row %d: want qty %d, got %q", i+1, rc.qty, row[1])
			}
			if c, ok := parseCents(row[2]); !ok || c != rc.cents {
				return false, fmt.Sprintf("row %d: want price %s, got %q", i+1, money(rc.cents), row[2])
			}
		}
		return true, "valid CSV, exact match"
	}
	return Instance{Prompt: prompt, Canon: canon.String(), Grade: grade}
}

var punctRe = regexp.MustCompile(`[^a-z0-9 ]+`)

func genToolArgs(params map[string]any, rng *rand.Rand) Instance {
	people := pick(rng, poolNames, 2)
	emails := []any{
		strings.ToLower(people[0]) + "@example.com",
		strings.ToLower(people[1]) + "@example.com",
	}
	rawTitles := []string{"Quarterly Budget Review!", "Supply-Chain Sync: Phase Two", "Launch Readiness (Final!)", "Hiring Pipeline Check-In"}
	raw := one(rng, rawTitles)
	title := strings.Join(strings.Fields(punctRe.ReplaceAllString(strings.ToLower(raw), " ")), " ")
	date := randDate(rng)
	duration := []int{15, 30, 45, 60}[rng.IntN(4)]

	want := map[string]any{
		"title": title, "date": date.Format("2006-01-02"),
		"duration_minutes": duration, "attendees": emails,
	}
	prompt := fmt.Sprintf(`You are preparing a call to this function:

schedule_meeting(
  title: string             - lowercase, punctuation removed
  date: string              - format YYYY-MM-DD
  duration_minutes: integer - one of 15, 30, 45, 60
  attendees: array of strings - email addresses, in the order given
)

Facts: the meeting "%s" runs %d minutes on %s, with %s and %s attending (%s, %s).

Return ONLY the JSON arguments object for the call. No function name, no commentary.`,
		raw, duration, date.Format("January 2, 2006"),
		people[0], people[1], emails[0], emails[1])
	canon, _ := json.Marshal(want)
	return Instance{Prompt: prompt, Canon: string(canon), Grade: gradeJSONObject(want)}
}

func genFormat(params map[string]any, rng *rand.Rand) Instance {
	if pString(params, "variant", "tags") == "number" {
		a, b := 10+rng.IntN(90), 10+rng.IntN(90)
		wantN := strconv.Itoa(a + b)
		prompt := fmt.Sprintf("What is %d + %d? Reply with the number only - no words, no punctuation, no equation.", a, b)
		grade := func(text string) (bool, string) {
			t := strings.TrimSpace(text)
			if !regexp.MustCompile(`^[0-9]+$`).MatchString(t) {
				return false, fmt.Sprintf("reply is not a bare number: %q", firstLine(t))
			}
			if t != wantN {
				return false, fmt.Sprintf("want %s, got %s", wantN, t)
			}
			return true, "bare number, correct"
		}
		return Instance{Prompt: prompt, Canon: wantN, Grade: grade}
	}
	city := one(rng, poolCities)
	h, m := 5+rng.IntN(18), rng.IntN(60)
	prompt := fmt.Sprintf("The ferry to %s departs at %02d:%02d.\n\nWhich city does the ferry go to? "+
		"Your entire reply must be exactly <answer>%s</answer> - nothing before it, nothing after it.",
		city, h, m, city)
	want := "<answer>" + city + "</answer>"
	grade := func(text string) (bool, string) {
		if strings.TrimSpace(text) != want {
			return false, fmt.Sprintf("want exactly %s, got %q", want, firstLine(strings.TrimSpace(text)))
		}
		return true, "exact format"
	}
	return Instance{Prompt: prompt, Canon: want, Grade: grade}
}

func genLineRules(params map[string]any, rng *rand.Rand) Instance {
	n := 3 + rng.IntN(3)
	word := one(rng, poolTerms)
	banned := "very"
	topic := one(rng, poolTopics)
	var rules strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&rules, "line %d must begin with %q; ", i, fmt.Sprintf("%d) ", i))
	}
	prompt := fmt.Sprintf("Write exactly %d lines about %s. %sEach line must contain the word %q exactly once. "+
		"Do not use the word %q anywhere. No blank lines, nothing before line 1 or after line %d.",
		n, topic, rules.String(), word, banned, n)
	wordRe := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(word) + `\b`)
	bannedRe := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(banned) + `\b`)
	grade := func(text string) (bool, string) {
		lines := strings.Split(strings.TrimSpace(text), "\n")
		if len(lines) != n {
			return false, fmt.Sprintf("want %d lines, got %d", n, len(lines))
		}
		for i, l := range lines {
			prefix := fmt.Sprintf("%d) ", i+1)
			if !strings.HasPrefix(l, prefix) {
				return false, fmt.Sprintf("line %d does not begin with %q", i+1, prefix)
			}
			if c := len(wordRe.FindAllString(l, -1)); c != 1 {
				return false, fmt.Sprintf("line %d contains %q %d times, want exactly 1", i+1, word, c)
			}
		}
		if bannedRe.MatchString(text) {
			return false, fmt.Sprintf("used the banned word %q", banned)
		}
		return true, "all line rules held"
	}
	var canon strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&canon, "%d) A %s rests near %s.\n", i, word, topic)
	}
	return Instance{Prompt: prompt, Canon: strings.TrimRight(canon.String(), "\n"), Grade: grade}
}

func genKeywords(params map[string]any, rng *rand.Rand) Instance {
	terms := pick(rng, poolTerms, 3)
	banned := "interesting"
	topic := one(rng, poolTopics)
	maxWords := 60
	prompt := fmt.Sprintf("Write one paragraph of at most %d words about %s. It must include the terms %q, %q and %q "+
		"exactly as written (keep them lowercase). It must not contain the word %q. One paragraph only.",
		maxWords, topic, terms[0], terms[1], terms[2], banned)
	bannedRe := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(banned) + `\b`)
	grade := func(text string) (bool, string) {
		t := strings.TrimSpace(text)
		if strings.Contains(t, "\n\n") {
			return false, "more than one paragraph"
		}
		wc := len(strings.Fields(t))
		if wc > maxWords {
			return false, fmt.Sprintf("%d words, limit %d", wc, maxWords)
		}
		if wc < 10 {
			return false, fmt.Sprintf("only %d words - not a paragraph", wc)
		}
		for _, term := range terms {
			if !regexp.MustCompile(`\b` + regexp.QuoteMeta(term) + `\b`).MatchString(t) {
				return false, fmt.Sprintf("missing the term %q (verbatim, lowercase)", term)
			}
		}
		if bannedRe.MatchString(t) {
			return false, fmt.Sprintf("used the banned word %q", banned)
		}
		return true, "constraints held"
	}
	canon := fmt.Sprintf("Standing beside %s, we counted a %s overhead, a slab of %s underfoot, "+
		"and one %s glowing while the water kept moving slowly past us.", topic, terms[0], terms[1], terms[2])
	return Instance{Prompt: prompt, Canon: canon, Grade: grade}
}

func genMathChain(params map[string]any, rng *rand.Rand) Instance {
	distract := pBool(params, "distractors")
	prods := pick(rng, poolProducts, 3)
	subtotal := 0
	var items strings.Builder
	for _, p := range prods {
		qty := 1 + rng.IntN(6)
		cents := 150 + rng.IntN(2350)
		subtotal += qty * cents
		fmt.Fprintf(&items, "- %d x %s at $%s each\n", qty, p, money(cents))
	}
	pct := []int{5, 10, 15, 20, 25}[rng.IntN(5)]
	ship := []int{0, 499, 599, 799}[rng.IntN(4)]
	discount := subtotal * pct / 100 // stated rule: round the discount down to the cent
	total := subtotal - discount + ship

	var sb strings.Builder
	sb.WriteString("An order contains:\n")
	sb.WriteString(items.String())
	if distract {
		fmt.Fprintf(&sb, "The store has %d aisles and opened %d years ago. A display near the register shows $%s gift cards.\n",
			3+rng.IntN(20), 2+rng.IntN(30), money(2500))
	}
	fmt.Fprintf(&sb, "Apply a %d%% discount to the item subtotal, rounding the discount DOWN to the whole cent, "+
		"then add $%s shipping.\n", pct, money(ship))
	sb.WriteString("You may reason step by step, but end your reply with a line of exactly this form:\nAnswer: <total>\n")
	sb.WriteString("where <total> is a plain number with two decimals, no currency sign.")

	grade := func(text string) (bool, string) {
		ans, ok := lastAnswer(text)
		if !ok {
			return false, "no `Answer:` line found"
		}
		c, ok := parseCents(ans)
		if !ok {
			return false, fmt.Sprintf("answer is not a number: %q", ans)
		}
		if c != total {
			return false, fmt.Sprintf("want %s, got %s", money(total), ans)
		}
		return true, "correct total"
	}
	return Instance{Prompt: sb.String(), Canon: "Answer: " + money(total), Grade: grade}
}

func genDateMath(params map[string]any, rng *rand.Rand) Instance {
	base := randDate(rng)
	n := 25 + rng.IntN(300)
	want := base.AddDate(0, 0, n).Format("2006-01-02")
	prompt := fmt.Sprintf("What is the date exactly %d days after %s? "+
		"You may reason step by step, but end your reply with a line of exactly this form:\nAnswer: YYYY-MM-DD",
		n, base.Format("January 2, 2006"))
	grade := func(text string) (bool, string) {
		ans, ok := lastAnswer(text)
		if !ok {
			return false, "no `Answer:` line found"
		}
		if strings.TrimSpace(ans) != want {
			return false, fmt.Sprintf("want %s, got %q", want, ans)
		}
		return true, "correct date"
	}
	return Instance{Prompt: prompt, Canon: "Answer: " + want, Grade: grade}
}

func genStateTrack(params map[string]any, rng *rand.Rand) Instance {
	x := 3 + rng.IntN(9)
	var steps strings.Builder
	fmt.Fprintf(&steps, "x starts at %d.\n", x)
	for i := 1; i <= 6; i++ {
		switch rng.IntN(4) {
		case 0:
			k := 2 + rng.IntN(9)
			fmt.Fprintf(&steps, "%d. Add %d to x.\n", i, k)
			x += k
		case 1:
			k := 2 + rng.IntN(2)
			fmt.Fprintf(&steps, "%d. Multiply x by %d.\n", i, k)
			x *= k
		case 2:
			k := 1 + rng.IntN(6)
			fmt.Fprintf(&steps, "%d. Subtract %d from x.\n", i, k)
			x -= k
		case 3:
			fmt.Fprintf(&steps, "%d. If x is even, divide it by 2; otherwise add 1.\n", i)
			if x%2 == 0 {
				x /= 2
			} else {
				x++
			}
		}
	}
	want := strconv.Itoa(x)
	prompt := steps.String() +
		"\nApply the steps in order. You may reason step by step, but end your reply with a line of exactly this form:\nAnswer: <integer>"
	grade := func(text string) (bool, string) {
		ans, ok := lastAnswer(text)
		if !ok {
			return false, "no `Answer:` line found"
		}
		if strings.TrimSpace(ans) != want {
			return false, fmt.Sprintf("want %s, got %q", want, ans)
		}
		return true, "correct final value"
	}
	return Instance{Prompt: prompt, Canon: "Answer: " + want, Grade: grade}
}

var arrayRe = regexp.MustCompile(`\[[^\[\]]*\]`)

func genListOps(params map[string]any, rng *rand.Rand) Instance {
	vals := rng.Perm(60)[:6]
	list := append([]int(nil), vals...)
	var steps strings.Builder
	fmt.Fprintf(&steps, "Start with the list %s.\n", intList(list))

	appendV := 61 + rng.IntN(30)
	fmt.Fprintf(&steps, "1. Append %d.\n", appendV)
	list = append(list, appendV)

	pos := 1 + rng.IntN(len(list))
	fmt.Fprintf(&steps, "2. Remove the element at position %d (positions count from 1).\n", pos)
	list = append(list[:pos-1], list[pos:]...)

	if rng.IntN(2) == 0 {
		steps.WriteString("3. Sort the list in descending order.\n")
		sort.Sort(sort.Reverse(sort.IntSlice(list)))
	} else {
		steps.WriteString("3. Sort the list in ascending order.\n")
		sort.Ints(list)
	}
	steps.WriteString("4. Reverse the list.\n")
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}

	want := make([]any, len(list))
	for i, v := range list {
		want[i] = v
	}
	prompt := steps.String() +
		"\nApply the operations in order. The LAST line of your reply must be the final list as a JSON array of integers, like [4, 1, 9]."
	grade := func(text string) (bool, string) {
		all := arrayRe.FindAllString(text, -1)
		if len(all) == 0 {
			return false, "no JSON array found in the reply"
		}
		v, err := strictJSON(all[len(all)-1])
		if err != nil {
			return false, "last array does not parse: " + err.Error()
		}
		if ok, why := jsonEqual(v, any(want)); !ok {
			return false, why
		}
		return true, "correct final list"
	}
	canon, _ := json.Marshal(list)
	return Instance{Prompt: prompt, Canon: string(canon), Grade: grade}
}

func intList(xs []int) string {
	parts := make([]string, len(xs))
	for i, v := range xs {
		parts[i] = strconv.Itoa(v)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// genStatic is the family for user-authored tasks: a fixed prompt with a
// declarative grader. No generation, no code execution - grading is the same
// pure-Go machinery the built-ins use. Contamination resistance is the user's
// call for their own tasks; their work is the point.
func genStatic(params map[string]any, rng *rand.Rand) Instance {
	prompt := pString(params, "prompt", "")
	canon := pString(params, "canon", "")
	g, _ := params["grader"].(map[string]any)
	typ := pString(g, "type", "")
	expected := pString(g, "expected", "")
	ci := pBool(g, "case_insensitive")

	var grade func(string) (bool, string)
	switch typ {
	case "exact":
		grade = func(text string) (bool, string) {
			got, want := strings.TrimSpace(text), strings.TrimSpace(expected)
			if ci {
				got, want = strings.ToLower(got), strings.ToLower(want)
			}
			if got != want {
				return false, fmt.Sprintf("want %q, got %q", want, firstLine(got))
			}
			return true, "exact match"
		}
	case "contains":
		grade = func(text string) (bool, string) {
			got, want := text, expected
			if ci {
				got, want = strings.ToLower(got), strings.ToLower(want)
			}
			if !strings.Contains(got, want) {
				return false, fmt.Sprintf("reply does not contain %q", expected)
			}
			return true, "contains expected text"
		}
	case "regex":
		re, err := regexp.Compile(pString(g, "pattern", ""))
		if err != nil {
			grade = func(string) (bool, string) { return false, "invalid pattern: " + err.Error() }
			break
		}
		grade = func(text string) (bool, string) {
			if !re.MatchString(text) {
				return false, fmt.Sprintf("reply does not match /%s/", re.String())
			}
			return true, "matches pattern"
		}
	case "json_object":
		want, _ := g["expected_object"].(map[string]any)
		grade = func(text string) (bool, string) {
			v, err := strictJSON(stripFence(text))
			if err != nil {
				return false, "not a single JSON value: " + err.Error()
			}
			if ok, why := jsonEqual(v, any(normalizeWant(want))); !ok {
				return false, why
			}
			return true, "valid JSON, exact match"
		}
	case "number":
		wantF, _ := toFloat(g["expected_number"])
		tol, ok := toFloat(g["tolerance"])
		if !ok {
			tol = 1e-9
		}
		grade = func(text string) (bool, string) {
			ans, found := lastAnswer(text)
			if !found {
				ans = strings.TrimSpace(text)
			}
			f, err := strconv.ParseFloat(strings.Trim(ans, " $%"), 64)
			if err != nil {
				return false, fmt.Sprintf("no parseable number (looked after `Answer:`, then at the whole reply): %q", firstLine(ans))
			}
			if f-wantF > tol || wantF-f > tol {
				return false, fmt.Sprintf("want %v, got %v", wantF, f)
			}
			return true, "number matches"
		}
	default:
		grade = func(string) (bool, string) {
			return false, fmt.Sprintf("unknown grader type %q", typ)
		}
	}
	return Instance{Prompt: prompt, Canon: canon, Grade: grade}
}

// normalizeWant converts a user-supplied expected object (JSON floats) into
// the int/float split jsonEqual expects: integral numbers compare as integers.
func normalizeWant(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = normalizeVal(v)
	}
	return out
}

func normalizeVal(v any) any {
	switch t := v.(type) {
	case float64:
		if t == float64(int(t)) {
			return int(t)
		}
		return t
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = normalizeVal(e)
		}
		return out
	case map[string]any:
		return normalizeWant(t)
	}
	return v
}

// ---------------------------------------------------------------- params
func pBool(p map[string]any, key string) bool {
	v, _ := p[key].(bool)
	return v
}

func pInt(p map[string]any, key string, def int) int {
	if f, ok := toFloat(p[key]); ok {
		return int(f)
	}
	return def
}

func pString(p map[string]any, key, def string) string {
	if s, ok := p[key].(string); ok && s != "" {
		return s
	}
	return def
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "..."
	}
	return s
}
