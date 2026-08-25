# fitr

[![CI](https://github.com/blisspixel/fitr/actions/workflows/ci.yml/badge.svg)](https://github.com/blisspixel/fitr/actions/workflows/ci.yml)

**fitr tells you which local models actually run well on your machine, and what
settings to use.**

You have a GPU with a fixed amount of VRAM. New open models come out every
week, and it is never obvious which ones will fit, how much context you can
give them, or whether any of them beat what you are already running. Published
benchmarks do not answer that: they were run on someone else's hardware, often
at a quant nobody mentions.

So you either spend an evening testing by hand every time something drops, or
you keep running whatever you set up months ago and hope it is still a good
choice.

fitr does that testing for you. Point it at Ollama or llama.cpp and it lists
the models you already have, works out which ones fit in your VRAM and at what
context length, measures how each actually performs here, and tells you the
exact setting to change when one does not fit.

> `llmfit` tells you what fits. Leaderboards tell you what is smart on someone
> else's machine. **`fitr` tells you what is true on yours**, and what is
> quietly wrong.

<img src="docs/assets/inventory.svg" alt="fitr inventory (demo data)" width="820">

## Install

macOS / Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/blisspixel/fitr/main/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/blisspixel/fitr/main/install.ps1 | iex
```

One command, then it runs. No Python, no venv, no CUDA wrangling, nothing to
keep up to date. No telemetry either: fitr only ever talks to the runtime you
point it at. Installers verify the download against its published checksum.
Pinning, relocating and building from source are in
[install](docs/usage.md#install).

## The loop

```bash
fitr                          # what is installed, what is measured, what next
fitr qwen3:30b                # does it fit, and which flag if not
fitr run qwen3:30b --ctx 16384   # measure it here
fitr apply qwen3:30b          # print how to persist that context
fitr board                    # compare only runs this device can honestly compare
```

Every row ends in one next command, so there is never a question of what to do
next.

**Does it fit, and what do I change if not.** Weights, KV cache and headroom at
each context length, with a flag and a resulting number on every negative
verdict.

<img src="docs/assets/advise.svg" alt="fitr advise (demo data)" width="820">

**What it actually does here.** Speed, memory, structured output, instruction
following, refusal, tool use, all graded in Go against computed answers, never by
another model's opinion.

<img src="docs/assets/run.svg" alt="fitr run scorecard (demo data)" width="820">

## What it refuses to do

This is the part that makes the rest worth trusting.

- **It will not rank across machines.** Results carry the device, runtime,
  quant and context that produced them, and `board` compares only within a
  matching fingerprint.
- **It will not invent a number.** A missing input is SKIP, not an estimate.
  INCONCLUSIVE is a real answer.
- **It will not call an unmeasured model good.** Unmeasured is a candidate,
  never a recommendation.
- **It will not touch your server.** `apply` prints a recipe; it never restarts
  or mutates a running runtime.
- **It will not run generated code by default**, and it says so rather than
  scoring coding as if it had.

## Local, and yours

Everything happens on your machine. No account, no sign-in, no telemetry, no
upload. fitr only ever talks to the runtime you point it at, and it works the
same with the network unplugged. Sharing a result is an explicit command, and
the artifact it writes leaves out the raw model output.

It also has no opinion about which models you should run. Whatever your runtime
serves is what fitr measures, and how often a model refuses is a first-class
need in the battery (`no filtering / low refusal`) rather than an awkward
footnote, because whether a model will actually answer you is a property of
that model on your hardware, and worth knowing before you commit to it.

Apache 2.0, and the evidence stays where it was produced.

## Documentation

**Start here.** [design](docs/design.md) for what a result means and why, then
[usage](docs/usage.md) for every command, flag and output mode.

**Going deeper.** [statistics](docs/statistics.md) (methods and rejected
alternatives), [tasks](docs/tasks.md) (the battery, and adding your own without
forking), [backends](docs/backends.md) (Ollama, llama-server,
OpenAI-compatible), [doctor](docs/doctor.md) (can this box be measured fairly
at all), [calibration](docs/calibration.md) (paired-quant protocol),
[TUI](docs/tui.md) (the opt-in monitor and its privacy contract).

**Project.** [roadmap](ROADMAP.md), [release
acceptance](docs/release-acceptance.md), [retonr](docs/retonr.md) (optional
sister project; fitr works without it).

Screenshots use demo data and regenerate from the real printers via
`make screenshots`.

## License

[Apache License 2.0](LICENSE). Dependency licenses are in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
