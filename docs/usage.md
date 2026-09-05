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

That installs one static binary. The shell installer reports when its
destination is not on `PATH`; the PowerShell installer adds its destination to
the user `PATH`. Set `FITR_NO_PATH=1` to leave the persistent user `PATH`
unchanged during automation or an isolated install. The current PowerShell host
can still invoke fitr immediately. The default evidence path needs no Go,
Python, or venv. Pin a release with `FITR_VERSION=v0.9.12`; relocate with
`FITR_BIN`.

Both installers bind the downloaded binary to the exact asset entry in the
release's `SHA256SUMS` file. A missing checksum tool, manifest, or asset entry
is an error. `FITR_NO_VERIFY=1` is an explicit unsafe opt-out for environments
where verification is impossible.

From source (Go 1.25+):

```bash
git clone https://github.com/blisspixel/fitr && cd fitr
make install
```

### Update

```bash
fitr update --check             # validate the latest stable release receipt
fitr update                     # verify, stage, and replace this binary
fitr update --reinstall         # verify and reinstall the same latest version
fitr update --display json      # fitr.update.v1 event for automation
```

The updater is deliberately bounded to stable releases in
`blisspixel/fitr`. It accepts no repository, URL, target-path, or
checksum-bypass flag. It selects the exact asset for the running OS and
architecture, requires exactly one matching entry in the release's
`SHA256SUMS`, streams the candidate beside the current executable, checks its
SHA-256, and runs the staged binary's `version` command before replacement.
It refuses downgrades, unsupported platforms, symlink-managed binaries, and a
target whose digest differs at the pre-replacement check.

On Linux and macOS, replacement is a same-directory rename. On Windows, the
verified candidate is staged and a hidden helper attempts replacement after
the running process exits, because Windows does not replace its active image.
Run `fitr version` afterward to verify completion.
If the executable directory is not writable or a package manager owns the
path, use that package manager or rerun the installer. No automatic update
check occurs during ordinary commands.

The checksum binds the binary to the manifest published with the same GitHub
release. It detects corruption and mismatched assets; it is not an independent
publisher signature.

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

Terminal views of the loop, regenerated from the real printers:

<img src="assets/advise.svg?v=0.10.9" alt="fitr advise (demo data)" width="820">
<img src="assets/apply.svg?v=0.10.9" alt="fitr apply (demo data)" width="820">
<img src="assets/board.svg?v=0.10.9" alt="fitr board (demo data)" width="820">
<img src="assets/top.svg?v=0.10.9" alt="fitr top (demo data)" width="820">

### Disk

`--pull` refuses a download that would not leave the model volume its headroom:
min(10% of the volume, 10 GB), which is BuildKit's reserve policy and the
closest well-tested analogue for artifacts this size.

The check fires on the first frame that reports a size, so an oversized pull is
abandoned before it writes rather than after it fills the disk. Ollama reports
size per layer, so the figure is a floor on the requirement rather than the
requirement: it catches the case that matters and never invents a total it was
not given. The volume is `$OLLAMA_MODELS` if set, else `~/.ollama/models`.

If free space cannot be read the pull proceeds. fitr does not invent numbers and
will not act on one it failed to read.

### Hardware coverage

What fitr can read, by platform and vendor. Where it cannot read something it
says so and omits the caveat rather than estimating one.

| | GPU name | Total budget | Free right now |
|---|---|---|---|
| NVIDIA discrete, any OS | `nvidia-smi` | `nvidia-smi` | `nvidia-smi` |
| NVIDIA GB10 / Thor, Linux | `nvidia-smi` | `/proc/meminfo` `MemTotal` addressable pool | unavailable |
| AMD, Linux | `rocm-smi`, else `lspci` | `drm sysfs` | `drm sysfs` (total minus used) |
| AMD, Windows | `Win32_VideoController` | registry `qwMemorySize` | not available |
| Apple Silicon | `system_profiler` | `iogpu.wired_limit_mb`, else assumed share | not applicable |
| Intel iGPU | `lspci` / CIM | treated as unified memory | not available |

Three honest gaps. AMD free VRAM on Windows needs DXGI's
`QueryVideoMemoryInfo`, which is COM and per-adapter; the registry figure is a
static capacity, not live state. Apple Silicon has no separate VRAM to be free.
NVIDIA GB10 and Thor likewise share one Linux system pool, while their
dedicated-memory fields report no usable total or free reading. On those
systems `MemTotal` is addressable capacity, not a safe current model budget,
and fitr does not relabel it as free memory. If a future driver reports a
nonzero capacity through `nvidia-smi`, fitr preserves the value but labels it
as unified capacity rather than dedicated VRAM.

The GPU-memory probes have been exercised on a Windows host with a discrete
NVIDIA card. Clean Linux and macOS runners exercise installation and the full
decision loop against a pinned native runtime, but their hosted hardware does
not validate the physical GPU-memory probes. The remaining probe paths follow
their platform interfaces and have automated tests; treat them as unverified
on physical hardware until a native acceptance row records otherwise.

