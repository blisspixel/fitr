// fitr doctor: does this box even produce trustworthy measurements?
//
// Every benchmark silently assumes the stack under it is healthy - that a
// reachable server actually infers, that the served context is the requested
// one, and that temperature-0 greedy decoding reproduces. None of that is safe
// to assume on a local stack, and nothing else checks it:
//
//   - HTTP 200 on /api/tags is not inference. A misconfigured offload can
//     accept requests and emit zero tokens.
//   - Structured-output (grammar-constrained) generation breaks seed
//     reproducibility on some stacks even at temperature 0 while the SAME
//     prompt in plain text reproduces - a known, open Ollama issue. A pipeline
//     that assumes otherwise retries forever or trusts a moving target.
//   - Multi-GPU splits, parallel slots, and partial CPU offload each change
//     what a number means before any model quality is involved.
//
// Nondeterminism here is a WARN, not a FAIL: fitr's own method (repeats and
// intervals) survives it. But you should know, because every single-run number
// anyone quotes at you assumes it away.
package eval

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/stats"
)

type DoctorCheck struct {
	ID     string `json:"id"`
	State  string `json:"state"` // PASS | WARN | FAIL | SKIP
	Detail string `json:"detail"`
}

type DoctorResult struct {
	Model   string        `json:"model"`
	Runs    int           `json:"runs"`
	Checks  []DoctorCheck `json:"checks"`
	Verdict string        `json:"verdict"`
	Healthy bool          `json:"healthy"` // no FAILs
}

// DoctorOpts carries what the doctor needs from the environment so this
// package stays independent of how it was detected.
type DoctorOpts struct {
	// Config is the resolved server config (from the server log when
	// available, which is authoritative over this process's environment).
	Config map[string]string
	// ConfigObserved is false when Config was not read from the serving
	// runtime. Empty values are then unobserved, not unset.
	ConfigObserved bool
	// Placement reports where the loaded model actually computes, e.g.
	// "GPU 100%", "GPU 62%", "CPU". Called after the model is warm.
	Placement func(ctx context.Context) string
	// Contention is what else held the accelerator when the check ran. It is
	// supplied rather than probed here so this package stays pure logic: a
	// check that read the real host would make the result depend on whatever
	// happened to be open on the machine running the tests.
	Contention func(ctx context.Context) device.GPUContention
}

const doctorTextPrompt = "List the eight planets of the solar system in order from the Sun, one per line. No commentary."
const doctorJSONPrompt = "Return a JSON object with a single key \"planets\" whose value is an array of the eight planet names in order from the Sun."

// RunDoctor executes the health battery. Cheap by design: a few dozen short
// generations, so it is reasonable to run before trusting anything else.
func RunDoctor(ctx context.Context, c llm.Backend, model string, runs int, opts DoctorOpts) (DoctorResult, error) {
	if runs < 2 {
		runs = 2
	}
	r := DoctorResult{Model: model, Runs: runs}
	if ready, err := doctorRealToken(ctx, c, model, &r); !ready {
		return r, err
	}
	doctorPlacement(ctx, opts, &r)
	if err := doctorServedContext(ctx, c, model, &r); err != nil {
		return r, err
	}
	textDeterministic, err := doctorTextDeterminism(ctx, c, model, runs, &r)
	if err != nil {
		return r, err
	}
	if err := doctorJSONDeterminism(ctx, c, model, runs, textDeterministic, &r); err != nil {
		return r, err
	}
	doctorConfig(opts.Config, opts.ConfigObserved, &r)
	doctorGPUContention(ctx, opts, &r)
	finishDoctor(&r)
	return r, nil
}

