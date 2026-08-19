// Command fitr answers: is this local model any good ON THIS DEVICE?
//
// Conventions borrowed from tools that got them right:
//   - progress to stderr, results to stdout, so output is pipeable (promptfoo)
//   - errors are error/note/hint, plain text on stderr even under --json;
//     nobody wraps diagnostics in JSON. The exit code is the machine channel
//     (rustc, gh, uv)
//   - -q silences chrome, -v hides the progress display so it cannot interleave
//     with diagnostics (uv)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/lock"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

const version = "0.1.0"

// Exit codes: small, documented, domain-specific. Not sysexits -- nobody uses it.
const (
	exitOK        = 0 // ran, and every measured need passed
	exitError     = 1 // something broke
	exitUsage     = 2 // bad invocation
	exitGates     = 3 // ran fine, but a need FAILED (useful as a CI gate)
	exitInterrupt = 130
)

func errPrint(msg, note, hint string) {
	fmt.Fprintf(os.Stderr, "error: %s\n", msg)
	if note != "" {
		fmt.Fprintf(os.Stderr, " note: %s\n", note)
	}
	if hint != "" {
		fmt.Fprintf(os.Stderr, " hint: %s\n", hint)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `fitr `+version+` - is this local model any good ON THIS DEVICE?

usage:
  fitr run <model> [--quick|--full] [-k N] [--profile P] [--display MODE]
  fitr board [--current]
  fitr diag <model>
  fitr doctor <model> [-n N]
  fitr device
  fitr profiles
  fitr compare <model-a> <model-b>

flags:
  --display  auto|plain|json|none   output mode (default auto)
  -k         repeats per noisy task (default 3, 1 with --quick)
             A single run is not a measurement: identical configs vary 10-20pp.
  -q         quiet (repeat for silent)      -v  verbose

exit codes:
  0 ok   1 error   2 usage   3 a need FAILED   130 interrupted

examples:
  fitr run qwen3-coder:30b --full
  fitr run some-new-model:tag -k 3
  fitr diag dolphin3:8b
  fitr doctor qwen3-coder:30b
`)
}

func main() { os.Exit(run()) }

