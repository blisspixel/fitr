# Competitive landscape, August 2026

Research pass on "what already exists" before committing roadmap effort.
Findings that **contradict** earlier assumptions are listed first, because those
are the ones that change what we build.

## Assumptions that turned out to be wrong

### 1. Device-aware refusal is not novel

We believed refusing to rank across hardware fingerprints was the differentiator.
It is an emerging consensus:

- `homebench` warns on cross-machine `diff`
- `bench-loop` refuses to score a remote endpoint on a local curve
- `localbench.ai` keeps Ollama and LM Studio results separate, explicitly to
  avoid "false apples-to-oranges comparisons"
- `PocketPal` only matches devices that ran the same model at the same config
- `llmfit` keys measurements to identical hardware

**We are joining a consensus, not founding one.** Lead with something else.

### 2. Statistical rigor is table stakes, not an edge

- `local-inference-lab/llm-inference-bench` (69*) - Wilson intervals **and**
  exact McNemar paired significance tests, plus live GPU temp / SM-util / watts
- `uncSoft/anubis-oss` (200*, Apple-only) - the current bar. Returns
  `group_mean / stdev / ci_low / ci_high` for tok/s, TTFT **and watts-per-token**,
  2-20 reps with 95% bootstrap CIs, a seed toggle separating hardware variance
  from sampler variance, and a `methodology_version` column
- `Inspect AI` - clustered standard errors

Keep the rigor. Stop selling it.

### 3. Tool-calling plumbing is contested

`SeraphimSerapis/tool-eval-bench` (296*, active) covers 69 deterministic
scenarios across serving stacks, including a **Restraint & Refusal** category
and a 52-tool selection test.

Its stated policy: *"Infrastructure failures are not scored. A timeout,
connection error, or persistent 429/5xx measures the serving environment, not
the model, so those scenarios are dropped from both the numerator and the
denominator."*

**The surviving edge is narrow but real: they exclude plumbing failures to get a
clean model score. We diagnose them.** Nobody turns "your chat template renders
tool definitions wrong" into an actionable finding.

### 4. k=3 is probably not enough

`MikeVeerman/tool-calling-benchmark` (122*) scores Action 40% / Restraint 30% /
Wrong-Tool-Avoidance 30%, and concluded from 20 runs per prompt that
**"3-run majority voting was inadequate."** This is a direct warning about our
default. Either raise k, or state the limitation louder than we currently do.

## What survives as genuinely ours

In order of defensibility:

1. **Degenerate / looping output detection.** Searched six ways; four query
   formulations returned zero repos. Not in promptfoo's assertion list.
   llama.cpp ships DRY/XTC/repeat-penalty samplers that *suppress* loops but
   never *report* them. Prior art is academic (`ari-holtzman/degen`, ICLR 2020)
   or inflight cloud mitigation. **First-of-kind in a local harness** - and the
   failure is hardware-specific: llama.cpp has open 2026 issues for garbled
   output tied to dual-GPU CUDA, batch size 512, Vulkan on specific gfx IDs,
   quantized KV cache, and long agentic sessions. This is the headline.
2. **Diagnosing plumbing rather than excluding it.**
3. **Memory watermark at 32k-128k under sustained agentic load.** Anubis has
   peak memory, ollama's `cmd/bench` has VRAM+CPU-spill, homebench has RSS - but
   no long-context memory sweep exists above 0 stars.
4. **Go, single static binary.** Confirmed empty: `rig-rank` (20*) died after
   three weeks in Feb 2026; `eval-dev-quality` (186*, Go) dormant 15 months.
5. **Cross-vendor NPU.** MLPerf Client v2.0 (shipped 2026-08-18) covers
   Qualcomm / AMD XDNA2 / Intel NPU paths but publishes **no results database**.

## Ideas worth stealing

- **PocketPal's Glicko-2 pairwise matchups.** 10,989 submissions across 364
  devices. Only devices running the same model at the same config are compared;
  sorted by conservative rating (rating - 2*RD). This is a **more principled
  answer to cross-device comparison than our flat refusal** - it extracts signal
  where a shared configuration exists instead of discarding it.
- **`gpustack/gguf-parser-go`** (286*, Go, active) - vendor this rather than
  rebuilding memory-fit estimation.
