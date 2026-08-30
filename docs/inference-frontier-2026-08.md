# What a local-eval tool must measure to be current, August 2026

Research pass on the inference frontier. Sources are primary: llama.cpp /
vLLM / SGLang / Ollama / MLX repos at master, HF model cards, arXiv, vendor
docs. Verified 2026-08-18.

The value of this document is mostly **negative**: it lists assumptions `fitr`
holds that are no longer true.

## The twelve dated assumptions

Ranked by how badly each invalidates results, not by effort to fix.

| # | Dated assumption | 2026 reality |
|---|---|---|
| 1 | tok/s is a property of the model | It is a property of **speculative-decoding config**. llama.cpp has 10 backends behind `--spec-type`. Muse Glimmer + DFlash: **74.9 -> 233.4 tok/s (3.1x)**, identical output. A decode number without spec-type and acceptance rate is meaningless. |
| 2 | One TTFT number | Loaded/uncached and prefix-cache-hit TTFT differed by **70-200x** in the cited Strix Halo 16k-prompt case (41 s -> 0.2 s). Runtime-unloaded latency is a third protocol. Blending these states can dominate the reported number. |
| 3 | "Thinking" is a boolean | Graded `reasoning_effort` everywhere, with **mutually incompatible vocabularies** (`low/medium/xhigh` Qwen, `low/high/max` DeepSeek and Kimi, `no_think/low/high` Hunyuan, `enabled/adaptive/disabled` MiniMax). Kimi K3 **cannot** disable thinking. |
| 4 | Reasoning content is cosmetic | Kimi K3 and Poolside Laguna **require** `reasoning_content` round-tripped across turns or they degrade. llama.cpp has `--reasoning-preserve` and a `supports_preserve_reasoning` capability flag. FitR now preserves it across turns. |
| 5 | Ollama is the local runtime | llama-server is a strict superset: `/v1/responses`, Anthropic `/v1/messages`, router mode, built-in tools, an MCP stdio client, Prometheus spec-decode counters, per-slot state. |
| 6 | 20 turns is an agentic loop | little-coder caps Terminal-Bench at **40**; GDPval-AA v2 allows **250**. 20 turns measures early-abort behaviour. |
| 7 | Quant = Q4_K_M vs Q8_0 | GGUF now has **NVFP4 (40), MXFP4 (39), Q1_0 (41), Q2_0 (42)**. PrismML Bonsai-27B runs on an iPhone at **1.125 bpw retaining 90%** of baseline. |
| 8 | KLD/perplexity ranks quants | arXiv:2606.19558 tested **14 fidelity metrics**; correlation with real quality **collapses to rho ~ 0.00 in the near-baseline "silent zone"** -- exactly where you choose between Q4_K_M and UD-Q4_K_XL. They are damage detectors, not rankers. |
| 9 | Resident memory at 32K is comparable | Hybrid linear attention (Qwen3.6/3.8 Gated DeltaNet, Gemma 4 iSWA, DeepSeek V4 at **10% of V3.2 KV**) broke the linear KV model. |
| 10 | Text-only harness | Qwen3.5/3.6/3.8, Gemma 4, Kimi K3, MiniMax M3, Mistral Small 4, Muse Glimmer all ship `image-text-to-text`. llama-server accepts image, **audio** and **video**. |
| 11 | Greedy decoding is reproducible | llama.cpp **#25618 (open)**: `draft-mtp` / `draft-dspark` / `draft-dflash` are **not lossless at temp=0 on quantized targets**. ngram paths are safe. |
| 12 | Accuracy is the metric for quantization | INT3 AWQ inflates Qwen3-4B chain-of-thought by **+276%** at under 4pt accuracy loss (arXiv:2606.25519). Up to **52% of quantized-model failures are overthinking errors** (arXiv:2606.00206). Measure **tokens-to-correct-answer**. |

## Checked against our code