func run() int {
	if len(os.Args) < 2 {
		usage()
		return exitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	switch os.Args[1] {
	case "run":
		return cmdRun(ctx, os.Args[2:])
	case "board":
		return cmdBoard(ctx, os.Args[2:])
	case "diag":
		return cmdDiag(ctx, os.Args[2:])
	case "doctor":
		return cmdDoctor(ctx, os.Args[2:])
	case "device":
		return cmdDevice(ctx, os.Args[2:])
	case "profiles":
		return cmdProfiles(ctx)
	case "compare":
		return cmdCompare(ctx, os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return exitOK
	case "--version", "version":
		fmt.Println("fitr", version)
		return exitOK
	default:
		errPrint(fmt.Sprintf("unknown command %q", os.Args[1]), "",
			"run `fitr --help` for usage")
		return exitUsage
	}
}

// permute moves positional arguments after flags.
//
// Go's flag package stops parsing at the first non-flag argument, so
// `fitr run mymodel --quick` would silently ignore --quick. That is the
// natural way to type it, so reorder rather than making the user adapt.
func permute(args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			// A value-taking flag written as `-k 3` consumes the next token.
			if !strings.Contains(a, "=") && i+1 < len(args) &&
				!strings.HasPrefix(args[i+1], "-") && takesValue(a) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	return append(flags, positional...)
}

func takesValue(flagArg string) bool {
	name := strings.TrimLeft(flagArg, "-")
	switch name {
	case "k", "n", "profile", "display", "q":
		return true
	}
	return false
}

func newClient(ctx context.Context, model string) (*ollama.Client, int) {
	c := ollama.New()
	if !c.Reachable(ctx) {
		errPrint("cannot reach Ollama at "+c.BaseURL,
			"every measurement needs a running server",
			"start it with `ollama serve`, or set OLLAMA_BASE_URL")
		return nil, exitError
	}
	if model != "" {
		tags, err := c.Tags(ctx)
		if err == nil && len(tags) > 0 {
			found := false
			var near []string
			base := strings.SplitN(model, ":", 2)[0]
			for _, t := range tags {
				if t.Name == model {
					found = true
				}
				if strings.Contains(t.Name, base) {
					near = append(near, t.Name)
				}
			}
			if !found {
				hint := "pull it first: `ollama pull " + model + "`"
				if len(near) > 0 {
					hint = "did you mean: " + strings.Join(near, ", ")
				}
				errPrint(fmt.Sprintf("model %q is not installed", model),
					fmt.Sprintf("%d model(s) available", len(tags)), hint)
				return nil, exitUsage
			}
		}
	}
	return c, exitOK
}

// ---------------------------------------------------------------- run
func cmdRun(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	quick := fs.Bool("quick", false, "speed+memory+coding+tools")
	full := fs.Bool("full", false, "adds the 40-turn agentic task")
	k := fs.Int("k", 0, "repeats per noisy task")
	profileName := fs.String("profile", "", "device profile (default: auto-match)")
	mode := fs.String("display", "auto", "auto|plain|json|none")
	quiet := fs.Int("q", 0, "quiet level")
	verbose := fs.Bool("v", false, "verbose")
	if err := fs.Parse(permute(args)); err != nil {
		return exitUsage
	}
	if fs.NArg() < 1 {
		errPrint("missing model", "", "fitr run <model> --full")
		return exitUsage
	}
	model := fs.Arg(0)

	level := "default"
	if *quick {
		level = "quick"
	} else if *full {
		level = "full"
	}
	reps := *k
	if reps == 0 {
		reps = 3
		if level == "quick" {
			reps = 1
		}
	}
	if *quiet > 1 {
		*mode = "none"
	} else if (*quiet > 0 || *verbose) && *mode == "auto" {
		*mode = "plain"
	}

	c, code := newClient(ctx, model)
	if code != exitOK {
		return code
	}
	disp := render.New(*mode)
	defer disp.Close()

	// Check tasks generate a FRESH instance per repeat, so one pass per task is
	// already a set of independent trials pooled per need. Repeats multiply
	// wall-clock across ~16 tasks, so they follow -k only when asked.
	checksReps := 1
	if *k > 0 {
		checksReps = *k
	}

	res, err := execute(ctx, c, model, level, *profileName, reps, checksReps, disp)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "\ninterrupted")
			return exitInterrupt
		}
		errPrint(err.Error(), "", "re-run with -v for detail")
		return exitError
	}

	path, err := save(res)
	if err != nil {
		errPrint("could not save result: "+err.Error(), "", "")
	}
	res.Meta.SavedPath = path
	disp.Result(res.Scorecard, res.Meta)

	if *quiet == 0 && render.Resolve(*mode) != "json" {
		fmt.Fprintf(os.Stderr, "\n  saved  %s\n", path)
		fmt.Fprintf(os.Stderr, "  next   fitr board\n")
		if reps < 3 {
			fmt.Fprintf(os.Stderr, "         fitr run %s -k 3   for a rankable result\n", model)
		}
	}
	if res.Scorecard.Fails > 0 {
		return exitGates
	}
	return exitOK
}