- **`Blackwellboy/model-serving-minefield`** (86*) - a registry of serving-path
  traps that produce "confidently wrong measurements." Directly relevant to our
  own hard-won bugs (prompt-cache contamination, concurrent residency).

## Vacancies

- **LocalScore is dead.** Maintainer, publicly, three times: *"I have no time to
  maintain LocalScore... the version of llama.cpp distributed is over a year old
  and there is already 30%+ performance improvements... There's a chance we will
  have to deprecate the binary itself."* llamafile 0.10.x removed it from the
  build entirely; the directory is orphaned dead code. Last binary 2025-05-14.
  Known-invalid pre-0.9.3 Windows CUDA rows (~2.1x too slow) are still ranked in
  the public database. It is a vacancy with Mozilla branding on it.
- **Dubesor** retired 2026-04-19 (347 models, 83 manual tasks).
- **oobabooga's leaderboard** frozen at 868 models; its named successor
  published 10 posts in April 2026 and nothing since.

**The community's quality references for local, quantized models are all gone.**

## The competition, sized

| Tool | Stars | Note |
|---|---|---|
| `llmfit` | 32,739 | The gorilla. Rust, releases every 1-2 weeks. |
| `whichllm` | 6,345 | Owns the query "which local LLM for my hardware" - and **never runs a timed inference.** Pure bandwidth roofline. |
| `PocketPal` | 7,970 | Mobile-only. Best comparison methodology in the field. |
| `BenchLocal` | 408 | |
| `aidatatools` | 384 | 29,806 submissions from the worst methodology found: scrapes `ollama run --verbose`, **deliberately discards prompt eval rate**, no warmup, no repeats, no dedupe, mixes Ollama 0.19-0.32 in one ranking. Volume is not validity. |
| `tool-eval-bench` | 296 | |
| `anubis-oss` | 200 | The statistical bar. |
| `bench-loop` | 44 | Closest functional twin. |

## Caveat on sourcing

Reddit's API was blocked for both completed research agents, so r/LocalLLaMA
sentiment is absent from this picture. HN and GitHub evidence stands on its own.

---

# Final verdict (supersedes the sections above)

The research went through three rounds of self-correction. Where this section
disagrees with anything above, this section is right. The earlier text is kept
because the corrections are more informative than a clean answer would be.

## Further corrections

### Plumbing-vs-capability has more prior art than stated above

The canonical metric predates all of this: **Aider's
`percent_cases_well_formed`** - literally "did the model emit parseable edits,"
reported separately from whether tests passed, alongside
`num_malformed_responses`, `syntax_errors`, `indentation_errors`,
`exhausted_context_windows`, `lazy_comments` and `test_timeouts`. Also:

- **Inspect AI** has a typed `ToolCallError` with a first-class `"parsing"`
  variant, alongside `timeout`, `permission`, `limit`, `sandbox_unavailable`.
- **Harbor** classifies errors into a taxonomy including
  `AgentSafetyRefusalError`, whose docstring states the goal in our own words:
  keeping *"a legitimate model refusal (a real `reward 0` outcome) from reading
  as an unknown/flaky API error."*

**But here is the crack, and it is the one that matters:** Harbor's taxonomy
keys on *provider* error strings - Anthropic and OpenAI rate-limit and
content-filter messages. Point it at a local vLLM or llama-server and
`exception_stats` comes back near-empty.

> The industry's plumbing taxonomy is built for cloud APIs and goes blind on
> local backends.

That is the defensible version of the claim.

### Refusal rate is not a differentiator

promptfoo ships an `is-refusal` assertion, Inspect counts refusals natively,
Harbor has `AgentSafetyRefusalError`. Keep it as a **need axis** - it is one  - 
but drop it from any novelty claim.

### Statistics: split the verdict, do not drop it

The earlier "drop statistical rigor entirely" was too blunt. Accurately:

- Local **performance** tools do compute CIs: Inspect AI (clustered/bootstrap
  stderr, epochs), Anubis (95% bootstrap on tok/s, TTFT, watts/token),
  `llm-inference-bench` (Wilson + exact McNemar), `quant-toolcall-bench`
  (bootstrapped paired deltas; prints *"No (all CIs cross 0)"*).
- The big **agentic quality** benchmarks do not. **Every ± on tbench.ai,
  frontierbench.ai and eqbench.com is computed leaderboard-side.** To get error
  bars on Terminal-Bench or SWE-bench you must run `-k >= 5` and compute them
  yourself from per-trial JSON. Harbor's `n_attempts` defaults to **1**, and its
  `pass@k` helper never emits `pass@1`.

