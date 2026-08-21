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

That puts one static binary on your PATH. The default evidence path needs no Go,
Python, or venv. Pin a release with `FITR_VERSION=v0.6.0`; relocate with
`FITR_BIN`.

From source (Go 1.25+):

```bash
git clone https://github.com/blisspixel/fitr && cd fitr
make install
```

A rankable run needs a serving runtime and verified artifact identity. Ollama
exposes a runtime content digest. A readable GGUF reported by an already-running
llama-server can be hashed for inspection, but that post-load file hash does not
prove which bytes the process loaded, so the result remains unrankable without
a runtime binding receipt. For a generic OpenAI-compatible endpoint, set
`FITR_OPENAI_MODEL_SHA256` to an independently obtained expected digest; the
endpoint must assert the same digest. A mutable model label or an endpoint
assertion by itself is not artifact evidence.

Authenticated OpenAI-compatible endpoints use `FITR_OPENAI_API_KEY`.
`OPENAI_API_KEY` is considered only for the official HTTPS OpenAI host. Tokens
are intentionally not accepted as command-line flags, where shell history and
process listings could expose them. Auto-discovery never attaches credentials.

Generated-code execution is disabled by default. `--allow-unsafe-exec` opts
into unisolated built-in Python diagnostics and therefore requires `python` on
PATH. Generated code in that mode has the current user's ordinary filesystem,
environment, credential, and network access. Run it only on a disposable,
credential-free machine. Its observations are always INCONCLUSIVE and never
enter a PASS or FAIL denominator.

## Commands

| Command | Does |
|---|---|
| `fitr` | installed inventory: measured / unproven / incompatible / stale, one next command per row |
| `fitr run <model> [--quick\|--full\|--checks-only] [-k N] [--ctx N]` | measure a model; checks-only runs the generated battery for calibration |
| `fitr advise [model] [--vram-gb N] [--ctx N] [--load] [--fit]` | no model: inventory. With a model: does it fit, and if not, which flag to try |
| `fitr apply [model] [--ctx N]` | print how to persist a measured context; never restarts the server |
| `fitr tune [a b]` | print request-level knobs; diff two saved fingerprints |
| `fitr export <model> [--out PATH] [--retonr]` | HTML scorecard, and/or opt-in evidence for [retonr](retonr.md) |
| `fitr view [model\|result.json]` | reopen the newest or selected saved result as a terminal data view |
| `fitr board [--current]` | compare everything, grouped by device |
| `fitr top [--view VIEW]` | open the keyboard-first Live, Result, Board, and History monitor |
| `fitr top view [model\|result.json]` | open a selected saved result in the monitor |
| `fitr top run <model> [run flags]` | run the same evaluator with structured live progress |
| `fitr top --snapshot` | emit the versioned privacy-safe presentation snapshot as JSON |
| `fitr top history [path\|clear --yes]` | browse, locate, or clear archived runs while keeping canonical results |
| `fitr doctor <model> [-n N]` | can this box be measured fairly at all? (~1 min) |
| `fitr diag <model>` | 5-rung tool-use plumbing diagnostic |
| `fitr compare <a> <b>` | difference/ratio intervals; paired flips (accuracy can hide them) |
| `fitr device` / `fitr profiles [new]` | fingerprint and gates; `new` writes an UNCALIBRATED local profile |
| `fitr calibrate <a> <b> [--out PATH] [--lineage PATH]` | paired item discrimination; optional same-base lineage receipt |
| `fitr calibrate merge <pair.json>... [--out PATH]` | aggregate unsigned leads without claiming verified campaign readiness |

| Level | Runs | ~Time |
|---|---|---|
| `--quick` | speed, memory, plumbing; executable tasks recorded as SKIP | minutes; smoke-test the stack |
| *(default)* | + 16 generated checks, refusal, safe tool withdrawal | many minutes; first real measurement |
| `--full` | + long-horizon agent task, SKIP while execution is disabled | tens of minutes; coding still unproven |
| `--checks-only` | generated checks only; requires `--seedset`, defaults to 5 repeats | model-dependent |