// doctorGPUContention reports what else holds the accelerator before anyone
// spends minutes measuring against it.
//
// Memory another process is holding is memory this measurement does not get,
// and the difference does not announce itself: the run simply places fewer
// layers, or the allocation lands somewhere else, and the numbers come out
// describing a machine the operator did not think they were testing. Naming
// the processes turns that into a decision the operator can make in a few
// seconds.
//
// fitr does not close them. It may mutate or remove only what it created, and
// terminating an editor with unsaved work or somebody's training job would be
// a worse outcome than a contended measurement. The report ends at a next
// action, the way every other negative verdict here does.
func doctorGPUContention(ctx context.Context, opts DoctorOpts, r *DoctorResult) {
	if opts.Contention == nil {
		return
	}
	contention := opts.Contention(ctx)
	if !contention.Observed {
		addDoctorCheck(r, "gpu_contention", "SKIP",
			"no vendor tool reported accelerator memory; other work on the GPU is unmeasured")
		return
	}
	free := fmt.Sprintf("%.1f GiB free of %.1f GiB",
		float64(contention.FreeMiB)/1024, float64(contention.TotalMiB)/1024)
	if !contention.Busy() {
		addDoctorCheck(r, "gpu_contention", "PASS", free+"; nothing substantial is holding the GPU")
		return
	}
	detail := fmt.Sprintf("%s, so %.1f GiB is held by other work", free,
		float64(contention.UsedMiB)/1024)
	if names := contentionNames(contention); names != "" {
		detail += "; on the GPU now: " + names
	}
	if !contention.PerProcess {
		detail += "; this platform does not report per-process accelerator memory, " +
			"so the share each one holds is unknown"
	}
	detail += ". Close what you can spare and re-run, or measure as-is and read the " +
		"result as this machine under load"
	addDoctorCheck(r, "gpu_contention", "WARN", detail)
}

// contentionNames lists what a reader could act on. The serving runtime is
// excluded: it is the subject of the measurement, and suggesting it be closed
// would be advice that breaks the thing being measured. Processes the driver
// would not name are counted rather than printed, because a marker standing in
// for a name reads as a program that does not exist.
func contentionNames(contention device.GPUContention) string {
	var parts []string
	unnamed, runtimes := 0, 0
	for _, consumer := range contention.Top(12) {
		switch {
		case consumer.ServingRuntime():
			runtimes++
			continue
		case !consumer.Named():
			unnamed++
			continue
		}
		if consumer.UsedKnown {
			parts = append(parts, fmt.Sprintf("%s (%d, %.1f GiB)",
				consumer.Name, consumer.PID, float64(consumer.UsedMiB)/1024))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%d)", consumer.Name, consumer.PID))
	}
	if len(parts) > 5 {
		remaining := len(parts) - 5
		parts = append(parts[:5], fmt.Sprintf("and %d more", remaining))
	}
	if runtimes > 0 {
		parts = append(parts, "the serving runtime itself, which this measurement needs")
	}
	if unnamed > 0 {
		parts = append(parts, fmt.Sprintf("%d process(es) this session cannot identify", unnamed))
	}
	return strings.Join(parts, ", ")
}

func addDoctorCheck(r *DoctorResult, id, state, detail string) {
	r.Checks = append(r.Checks, DoctorCheck{ID: id, State: state, Detail: detail})
}

func doctorRealToken(ctx context.Context, c llm.Backend, model string, r *DoctorResult) (bool, error) {
	// 1. A real generated token, not an HTTP 200. Cold on purpose: load time
	// is part of what this box is.
	text, m, err := c.Generate(ctx, model, "Say OK.", ollama.Deterministic(8, numCtx(ctx)))
	if ollama.IsLocalityError(err) {
		return false, err
	}
	if err != nil {
		addDoctorCheck(r, "real_token", "FAIL", "generation failed: "+err.Error())
		r.Verdict = "the server is reachable but did not generate - nothing else is measurable"
		return false, nil //nolint:nilerr // ordinary generation faults remain explicit diagnostic findings
	}
	if strings.TrimSpace(text) == "" {
		addDoctorCheck(r, "real_token", "FAIL", fmt.Sprintf(
			"0 tokens of text produced (done_reason=%s) - a reachable server that does not infer", m.DoneReason))
		r.Verdict = "the server accepts requests but emits no tokens - fix the stack before measuring anything"
		return false, nil
	}
	addDoctorCheck(r, "real_token", "PASS", fmt.Sprintf(
		"generated %d token(s), TTFT %.2fs, load %.1fs", m.EvalCount, m.TTFTSeconds, m.LoadSeconds))
	return true, nil
}

