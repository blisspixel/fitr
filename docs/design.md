# Design: what fitr believes and why

A new model lands every few days. The datacenter number is not your number.
The post skipped the quant. The leaderboard ran on someone else's GPU. fitr
is being built to determine what local AI actually works for a declared
workload on this machine, what evidence proves it, and which configuration
gives the best validated outcome. FIT, behavior, and burst performance ship
today. Explanation, validated work, tradeoffs, and workload coverage are the
pre-1.0 direction.

The default path is five commands (`fitr` → `advise` → `run` → `apply` → `board`).
Bare `fitr` is the installed inventory: measured, unproven, incompatible, or
stale, each with one next command. Architecture, when known, adds a compact
context-fit graph. A measured non-default ctx asks `apply` until the server
is already serving that window. `fitr <model>` is named advise. Unmeasured
is a candidate, never a recommendation.

The interior is strict so that surface can be trusted. You should not need
the vocabulary below to get through Thursday night. You should be able to
see it when you look.

The current product loop establishes three layers: FIT, BEHAVIOR, and burst
PERFORMANCE. The pre-1.0 work adds EXPLAIN, VALIDATED WORK, TRADEOFFS, and
COVERAGE. Those are separate evidence views, not ingredients in one score. A
configuration can be fast and unreliable, slow and dependable, or proven for
extraction while still unproven for a repository workflow.

fitr's verdicts rest on five commitments.

## 1. A score is meaningless without the device it was measured on

The *same model*, *same tag*, on the *same laptop* went from "crashes on
load" to "daily driver" because of a GPU driver update. Nothing about the
model changed.

Every result embeds a fingerprint: OS, CPU, RAM, GPU, driver, serving runtime
and version, inference placement, and the config that actually moves numbers
(`OLLAMA_FLASH_ATTENTION`, `OLLAMA_KV_CACHE_TYPE`). `fitr board` groups by
fingerprint and **refuses to rank across groups**. Requested context and the
runtime-reported effective allocation are separate facts in fingerprint v2;
an unverified effective context is visible in History but cannot enter a
ranking. Change the driver and your old numbers are explicitly void.

## 2. "Is it good" is unanswerable. "Does it serve need X" is answerable.

Independent needs, each with its own gate. A model can serve one brilliantly
and fail another, and one number cannot say that.

| Need | Why separate |
|---|---|
| **fast + pretty good** | responsiveness; decode plus uncached, already-loaded TTFT when gated-request residency and cache state are proven |
| **great coding / reasoning** | computed-answer checks today; executable assertions stay INCONCLUSIVE until isolated |
| **valid structured output** | quantization breaks JSON before prose - the earliest damage signal |
| **follows exact instructions** | verifiable constraints, graded by code |
| **no filtering / low refusal** | a first-class need, not a footnote |
| **calls tools correctly** | measured in the tool channel, not as text. Asking for "the JSON arguments" and handing over a tool it must call are different skills, and the second exercises the chat template and the runtime's tool-call parser too |
| **works unattended (agent)** | a bounded multi-turn tool loop whose behavior, context, and performance are reported separately |
| **leaves unused tools alone** | restraint at rest (no calls on an unrelated question) and under change (a tool vanishes mid-loop) |
| **keeps a small footprint** | resident bytes after a requested 32K load probe, only when the runtime confirms that effective context |
| **reads images** | a capability, not a grade |
| **no degenerate output** | five independent loop/repetition signals; length correlates negatively with quality |
| **your tasks** | `~/.fitr/tasks/*.json` - the built-ins are defaults, your work is the point |

Verdicts are **PASS / FAIL / INCONCLUSIVE / SKIP / n/a / BLKD**. `SKIP` means
not measured. `INCONCLUSIVE` means an observation exists but its integrity or
uncertainty cannot support either binary claim.
`n/a` means the model never claimed it - a text-only model is not *bad at
vision*. `BLKD` means we could not fairly test it.

## 3. A single run is not a measurement

In one repeated local battery, identical configurations moved by 10 to 20
percentage points between runs. The same coding task passed on one run and
failed on the next with no declared configuration change. That observation is
not a universal variance estimate; it is why one trial cannot establish a
rate.

So tasks repeat (`-k 3`), each need carries its own family-aware interval, and
`fitr compare` says "cannot separate" rather than inventing a winner. A
single pass gives a Wilson interval of `[0.21, 1.0]`; repeating one family
still does not prove a broader need. fitr never combines unrelated outcomes
into a global quality sample or resolution claim. Paired tests, clustered
intervals, the rejected adaptive design, and the requirements for any future
sequential protocol are documented in [statistics.md](statistics.md).

## 4. A model that passes every test can still be broken here

Quantization can degrade structured output before prose, and looping can be
**hardware-specific**: llama.cpp has reported issues tying garbled
output to dual-GPU CUDA but not single, batch 512 but not 1024, Vulkan on
particular gfx IDs, quantized KV cache, and long agentic sessions.

So every run scores the longest text the model produced against **five
distinct degeneracy signals**: duplicate paragraph and line ratios, top
n-gram character share, gzip compression ratio, and distinct 4-grams.