| Claim | Our status |
|---|---|
| Ollama context default is VRAM-tiered (under 24 GiB -> **4k**), so an unset `num_ctx` silently truncates | **Safe** - `num_ctx` is pinned explicitly on every call |
| Ollama v0.32.10 changed default `repeat_penalty` **1.1 -> 1.0** | **Was exposed.** Now pinned at 1.0. See below |
| Results can silently merge across runtime versions | **Safe** - Ollama version is part of `Fingerprint.Key()` |
| `reasoning_content` must be round-tripped in multi-turn | **Fixed** - the agentic transcript preserves and replays it |
| 20 turns is short | **Fixed** - `--full` now uses a 40-turn floor |

### Why `repeat_penalty` is now pinned to 1.0

A repetition penalty is precisely the mechanism that **hides** looping. llama.cpp
ships DRY, XTC and repeat-penalty samplers that suppress loops but never report
them. `fitr` measures whether this model, at this quant, on this hardware
degenerates -- masking that with a sampler measures the sampler.

Pinning it also removes a version dependency: leaving it unset made our headline
metric depend on which Ollama the user happened to have.

**This is also the answer to "just raise the temperature."** We measure at
temp 0, top_k 1, fixed seed, repetition penalty off, because that is the
configuration that exposes whether a model / quant / hardware combination
degenerates at all. Sampler settings that paper over it are a mitigation, not a
refutation -- and the loop is still there when someone runs at temp 0 for
reproducible tool calls.

## Silent-failure class -- fix before trusting any more results

1. **Probe the served context window by generating at depth.** Never trust the
   model card, `/health`, `lms ps`, or a VRAM-tiered default.
2. **Require a real generated token in preflight, not HTTP 200.** Documented
   case: Qwen3.8-27B on an 8 GB RTX 5070 Laptop at 32k with `-ngl 20` passes
   `/health` and produces **zero tokens** -- it fits in VRAM with no room to
   compute.
3. **Split loaded/uncached and prefix-cache-hit TTFT** using `timings.cache_n` /
   `usage.prompt_tokens_details.cached_tokens`, never wall-clock ordering.
   Record runtime-unloaded latency separately with load and residency evidence.
4. **Record the resolved config, not the requested one.** `-fa auto` resolves
   differently per backend; llama.cpp `--fit` is **on by default** and silently
   mutates `-ngl` / `-c` / `-ncmoe` per machine.
5. **Round-trip `reasoning_content`** in the agentic loop and report the A/B
   delta against dropping it.
6. **Assert reasoning toggles actually take effect** (monotonic reasoning-token
   count across effort levels). Silently no-op toggles are the most common
   local-stack bug of 2026 -- LM Studio users report Qwen3.5/3.6 honouring
   *none* of `/no_think`, `enable_thinking: false`, or `reasoning_effort`.

## Metric changes

| Current | Change to |
|---|---|
| decode tok/s | + spec-type, **draft acceptance rate and per-position alpha_k**, at 3 or more context depths |
| TTFT | **runtime-unloaded / loaded-uncached / prefix-cache-hit / time-to-first-answer token** (post-thinking) |
| prefill tok/s | + measured-vs-roofline efficiency. Prefill and decode stress different resources; counters or controlled interventions are required before classifying either bottleneck. |
| resident memory at 32K | + peak RSS across the run; note SWA/hybrid models break linear KV scaling |
| coding tasks | + **tokens-to-correct-answer** |
| 40-turn agentic | + A/B on preserved reasoning and tool selection under **10-25 tools** (MCP-Atlas shape); observed prompt-token contraction is already recorded, but it is not yet a compaction receipt |
| degeneracy | + **overthinking-error rate** (correct answer reached in an intermediate step, then lost) and truncation rate |
| refusal | + **prompt-injection resistance** (`strongreject` ships in Harbor) |
| - | **new:** schema adherence - free-form vs `json_schema` vs `structural_tag`, reporting valid rate, semantic accuracy, tok/s, and **grammar compile time separately** |
| - | **new:** vision / audio capability probe on models we treat as text-only |

Adopt vLLM vocabulary -- **TTFT / TPOT / ITL / E2EL / goodput**, P50 not mean --
so numbers are comparable across tools.

