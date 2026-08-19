# fitr

**Is this local model any good — *on your machine*?**

A new model drops. You want one answer, in ~15 minutes, that is true for *your*
hardware rather than for someone's A100.

```bash
ollama pull some-new-model:tag
fitr run some-new-model:tag --full
fitr board
```

```
------------------------------------------------------------------------------
model    qwen3-coder:30b
size     30.5B  Q4_K_M  qwen3moe
use for  daily driver (coding + agents), no-filter writing/chat, (small footprint)
device   AMD Radeon(TM) 780M | driver 32.0.31007.5012 | GPU 100% | profile lappy
------------------------------------------------------------------------------
[PASS] fast + pretty good (chat)          23.16 tok/s (need >=10.0), TTFT 0.89s
[PASS] great coding / reasoning           6/6 passes [0.61-1.00]
[PASS] no filtering / low refusal         refused/partial 0/3 (need <=0)
[PASS] works unattended (agent loop)      prefill 226.6 tok/s, 20-turn pass=true
[PASS] leaves tools alone when irrelevant left tools alone on an unrelated question
[PASS] small enough to keep resident      resident@32K 20.34 GB (need <=22)
[n/a ] reads images                       text-only model - not what it is for
[PASS] no degenerate output               gzip 2.39, dup_lines 0, top_4gram 0.036

over 3 repeats   decode 23.16 +/-0.44 (min 22.71, max 23.60)   prefill 226.64 +/-3.10
------------------------------------------------------------------------------
```

Single static binary. No Python, no venv, no package manager, no runtime.

---

## Three ideas that make this different from a leaderboard

### 1. A score is meaningless without the device it was measured on

The *same model*, *same tag*, on the *same laptop* went from **"crashes on
load"** to **"daily driver"** because of a GPU driver update. Nothing about the
model changed.

Every result embeds a fingerprint — GPU, driver, Ollama version, inference
backend, and the config that actually moves numbers (`OLLAMA_FLASH_ATTENTION`,
`OLLAMA_KV_CACHE_TYPE`). `fitr board` groups by fingerprint and **refuses to
rank across groups**. Change the driver and your old numbers are explicitly void.

### 2. "Is it good" is unanswerable. "Does it serve need X" is answerable.

Independent needs, each with its own gate. A model can serve one brilliantly and
fail another, and one number cannot say that.

| Need | Why separate |
|---|---|
| **fast + pretty good** | responsiveness; TTFT-dominated |
| **great coding / reasoning** | executed assertions, never judged by a model |
| **no filtering / low refusal** | a first-class need, not a footnote |
| **works unattended** | prefill-bound, not decode-bound |
| **leaves tools alone when irrelevant** | the most common local tool failure |
| **small enough to keep resident** | measured resident bytes, not file size |
| **reads images** | a capability, not a grade |

Verdicts are **PASS / FAIL / SKIP / n/a / BLKD**. `SKIP` means not measured.
`n/a` means the model never claimed it — a text-only model is not *bad at
vision*. `BLKD` means we could not fairly test it.

### 3. A single run is not a measurement

Identical configs vary **10–20 percentage points** between runs. We watched the
same coding task pass on one run and fail on the next with nothing changed.

So tasks repeat (`-k 3` default), results carry **Wilson score intervals**, and
`fitr compare` says **"INDISTINGUISHABLE on this sample size"** rather than
inventing a winner. A single pass gives a Wilson interval of `[0.21, 1.0]`.

---

## Install

```bash
# from source
git clone https://github.com/blisspixel/fitr && cd fitr
make install

# or grab a binary for your platform from Releases
```

Needs Go 1.24+ to build, a running Ollama to measure, and `python3` on PATH
**only** to execute the Python coding fixtures. The harness language and the
task language are deliberately separate: a task declares the interpreter it
needs in its spec, so adding Rust tasks would need `cargo`, not a rewrite.

## Commands

| Command | Does |
|---|---|
| `fitr run <model> [--quick\|--full] [-k N]` | measure a model on this device |
| `fitr board [--current]` | compare everything, grouped by device |
| `fitr diag <model>` | 5-rung tool-use plumbing diagnostic |
| `fitr compare <a> <b>` | paired comparison with propagated error |
| `fitr device` / `fitr profiles` | fingerprint and gates |