A default run tells *broken* from *working*, not 71 from 74. `--full` adds up
to 40 agent turns; it is not a coding grade until the isolated worker exists.
Ctrl-C is safe (exit 130).

## Flags worth knowing

- `-k N` repeats the noisy tasks N times (default 3, 1 with `--quick`, 5 with
  `--checks-only`). A single run is not a measurement: identical configs vary
  10-20 pp.
- `--adaptive` replaces the fixed check-repeat count with a sequential test:
  keep generating fresh instances until each gated need is decided against
  its gate, or report that the sample cannot separate it. See
  [statistics.md](statistics.md).
- `--seedset NAME` pins the generated-instance set. Two runs sharing a
  seedset face identical instances, which upgrades `fitr compare` to a
  paired test. Fresh instances per run remain the default.
- `--checks-only` is the efficient battery-calibration level. It requires a
  seedset and uses five fixed repeats by default. It cannot be combined with
  adaptive stopping because both sides of a pair must see every instance. See
  [calibration.md](calibration.md).
- `--lineage PATH` (`fitr calibrate`) attaches a `fitr.lineage.same-base.v1`
  receipt from a `fitr.lineage.conversion.v1` manifest that names both
  runtime-bound artifact digests and one base revision. Family names are not
  lineage. Unsigned pairs with a valid receipt are still not decision-grade.
- `--backend auto|ollama|llama-server|openai` picks the serving runtime;
  see [backends.md](backends.md). Extra listen URLs: `$FITR_DISCOVER_URLS`.
- `--ctx N` sets the request context (default 8192). This is how you *measure*
  an `advise` remedy: `fitr run m --ctx 4096`. A non-default ctx is
  recorded beside the runtime-reported effective context. `board` and
  `compare` require that effective receipt and will not rank a request the
  runtime did not verify. `fitr apply` then prints the command to persist that
  setting; fitr never restarts the server.
- `--load` (advise) loads an Ollama model and reads `/api/ps` so fit includes
  the live resident allocation and compute buffers. `--fit` runs
  `llama-fit-params` on a GGUF when that binary is on PATH and uses its dummy
  allocation. The weights+KV estimate is the default for conventional
  attention and is labeled as such. Hybrid recurrent architectures stay SKIP
  until `--load` or `--fit` measures their extra state. Split GGUF weights are
  summed only after every declared shard is present.
- `--pull` fetches a missing Ollama tag before measuring. Pasted Hugging
  Face GGUF URLs pull automatically (they *are* the request to fetch).