Why five and not one: a local model once produced a 104 KB report that passed
every structural check while 25% of its paragraphs were duplicates - it had
looped one table 11 times. Length correlated *negatively* with quality. And a
single metric has blind spots: on a *short* looping sample the paragraph
metric reads 0.0 while `dup_line_ratio` reads 0.91 and gzip ratio 9.2.

llama.cpp ships DRY and XTC samplers that suppress loops rather than reporting
the five observations fitr records. The dated comparison notes are in
[competitive landscape](competitive-landscape-2026-08.md).

## 5. The worker does not own the evidence

A model claiming that it succeeded is not proof that it succeeded. Core PASS
and FAIL verdicts come from harness-owned deterministic checks or observed
runtime state. Runtime counters are measurement receipts with provenance.
Inferred bottlenecks are explicitly hypotheses. Missing sensors remain `n/a`.

Bounded workflow evaluation follows the same rule. A workflow can earn a PASS
only when its declared definition of done is verified independently of the
worker that attempted it. Model judging and self-report may be useful
observations, but they are labeled weaker evidence and cannot silently become
a deterministic verdict. The evidence classes and planned workflow contract
are specified in [workload evidence](workload-evidence.md).

---

## How to read a result

**A `FAIL` is a statement about this model *in this configuration*, not a
verdict on the model.**

### A zero often means "we haven't learned how to talk to it yet"

When a local model appears unable to use tools, the cause can be the chat
template, tool-call parser, quant, or context size rather than the weights.
Formats differ widely (`<tool_call>{json}</tool_call>`
vs XML `<function=name>` vs `<|python_tag|>`), and the wrong parser produces
zero tool calls and no error at all.

Not hypothetical: this tool once recorded a model as failing tool use. The
plumbing diagnostic then showed it emits valid calls with valid arguments and
consumes results correctly - it just fires them on irrelevant questions.
"Can't use tools" was wrong; "can't restrain tool use" was right.

So `run` executes the plumbing diagnostic **before** the tools test. If
plumbing is broken the need is **`BLKD`**, never `FAIL`.

```bash
fitr diag <model>   # run this before believing any tools failure
```

### Not needing a capability is normal

Most work does not need tool calling. A model that cannot drive an agent loop
can still be the best thing on your machine for drafting or fast uncensored
chat. Results are a **set of needs served**, not a rank. The tool never
prints "not recommended."

---

## Known limits

Current schema-6 results have one derived analysis boundary. After the sealed
record, completion receipt, profile, summaries, and scorecard validate,
`fitr.analysis.run.v1` projects requested and effective context, decode,
prefill, request TTFT, receipt-proven loaded TTFT, supported runtime-unloaded and cache-hit TTFT states,
runtime load time, exact-context resident and accelerator allocation bytes,
typed evidence gaps, direct receipt-state diagnoses, and a semantic next
action. CLI, TUI, Board, and HTML consume supported subsets of that projection
rather than deriving their own claims; the compatibility JSON shapes remain
unchanged.

The analysis is rebuilt from the record and is never written into schema 6 as
new evidence. Its estimates use pointers so an observed zero is not confused
with absence. Units, acquisition source, claim support, and
`available`/`descriptive_only`/`unavailable` status are explicit. Cache hits,
unknown cache state, resident-model contamination, or an artifact not bound to
the serving runtime remove the affected support claim without deleting the
observation. Schema 6 has no sealed usable-capacity policy, so the report
always blocks headroom and fit claims even when resident bytes and a device
memory value both exist.

Latency states never borrow evidence from one another. Loaded TTFT requires a
gated-request residency receipt. Runtime-unloaded TTFT and runtime load require
the versioned Ollama protocol receipt. A loaded
cache-hit TTFT separately requires a positive cached-token receipt. The
runtime-unloaded label describes runtime residency only, not operating-system
page-cache state. Exact-context placement uses runtime-classified accelerator
bytes and derives only the non-accelerator remainder. On unified-memory systems
that arithmetic does not identify exclusive pools, spill, layer placement, or
host traffic.

New results use `fitr.scoring.policy.v5`. Policy v4 keeps auxiliary latency
states out of behavior-verdict prose; v5 additionally requires the gated
request's own residency receipt before a loaded-TTFT gate can clear. Sealed
schema-6 results written with policies v3 and v4 are reconstructed with their
original scorers and remain valid. The policy hashes are different, so Board
does not compare across those presentation-contract changes.

- **Small, heterogeneous battery.** The default battery has 22 generated task
  specs across 16 families and five measured needs. Each need has its own
  count, cluster structure, gate, and interval. Thin evidence stays
  `INCONCLUSIVE`; unrelated outcomes are never pooled into a global precision
  claim. `-k 3` adds fresh instances within every declared family, but does
  not make one family prove an entire need.
- **Burst, not sustained.** A thin laptop throttles under multi-hour load.
- **Single-request regime.** The normal run deliberately avoids concurrent
  scored inference. It does not predict multi-user serving throughput.