Hardware choice is not a single speed ranking. Capacity, prompt processing,
generation, first-response latency, model behavior, placement, and intended
workload answer different questions. See [Choosing hardware with fitr](choosing-hardware.md)
for the current evidence boundary and the planned context, quant, sustained,
and serving experiments.

### Memory budget by platform

`fitr` prints the provenance of every capacity input rather than silently
substituting an advertised number. A dedicated-memory total can be a planning
budget. An addressable unified pool is only physical capacity until an
operator supplies a safe budget or a runtime receipt proves one loaded point.

| Platform | Source | Kind |
|---|---|---|
| NVIDIA discrete | `nvidia-smi` total, and free where a contention caveat applies | measured |
| NVIDIA GB10 / Thor on Linux | `/proc/meminfo` `MemTotal` and timestamped `MemAvailable` | measured addressable pool plus transient availability, not an implicit safe budget |
| AMD or Intel dedicated memory on Linux | `drm sysfs` | measured |
| AMD or Intel integrated whole-system fallback | system RAM | measured addressable pool, not safe free budget |
| Apple Silicon, limit set | `iogpu.wired_limit_mb` | measured |
| Apple Silicon, default | 75% of `hw.memsize` | **assumed** |
| Override | `--vram-gb N` | you said so |

Apple Silicon shares one pool, but the GPU cannot wire all of it. Reporting
installed RAM as GPU-available memory is the unified-memory version of grading
against total instead of the active kernel policy: it can certify a model that
does not load. Raise the ceiling with `sudo sysctl iogpu.wired_limit_mb=N` and
fitr reads it back as a measurement rather than an assumption.

The 75% figure is kernel policy, not a published constant, so it is labelled
assumed and is intended to err conservatively. It avoids treating every byte
of installed RAM as an unconditional model budget.

## Commands

| Command | Does |
|---|---|
| `fitr` | installed inventory: measured / unproven / incompatible / stale, fit windows, and the one thing to do next on each row |
| `fitr run <model> [--quick\|--full\|--checks-only] [-k N] [--ctx N] [--capacity-budget-gb N\|--capacity-reserve-gb N]` | measure a model; optionally seal an explicit safe-capacity policy before loading; checks-only runs the generated battery for calibration |
| `fitr [model]` / `fitr advise [model] [--vram-gb N] [--ctx N] [--load] [--fit]` | no model: inventory. With a model: does it fit, and if not, which flag to try |
| `fitr apply [model] [--ctx N]` | print how to persist a measured context; never restarts the server |
| `fitr tune [a b]` | print request-level knobs; diff two saved fingerprints |
| `fitr export <model> [--out PATH] [--retonr]` | HTML scorecard, and/or opt-in evidence for [retonr](retonr.md) |
| `fitr view [model\|result.json] [--display MODE] [--full]` | reopen a saved result; `--full` emits the complete sealed record only with JSON display |
| `fitr board [--current] [--display MODE] [--full]` | compare by device; `--full` emits complete sealed records only with JSON display |
| `fitr top [--view VIEW]` | open the keyboard-first Live, Result, Board, History, and Inventory monitor |
| `fitr top view [model\|result.json]` | open a selected saved result in the monitor |
| `fitr top run <model> [run flags]` | run the same evaluator with structured live progress |
| `fitr top --snapshot` | emit the versioned privacy-safe presentation snapshot as JSON |
| `fitr top history [path\|clear --yes]` | browse, locate, or clear archived runs while keeping canonical results |
| `fitr doctor <model> [-n N]` | can this box be measured fairly at all? (~1 min) |
| `fitr diag <model>` | 5-rung tool-use plumbing diagnostic |
| `fitr compare <a> <b>` | difference/ratio intervals; paired flips (accuracy can hide them) |
| `fitr decide [model\|result.json] --spec decision.json` | evaluate sealed evidence against explicit workload requirements; unresolved required evidence exits 4 |
| `fitr experiment context <model> --ctx 4096,8192,... [-k N]` | run a predeclared exploratory context plan with point-specific allocation and required-equal factor checks |
| `fitr experiment quant <result.json>... --spec decision.json [--lineage conversion.json]` | build a decision-relative conservative configuration frontier; optional lineage verifies a shared base revision |
| `fitr experiment confirm <model> <model>... --spec decision.json [--ctx N] [-k N]` | seal the selected candidate set, collect fresh paired full-run evidence, and confirm only a separated decision objective |
| `fitr experiment workload <model> [-n 3] [--ctx N]` | run the fixed bounded policy-repair workflow with independent deterministic verification and signed per-trial receipts |
| `fitr discover add <source> --role <role> [--model <reference>]` | capture a private, unmeasured model or harness idea |
| `fitr discover list\|plan [--role <role>]` | inspect the private inbox or draft next evidence steps without network access |
| `fitr discover attach-source <idea-id> <receipt.json>` | copy validated source metadata into a private association; keep the idea unmeasured |
| `fitr discover plan <idea-id> [--source <resolution-sha256>]` | inspect one source-aware plan; select explicitly when multiple receipts exist |
| `fitr discover detach-source <idea-id> <resolution-sha256>` | remove only the managed association copy; see [source attachments](source-attachments.md) |
| `fitr role init <name> --quality <need> --memory-gb <limit> [--ctx N]` | declare mandatory quality and resource floors for a personal role |
| `fitr role define <role.json>` / `show <name>` | version or export explicit requirements, preferences and freshness limits |
| `fitr role list` / `review <name>` | inspect role screening, evidence gaps, preference bounds and sensitivity; see [roles](roles.md) |
| `fitr role attach <name> <result.json>` / `detach <name> <digest>` | pin or remove a canonical evidence reference without deleting its source |
| `fitr role confirm <name\|bundle.json>` | seal role preferences before fresh trials, or inspect a saved confirmation; see [confirmation](role-confirmation.md) |
| `fitr role adopt <name> <bundle.json>` / `status <name>` / `rollback <name>` | explicitly retain a qualified selection, recheck it or restore valid previous evidence |
| `fitr mcp serve` | serve bounded read-only role tools over MCP 2026-07-28 stdio; see [interop](agent-interop.md) |
| `fitr source resolve hf --repo <owner/model> --revision <revision> --file <path> --out <receipt.json>` | resolve explicit public file metadata at an immutable commit without downloading weights; see [source resolution](source-resolution.md) |
| `fitr artifact bind --source <receipt.json> --mapping <files.json> --out <artifact.json>` | hash explicitly mapped local files within byte/time bounds; see [artifact binding](artifact-binding.md) |
| `fitr artifact show <artifact.json>` | validate and inspect a saved local-byte observation without reopening model files |
| `fitr source show <receipt.json>` | validate and inspect a saved metadata receipt offline |
| `fitr cleanup plan <directory> [--min-age-days 7]` | bounded read-only storage inventory with aged partial-download review candidates; see [cleanup](cleanup.md) |
| `fitr device [--display MODE]` / `fitr profiles [new]` | fingerprint and gates; `new` writes an UNCALIBRATED local profile |
| `fitr calibrate <a> <b> [--out PATH] [--lineage PATH]` | paired item discrimination; optional same-base lineage receipt |
| `fitr calibrate merge <pair.json>... [--out PATH]` | aggregate unsigned leads without claiming verified campaign readiness |

