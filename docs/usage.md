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
| `fitr board [--current]` | compare everything, grouped by device |
| `fitr doctor <model> [-n N]` | can this box be measured fairly at all? (~1 min) |
| `fitr diag <model>` | 5-rung tool-use plumbing diagnostic |
| `fitr compare <a> <b>` | difference/ratio intervals; paired flips on shared instances |
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
| 0 | ran, every measured need passed |
| 1 | error |
| 2 | usage |
| 3 | ran fine, a need FAILED (use as a CI gate) |
| 130 | interrupted |

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
