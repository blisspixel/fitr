# fitr

[![CI](https://github.com/blisspixel/fitr/actions/workflows/ci.yml/badge.svg)](https://github.com/blisspixel/fitr/actions/workflows/ci.yml)
[![Native acceptance](https://github.com/blisspixel/fitr/actions/workflows/native-acceptance.yml/badge.svg)](https://github.com/blisspixel/fitr/actions/workflows/native-acceptance.yml)

**The fitr thesis is to determine what local AI actually works for your
workload on this machine, what evidence proves it, and which configuration
gives the best validated outcome.** FIT, behavior, and burst performance ship
today; validated-work, tradeoff, explanation, and coverage contracts are the
pre-1.0 direction.

You have an accelerator with a finite memory budget. That may be dedicated
VRAM, or one unified pool shared with the operating system and every other
process. New open models come out every week, and it is never obvious which
ones will fit, how much context you can give them, or whether any of them beat
what you are already running. Published benchmarks do not answer that: they
were run on someone else's hardware, often at a quant nobody mentions.

So you either spend an evening testing by hand every time something drops, or
you keep running whatever you set up months ago and hope it is still a good
choice.

fitr does that testing for you. Point it at Ollama, llama-server, or a supported
OpenAI-compatible endpoint and it lists the models already served. When the
runtime exposes the required artifact and allocation evidence, fitr works out
which ones fit within the measured or configured memory budget and at what
context length. It measures how each actually performs here and tells you the
exact setting to change when one does not fit.

> `llmfit` estimates what fits, and benchmarks how fast. Leaderboards rank what
> is smart on someone else's machine. Neither checks whether the model is
> *behaving*. **`fitr` tells you what is silently broken on yours** - the Q4
> that still writes clean prose but emits malformed tool calls, the parser that
> swallows them, the loop your GPU triggers and nobody else's does.

<img src="docs/assets/inventory.svg?v=0.9.10" alt="fitr inventory (demo data)" width="820">

## Install

The full loop needs a supported runtime that is running with at least one
installed model. Backend identity requirements and limits are documented in
[backends](docs/backends.md).

macOS / Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/blisspixel/fitr/main/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/blisspixel/fitr/main/install.ps1 | iex
```

One command, then it runs. No Python, no venv, no CUDA wrangling, nothing to
keep up to date. fitr sends no telemetry. Installed-model evaluation talks only
to the endpoint you selected; network access otherwise occurs only for an
explicit install, pull, or remote endpoint. Installers verify the download
against its published checksum.
Pinning, relocating and building from source are in
[install](docs/usage.md#install).
Candidate releases are also installed through that checksum-verifying path and
run against a pinned native llama-server on clean Linux and macOS runners. The
reviewable receipts are tracked in [release acceptance](docs/release-acceptance.md).

## The loop

```bash
fitr                          # what is installed, what is measured, what next
fitr qwen3:30b                # does it fit, and which flag if not
fitr run qwen3:30b --ctx 16384   # measure it here
fitr apply qwen3:30b          # print how to persist that context
fitr board                    # compare only runs this device can honestly compare
```

Every row ends in the one thing to do next, so there is never a question of
what to run.

The product is organized around evidence, not a composite score:

- **FIT:** can the artifact, context, and placement live within this machine's
  measured or configured memory budget?
- **BEHAVIOR:** does it produce correct structured output, follow instructions,
  call tools through the real tool channel, and avoid degeneration?
- **PERFORMANCE:** how long do load, prompt processing, first response, and
  generation take in this configuration?
- **EXPLAIN:** which observed constraint is most likely to matter, with the
  evidence and uncertainty behind that diagnosis?
- **VALIDATED WORK:** does raw speed survive contact with correctness, retries,
  and independent verification?
- **TRADEOFFS:** which context, quant, or model configuration is dominated, and
  which alternatives remain genuine choices?
- **COVERAGE:** which declared workloads have earned local trust, and which
  still require evidence or a fallback?

FIT, behavior, and burst performance are measured today. The remaining layers
are the pre-1.0 direction, with their evidence contracts specified before
their CLI shape is frozen. See the [roadmap](ROADMAP.md) for the boundary
between shipped behavior and planned experiments.

**Does it fit, and what do I change if not.** Artifact bytes, derived KV cache,
and remaining capacity at each context length, with evidence labels and a flag
plus resulting number on every negative verdict.

<img src="docs/assets/advise.svg?v=0.9.10" alt="fitr advise (demo data)" width="820">

**What it actually does here.** Load, first-response, prompt-processing, and
generation timings are observed from the selected runtime. Resident allocation
is observed when the runtime reports it; KV and remaining-capacity parts are
labeled as derived. Structured output, instruction following, and tool use are
graded mechanically against computed or declarative answers. Refusal uses a
disclosed deterministic classifier. Another model's opinion never supplies a
core verdict.

Tool calls are measured **in the tool channel**, not as text. The most common
local tool failure is not bad JSON: it is a perfectly well-formed call that
arrives in the message body instead of the tool channel, because a chat
template or a tool-call parser did not fire. An agent harness reads that as
silence. fitr names it, and separates it from a model that genuinely cannot
call tools.

<img src="docs/assets/run.svg?v=0.9.10" alt="fitr run scorecard (demo data)" width="820">

## What it refuses to do

This is the part that makes the rest worth trusting.

- **It will not rank across machines.** Results carry the device, runtime,
  quant and context that produced them, and `board` compares only within a
  matching fingerprint.
- **It will not invent a number.** A missing input is SKIP, not an estimate.
  INCONCLUSIVE is a real answer.
- **It will not call an unmeasured model good.** Unmeasured is a candidate,
  never a recommendation.
- **It will not reuse evidence across changed weights.** Reuse requires a
  runtime-bound artifact digest. If a pull replaces the bytes behind a mutable
  tag, inventory marks the prior run stale and asks for a new measurement. A
  runtime that supplies only an observed local-file hash leaves the result
  display-only.
- **It will not add single-model estimates and call the sum co-residency.**
  Ordinary measurements keep one model resident. A multi-model capacity claim
  needs its own observed runtime contract.
- **It will not touch your server.** `apply` prints a recipe; it never restarts
  or mutates a running runtime.
- **It will not run generated code by default**, and it says so rather than
  scoring coding as if it had.

## Local, and yours

Evidence stays on your machine. No fitr account, sign-in, telemetry, or
automatic upload is involved. Evaluation of an installed model can run with
the network unplugged when its selected runtime is local. Sharing a result is
an explicit command. The exported artifact leaves out raw model output,
hostnames, local paths, the raw fingerprint key, and arbitrary runtime
configuration; it carries an opaque device ID and an allowlisted comparison
configuration instead.

fitr imposes no editorial or content-policy preference on which models you
should run. It measures whatever your runtime serves and reports which declared
needs each configuration supports. How often a model refuses is a first-class
need in the battery (`no filtering / low refusal`) rather than an awkward
footnote, because whether a model will actually answer you is a property of
that model on your hardware, and worth knowing before you commit to it.

Apache 2.0, and the evidence stays where it was produced.

## Documentation

**Start here.** [design](docs/design.md) for what a result means and why, then
[usage](docs/usage.md) for every command, flag and output mode.

**Going deeper.** [choosing hardware](docs/choosing-hardware.md) (capacity,
performance, workload fit, and honest buying evidence), [workload evidence](docs/workload-evidence.md)
(bounded workflows, independent proof, and validated work),
[statistics](docs/statistics.md) (methods and rejected alternatives),
[tasks](docs/tasks.md) (the battery, and adding your own without forking),
[backends](docs/backends.md) (Ollama, llama-server, OpenAI-compatible),
[doctor](docs/doctor.md) (can this box be measured fairly at all),
[calibration](docs/calibration.md) (paired-quant protocol), and [TUI](docs/tui.md)
(the opt-in monitor and its privacy contract).

**Project.** [roadmap](ROADMAP.md), [release
acceptance](docs/release-acceptance.md), [retonr](docs/retonr.md) (optional
sister project; fitr works without it).

Screenshots use demo data and regenerate from the real printers via
`make screenshots`. They reflect current `main` and may be ahead of the latest
stable release.

## License

[Apache License 2.0](LICENSE). Dependency licenses are in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