| Level | Runs | ~Time |
|---|---|---|
| `--quick` | speed, memory, plumbing; executable tasks recorded as SKIP | minutes; smoke-test the stack |
| *(default)* | + all 22 generated checks across 16 families, refusal, safe tool withdrawal | many minutes; first real measurement |
| `--full` | + long-horizon agent task, SKIP while execution is disabled | tens of minutes; coding still unproven |
| `--checks-only` | generated checks only; requires `--seedset`, defaults to 5 repeats | model-dependent |

A default run tells *broken* from *working*, not 71 from 74. `--full` adds up
to 40 agent turns; it is not a coding grade until the isolated worker exists.
Ctrl-C is safe (exit 130).

## Flags worth knowing

- `-k N` explicitly sets the repeat count for noisy tasks and generated-check
  rounds. Without it, standard and full runs use three speed and classic-task
  repeats but one complete generated-check round; quick uses one, and
  checks-only uses five. A single observation is not a measurement: identical
  configs vary 10-20 pp.
- `--seedset NAME` pins the generated-instance set. Two runs sharing a
  seedset face identical instances, which upgrades `fitr compare` to a
  paired test. Fresh instances per run remain the default.
- `--checks-only` is the efficient battery-calibration level. It requires a
  seedset and uses five fixed repeats by default. Both sides of a pair see
  every declared instance. See [calibration.md](calibration.md).
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
  `compare` never changes or reinterprets a saved context. Measure both
  configurations with the same `--ctx`, then compare them. The current CLI
  selects the newest exact named result; use History to select a specific
  saved pair.
- `--capacity-budget-gb N` (run) seals the operator's final safe memory
  budget before the allocation probe. It does not derive that budget from a
  device name, nominal capacity, or current free memory.
- `--capacity-reserve-gb N` (run) seals current availability, its source and
  observation time, then subtracts the exact operator reserve. If a process
  container has less headroom, the formula uses the smaller base. Zero is a
  valid explicit reserve. This flag is mutually exclusive with
  `--capacity-budget-gb`. Swap is excluded. Platforms without a defensible
  current-availability reading require the explicit budget form.