func doctorPlacement(ctx context.Context, opts DoctorOpts, r *DoctorResult) {
	// 2. Where did the runtime place it? Placement defines comparability, but
	// does not by itself prove which subsystem limits performance.
	if opts.Placement != nil {
		place := opts.Placement(ctx)
		switch {
		case place == "GPU 100%":
			addDoctorCheck(r, "placement", "PASS", place)
		case strings.HasPrefix(place, "GPU "):
			addDoctorCheck(r, "placement", "WARN", place+
				" - runtime reported partial accelerator placement; compare only with the same placement")
		case place == "CPU":
			addDoctorCheck(r, "placement", "WARN",
				"CPU - runtime reported no accelerator offload; compare only with CPU-only runs")
		default:
			addDoctorCheck(r, "placement", "SKIP", "could not determine placement")
		}
	}
}

func doctorServedContext(ctx context.Context, c llm.Backend, model string, r *DoctorResult) error {
	// 3. Served context. A server can silently evaluate less prompt than you
	// sent; prompt_eval_count is the receipt. Nonce defeats the prefix cache.
	_, mp, err := c.Generate(ctx, model, buildLongPrompt("doctor-ctx-probe"), ollama.Deterministic(16, numCtx(ctx)))
	if ollama.IsLocalityError(err) {
		return err
	}
	// Cached prompt tokens count as served: on backends that report the split,
	// prompt_n alone under-reads a partially cached prompt.
	served := mp.PromptTokens + mp.CachedTokens
	switch {
	case err != nil:
		addDoctorCheck(r, "served_context", "FAIL", "long-prompt probe failed: "+err.Error())
	case served < 2000:
		addDoctorCheck(r, "served_context", "WARN", fmt.Sprintf(
			"a ~2.8k-token prompt evaluated as %d tokens - the server may be truncating or shrinking context; check OLLAMA_CONTEXT_LENGTH and num_ctx", served))
	default:
		addDoctorCheck(r, "served_context", "PASS", fmt.Sprintf("~2.8k-token prompt evaluated as %d tokens", served))
	}
	return nil
}

func doctorTextDeterminism(ctx context.Context, c llm.Backend, model string, runs int,
	r *DoctorResult) (bool, error) {
	// 4. Determinism, plain text. Identical request, N times, byte-compared.
	texts := make([]string, 0, runs)
	for range runs {
		t, _, err := c.Generate(ctx, model, doctorTextPrompt, ollama.Deterministic(64, numCtx(ctx)))
		if err != nil {
			return false, err
		}
		texts = append(texts, t)
	}
	return reportDeterminism(r, "determinism_text", texts,
		"greedy decoding reproduces on this stack"), nil
}

func doctorJSONDeterminism(ctx context.Context, c llm.Backend, model string, runs int,
	textDeterministic bool, r *DoctorResult) error {
	// 5. Determinism, grammar-constrained JSON mode. The constrained path is a
	// DIFFERENT code path and is known to break seed reproducibility on some
	// stacks while plain text reproduces.
	jsons := make([]string, 0, runs)
	samp := ollama.Deterministic(128, numCtx(ctx))
	samp.Format = "json"
	jsonOK := true
	for range runs {
		t, _, err := c.Generate(ctx, model, doctorJSONPrompt, samp)
		if ollama.IsLocalityError(err) {
			return err
		}
		if err != nil {
			addDoctorCheck(r, "determinism_json", "SKIP", "JSON-mode generation failed: "+err.Error())
			jsonOK = false
			break
		}
		jsons = append(jsons, t)
	}
	if jsonOK {
		jsonDeterministic := reportDeterminism(r, "determinism_json", jsons,
			"grammar-constrained output reproduces on this stack")
		if textDeterministic && !jsonDeterministic {
			last := &r.Checks[len(r.Checks)-1]
			last.Detail += " - plain text reproduces but JSON mode does not: the constrained decoding path " +
				"is the culprit (a known local-stack bug class); prefer prompt-level JSON when you need repeatability"
		}
	}
	return nil
}

