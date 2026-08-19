# Usage

## Install

macOS / Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/blisspixel/fitr/main/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/blisspixel/fitr/main/install.ps1 | iex
```

That puts one static binary on your PATH. No Go, no Python, no venv. Pin a
release with `FITR_VERSION=v0.2.0`; relocate with `FITR_BIN`.

From source (Go 1.25+):

```bash
git clone https://github.com/blisspixel/fitr && cd fitr
make install
```

A run needs a serving runtime (Ollama, llama-server, or any OpenAI-compatible
server) and `python3` on PATH **only** to execute the Python coding fixtures.
The harness language and the task language are deliberately separate: a task
declares the interpreter it needs in its spec, so adding Rust tasks would
need `cargo`, not a rewrite.

## Commands

| Command | Does |
|---|---|
| `fitr run <model> [--quick\|--full] [-k N]` | measure a model on this device |
| `fitr advise <model> [--vram-gb N] [--ctx N]` | does it fit here, and if not, which flag to try |
| `fitr export <model> [--out PATH]` | write a self-contained HTML scorecard (opt-in) |
| `fitr board [--current]` | compare everything, grouped by device |
| `fitr doctor <model> [-n N]` | can this box be measured fairly at all? (~1 min) |
| `fitr diag <model>` | 5-rung tool-use plumbing diagnostic |
| `fitr compare <a> <b>` | difference/ratio intervals; paired flips (accuracy can hide them) |
| `fitr device` / `fitr profiles` | fingerprint and gates |

| Level | Runs | ~Time |
|---|---|---|
| `--quick` | speed, memory, coding, plumbing, tools | ~4 min |
| *(default)* | + 16 generated checks + refusal + tool withdrawal | ~11 min |
| `--full` | + 40-turn unattended agentic task | ~18 min |

## Flags worth knowing

- `-k N` repeats the noisy tasks N times (default 3, 1 with `--quick`). A
  single run is not a measurement: identical configs vary 10-20 pp.
- `--adaptive` replaces the fixed check-repeat count with a sequential test:
  keep generating fresh instances until each gated need is decided against
  its gate, or report that the sample cannot separate it. See
  [statistics.md](statistics.md).
- `--seedset NAME` pins the generated-instance set. Two runs sharing a
  seedset face identical instances, which upgrades `fitr compare` to a
  paired test. Fresh instances per run remain the default.
- `--backend auto|ollama|llama-server|openai` picks the serving runtime;
  see [backends.md](backends.md). Extra listen URLs: `$FITR_DISCOVER_URLS`.
- `--pull` fetches a missing Ollama tag before measuring. Pasted Hugging
  Face GGUF URLs pull automatically (they *are* the request to fetch).
- `--profile P` forces a device profile instead of auto-matching.
- `--vram-gb N` (advise) supplies the memory budget when fitr cannot read
  one. Unmeasured VRAM is a SKIP, never a guess from the GPU's name.
- `--ctx N` (advise) is the context to size against; default is the model's
  max from GGUF metadata.
- `--html` (run) writes a self-contained HTML scorecard next to the JSON.
  Off unless you pass it. `fitr export <model> [--out PATH]` does the same
  from a saved result. The page carries the hardware fingerprint; do not
  rank it against another device. Never uploaded.

## Output modes and exit codes

```bash
fitr run m --display json    # NDJSON on stdout, nothing else
fitr run m -q                # results only     -v  detail, no progress
NO_COLOR=1 fitr board        # honored (empty string means unset)
FITR_ASCII=1 fitr board      # force ASCII glyphs
```

Progress goes to **stderr**, results to **stdout**, so `fitr run m > out.txt`
is clean. Errors are plain text on stderr even under `--display json`; the
exit code is the machine channel.

| Exit | Meaning |
|---|---|
| 0 | ran, every measured need passed (`advise`: compatible or SKIP) |
| 1 | error |
| 2 | usage |
| 3 | ran fine, a need FAILED (`advise`: low memory or incompatible) |
| 130 | interrupted |

## Advise

`fitr advise <model>` sizes a model against this box and prints a three-tier
verdict: **compatible**, **low memory** (`try num_ctx=4096 -> fits in 19.4 GB`),
or **incompatible**. Negative tiers carry the flag that fixes them.

The number is **weights + KV**, from GGUF architecture (a `.gguf` path or
Ollama `/api/show` `model_info`). Compute buffers are not included and the
report says so. MoE decode class uses *active* parameters, not total.

SKIP, never a guess, when GPU memory was not measured, weights are unknown,
or architecture metadata is missing. `--vram-gb N` supplies a budget; a GPU
name is never turned into a VRAM number. There is no catalog of models to
pick from - advise answers "does THIS fit", not "what exists".

## Device profiles

Gates live in `spec/profiles/*.json`, embedded in the binary and
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

Every threshold carries a `why`. **Copy `default.json`, tune it, set
`match`.** Do not reuse another machine's numbers.

Results are stored as JSON under `~/.fitr/results` (override with
`$FITR_RESULTS`); `fitr board` and `fitr compare` read them from there.