- `--load` (advise) loads an Ollama model and reads `/api/ps` so fit includes
  the live resident allocation, including runtime-managed memory beyond
  modeled weights and KV. It requires an explicit `--ctx`; compatibility is
  earned only when `/api/ps` reports that same effective context. `--fit` runs
  `llama-fit-params` on a GGUF when that binary is on PATH and reports its
  final device-memory projection. The
  current adapter does not capture the fitter's adjusted context, offload,
  tensor placement, binary version, or host-memory domain, so the projection
  is descriptive: it remains SKIP and does not enter a context row or produce
  a fit remedy. The weights+KV projection remains the default for conventional
  attention and is labeled as such. Hybrid recurrent architectures stay SKIP
  until `--load` observes their allocation at the runtime-reported context.
  Split GGUF weights are summed only after every declared shard is present.
  Pass any numbered shard of a split GGUF to `fitr advise`. fitr requires the
  complete declared set and sums every shard before evaluating capacity. A
  local path is an advice input, not a launch request: `fitr run` measures the
  exact model already served by the selected runtime. `--fit` invokes
  `llama-fit-params` for that one artifact; it is not a multi-model fit-set
  test.

  ```bash
  fitr advise "/models/model-Q4_K_M-00001-of-00009.gguf" \
    --ctx 32768 --display json
  ```

  Any numbered shard may identify the set, but every declared sibling must be
  present. The JSON reports the same evidence contract as the terminal table;
  automation should read its `tier` because compatible and SKIP both exit 0,
  while low-memory and incompatible exit 3.
- `--pull` fetches a missing Ollama tag before measuring. Pasted Hugging
  Face GGUF URLs pull automatically (they *are* the request to fetch).
- `--allow-unsafe-exec` runs unisolated built-in Python diagnostics after an
  interpreter preflight. The process must exit successfully and finish with
  one exact verifier receipt. These defenses do not create a sandbox, so the
  observation remains INCONCLUSIVE and is excluded from rankings.
- `--profile P` forces a device profile instead of auto-matching.
- `--vram-gb N` (advise) supplies an operator-declared GPU or unified-memory
  planning budget. Use it to encode a deliberate operating reserve, not the
  nominal sticker capacity. Without a safe budget or a point-specific runtime
  receipt, addressable unified capacity remains SKIP; fitr never guesses from
  the GPU's name.
- `--ctx N` (advise) is the context to size against; default is the model's
  max from GGUF metadata.
- `--html` (run) writes a self-contained HTML scorecard next to the JSON.
  Off unless you pass it. `fitr export <model> [--out PATH]` does the same
  from a saved result. The page carries an opaque device ID and only the
  allowlisted configuration needed to interpret the result. It omits raw model
  output, hostnames, local paths, the raw fingerprint key, and arbitrary
  runtime configuration. Performance and capacity are separate sections;
  decode and prefill carry `tok/s`, TTFT carries seconds, and resident memory
  appears only for a verified requested-32K allocation receipt. It is never
  uploaded automatically.
- `--full` with `fitr view --display json` or `fitr board --display json`
  emits the complete sealed local record instead of presentation JSON. Full
  records can contain raw model output, hostname, and device details; keep
  them local unless you have reviewed them.

## Output modes and exit codes

Every documented subcommand accepts `--help` and exits 0 after printing its
flag help. Malformed invocations write a diagnostic and next action to stderr,
exit 2, and do not begin runtime discovery or modify stored evidence.

```bash
fitr run m --display json    # NDJSON on stdout, nothing else
fitr advise qwen3:30b --display json  # one fitr.advise.v1 document
fitr view                    # newest saved result, with repeat-shape graphs on a rich terminal
fitr view m --display json   # privacy-safe presentation scorecard JSON
fitr view m --display json --full  # complete sealed local record; may contain sensitive data
fitr decide m --spec local-coding.json  # requirement-relative eligibility and next evidence action
fitr run m -q                # results only     -v  detail, no progress
NO_COLOR=1 fitr board        # honored (empty string means unset)
FITR_ASCII=1 fitr board      # force ASCII glyphs
FITR_UNICODE=1 fitr board    # force Unicode; Windows Terminal already enables it
FITR_WIDTH=120 fitr view     # compose to a width other than the default 80
fitr top                     # interactive terminal only
fitr top --snapshot          # pipe-safe presentation JSON, no terminal controls
```

`--display auto` chooses `rich` on a capable terminal and `plain` when stdout
is redirected. `rich` can be forced for a capture; `plain`, `json`, and `none`
are stable automation surfaces. Unknown mode names are usage errors.

### Width

Reports compose to **80 columns**. A narrower terminal narrows them; a wider
one does not widen them, so what you see on screen and what lands in
`fitr run m > out.txt` are the same bytes and a pasted result is the result.
`FITR_WIDTH` overrides both directions and is clamped to a 40-column floor.

Nothing is truncated to make the width: long text -- a family breakdown, an
undecided verdict's explanation, a doctor diagnosis -- wraps under its own
column rather than running off the edge. Two things are exempt on purpose. The
`fitr board` table is a fixed-column comparison grid and states its own width.
The device comparability key from `fitr device` is one identifier containing
spaces, so wrapping it would put a line break where a space is and anyone
copying it would copy something else; it gets its own line and the terminal
decides what happens next.

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
| 4 | a `decide` requirement is unresolved or blocked |
| 130 | interrupted |