**So: statistically-honest quality eval on local agentic tasks is genuinely
underserved. Just never claim Wilson intervals as novel.**

## The unexpected moat: local agentic eval is mechanically broken

Getting any major agentic benchmark to run against a local model is a minefield.
This is *why* nobody publishes local agentic numbers:

- **Harbor** - Terminus-2 runs on the host so `localhost` works, but
  `claude-code`, `codex` and `openhands` run *inside* the container, and the
  compose templates ship **no `extra_hosts`**, so `host.docker.internal` fails
  on Linux. `hosted_vllm/` also rejects model names containing more than one `/`.
- **BFCL** - `--model` must be a registered key with a hand-written handler. The
  generic `QuickTestingOSSHandler` is **imported but attached to zero registry
  entries**; an unlisted local model requires editing source.
- **SWE-bench** - its inference module is fossilized at 12 hardcoded early-2024
  models with **no `--base_url` flag**. Usable only as a grader.
- **Aider** - silently truncates unless `OLLAMA_CONTEXT_LENGTH` is set; its own
  docs warn Ollama defaults to 2k and *"silently discards context that exceeds
  the window."*
- **Harbor GPU tasks** - impossible locally: `EnvironmentCapabilities.gpus` is
  never set by the Docker environment.

> If `fitr` makes a 20-turn agentic task against a local endpoint work in one
> command, that alone is worth the project - independent of any scoring
> innovation.

**`fitr run <model> --full` already does this.** It is a shipped capability that
was not recognised as a differentiator.

## More dead references

**LiveBench's public data is frozen at 2024-11-25** - 1,436 questions, all
parquets `lastModified 2025-04-07`, README conceding *"not all questions for
this release are public."* Its `agentic_coding` categories are not publicly
runnable. A contamination-free benchmark that is publicly 21 months stale is
contamination-*exposed* for every 2026 model.

The dead list: LiveBench, Dubesor (retired 2026-04), oobabooga's board (frozen),
LocalBench Substack (10 posts, all April 2026), Aider's leaderboard (last
updated Nov 2025), EQ-Bench 1-3, LocalScore.

Live successors: **Terminal-Bench 3.0** (2026-07-23, 74 tasks, 510*) and
**EQ-Bench 4** - the only self-runnable harness publishing 95% CIs.

## Do not claim these

Prefill/decode separation · device fingerprinting and per-hardware leaderboards ·
refusal rate · Wilson/bootstrap CIs as a concept · "speed + memory + quality in
one command" (homebench, 2026-08-03) · well-formedness as a metric (Aider) ·
typed parsing errors (Inspect AI).

## Genuinely ours, final ordering

1. **Repetition / looping detection.** Zero implementations. Two failed upstream
   llama.cpp PRs ([#22007](https://github.com/ggml-org/llama.cpp/pull/22007)
   stalled, [#26039](https://github.com/ggml-org/llama.cpp/pull/26039) closed)
   and maintainer resistance on principle, which guarantees the gap persists.
   DeepEval's `AgentLoopDetection` compares *reasoning steps*, not token output.
   The failures are hardware-specific: garbled on dual-GPU CUDA but not single,
   batch 512 but not 1024, Vulkan/gfx1150, quantized KV, long agentic sessions.
   **Prepare an answer to "just raise the temperature."**
2. **Quant x structured-output validity, per machine.** Prose survives
   quantization; JSON does not. KLD has a documented "silent zone" that cannot
   rank near-baseline quants. Measured evidence exists (23-point drop at Q4_K_M
   for Llama-family, Qwen3 unmoved) - at 1 star.
3. **A plumbing taxonomy built for local backends**, where cloud-error-string
   taxonomies go blind.
4. **Local agentic eval that actually runs.**
5. **Statistically-honest local agentic quality**, since the big harnesses
   default to `n=1` and compute error bars leaderboard-side.
6. **Go static binary** and **cross-vendor NPU**.

## Positioning, final form

> `llmfit` tells you what fits.
> Leaderboards tell you what's smart on someone else's machine.
> **`fitr` tells you what's silently broken on yours** - the Q4 that still
> writes clean prose but emits malformed tool calls, the parser that swallows
> them, the loop your GPU triggers and nobody else's does.