func doctorConfig(config map[string]string, observed bool, r *DoctorResult) {
	// 6. Config red flags. Observed values come from the server log or an
	// owned launch. Unobserved empties are not the daemon's unset settings.
	if config == nil {
		return
	}
	var flags []string
	if v, err := strconv.Atoi(config["OLLAMA_NUM_PARALLEL"]); err == nil && v > 1 {
		flags = append(flags, fmt.Sprintf("OLLAMA_NUM_PARALLEL=%d divides the context between slots and adds batching variance", v))
	}
	if v, err := strconv.Atoi(config["OLLAMA_MAX_LOADED_MODELS"]); err == nil && v > 1 {
		flags = append(flags, fmt.Sprintf("OLLAMA_MAX_LOADED_MODELS=%d allows a second resident model to contaminate timings", v))
	}
	if len(flags) > 0 {
		addDoctorCheck(r, "config", "WARN", strings.Join(flags, "; "))
		return
	}
	label := orUnset
	if !observed {
		label = orUnobserved
	}
	addDoctorCheck(r, "config", "PASS", fmt.Sprintf("flash_attention=%s kv_cache_type=%s",
		label(config["OLLAMA_FLASH_ATTENTION"]), label(config["OLLAMA_KV_CACHE_TYPE"])))
}

func finishDoctor(r *DoctorResult) {
	r.Healthy = true
	warns := 0
	for _, ck := range r.Checks {
		switch ck.State {
		case "FAIL":
			r.Healthy = false
		case "WARN":
			warns++
		}
	}
	switch {
	case !r.Healthy:
		r.Verdict = "this stack cannot be measured fairly yet - fix the FAILs first"
	case warns > 0:
		r.Verdict = fmt.Sprintf("measurable, with %d caveat(s) worth knowing before trusting numbers", warns)
	default:
		r.Verdict = "healthy - measurements on this box mean what they say"
	}
}

// reportDeterminism folds N identical requests into one verdict. Returns
// whether all runs were byte-identical. Nondeterminism is a WARN, never a
// FAIL: repeats-and-intervals survive it, single-run numbers do not.
//
// A clean streak is reported with the exact upper bound on what it proves:
// zero divergences in n runs bounds the per-run divergence probability at
// 1 - 0.05^(1/n), not at zero. Five identical runs still admit a 45% rate.
func reportDeterminism(r *DoctorResult, id string, outputs []string, passWhy string) bool {
	identical, distinct, firstDiff := Divergence(outputs)
	if identical {
		bound := 100 * stats.ZeroEventUpperBound(len(outputs))
		r.Checks = append(r.Checks, DoctorCheck{ID: id, State: "PASS",
			Detail: fmt.Sprintf("%d/%d runs byte-identical - %s (bounds divergence <%.0f%% at 95%% CL; raise -n to tighten)",
				len(outputs), len(outputs), passWhy, bound)})
		return true
	}
	r.Checks = append(r.Checks, DoctorCheck{ID: id, State: "WARN",
		Detail: fmt.Sprintf("%d distinct output(s) across %d identical requests (first divergence at byte %d) - "+
			"single-run numbers on this box are noisier than they look; -k repeats matter more here",
			distinct, len(outputs), firstDiff)})
	return false
}

// Divergence reports whether all outputs are identical, how many distinct
// outputs there are, and the byte offset of the first difference from run 0.
func Divergence(outputs []string) (identical bool, distinct int, firstDiff int) {
	seen := map[string]bool{}
	for _, o := range outputs {
		seen[o] = true
	}
	distinct = len(seen)
	if distinct <= 1 {
		return true, distinct, -1
	}
	firstDiff = -1
	base := outputs[0]
	for _, o := range outputs[1:] {
		if o == base {
			continue
		}
		n := min(len(base), len(o))
		d := n // differ by length only
		for i := range n {
			if base[i] != o[i] {
				d = i
				break
			}
		}
		if firstDiff == -1 || d < firstDiff {
			firstDiff = d
		}
	}
	return false, distinct, firstDiff
}

func orUnset(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}

func orUnobserved(s string) string {
	if s == "" {
		return "(unobserved)"
	}
	return s
}