For `advise`, SKIP exits 0 because no measured need failed. Automation that
must distinguish SKIP from compatible should read the JSON `tier`. Low-memory
and incompatible are completed negative measurements and exit 3; they are not
transport or parser errors. JSON is written before that semantic exit code is
returned.

Bare `fitr` and `fitr advise` with no model print the installed inventory:
what the serving runtime already has, joined to current fitr evidence. Each
row is **measured**, **unproven**, **incompatible**, or **stale**, with one
next command. Architecture, when already known, adds a compact context-fit
graph (`2k ok | 4k ok | *8k ok | 16k no`). EVID is the measured window, or
`measured/serving` when a live process reports a different allocation, not the
artifact maximum.
A measured run at a non-default ctx asks `fitr apply` until the server is
already serving that window. Unmeasured is a candidate, never a
recommendation. Color does not carry the state. `fitr <model>` is named
advise; inventory does not `Show()` every blob or pull anything.

Model arguments are exact, apart from the explicit `name` and `name:latest`
equivalence. Long names may be filtered and selected in `fitr top`, but
truncated display text never becomes identity. fitr does not guess from a
prefix or a row number.

If a saved result cannot be decoded or trusted, inventory keeps every healthy
row visible and prints an evidence warning. The damaged file never contributes
to a `measured` state. `fitr top history` shows the file-level details needed
to repair or remove it.

A named `fitr advise` prints a context-fit table at 2k / 4k / 8k / 16k / 32k
and the architecture max: weights, KV, other allocation where its evidence is
known, need, and
remaining capacity. The non-weight, non-KV part is `n/a` until `--load` or
another exact-context runtime receipt provides a total. `--fit` remains a
descriptive allocator projection and is not attached to a context row until
its final context and placement are captured.
Decode/prefill appear only from a saved run at that ctx. Hybrid recurrent
models skip the algebraic table until a measurement exists. The suggested
row is the largest window that still fits when the requested one does not.

## Reading performance and capacity

Performance and capacity are deliberately separate. A model may fit and still
generate slowly, process long prompts quickly while streaming slowly, or have
good loaded responsiveness while paying a large runtime-unloaded startup cost.

- **Request TTFT** is wall time from the gated request start to the first
  non-empty streamed chunk. It is labeled **Loaded TTFT** only when that
  request has a runtime residency receipt showing no material reload. Its
  cache state is reported separately. Unknown residency, unknown cache state,
  or a cache hit remains an observation but cannot prove the loaded-uncached
  responsiveness gate.
- **Runtime-unloaded TTFT** is wall time from a request made after fitr unloaded
  the model to the first non-empty streamed chunk. It is not machine-cold or
  disk-cold because the operating system may still cache model pages.
- **Loaded cache-hit TTFT** is reported separately only when the backend
  receipt identifies a positive cached-token count.
- **Runtime load** is the backend's model-load duration when the versioned
  protocol provides it. It is not inferred by subtracting TTFT observations.
- **Prefill** describes input-token processing and matters for long prompts,
  retrieval, and repeated agent histories. A gate or comparison requires an
  explicit zero-cache receipt for every sample; unknown state or any positive
  cached-token count remains descriptive only.
- **Decode** describes generated-token streaming after prompt processing.
- **Resident** is observed loaded allocation when the runtime reports it.
- **Accelerator allocation** is the runtime-classified byte count at that same
  exact-context memory probe. **Non-accelerator allocation** is only resident
  minus accelerator bytes. Neither proves dedicated VRAM, host spill, layer
  placement, or the absence of host traffic on a unified-memory system.
- **Weights** and **KV** are derived from artifact metadata. The remainder of
  observed resident allocation is labeled as other resident memory, not as a
  directly measured compute-buffer component.

### Capacity policy for a run

When the memory probe is planned, a new run creates a versioned capacity plan
before the runtime allocation is observed. The plan keeps these facts apart:

| Fact | Meaning |
|---|---|
| Addressable | Memory the selected resource domain can address |
| Available now | A timestamped transient operating-system or device reading |
| Container headroom | The remaining process-memory allowance when measurable |
| Operator budget | A final safe limit supplied with `--capacity-budget-gb` |
| Operator reserve | Bytes subtracted from current availability with `--capacity-reserve-gb` |
| Component projection | Artifact bytes plus conventional KV arithmetic before load |
| Observed resident | Runtime allocation at the verified memory-probe context |

The exact formula is either `operator_budget`,
`current_available-operator_reserve`, or
`min(current_available,container_headroom)-operator_reserve`. Availability
without an operator choice remains an unresolved budget. Artifact plus KV is
marked as a component projection because runtime buffers, mappings, allocator
overhead, in-flight peaks, and placement are not yet observed. It cannot prove
fit or failure.