// ---------------------------------------------------------------- result
type Result struct {
	SchemaVersion int                `json:"schema_version"`
	Model         string             `json:"model"`
	StartedAt     string             `json:"started_at"`
	Level         string             `json:"level"`
	Repeats       int                `json:"repeats"`
	WallSeconds   float64            `json:"wall_s"`
	Device        device.Fingerprint `json:"device"`
	DeviceKey     string             `json:"device_key"`
	Profile       string             `json:"profile"`
	ModelMeta     ollama.ModelInfo   `json:"model_meta"`

	Speed      []eval.SpeedResult             `json:"speed_repeats"`
	DecodeSum  stats.Summary                  `json:"decode_summary"`
	TTFTSum    stats.Summary                  `json:"ttft_summary"`
	PrefillSum stats.Summary                  `json:"prefill_summary"`
	Memory     eval.MemoryResult              `json:"memory"`
	CodeWrite  []eval.ExecResult              `json:"code_write"`
	CodeFix    []eval.ExecResult              `json:"code_fix"`
	Checks     []eval.CheckOutcome            `json:"checks,omitempty"`
	Tools      []eval.ToolLoopResult          `json:"tools"`
	Agentic    *eval.ToolLoopResult           `json:"agentic,omitempty"`
	Refusal    map[string]eval.RefusalVerdict `json:"refusal,omitempty"`
	Refused    int                            `json:"refused_count"`
	Plumbing   *eval.PlumbingResult           `json:"plumbing,omitempty"`
	Rep        score.Repetition               `json:"repetition"`
	Density    score.Density                  `json:"density"`

	// Contamination lists models that refused to unload. A non-empty value
	// means every timing in this result is suspect.
	Contamination []string `json:"contamination,omitempty"`

	Scorecard score.Scorecard `json:"scorecard"`
	Meta      render.Meta     `json:"-"`
}

