# Design: what fitr believes and why

fitr's verdicts rest on four commitments. The first three are becoming
industry consensus - `homebench`, `bench-loop`, `PocketPal` and `llmfit` all
partition by hardware now, and Wilson intervals ship in several local
harnesses. We hold them because they are correct, not because they are rare.
The fourth is, as of August 2026, ours alone.

## 1. A score is meaningless without the device it was measured on

The *same model*, *same tag*, on the *same laptop* went from "crashes on
load" to "daily driver" because of a GPU driver update. Nothing about the
model changed.

Every result embeds a fingerprint: GPU, driver, serving runtime and version,
inference backend, and the config that actually moves numbers
(`OLLAMA_FLASH_ATTENTION`, `OLLAMA_KV_CACHE_TYPE`). `fitr board` groups by
fingerprint and **refuses to rank across groups**. Change the driver and your
old numbers are explicitly void.

## 2. "Is it good" is unanswerable. "Does it serve need X" is answerable.

Independent needs, each with its own gate. A model can serve one brilliantly
and fail another, and one number cannot say that.

| Need | Why separate |
|---|---|
| **fast + pretty good** | responsiveness; TTFT-dominated |
| **great coding / reasoning** | executed assertions plus computed-answer checks, never judged by a model |
| **emits valid structured output** | quantization breaks JSON before prose - the earliest damage signal |
| **follows exact instructions** | verifiable constraints, graded by code |
| **no filtering / low refusal** | a first-class need, not a footnote |
| **works unattended** | prefill-bound, not decode-bound |
| **leaves tools alone when they don't apply** | restraint at rest (no calls on an unrelated question) and under change (a tool vanishes mid-loop) |
| **small enough to keep resident** | measured resident bytes, not file size |
| **reads images** | a capability, not a grade |
| **your tasks** | `~/.fitr/tasks/*.json` - the built-ins are defaults, your work is the point |

Verdicts are **PASS / FAIL / SKIP / n/a / BLKD**. `SKIP` means not measured.
`n/a` means the model never claimed it - a text-only model is not *bad at
vision*. `BLKD` means we could not fairly test it.

## 3. A single run is not a measurement

Identical configs vary **10-20 percentage points** between runs. We watched
the same coding task pass on one run and fail on the next with nothing
changed.

So tasks repeat (`-k 3`), results carry Wilson score intervals, every run
prints its minimum detectable effect, and `fitr compare` says "cannot
separate" rather than inventing a winner. A single pass gives a Wilson
interval of `[0.21, 1.0]`. The full machinery - difference intervals, paired
tests, sequential decisions - is documented in [statistics.md](statistics.md).

## 4. A model that passes every test can still be broken here

Quantization degrades structured output long before it degrades prose, and
looping is **hardware-specific**: llama.cpp has open 2026 issues tying garbled
output to dual-GPU CUDA but not single, batch 512 but not 1024, Vulkan on
particular gfx IDs, quantized KV cache, and long agentic sessions.

So every run scores the longest text the model produced against **five
independent degeneracy signals**: duplicate paragraph and line ratios, top
n-gram character share, gzip compression ratio, and distinct 4-grams.

Why five and not one: a local model once produced a 104 KB report that passed
every structural check while 25% of its paragraphs were duplicates - it had
looped one table 11 times. Length correlated *negatively* with quality. And a
single metric has blind spots: on a *short* looping sample the paragraph
metric reads 0.0 while `dup_line_ratio` reads 0.91 and gzip ratio 9.2.

No other harness reports this. llama.cpp ships DRY and XTC samplers that
*suppress* loops but never *report* them, and two upstream attempts to add
detection stalled or were closed.

---

## How to read a result

**A `FAIL` is a statement about this model *in this configuration*, not a
verdict on the model.**

### A zero often means "we haven't learned how to talk to it yet"

When a local model appears unable to use tools, roughly **4 times in 5** the
cause is the chat template, the tool-call parser, the quant, or the context
size - not the weights. Formats differ wildly (`<tool_call>{json}</tool_call>`
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

- **Small battery.** ~23 binary trials per default run puts the minimum
  detectable effect near 29 pp - and the run says so, out loud, every time.
  It separates *broken* from *working*, not *good* from *slightly better*.
  `-k 3` regenerates every check three times and roughly halves the MDE.
- **Burst, not sustained.** A thin laptop throttles under multi-hour load.
- **Shared memory.** On an iGPU the "GPU memory" is system RAM; a resident
  19 GB model still costs 19 GB of your budget.
- **The refusal battery is 3 prompts.** It detects "will refuse ordinary
  work", not the full alignment surface.
- **One model at a time.** Concurrent models contaminate timings enough to
  invalidate a run; every phase clears residents first.
- **`advise` estimates, it does not dummy-allocate.** The number is weights
  plus KV from GGUF metadata. Compute buffers are excluded and said so.
  Unmeasured VRAM or architecture is SKIP, not a name-to-GB guess.

## Advise (design rule 7)

A verdict without a remedy is half an answer. `fitr advise` prints
Compatible / Low memory / Incompatible, and every negative tier carries the
flag that fixes it and the GB that results (`try num_ctx=4096 -> fits in
19.4 GB`). "Too large" is a dead end; the flag is the product. MoE decode
class uses active parameters, not total.

## Why Go

Not performance - a run is ~99.9% blocked waiting on the model, so the
harness language is irrelevant to runtime. It is **distribution**: one static
binary, no interpreter or package manager on the user's machine, ~10 ms
startup, and trivial cross-compilation for every platform the runtimes serve.

The eval definition lives in `spec/` as language-neutral JSON, so the spec is
the contract and the harness is just glue. The classic tasks were extracted
from a Python reference implementation and are held in lockstep by its tests;
`check` tasks name a parameterized family whose instances - and answers - are
computed at run time from a recorded seed.