## The hardware table that justifies separating prefill from decode

gpt-oss-120b, MXFP4:

| System | Memory | Bandwidth | Prefill | Decode |
|---|---|---|---|---|
| DGX Spark (GB10) | 128 GB | 273 GB/s | **1,723 tok/s** | 38.6 tok/s |
| Strix Halo | 128 GB | 273 GB/s | **340 tok/s** | 34.1 tok/s |
| Mac Studio M3 Ultra | 256 GB | **819 GB/s** | - | **70.8 tok/s** |
| 3x RTX 3090 | 72 GB | - | 1,642 tok/s | **124 tok/s** |

**Identical bandwidth, 5x different prefill.** Any tool reporting one number, or
ranking machines on decode alone, misleads everyone doing agentic or
long-prompt work.

Also: decode degrades **23-28% from empty to 76k context**. A single
empty-context number overstates real use by roughly a quarter.

## Why llama-server was the next backend, not vLLM

Same audience as Ollama, strictly larger API surface, and it uniquely provides
what Ollama cannot:

- `timings.cache_n` -- prefix-cache hit rate, so loaded/uncached and prefix-cache-hit TTFT can be separated
- `/props.chat_template_caps` -- a real capability probe
- `/metrics` -- Prometheus spec-decode counters
- `logprobs`, `tool_choice`, GBNF and `json_schema`
- `/slots` per-slot state

**Ollama OpenAI-compat gaps that silently corrupt evals:** no `logprobs`, no
`tool_choice`, no `logit_bias`, no `n`, base64-only images. Remember llama-server
needs `--jinja` or tool calling silently does not work.

Then build **one generic OpenAI adapter plus a capability probe**, not N bespoke
clients -- roughly 10 of 12 relevant runtimes are OpenAI-shaped. Do not hardcode
ports: Lemonade moved to **13305**, Foundry Local is dynamic by design.

## Do not rebuild what exists

- **Harbor** (`uv tool install harbor`) is the official Terminal-Bench 2.0
  runner and carries a 75-dataset registry that is a ready-made cost ladder:
  `hello-world` (1) -> `terminal-bench-sample` (10) -> `compilebench` (15) ->
  `bfcl_parity` (123) -> `strongreject` (150) -> `swebench-verified` (500).
  Shell out to it; reserve our own tests for the plumbing and config failures
  Harbor does not look at -- which is exactly where we differentiate.
- **`llama-fit-params`** already prints the optimal `-ngl` / `-c` / `-ot` for a
  host free memory. Call it or replicate it, then benchmark *that* config.
- **`llama-results --check`** gives NMSE-based regression detection at 1e-6 when
  a build or backend changes model outputs.
- **`ollama/cmd/bench`** is Go, in-tree, benchstat-compatible. Direct prior art
  in our own language -- interoperate or differentiate deliberately.

## Warnings worth surfacing to users

1. `--draft-max` / `--draft-min` are **removed** in llama.cpp.
2. `--fit on` (**the default**) silently mutates the config you think you are measuring.
3. `-fa auto` resolves differently per backend.
4. **Greedy spec decoding is not lossless on quantized targets** (#25618, open).
5. ngram speculation inflates warm-repeat benchmarks -- a documented "repetition artifact."
6. `-sps 0.10` (the default) causes pathological slot thrashing in multi-turn
   agentic workloads. Community guidance: raise to **0.6-0.9**.
7. `-np N` without `-kvu` **divides `-c` by N** -- your "256K benchmark" was 32K.
8. `-sm tensor` is incompatible with quantized KV, `-fa off`, `--fit`, backend
   sampling, and roughly 22 architectures including most 2026 MoE and hybrids.
9. SWA models have lossy slot save/restore and disabled prompt cache on
   FULL-only backends.
10. **Never report acceptance rate without throughput.** Measured and
    reproducible: Qwen3.6-35B-A3B with a Qwen3.5-0.8B draft on one RTX 3090 --
    **100% acceptance, -12% throughput** across all 19 configs. On a 3B-active
    MoE each drafted token pulls a fresh expert slice.