- `--allow-unsafe-exec` runs unisolated built-in Python diagnostics after an
  interpreter preflight. The process must exit successfully and finish with
  one exact verifier receipt. These defenses do not create a sandbox, so the
  observation remains INCONCLUSIVE and is excluded from rankings.
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
fitr view                    # newest saved result, with repeat-shape graphs on a rich terminal
fitr view m --display json   # full saved result JSON
fitr run m -q                # results only     -v  detail, no progress
NO_COLOR=1 fitr board        # honored (empty string means unset)
FITR_ASCII=1 fitr board      # force ASCII glyphs
FITR_UNICODE=1 fitr board    # force Unicode; Windows Terminal already enables it
fitr top                     # interactive terminal only
fitr top --snapshot          # pipe-safe presentation JSON, no terminal controls
```

`--display auto` chooses `rich` on a capable terminal and `plain` when stdout
is redirected. `rich` can be forced for a capture; `plain`, `json`, and `none`
are stable automation surfaces. Unknown mode names are usage errors.

Progress goes to **stderr**, results to **stdout**, so `fitr run m > out.txt`
is clean. Errors are plain text on stderr even under `--display json`; the
exit code is the machine channel.

`fitr top` is deliberately opt-in and requires an interactive input and output
terminal. It never emits terminal controls when redirected or when
`TERM=dumb`; use `fitr top --snapshot`, `fitr view`, or `fitr board` in those
environments. `top run` owns the screen while measurement is active, so
diagnostics and save state appear inside the monitor instead of corrupting the
terminal.

| Exit | Meaning |
|---|---|
| 0 | ran, every measured need passed (`advise`: compatible or SKIP) |
| 1 | error |
| 2 | usage |
| 3 | ran fine, a need FAILED (`advise`: low memory or incompatible) |
| 130 | interrupted |

Bare `fitr` and `fitr advise` with no model print the installed inventory:
what the serving runtime already has, joined to current fitr evidence. Each
row is **measured**, **unproven**, **incompatible**, or **stale**, with one
next command. Unmeasured is a candidate, never a recommendation. Color does
not carry the state. `fitr advise <model>` remains the one-artifact fit
verdict; inventory does not `Show()` every blob or pull anything.

## Cores

fitr already schedules on every logical CPU Go can see. Runtime discovery
probes ports at once; hardware probes overlap; split GGUF shards are sized
in parallel. `fitr` and `fitr device` print that CPU count as display-only.
It is not part of the fingerprint key.

A scored `fitr run` is still one request at a time. Concurrent prompts on
the same server contaminate timings and divide the context (doctor warns
on `OLLAMA_NUM_PARALLEL>1`). Faster runs come from `--quick`, fewer
repeats, or a smaller model - not from parallel inference. The serving
runtime owns how many threads actually decode.

## Advise

`fitr advise <model>` sizes a model against this box and prints a three-tier
verdict: **compatible**, **low memory** (`try num_ctx=4096 -> fits in 19.4 GB`),
or **incompatible**. Negative tiers carry the flag that fixes them.

If the model is already loaded, the number is the server's own resident
bytes (measured, compute buffers included). Otherwise it is **weights + KV**
from GGUF architecture, and compute buffers are excluded and said so. A
running process that exceeds the VRAM *reading* is SKIP, not Incompatible -
the budget is the suspect number. MoE decode class uses *active* parameters,
not total.

Hybrid recurrent architectures are not conventional KV-only models. fitr does
not project their memory from an incomplete formula: use `--load` at the
requested context or `--fit`. For split GGUFs, every declared shard must be
present and the weight total is the sum of the complete set; a shard header is
never mistaken for the whole model.

SKIP, never a guess, when GPU memory was not measured, weights are unknown,
or architecture metadata is missing. `--vram-gb N` supplies a budget; a GPU
name is never turned into a VRAM number. There is no catalog of models to
pick from - advise answers "does THIS fit", not "what exists".

## Apply

`fitr apply [model] [--ctx N]` prints the command to persist a measured
context on whatever is serving. It never restarts or mutates the process.

Ollama can take `num_ctx` per request (`fitr run --ctx` already does) or
persist it in a derived tag via a Modelfile. llama-server allocates KV at
launch, so the printout is a `--ctx-size` restart line. OpenAI-compat
servers get the launch flags they actually use (`--max-model-len`,
`--context-length`, or the LM Studio UI) rather than a guessed one-true
flag. Pass `--backend` to see one recipe; with no runtime reachable, all
three print.

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
`$FITR_RESULTS`). Canonical files use a readable model prefix plus a short
SHA-256 suffix so two model names that sanitize alike cannot overwrite each
other. Pre-v0.4 `<model>.json` files remain readable and migrate naturally on
the next save. Canonical latest results remain the source for ordinary `view`,
`board`, `compare`, `calibrate`, and `export` commands.
Completed runs are also copied atomically into a private `.history` directory
for `fitr top History`. These archives contain the same raw prompts, responses,
hostname, and device details as the canonical result, can grow without a fixed
retention limit, and are never uploaded. A canonical result is eligible for a
ranking or comparison claim only when it exactly matches its private history
twin. History entries and explicit external result paths remain available for
inspection, but are display-only unless they reconcile with the canonical
current record. Local completion receipts bind the sealed run manifest and
measured outcome; they do not replace external attestation.

Use `fitr top history path` to locate the archive and
`fitr top history clear --yes` to delete archived copies while keeping the
canonical latest result for each model. Deleting a canonical file removes that
model from ordinary commands even if an archived copy remains. See
[Terminal monitor](tui.md) for keys and fallbacks, and
[Interface direction](interface.md) for the truly native desktop plan.