`FIT` or `EXCEEDED` appears only when an exact-context resident allocation can
be compared with the sealed usable budget. Headroom is the signed difference
between those two values. Older records remain readable, but they retain an
explicit capacity-policy gap rather than receiving a budget retroactively.

The footprint check requests a 32K load probe. It is scored only when the
runtime receipt confirms that the effective context is exactly 32K. If the
runtime clamps or does not report the effective context, the raw receipt is
retained, but Result and Board do not present its allocation as verified 32K
capacity and the need is SKIP.

The allocation attribution applies to the exact memory-probe point, not every
speed repeat in the battery. Current output does not claim a hardware
bottleneck. Evidence-backed limiter diagnoses, context sweeps, quant tradeoff
frontiers, sustained runs, and concurrent serving tests are planned as
distinct experiment types so their measurements cannot contaminate an
ordinary scored run.

## Cores

fitr already schedules on every logical CPU Go can see. Runtime discovery
probes ports at once; hardware probes overlap, share a five-second deadline,
and honor cancellation; split GGUF shards are sized in parallel. `fitr` and
`fitr device` print that CPU count as display-only.
It is not part of the fingerprint key.

A scored `fitr run` is still one request at a time. Concurrent prompts on
the same server contaminate timings and divide the context (doctor warns
on `OLLAMA_NUM_PARALLEL>1`). Faster runs come from `--quick`, fewer
repeats, or a smaller model - not from parallel inference. The serving
runtime owns how many threads actually decode.

## Advise

`fitr advise <model>` sizes a model against this box and prints three decision
tiers: **compatible**, **low memory** (`try num_ctx=4096 -> fits in 19.4 GB`),
or **incompatible**. It prints **SKIP** when the required evidence is missing.
Negative tiers carry a concrete remedy.

If the model is already loaded, the number is the server's own resident
bytes. The non-weight, non-KV part is a derived remainder, not an independently
measured compute-buffer component. Otherwise it is **weights + KV** from GGUF
architecture, and other runtime allocation is excluded and said so. A
running process that exceeds the VRAM *reading* is SKIP, not Incompatible -
the budget is the suspect number. MoE decode class uses *active* parameters,
not total.

Hybrid recurrent architectures are not conventional KV-only models. fitr does
not project their memory from an incomplete formula. Use `--load` at the
requested context for a runtime observation. `--fit` can display an unbound
allocator projection, but it cannot establish a point-specific verdict. For
split GGUFs, every declared shard must be present and the weight total is the
sum of the complete set; a shard header is never mistaken for the whole model.

SKIP, never a guess, when GPU memory was not measured, weights are unknown,
or architecture metadata is missing. `--vram-gb N` supplies a budget; a GPU
name is never turned into a VRAM number. There is no catalog of models to
pick from - advise answers "does THIS fit", not "what exists".

User task families can show whether a configuration handles representative
work, but current aggregate records cannot honestly produce time to valid
result or accepted outcomes per hour. Those require sealed per-trial timing,
attempt, verifier, retry, and escalation receipts. See
[Workload evidence and bounded workflows](workload-evidence.md).

## Decide

`fitr decide [model|result.json] --spec decision.json` applies a strict,
versioned workload declaration to one validated sealed result. The original
profile-bound scorecard remains unchanged. This means the same measurement can
be evaluated against a new reliability or latency requirement without
pretending a new benchmark occurred.

```json
{
  "schema": "fitr.decision.spec.v1",
  "name": "local coding",
  "evidence_level": "decide",
  "requirements": [
    {
      "id": "structured",
      "behavior": {"need": "structured_output", "minimum_rate": 0.90}
    },
    {
      "id": "context",
      "context": {"minimum_effective_tokens": 16384}
    },
    {
      "id": "memory",
      "capacity": {
        "maximum_resident_gb": 22,
        "requested_context": 16384
      }
    },
    {
      "id": "responsiveness",
      "performance": {
        "metric": "loaded_ttft_seconds",
        "at_most": 1.0
      }
    }
  ]
}
```

Unknown fields, duplicate requirement IDs, mixed requirement kinds, invalid
bounds, and trailing JSON are rejected. Behavior rate requirements use the
same family-clustered interval as the scorer. Repeats of one scenario family
cannot impersonate independent evidence, and unavailable planned observations
cannot establish a requirement. `loaded_ttft_seconds` requires its exact
residency-supported claim; a request-TTFT number is not relabeled.
Profile-bound speed and footprint screening rows are not accepted as behavior
requirements. Express those needs as typed performance and capacity limits.

The decision states are:

- `eligible`: every required claim is established, exit 0.
- `ineligible`: at least one required claim is disproven, exit 3.
- `unresolved`: no requirement is disproven, but at least one is unresolved or
  blocked, exit 4.

