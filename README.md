# fitr

[![CI](https://github.com/blisspixel/fitr/actions/workflows/ci.yml/badge.svg)](https://github.com/blisspixel/fitr/actions/workflows/ci.yml)

**Is this local model any good - on your machine?**

A new model lands on your feed. The post skipped the quant. The leaderboard
was someone else's GPU. The datacenter number is not your number. You still
do not know whether this artifact fits in *your* VRAM, or what it actually
does on this box, in a measurement you can compare to the last one you tried.

```bash
fitr                              # what's installed, what's measured, what to run next
fitr advise some-new-model:tag    # does it fit, which flag if not
fitr run some-new-model:tag --pull
fitr apply some-new-model:tag     # print how to persist the measured ctx
fitr board                        # compare everything you have measured
fitr top                          # the same loop, full-screen
```

<img src="docs/assets/inventory.svg" alt="fitr inventory (demo data)" width="820">
<img src="docs/assets/advise.svg" alt="fitr advise (demo data)" width="820">
<img src="docs/assets/run.svg" alt="fitr run scorecard (demo data)" width="820">
<img src="docs/assets/apply.svg" alt="fitr apply (demo data)" width="820">
<img src="docs/assets/board.svg" alt="fitr board (demo data)" width="820">
<img src="docs/assets/top.svg" alt="fitr top full-screen monitor (demo data)" width="820">

The CLI is the primary product surface. Rich terminals get semantic color,
throughput bars, and repeat-shape graphs; plain and JSON output remain clean
automation interfaces. `fitr top` is an opt-in full-screen monitor with Live,
Result, Board, and immutable History views. The [interface direction](docs/interface.md)
keeps this renderer-neutral foundation on a path to truly native desktop clients.

One evening, one device, one config:

- **Keep it, drop it, or try this flag.** Compatible / Low memory /
  Incompatible. A no always carries the next command (`try --ctx 4096`).
  Too large is not the end of the answer.
- **What is silently broken.** The Q4 that writes clean prose and emits
  malformed tool calls. The chat template that looks fine and produces zero
  calls. The loop this GPU triggers and the screenshot's GPU did not.
- **What it is actually for here.** Independent PASS, FAIL, INCONCLUSIVE,
  SKIP, `n/a`, and blocked results. Never one number. Never a rank across
  CUDA and Vulkan, or last month's driver and this one.
- **What it refuses to guess.** If the run cannot be named, fairly tested, or
  separated from noise, the scorecard says so. INCONCLUSIVE is a real answer.

> `llmfit` tells you what fits. Leaderboards tell you what is smart on
> someone else's machine. **`fitr` tells you what is true on yours** - and
> what is quietly wrong.

## Why this exists

Local models ship faster than anyone can re-bench them on your hardware.
Folklore ("Q4_K_M should fit," "looks fine in chat") is how evenings
disappear and how a silently broken quant becomes the daily driver. Fit
tools stop at memory. Public boards stop at someone else's machine.

fitr exists so the measurement and the next flag live in the same loop.
`advise` says whether it belongs in VRAM, and what to change if not. `run`
says whether it holds up here: structured output, instruction following,
tool plumbing, loops, long sessions. `apply` prints how to keep the context
that actually worked. `board` compares only runs this device can honestly
compare.

The interior is strict so the surface can be trusted: independent needs
instead of one score, INCONCLUSIVE instead of a lie, no ranking across
fingerprints, no fabricated GB from a GPU name. You should not need that
vocabulary to get through Thursday night. You should be able to see it
when you look.

The default evidence path is one static binary with no Python runtime, venv, or
package manager. Generated-code execution is disabled by default. The explicit
`--allow-unsafe-exec` diagnostic requires Python on PATH, is not sandboxed, and
can never contribute PASS or FAIL evidence. See [task safety](docs/tasks.md).

fitr works against **Ollama, llama.cpp's llama-server, or an OpenAI-compatible
server whose model identity can be verified**. Ranking needs a runtime digest
we can check; a mutable label is never enough. How each backend is identified,
and what remains visible but unrankable, is in [backends](docs/backends.md).
Signed releases and externally anchored share provenance remain later trust
work; local history reconciliation is tamper evidence, not attestation.

Optional sister: [retonr](https://github.com/blisspixel/retonr) reconstructs
drafts in your style under fidelity gates. fitr can export device-measurement
evidence for it (`fitr export <model> --retonr`). **fitr works without
retonr.** The export is not a qualification.

## Install

macOS / Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/blisspixel/fitr/main/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/blisspixel/fitr/main/install.ps1 | iex
```

Then:

```bash
fitr                             # what's installed, what's measured, what next
fitr advise qwen3:30b            # does it fit, and if not, which flag to try
fitr run qwen3:30b --ctx 8192    # default battery; --quick to smoke-test, --full for the long agent loop
fitr apply qwen3:30b             # print how to persist the measured ctx
fitr view                        # data-rich view of the newest saved run
fitr board
fitr top                         # keyboard-first full-screen data view
fitr top run qwen3:30b           # the same measurement with live monitoring
```

From source (Go 1.25+): `git clone https://github.com/blisspixel/fitr && cd fitr && make install`.
Pin a release with `FITR_VERSION=v0.6.0`, relocate with `FITR_BIN`.

`--quick` is speed, memory, and plumbing. The default run adds generated
checks, refusal, and tool withdrawal. `--full` adds a long agentic loop;
coding and executable-agent evidence stay SKIP until an isolated worker
exists. A default run tells *broken* from *working*, not 71 from 74.

## Commands

Everyday loop:

| Command | Does |
|---|---|
| `fitr` | installed models joined to evidence: measured, unproven, incompatible, or stale; one next command per row |
| `fitr advise [model]` | no model: the same inventory table. With a model: does it fit, and if not, which flag (`--load` / `--fit` to measure) |
| `fitr run <model> [--quick\|--full\|--checks-only] [-k N] [--ctx N]` | measure a model on this device; generated-code execution is disabled by default |
| `fitr apply [model]` | print how to persist a measured context; never restarts the server |
| `fitr view [model\|result.json]` | reopen the newest or selected saved result with repeat-shape graphs |
| `fitr board [--current]` | compare everything, grouped by device |
| `fitr top [--view VIEW]` | opt-in full-screen Live, Result, Board, and History monitor |

Also:

| Command | Does |
|---|---|
| `fitr top view` / `top run` / `top history` | open a result, run with live progress, or browse archives; `top --snapshot` emits the privacy-safe presentation snapshot |
| `fitr doctor <model>` | can this box be measured fairly at all? (several short generations; typically ~1 min on a loaded GPU) |
| `fitr compare <a> <b>` | difference/ratio intervals; paired flips on shared instances |
| `fitr diag <model>` | 5-rung tool-use plumbing diagnostic |
| `fitr tune [a b]` | request-level knobs; fingerprint diff of two saved runs (no silent sweep) |
| `fitr export <model> [--out PATH]` | write a self-contained HTML scorecard (opt-in; contains the fingerprint) |
| `fitr device` / `fitr profiles [new]` | fingerprint and gates; `new` scaffolds an UNCALIBRATED local profile |
| `fitr calibrate <a> <b> [--out PATH] [--lineage PATH]` | paired discrimination; optional same-base lineage receipt |
| `fitr calibrate merge <pair.json>... [--out PATH]` | aggregate unsigned leads; decision-grade still needs lineage, trust, and coverage |

## Three ideas carry everything

1. **A score is meaningless without the device it was measured on.** Every
   result embeds a hardware/runtime fingerprint and context receipt. `board`
   refuses to rank across fingerprints or when effective context is unknown.
2. **Tasks are generated, answers are computed, never stored.** There is no
   answer string in this repo to leak into training data, and every repeat
   is an independent trial.
3. **Small samples get honest statistics.** Most local runs live in n=3 to
   n=50, the regime where a lot of benchmark statistics quietly stop being
   true. Every run prints how sure to be, including "cannot separate." The
   methods and references live in [statistics](docs/statistics.md).

## Documentation

| Doc | Covers |
|---|---|
| [Design](docs/design.md) | the four commitments, the needs, how to read a result, known limits |
| [Statistics](docs/statistics.md) | every method, the rejected alternative, and the references |
| [Task battery](docs/tasks.md) | generated checks with computed answers; your own tasks without forking |
| [Calibration](docs/calibration.md) | paired quant protocol, safe evidence, and multi-device aggregation |
| [Doctor](docs/doctor.md) | determinism, served context, placement, config red flags |
| [Backends](docs/backends.md) | Ollama, llama-server, OpenAI-compatible - and what each can measure honestly |
| [Usage](docs/usage.md) | flags, output modes, exit codes, device profiles |
| [Interface direction](docs/interface.md) | CLI-first data UX, opt-in TUI, and truly native desktop path |
| [Terminal monitor](docs/tui.md) | full-screen views, keys, privacy contract, history, and fallbacks |
| [Roadmap](ROADMAP.md) | what is next and why |
| [retonr](docs/retonr.md) | optional sister project; fitr works without it |

Screenshots use demo data and regenerate from the real printers via
`make screenshots`, so they cannot drift from what the tool prints.

## License

MIT. Dependency licenses are listed in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
