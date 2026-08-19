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

- `local-inference-lab/llm-inference-bench` (69*) — Wilson intervals **and**
  exact McNemar paired significance tests, plus live GPU temp / SM-util / watts
- `uncSoft/anubis-oss` (200*, Apple-only) — the current bar. Returns
  `group_mean / stdev / ci_low / ci_high` for tok/s, TTFT **and watts-per-token**,
  2-20 reps with 95% bootstrap CIs, a seed toggle separating hardware variance
  from sampler variance, and a `methodology_version` column
- `Inspect AI` — clustered standard errors

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
   or inflight cloud mitigation. **First-of-kind in a local harness** — and the
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
  answer to cross-device comparison than our flat refusal** — it extracts signal
  where a shared configuration exists instead of discarding it.
- **`gpustack/gguf-parser-go`** (286*, Go, active) — vendor this rather than
  rebuilding memory-fit estimation.
- **`Blackwellboy/model-serving-minefield`** (86*) — a registry of serving-path
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
| `whichllm` | 6,345 | Owns the query "which local LLM for my hardware" — and **never runs a timed inference.** Pure bandwidth roofline. |
| `PocketPal` | 7,970 | Mobile-only. Best comparison methodology in the field. |
| `BenchLocal` | 408 | |
| `aidatatools` | 384 | 29,806 submissions from the worst methodology found: scrapes `ollama run --verbose`, **deliberately discards prompt eval rate**, no warmup, no repeats, no dedupe, mixes Ollama 0.19-0.32 in one ranking. Volume is not validity. |
| `tool-eval-bench` | 296 | |
| `anubis-oss` | 200 | The statistical bar. |
| `bench-loop` | 44 | Closest functional twin. |

## Caveat on sourcing

Reddit's API was blocked for both completed research agents, so r/LocalLLaMA
sentiment is absent from this picture. HN and GitHub evidence stands on its own.