An `objective` is comparative. Supplying one while evaluating a single result
keeps selection unresolved until a comparable eligible candidate set and
comparison policy exist. `evidence_level: confirm` also stays unresolved for
an ordinary run because discovery evidence cannot certify the candidate it
selected. The confirmation experiment will supply fresh lineage.

The full schema and evidence semantics are in
[Decision specifications](decisions.md).

## Context experiment

```bash
fitr experiment context qwen3:30b --ctx 4096,8192,16384 -k 3
```

The live command creates `fitr.experiment.context.plan.v1` before contacting
the runtime. Every point is a normal signed result whose manifest carries an
explicit `fitr.experiment.binding.v1` receipt with the plan digest, stage,
point index, and point count. The plan also creates one fresh shared task seed
set and binds every point to the same quick measurement level, so task drift
cannot invalidate or bias the context treatment. The point measures:

- requested and runtime-verified effective context
- decode, prefill, request TTFT, and their support state
- runtime allocation at that point's requested context
- exact-context placement attribution when the runtime supplies it
- evidence gaps such as unknown residency or cache state

Plans are bounded to 2 through 16 distinct points, at most 16,777,216 tokens
per point, and 1 through 20 performance samples per point. The same bounds are
enforced when a stored plan is replayed, not only by CLI flag parsing.

Requested context is the treatment. Artifact identity, backend and runtime,
device configuration, and the context measurement protocol are
required-equal factors. Observed placement is an outcome, so it is not
incorrectly required to remain equal when more context changes placement.
If a required factor changes, all points remain visible but the comparison is
unresolved.

Point execution follows the declared order. If one point fails operationally,
fitr stops, reports how many later points were not run, and keeps completed
point records. It does not insert failed values for unattempted contexts. A
fully completed plan writes a private local bundle under
`$FITR_RESULTS/.experiments` or `~/.fitr/results/.experiments`. The bundle
contains the plan, derived report, and signed point records. Reopening it
validates every source and re-derives the report:

```bash
fitr experiment context path/to/context-bundle.json
```

Two or more sealed result paths can also be analyzed retrospectively:

```bash
fitr experiment context result-4k.json result-8k.json --display json
```

That report is labeled `predeclared: false`. Result-file analysis never
contacts a runtime. Both forms are exploration, not confirmation. They map
capacity and performance but do not select or certify a winner on the same
observations.

## Quant configuration experiment

```bash
fitr experiment quant q8-result.json q5-result.json q4-result.json \
  --spec local-coding.json \
  --lineage conversion.json
```

The command evaluates every sealed candidate against the same decision spec.
Eligibility is constraint satisfaction. The objective is considered only
among eligible candidates, so a fast configuration that misses a required
behavior or capacity gate cannot win by speed. Unresolved eligibility and a
missing bounded objective metric keep the whole declared candidate set
unresolved. If exactly one fully resolved candidate is eligible, constraints
select it without inventing a metric comparison against failed candidates.

Backend/runtime, device configuration, verified context, task plan, seed set,
grading policy, and profile policy are required-equal factors. Artifact is the
treatment. A candidate appears on the conservative frontier unless another
eligible candidate is established as no worse on decode, request TTFT, and
resident bytes, and strictly better on at least one. Missing observations and
overlapping 95% intervals prevent a dominance claim rather than being replaced
with point-estimate rankings.

`--lineage` accepts a strict `fitr.lineage.conversion.v1` manifest. When it
names every runtime-bound artifact under one base revision, the report marks
same-base lineage verified. Without that receipt, the measurements can still
support a configuration choice, but they cannot attribute differences to
quantization. Even with lineage, the report uses the exact claim
`configuration_comparison`: a conversion manifest does not prove that chat
templates, tool parsers, or every other artifact-adjacent setting were equal.

An objective winner is labeled `exploratory`. The same observations that
selected it cannot certify it. If timing has only one sample or candidate
intervals overlap, the objective remains unresolved and the next action asks
for more comparable evidence. Use the separate confirmation stage once an
exploratory candidate set is worth testing.

## Fresh configuration confirmation

```bash
fitr experiment confirm qwen3:30b-q5 qwen3:30b-q4 \
  --spec local-coding-confirm.json --ctx 16384 -k 3
```

The decision spec must use `evidence_level: confirm` and declare one narrow
objective. Before inference, fitr resolves each candidate to a runtime-backed
artifact identity, observes the device configuration, creates a fresh shared
task seed, and seals all of those inputs into
`fitr.experiment.confirmation.plan.v1`. Two to four distinct candidates are
supported. At least three repeats are required.

Each candidate receives the full measurement battery under the same context,
task seed, runtime, device, grading policy, profile policy, and repeat count.
Its ordinary sealed run also contains a `confirm` experiment binding with the
plan digest and exact candidate position. A saved result without that binding
cannot be relabeled as fresh evidence after it looks favorable.

Confirmation applies the same conservative frontier rules as exploration. It
prints `CONFIRMED` only when one candidate clears every requirement and its
fresh 95% objective interval is better than every other eligible candidate.
Overlapping bounds remain ambiguous. Missing observations remain unresolved.
If no candidate is eligible because requirements are disproven, the command
exits 3. Unresolved confirmation exits 4.