| Level | Runs | ~Time |
|---|---|---|
| `--quick` | speed, memory, coding, plumbing, tools | ~4 min |
| *(default)* | + refusal battery | ~8 min |
| `--full` | + 20-turn unattended agentic task | ~15 min |

---

## How to read a result

**A `FAIL` is a statement about this model *in this configuration*, not a
verdict on the model.**

### A zero often means "we haven't learned how to talk to it yet"

When a local model appears unable to use tools, roughly **4 times in 5** the
cause is the chat template, the tool-call parser, the quant, or the context size
— not the weights. Formats differ wildly (`<tool_call>{json}</tool_call>` vs XML
`<function=name>` vs `<|python_tag|>`), and **the wrong parser produces zero tool
calls and no error at all.**

Not hypothetical: this tool once recorded a model as failing tool use. The
plumbing diagnostic then showed it emits valid calls with valid arguments and
consumes results correctly — it just fires them on irrelevant questions.
*"Can't use tools"* was wrong; *"can't restrain tool use"* was right.

So `run` executes the plumbing diagnostic **before** the tools test. If plumbing
is broken the need is **`BLKD`**, never `FAIL`.

```bash
fitr diag <model>   # run this before believing any tools failure
```

### Not needing a capability is normal

**Most work does not need tool calling.** A model that cannot drive an agent loop
can still be the best thing on your machine for drafting or fast uncensored chat.
Results are a **set of needs served**, not a rank. The tool never prints "not
recommended."

---

## Device profiles

Gates live in `internal/device/profiles/*.json`, embedded in the binary and
auto-selected by matching your GPU or hostname. `default` is deliberately
uncalibrated and says so at runtime.

```jsonc
{
  "name": "lappy",
  "match": { "gpu_contains": "780M" },
  "gates": {
    "fast_chat": {
      "decode_tps_min": 10.0, "ttft_s_max": 1.5,
      "why": "comfortable reading is ~5-6 tok/s; TTFT is what makes it FEEL fast"
    }
  }
}
```

Every threshold carries a `why`. **Copy `default.json`, tune it, set `match`.**
Do not reuse another machine's numbers.

---

## Degeneracy detection

Five independent deterministic signals, no model call: duplicate paragraph /
line ratios, top-4-gram character share, gzip compression ratio, distinct-4-gram.

**Why five and not one:** a local model once produced a 104 KB report that passed
every structural check while 25% of its paragraphs were duplicates — it had
looped one table 11 times. Length correlated *negatively* with quality. And a
single metric has blind spots: on a *short* looping sample the paragraph metric
reads **0.0** while `dup_line_ratio` reads **0.91** and gzip ratio **9.2**.

---

## Output modes and exit codes

```bash
fitr run m --display json    # NDJSON on stdout, nothing else
fitr run m -q                # results only     -v  detail, no progress
NO_COLOR=1 fitr board        # honored (empty string means unset)
FITR_ASCII=1 fitr board   # force ASCII glyphs
```

Progress → **stderr**, results → **stdout**, so `fitr run m > out.txt` is
clean. Errors are plain text on stderr even under `--json`; the exit code is the
machine channel.

| Exit | Meaning |
|---|---|
| 0 | ran, every measured need passed |
| 1 | error |
| 2 | usage |
| 3 | ran fine, a need FAILED (use as a CI gate) |
| 130 | interrupted |

## Known limits

- **Small battery.** ~6 tasks means a minimum detectable effect around 33 pp at
  `-k 3`. It separates *broken* from *working*, not *good* from *slightly better*.
- **Burst, not sustained.** A thin laptop throttles under multi-hour load.
- **Shared memory.** On an iGPU the "GPU memory" is system RAM; a resident 19 GB
  model still costs 19 GB of your budget.
- **The refusal battery is 3 prompts.** It detects "will refuse ordinary work",
  not the full alignment surface.
- **One model at a time.** Concurrent models contaminate timings enough to
  invalidate a run; every phase clears residents first.

## Why Go

Not performance — a run is ~99.9% blocked waiting on the model, so the harness
language is irrelevant to runtime. It is **distribution**: one static binary,
no interpreter or package manager on the user's machine, ~10 ms startup, and
trivial cross-compilation for every platform Ollama runs on.

The eval definition lives in `spec/` as language-neutral JSON, generated rather
than hand-written, so the spec is the contract and the harness is just glue.

## License

MIT