- **Shared memory.** On an iGPU or unified-memory accelerator, model memory,
  Linux, services, and CPU-side runtime allocation compete for one pool. An
  addressable `MemTotal` reading is capacity, not a safe current model budget.
- **The refusal battery is 3 sealed prompts.** The current receipt binds their
  exact canonical prompt-ID set, but it still detects only "will refuse ordinary work",
  not the full alignment surface.
- **One model at a time.** Concurrent models contaminate timings enough to
  invalidate a run; every phase clears residents first. Cores are used for
  discovery, fingerprinting, and shard stats, not for parallel scored
  inference. See [Cores, GPUs, and honesty](#cores-gpus-and-honesty).
- **`advise` estimates by default.** The default is weights plus KV from GGUF
  metadata, with other runtime allocation excluded and disclosed. `--load`
  observes an Ollama resident allocation at the runtime-reported context.
  `--fit` reports a `llama-fit-params` device-memory projection, but the
  current adapter does not capture the fitter's adjusted context, placement,
  version, or host-memory domain. It therefore remains descriptive and cannot
  establish a context row or fit verdict. Hybrid recurrent architectures
  require a load receipt, and split GGUFs require every shard. Unmeasured
  capacity, incomplete weights, or architecture is SKIP, not a name-to-GB
  guess.
- **Cache state can be unknown.** TTFT and prefill are still observed, but an
  unknown state or any cache hit cannot prove an uncached timing gate or
  comparison claim.
- **The 32K footprint probe is explicit.** The low-footprint need is scored
  only when the runtime confirms that the effective context equals the
  requested 32K. Older receipts without that fact remain visible but cannot
  support the claim.
- **No root-cause oracle.** Current output reports observations and
  contamination. Evidence-backed limiter diagnoses, context sweeps, quant
  frontiers, soak runs, and serving tests are pre-1.0 experiments.
- **No validated-work receipt yet.** Current records preserve aggregate
  behavioral outcomes and timing observations, but not the sealed per-trial
  attempts, acceptance times, verifier receipts, retries, and escalation
  events required for time-to-valid-result or validated work rate.
- **Sharing is opt-in.** `fitr export` / `--html` write a self-contained
  page with an opaque device ID and allowlisted comparison configuration. It
  omits hostnames, local paths, raw model output, the raw fingerprint key, and
  arbitrary runtime configuration. JSON under `~/.fitr` stays local.

## Advise (design rule 7)

A verdict without a remedy is half an answer. `fitr advise` prints
Compatible / Low memory / Incompatible, and every negative tier carries the
flag that fixes it and the GB that results (`try num_ctx=4096 -> fits in
19.4 GB`). `fitr run --ctx 4096` measures that setting; `fitr apply` prints
how to persist it and does not restart the server. "Too large" is a dead
end; the flag is the product. MoE decode class uses active parameters, not
total. The request context is on the scorecard, the HTML fingerprint, and
the board header, because a number without its config is meaningless.
`fitr advise` also prints a context-fit table at several windows so the
4k / 8k / 16k choice is a row with a GB, not folklore. The derived other
resident remainder stays `n/a` until `--load` observes total allocation at
that exact context. `--fit` is shown separately as an unbound projection until
its effective context and placement can be sealed.

## Why Go

Not performance - a run is ~99.9% blocked waiting on the model, so the
harness language is irrelevant to runtime. It is **distribution**: one static
binary, no interpreter or package manager on the user's machine, ~10 ms
startup, and trivial cross-compilation for every platform the runtimes serve.

Go still uses every logical CPU it can schedule (`GOMAXPROCS`). Runtime
discovery probes ports concurrently. Hardware fingerprint probes overlap
(on Windows each is a PowerShell round-trip), share a five-second deadline,
and honor cancellation. Split GGUF shard stats overlap.
None of that is the wall clock of `fitr run`.

## Cores, GPUs, and honesty

A 16-core machine does not make a scored run 16 times faster, and it should
not. The process lock exists because of a real incident: two evals talking
to the same server produced plausible-and-wrong timings. Doctor already
treats `OLLAMA_NUM_PARALLEL>1` as a red flag - parallel slots divide the
context window and add batching variance. Firing N prompts at once would
make tok/s a shared-GPU number, which is the lie this tool exists to refuse.

The serving runtime owns inference threads. llama.cpp and Ollama already
use CPU cores for prompt processing and for any layer that is not on the
GPU. fitr does not second-guess that, and it does not put logical CPU count
in the fingerprint key: adding it would void comparable history without
changing what a measurement means.

Where cores will earn their keep later: an isolated worker can execute
generated code on spare cores while the GPU is busy on the next prompt.
Inventory of many local GGUFs can parse headers in parallel. Neither of
those is scored inference.

The eval definition lives in `spec/` as language-neutral JSON, so the spec is
the contract and the harness is just glue. The classic tasks were extracted
from a Python reference implementation and are held in lockstep by its tests;
`check` tasks name a parameterized family whose instances - and answers - are
computed at run time from a recorded seed.