The private confirmation bundle contains the plan, decision spec, complete
sealed point records, and derived report. Reopening it validates all source
receipts and rebuilds the report:

```bash
fitr experiment confirm path/to/confirmation-bundle.json
```

The confirmation proves a choice among the exact measured configurations. It
does not prove that quantization alone caused the difference unless a separate
same-base causal protocol establishes every relevant conversion factor.

## Validated work experiment

```bash
fitr experiment workload qwen3:30b -n 3 --ctx 8192
```

This first vertical workflow measures whether a configuration can repair a
small policy document under a declared authority boundary. The harness owns a
virtual filesystem and exposes only `list_files`, `read_file`, `write_file`,
and `run_checks`. Only `policy.json` is writable. There is no arbitrary shell,
process, network, clock, receipt, or verifier access.

The command creates `fitr.workload.plan.v2` after backend identity and device
probes, before the first workload generation request. The
plan binds the runtime-backed artifact identity, device fingerprint, requested
context, trial count, turn and time budgets, retention policy, fixed workflow
version, and an ephemeral completion public key. Each trial records monotonic
model, tool, worker, verifier, and terminal events. A deterministic verifier
runs after the worker stops and independently reconstructs the final state.
The trial digest and signature cover the complete event sequence, counters,
outcome, and verifier receipt.

The v2 plan also seals a `fitr.workload.contract.v1` declaration covering the
scenario and tool digests, deterministic verifier, virtual-file authority,
capability isolation, one-attempt policy, unsupported approvals and requested
context. The bundle and trial envelopes remain v1; their plan digest binds
the new contract. Older v1 plans and analysis remain readable without being
upgraded to claims their schema did not carry.

Analysis v2 reconstructs worker, model, tool, verifier queue, verifier and
harness-overhead time. Model and tool time are components of worker time.
Time to valid result exists only for independently accepted terminal outcomes.
Retries are not permitted in this fixed workflow; human wait and escalation
are unsupported, not measured zeroes. The plan's requested context does not
establish the runtime's effective context.

The aggregate never hides unsuccessful exposure:

```text
planned                 5
accepted                3
rejected                1
timed out               1
infrastructure fault    0
```

Median accepted time is withheld below three accepted trials. Accepted work
rate divides accepted outcomes by elapsed time across every terminal outcome,
not only successes. Coverage is `ESTABLISHED` only when at least three planned
trials all receive independent acceptance.

Raw prompts, model replies, tool arguments, and tool results exist only during
execution. The saved private bundle retains their hashes, the deterministic
verifier output, and the signed harness events. This supports integrity and
aggregate analysis, but not full raw replay. Reopen a bundle to validate it and
rebuild the report:

```bash
fitr experiment workload path/to/workload-bundle.json
```

A rejection exits 3. A timeout or infrastructure fault without a rejection
exits 4. Interrupted live execution exits 130 and does not emit a partial
claim. This fixed workflow is a safe design probe, not authority to run
arbitrary generated code or user-supplied workflow definitions.

## Apply

`fitr apply [model] [--ctx N]` prints the command to persist a measured
context on whatever is serving. It never restarts or mutates the process.
When a live process reports `context_length`, apply names that serving
window. Matching the measured ctx is not "already persisted for next
launch" - llama-server and Ollama derived tags still need the printout.

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

`fitr profiles new [name]` creates a private file without overwriting an
existing profile. User profile JSON is strict: malformed files, trailing JSON,
unknown fields, unsupported match keys, surrounding match whitespace, and
files over 1 MiB are hard errors so a damaged calibration cannot silently fall
back to unrelated gates. `match` accepts only `gpu_contains` and `host`.

### Where fitr keeps things

| Path | Holds | Override |
|---|---|---|
| `~/.fitr/results` | canonical measurements, one JSON per model | `$FITR_RESULTS` |
| `~/.fitr/results/.history` | an archive copy of every completed run | - |
| `~/.fitr/profiles` | your calibrated gates | `$FITR_PROFILES` |
| `~/.fitr/tasks` | your own check tasks | `$FITR_TASKS` |

Model weights are not fitr's; they live wherever the runtime put them
(`$OLLAMA_MODELS`, else `~/.ollama/models`). fitr never deletes them.

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
measured outcome; they do not replace external attestation. Reads are capped
at 64 MiB per result and 16 MiB per calibration report or conversion manifest;
oversized local inputs are reported instead of loaded into memory.

Use `fitr top history path` to locate the archive and
`fitr top history clear --yes` to delete archived copies while keeping the
canonical latest result for each model. Deleting a canonical file removes that
model from ordinary commands even if an archived copy remains. See
[Terminal monitor](tui.md) for keys and fallbacks, and
[Interface direction](interface.md) for the truly native desktop plan.
