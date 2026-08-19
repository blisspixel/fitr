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
	// Placement reports where the loaded model actually computes, e.g.
	// "GPU 100%", "GPU 62%", "CPU". Called after the model is warm.
	Placement func(ctx context.Context) string
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
	add := func(id, state, detail string) {
		r.Checks = append(r.Checks, DoctorCheck{ID: id, State: state, Detail: detail})
	}

	// 1. A real generated token, not an HTTP 200. Cold on purpose: load time
	// is part of what this box is.
	text, m, err := c.Generate(ctx, model, "Say OK.", ollama.Deterministic(8, NumCtx))
	if err != nil {
		add("real_token", "FAIL", "generation failed: "+err.Error())
		r.Verdict = "the server is reachable but did not generate - nothing else is measurable"
		return r, nil
	}
	if strings.TrimSpace(text) == "" {
		add("real_token", "FAIL", fmt.Sprintf(
			"0 tokens of text produced (done_reason=%s) - a reachable server that does not infer", m.DoneReason))
		r.Verdict = "the server accepts requests but emits no tokens - fix the stack before measuring anything"
		return r, nil
	}
	add("real_token", "PASS", fmt.Sprintf("generated %d token(s), TTFT %.2fs, load %.1fs", m.EvalCount, m.TTFTSeconds, m.LoadSeconds))

	// 2. Where does it actually compute? Partial offload quietly turns a GPU
	// benchmark into a RAM-bandwidth benchmark.
	if opts.Placement != nil {
		place := opts.Placement(ctx)
		switch {
		case place == "GPU 100%":
			add("placement", "PASS", place)
		case strings.HasPrefix(place, "GPU "):
			add("placement", "WARN", place+" - partial offload; decode is bound by system RAM bandwidth, not the GPU")
		case place == "CPU":
			add("placement", "WARN", "CPU - no offload; expect a fraction of the GPU numbers this model gets elsewhere")
		default:
			add("placement", "SKIP", "could not determine placement")
		}
	}

	// 3. Served context. A server can silently evaluate less prompt than you
	// sent; prompt_eval_count is the receipt. Nonce defeats the prefix cache.
	_, mp, err := c.Generate(ctx, model, buildLongPrompt("doctor-ctx-probe"), ollama.Deterministic(16, NumCtx))
	// Cached prompt tokens count as served: on backends that report the split,
	// prompt_n alone under-reads a partially cached prompt.
	served := mp.PromptTokens + mp.CachedTokens
	switch {
	case err != nil:
		add("served_context", "FAIL", "long-prompt probe failed: "+err.Error())
	case served < 2000:
		add("served_context", "WARN", fmt.Sprintf(
			"a ~2.8k-token prompt evaluated as %d tokens - the server may be truncating or shrinking context; check OLLAMA_CONTEXT_LENGTH and num_ctx", served))
	default:
		add("served_context", "PASS", fmt.Sprintf("~2.8k-token prompt evaluated as %d tokens", served))
	}

	// 4. Determinism, plain text. Identical request, N times, byte-compared.
	texts := make([]string, 0, runs)
	for i := 0; i < runs; i++ {
		t, _, err := c.Generate(ctx, model, doctorTextPrompt, ollama.Deterministic(64, NumCtx))
		if err != nil {
			return r, err
		}
		texts = append(texts, t)
	}
	textDeterministic := reportDeterminism(&r, "determinism_text", texts,
		"greedy decoding reproduces on this stack")

	// 5. Determinism, grammar-constrained JSON mode. The constrained path is a
	// DIFFERENT code path and is known to break seed reproducibility on some
	// stacks while plain text reproduces.
	jsons := make([]string, 0, runs)
	samp := ollama.Deterministic(128, NumCtx)
	samp.Format = "json"
	jsonOK := true
	for i := 0; i < runs; i++ {
		t, _, err := c.Generate(ctx, model, doctorJSONPrompt, samp)
		if err != nil {
			add("determinism_json", "SKIP", "JSON-mode generation failed: "+err.Error())
			jsonOK = false
			break
		}
		jsons = append(jsons, t)
	}
	if jsonOK {
		jsonDeterministic := reportDeterminism(&r, "determinism_json", jsons,
			"grammar-constrained output reproduces on this stack")
		if textDeterministic && !jsonDeterministic {
			last := &r.Checks[len(r.Checks)-1]
			last.Detail += " - plain text reproduces but JSON mode does not: the constrained decoding path " +
				"is the culprit (a known local-stack bug class); prefer prompt-level JSON when you need repeatability"
		}
	}

	// 6. Config red flags. The values come from the server log when available,
	// so they are what the server actually started with.
	if opts.Config != nil {
		var flags []string
		if v, err := strconv.Atoi(opts.Config["OLLAMA_NUM_PARALLEL"]); err == nil && v > 1 {
			flags = append(flags, fmt.Sprintf("OLLAMA_NUM_PARALLEL=%d divides the context between slots and adds batching variance", v))
		}
		if v, err := strconv.Atoi(opts.Config["OLLAMA_MAX_LOADED_MODELS"]); err == nil && v > 1 {
			flags = append(flags, fmt.Sprintf("OLLAMA_MAX_LOADED_MODELS=%d allows a second resident model to contaminate timings", v))
		}
		if len(flags) > 0 {
			add("config", "WARN", strings.Join(flags, "; "))
		} else {
			add("config", "PASS", fmt.Sprintf("flash_attention=%s kv_cache_type=%s",
				orUnset(opts.Config["OLLAMA_FLASH_ATTENTION"]), orUnset(opts.Config["OLLAMA_KV_CACHE_TYPE"])))
		}
	}

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
	return r, nil
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
		for i := 0; i < n; i++ {
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