func execute(ctx context.Context, c *ollama.Client, model, level, profileName string,
	reps, checksReps int, disp render.Display) (*Result, error) {

	// Exactly one eval per machine at a time. Two runs against one inference
	// server contaminate each other, and the damage is not recoverable after
	// the fact -- the numbers still look plausible.
	lk, err := lock.Acquire("eval", "eval of "+model)
	if err != nil {
		return nil, err
	}
	defer lk.Release() //nolint:errcheck // cleanup failure is not worth failing a run over

	spec, err := eval.LoadSpec()
	if err != nil {
		return nil, err
	}
	// User tasks extend the battery without a fork. A malformed one is a hard
	// error with the filename in it - silently dropping your own task would
	// defeat the point of having one.
	userChecks, err := eval.LoadUserChecks(eval.UserTasksDir())
	if err != nil {
		return nil, err
	}
	if len(userChecks) > 0 {
		if spec.Checks, err = eval.MergeChecks(spec.Checks, userChecks); err != nil {
			return nil, err
		}
		disp.Note(fmt.Sprintf("%d user task(s) loaded from %s", len(userChecks), eval.UserTasksDir()), "")
	}
	fp := device.Detect(ctx, c)
	prof, err := device.SelectProfile(profileName, fp)
	if err != nil {
		return nil, err
	}
	info, _ := c.Show(ctx, model)

	res := &Result{
		SchemaVersion: spec.Version.ResultSchemaVersion,
		Model:         model,
		StartedAt:     time.Now().Format(time.RFC3339),
		Level:         level, Repeats: reps,
		Device: fp, DeviceKey: fp.Key(), Profile: prof.Name, ModelMeta: info,
	}
	if prof.Name == "default" {
		disp.Note("using the UNCALIBRATED default profile - verdicts are rough; "+
			"copy profiles/default.json and tune it for this box", "warn")
	}
	if device.IsDenseAndBig(info.Details.ParameterSize, info.Details.Family, prof) {
		disp.Note("dense "+info.Details.ParameterSize+" model on a bandwidth-bound "+
			"device - expect very low tok/s", "warn")
	}

	work, err := os.MkdirTemp("", "evalkit_")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	start := time.Now()
	step := func(name, detail string, fn func() error) error {
		disp.Phase(name, detail)
		t := time.Now()
		if err := fn(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		disp.Done(name, time.Since(t).Seconds())
		return nil
	}

	// One model resident at a time is non-negotiable between phases. A model
	// that will not unload is recorded and warned about -- data marked suspect
	// beats data silently trusted.
	stopAll := func() {
		left, err := c.StopAll(ctx)
		if err != nil {
			disp.Note("could not confirm models unloaded: "+err.Error(), "warn")
			return
		}
		if len(left) == 0 {
			return
		}
		res.Contamination = appendUnique(res.Contamination, left...)
		disp.Note("still resident after unload: "+strings.Join(left, ", ")+
			" - timings in this run may be contaminated", "warn")
	}

	stopAll()
	if err := step("speed", fmt.Sprintf("x%d", reps), func() error {
		for i := range reps {
			// The nonce MUST vary: identical long prompts hit the prefix cache
			// and prefill becomes fiction.
			nonce := fmt.Sprintf("%s-%d", res.StartedAt, i)
			s, err := eval.RunSpeed(ctx, c, model, spec, nonce)
			if err != nil {
				return err
			}
			res.Speed = append(res.Speed, s)
		}
		var dec, ttft, pre []float64
		for _, s := range res.Speed {
			dec = append(dec, s.DecodeTPS)
			ttft = append(ttft, s.TTFT)
			pre = append(pre, s.PrefillTPS)
		}
		res.DecodeSum, res.TTFTSum, res.PrefillSum = stats.MeanSD(dec), stats.MeanSD(ttft), stats.MeanSD(pre)
		return nil
	}); err != nil {
		return nil, err
	}
	res.Device.InferenceDevice = device.InferenceDeviceFor(ctx, c, model)

	if err := step("memory", "resident @32K", func() error {
		m, err := eval.RunMemory(ctx, c, model, 32768)
		res.Memory = m
		return err
	}); err != nil {
		return nil, err
	}

	if err := step("coding", fmt.Sprintf("x%d", reps), func() error {
		for i := range reps {
			w, err := eval.RunExec(ctx, c, model, spec.CodeWrite, filepath.Join(work, fmt.Sprintf("cw%d", i)))
			if err != nil {
				return err
			}
			res.CodeWrite = append(res.CodeWrite, w)
			f, err := eval.RunExec(ctx, c, model, spec.CodeFix, filepath.Join(work, fmt.Sprintf("cf%d", i)))
			if err != nil {
				return err
			}
			res.CodeFix = append(res.CodeFix, f)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Generated checks: fresh instances per run, graded in pure Go. Skipped on
	// --quick to keep the smoke test a smoke test.
	if level != "quick" && len(spec.Checks) > 0 {
		total := len(spec.Checks) * checksReps
		if err := step("checks", fmt.Sprintf("%d generated tasks", total), func() error {
			for _, cs := range spec.Checks {
				for rep := range checksReps {
					seed := eval.InstanceSeed(res.StartedAt, cs.ID, rep)
					o, err := eval.RunCheck(ctx, c, model, cs, seed)
					if err != nil {
						return err
					}
					res.Checks = append(res.Checks, o)
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	// Plumbing BEFORE capability: a tools failure is uninterpretable until you
	// know the template and parser work.
	if err := step("plumbing", "tool round-trip", func() error {
		p, err := eval.RunPlumbing(ctx, c, model, spec.Plumbing)
		if err == nil {
			res.Plumbing = &p
		}
		return nil
	}); err != nil {
		return nil, err
	}

	if err := step("tools", fmt.Sprintf("x%d", reps), func() error {
		for i := range reps {
			t, err := eval.RunToolLoop(ctx, c, model, spec.Tools, filepath.Join(work, fmt.Sprintf("tl%d", i)))
			if err != nil {
				return err
			}
			res.Tools = append(res.Tools, t)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	if level != "quick" {
		if err := step("refusal", "3 prompts", func() error {
			r, n, err := eval.RunRefusal(ctx, c, model, spec.Refusal)
			res.Refusal, res.Refused = r, n
			return err
		}); err != nil {
			return nil, err
		}
	}

	if level == "full" {
		if err := step("agentic", fmt.Sprintf("up to %d unsupervised turns", spec.Agentic.MaxTurns), func() error {
			stopAll()
			a, err := eval.RunToolLoop(ctx, c, model, spec.Agentic, filepath.Join(work, "ag"))
			if err != nil {
				return err
			}
			res.Agentic = &a
			return nil
		}); err != nil {
			return nil, err
		}
	}

	// Degeneracy over the longest text this model produced.
	longest := ""
	for _, r := range append(append([]eval.ExecResult{}, res.CodeWrite...), res.CodeFix...) {
		if len(r.Raw) > len(longest) {
			longest = r.Raw
		}
	}
	for _, v := range res.Refusal {
		if len(v.Text) > len(longest) {
			longest = v.Text
		}
	}
	res.Rep = score.RepetitionMetrics(longest)
	// Truncation is judged on free-running tasks only; the speed probe is
	// capped by design and would always look truncated.
	res.Density = score.InformationDensity(longest)
	res.WallSeconds = float64(int(time.Since(start).Seconds()*10)) / 10
	stopAll()

	res.Scorecard = score.Score(measure(res), prof)
	res.Meta = render.Meta{
		ParamSize: info.Details.ParameterSize, Quant: info.Details.QuantizationLevel,
		Family: info.Details.Family, GPU: fp.GPU, Driver: fp.GPUDriver,
		Device: res.Device.InferenceDevice, Profile: prof.Name, Repeats: reps,
		DecodeMean: res.DecodeSum.Mean, DecodeSD: res.DecodeSum.SD,
		DecodeMin: res.DecodeSum.Min, DecodeMax: res.DecodeSum.Max,
		DecodeN:     res.DecodeSum.N,
		PrefillMean: res.PrefillSum.Mean, PrefillSD: res.PrefillSum.SD,
		PrefillN: res.PrefillSum.N,
	}
	var decodes []float64
	for _, s := range res.Speed {
		decodes = append(decodes, s.DecodeTPS)
	}
	res.Meta.FirstRunSlow, res.Meta.FirstRunRatio = stats.FirstRunSlow(decodes)

	// State what this sample size CANNOT resolve, out loud, on every run.
	trials := len(res.CodeWrite) + len(res.CodeFix) + len(res.Tools) + len(res.Checks)
	if res.Agentic != nil {
		trials++
	}
	if trials > 0 {
		res.Meta.Trials = trials
		res.Meta.MDEpp = 100 * stats.MinDetectableEffect(trials, 1)
	}
	return res, nil
}

// measure folds raw results into what the scorer needs.
func measure(r *Result) score.Measured {
	m := score.Measured{
		Model: r.Model, Capabilities: r.ModelMeta.Capabilities,
		Rep: r.Rep,
	}
	if r.DecodeSum.N > 0 {
		m.SpeedKnown = true
		m.DecodeTPS, m.TTFT, m.PrefillTPS = r.DecodeSum.Mean, r.TTFTSum.Mean, r.PrefillSum.Mean
	}
	if r.Memory.ResidentGB > 0 {
		m.MemoryKnown, m.ResidentGB32K = true, r.Memory.ResidentGB
	}
	if len(r.CodeWrite) > 0 {
		m.CodeKnown = true
		var wOK, fOK []bool
		for _, x := range r.CodeWrite {
			wOK = append(wOK, x.Pass)
		}
		for _, x := range r.CodeFix {
			fOK = append(fOK, x.Pass)
		}
		fw, ff := stats.Flakiness(wOK), stats.Flakiness(fOK)
		m.CodeWritePass = fw.Passes*2 > fw.N
		m.CodeFixPass = ff.Passes*2 > ff.N
		m.CodeFlaky = fw.Flaky || ff.Flaky
		m.CodePasses = fw.Passes + ff.Passes
		m.CodeRepeats = fw.N + ff.N
	}
	for _, ck := range r.Checks {
		var pool *score.Pool
		switch ck.Need {
		case "structured_output":
			pool = &m.Structured
		case "instruction_precision":
			pool = &m.Precision
		case "reasoning":
			pool = &m.Reasoning
		default:
			pool = &m.User
		}
		pool.N++
		if ck.Pass {
			pool.Passes++
		}
	}
	if r.Refusal != nil {
		m.RefusalKnown, m.RefusedCount = true, r.Refused
	}
	if len(r.Tools) > 0 {
		m.ToolsRan = true
		pass := 0
		for _, t := range r.Tools {
			if t.Pass {
				pass++
			}
		}
		m.ToolsPass = pass*2 > len(r.Tools)
	}
	if r.Agentic != nil {
		m.AgenticRan, m.AgenticPass = true, r.Agentic.Pass
		m.AgenticMalformed = r.Agentic.Malformed
		m.AgenticTurns = r.Agentic.Turns
	}
	if r.Plumbing != nil {
		m.PlumbingRan = true
		m.PlumbingHealthy = r.Plumbing.Healthy
		m.PlumbingVerdict = r.Plumbing.Verdict
		if rung, ok := r.Plumbing.Rungs["5_irrelevance"]; ok {
			m.IrrelevanceRan, m.IrrelevancePass = true, rung.Pass
			if !rung.Pass {
				m.SpuriousCalls = 1
			}
		}
	}
	return m
}

// ---------------------------------------------------------------- storage
func resultsDir() string {
	if d := os.Getenv("FITR_RESULTS"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "results"
	}
	return filepath.Join(home, ".fitr", "results")
}

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func save(r *Result) (string, error) {
	dir := resultsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, safeName(r.Model)+".json")
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return p, os.WriteFile(p, b, 0o644)
}

func loadResults() ([]*Result, error) {
	dir := resultsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []*Result
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r Result
		if json.Unmarshal(b, &r) == nil && r.Model != "" {
			out = append(out, &r)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- board
func cmdBoard(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("board", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	current := fs.Bool("current", false, "only this machine's current config")
	mode := fs.String("display", "auto", "auto|plain|json|none")
	if err := fs.Parse(permute(args)); err != nil {
		return exitUsage
	}
	results, err := loadResults()
	if err != nil || len(results) == 0 {
		errPrint("no results yet", "", "run one first: fitr run <model> --full")
		return exitError
	}
	c := ollama.New()
	cur := device.Detect(ctx, c).Key()

	// Group by fingerprint. Rows measured under different hardware/config are
	// NOT comparable and must never be ranked against each other.
	groups := map[string][]*Result{}
	var order []string
	for _, r := range results {
		if _, ok := groups[r.DeviceKey]; !ok {
			order = append(order, r.DeviceKey)
		}
		groups[r.DeviceKey] = append(groups[r.DeviceKey], r)
	}
	sort.Strings(order)

	if render.Resolve(*mode) == "json" {
		b, _ := json.Marshal(map[string]any{"groups": groups, "current": cur})
		fmt.Println(string(b))
		return exitOK
	}

	for _, key := range order {
		rows := groups[key]
		if *current && key != cur {
			continue
		}
		g := rows[len(rows)-1]
		note := "different hardware/config - not comparable to other blocks"
		if key == cur {
			note = "this machine, current config"
		}
		fmt.Printf("\n%s | driver %s | KV %s\n", g.Device.GPU, g.Device.GPUDriver,
			g.Device.Config["OLLAMA_KV_CACHE_TYPE"])
		fmt.Printf("  %s\n", note)
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].DecodeSum.Mean > rows[j].DecodeSum.Mean
		})
		fmt.Printf("  %-28s %7s %8s %7s %8s %7s %3s  %s\n",
			"model", "params", "tok/s", "sd", "prefill", "GB@32K", "k", "serves")
		for _, r := range rows {
			sd := ""
			if r.DecodeSum.Valid() {
				sd = fmt.Sprintf("%.2f", r.DecodeSum.SD)
			}
			var codes []string
			for _, s := range r.Scorecard.Serves {
				codes = append(codes, score.NeedCode[s])
			}
			fmt.Printf("  %-28s %7s %8.2f %7s %8.1f %7.2f %3d  %s\n",
				trunc(r.Model, 28), r.ModelMeta.Details.ParameterSize,
				r.DecodeSum.Mean, sd, r.PrefillSum.Mean, r.Memory.ResidentGB,
				r.Repeats, strings.Join(codes, ", "))
		}
	}
	fmt.Println("\n  k = repeats. k<3 is a smoke test, not a rankable result.")
	if len(order) > 1 && !*current {
		fmt.Println("  blocks differ in hardware/config - re-measure rather than ranking across them")
	}
	return exitOK
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// ---------------------------------------------------------------- diag
func cmdDiag(ctx context.Context, args []string) int {
	if len(args) < 1 {
		errPrint("missing model", "", "fitr diag <model>")
		return exitUsage
	}
	model := args[0]
	c, code := newClient(ctx, model)
	if code != exitOK {
		return code
	}
	spec, err := eval.LoadSpec()
	if err != nil {
		errPrint(err.Error(), "", "")
		return exitError
	}
	fmt.Printf("tool plumbing: %s\n", model)
	r, err := eval.RunPlumbing(ctx, c, model, spec.Plumbing)
	if err != nil {
		errPrint(err.Error(), "", "")
		return exitError
	}
	for _, id := range r.Order {
		rung := r.Rungs[id]
		mark := "FAIL"
		if rung.Pass {
			mark = "PASS"
		}
		fmt.Printf("  [%s] %-20s %s\n", mark, id, render.Sanitize(rung.Detail))
	}
	fmt.Printf("  => %s\n", r.Verdict)
	if !r.Healthy {
		return exitGates
	}
	return exitOK
}

// ---------------------------------------------------------------- doctor
// cmdDoctor answers: can this box be measured fairly AT ALL? Every benchmark
// silently assumes yes; nothing else checks. ~60 seconds, worth running before
// believing any number - including ours.
func cmdDoctor(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	n := fs.Int("n", 5, "identical generations per determinism probe")
	if err := fs.Parse(permute(args)); err != nil {
		return exitUsage
	}
	if fs.NArg() < 1 {
		errPrint("missing model", "", "fitr doctor <model>")
		return exitUsage
	}
	model := fs.Arg(0)
	c, code := newClient(ctx, model)
	if code != exitOK {
		return code
	}
	// Doctor generates, so it takes the same one-eval-per-machine lock a run
	// does, and clears residents so its timings are its own.
	lk, err := lock.Acquire("eval", "doctor of "+model)
	if err != nil {
		errPrint(err.Error(), "", "")
		return exitError
	}
	defer lk.Release() //nolint:errcheck // cleanup failure is not worth failing a run over
	if left, err := c.StopAll(ctx); err == nil && len(left) > 0 {
		fmt.Fprintf(os.Stderr, "! still resident: %s - results may be contaminated\n", strings.Join(left, ", "))
	}

	fp := device.Detect(ctx, c)
	fmt.Printf("doctor: %s on %s (%s)\n", model, fp.GPU, fp.Ollama)
	r, err := eval.RunDoctor(ctx, c, model, *n, eval.DoctorOpts{
		Config: fp.Config,
		Placement: func(ctx context.Context) string {
			return device.InferenceDeviceFor(ctx, c, model)
		},
	})
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "\ninterrupted")
			return exitInterrupt
		}
		errPrint(err.Error(), "", "")
		return exitError
	}
	for _, ck := range r.Checks {
		fmt.Printf("  [%-4s] %-18s %s\n", ck.State, ck.ID, render.Sanitize(ck.Detail))
	}
	fmt.Printf("  => %s\n", r.Verdict)
	if !r.Healthy {
		return exitGates
	}
	return exitOK
}

// ---------------------------------------------------------------- device
func cmdDevice(ctx context.Context, args []string) int {
	c := ollama.New()
	fp := device.Detect(ctx, c)
	prof, _ := device.SelectProfile("", fp)
	if len(args) > 0 && args[0] == "--display=json" {
		b, _ := json.MarshalIndent(map[string]any{
			"fingerprint": fp, "key": fp.Key(), "profile": prof.Name}, "", "  ")
		fmt.Println(string(b))
		return exitOK
	}
	fmt.Printf("  host               %s\n", fp.Host)
	fmt.Printf("  os                 %s\n", fp.OS)
	fmt.Printf("  cpu                %s\n", fp.CPU)
	fmt.Printf("  ram_gb             %.1f\n", fp.RAMGb)
	fmt.Printf("  gpu                %s\n", fp.GPU)
	fmt.Printf("  gpu_driver         %s  (%s)\n", fp.GPUDriver, fp.GPUDriverDate)
	fmt.Printf("  ollama             %s\n", fp.Ollama)
	fmt.Printf("  inference_device   %s\n", fp.InferenceDevice)
	fmt.Println("  config")
	keys := make([]string, 0, len(fp.Config))
	for k := range fp.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := fp.Config[k]
		if v == "" {
			v = "(unset)"
		}
		fmt.Printf("    %-26s %s\n", k, v)
	}
	fmt.Printf("  profile            %s - %s\n", prof.Name, prof.Description)
	fmt.Printf("  key                %s\n", fp.Key())
	return exitOK
}

func cmdProfiles(ctx context.Context) int {
	c := ollama.New()
	fp := device.Detect(ctx, c)
	active, _ := device.SelectProfile("", fp)
	profs, err := device.LoadProfiles()
	if err != nil {
		errPrint(err.Error(), "", "")
		return exitError
	}
	for _, p := range profs {
		mark := " "
		if p.Name == active.Name {
			mark = "*"
		}
		fmt.Printf(" %s %-12s %s\n", mark, p.Name, p.Description)
	}
	fmt.Println("\n  * = auto-selected for this machine")
	return exitOK
}

// ---------------------------------------------------------------- compare
func cmdCompare(ctx context.Context, args []string) int {
	if len(args) < 2 {
		errPrint("need two models", "", "fitr compare <a> <b>")
		return exitUsage
	}
	results, err := loadResults()
	if err != nil {
		errPrint("no results", "", "fitr run <model> --full")
		return exitError
	}
	byName := map[string]*Result{}
	for _, r := range results {
		byName[r.Model] = r
	}
	a, b := byName[args[0]], byName[args[1]]
	for i, r := range []*Result{a, b} {
		if r == nil {
			errPrint(fmt.Sprintf("no stored result for %q", args[i]), "",
				"fitr run "+args[i]+" --full")
			return exitError
		}
	}
	if a.DeviceKey != b.DeviceKey {
		errPrint("these results were measured on different hardware/config",
			"tok/s is device-specific; the comparison would be meaningless",
			"re-measure both on this machine")
		return exitError
	}

	fmt.Printf("  %s  vs  %s\n\n", a.Model, b.Model)
	for _, p := range []struct {
		label string
		x, y  stats.Summary
	}{
		{"decode tok/s", a.DecodeSum, b.DecodeSum},
		{"prefill tok/s", a.PrefillSum, b.PrefillSum},
	} {
		ratio, sd, ok := stats.RatioWithError(p.x, p.y)
		if ok {
			fmt.Printf("  %-15s %8.2f vs %8.2f   %.2fx +/-%.2f\n",
				p.label, p.x.Mean, p.y.Mean, ratio, sd)
		} else {
			fmt.Printf("  %-15s %8.2f vs %8.2f   %.2fx (no +/- : single observation)\n",
				p.label, p.x.Mean, p.y.Mean, ratio)
		}
	}
	fmt.Println()
	wa := codeWilson(a)
	wb := codeWilson(b)
	fmt.Printf("  coding          %s\n", stats.Compare(a.Model, wa, b.Model, wb))
	fmt.Println("\n  note  overlapping intervals mean the sample cannot separate them;")
	fmt.Println("        that is a real answer, not a missing one.")
	return exitOK
}

func codeWilson(r *Result) stats.Interval {
	pass, n := 0, 0
	for _, x := range append(append([]eval.ExecResult{}, r.CodeWrite...), r.CodeFix...) {
		n++
		if x.Pass {
			pass++
		}
	}
	return stats.Wilson(pass, n)
}

// appendUnique adds items not already present, preserving order.
func appendUnique(dst []string, items ...string) []string {
	for _, it := range items {
		found := slices.Contains(dst, it)
		if !found {
			dst = append(dst, it)
		}
	}
	return dst
}
