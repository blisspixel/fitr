# fitr

[![CI](https://github.com/blisspixel/fitr/actions/workflows/ci.yml/badge.svg)](https://github.com/blisspixel/fitr/actions/workflows/ci.yml)

**Know which local models are worth running on your machine, and what to
change when one isn't.**

fitr is a single static binary that reads the models your runtime is already
serving, works out what fits in your VRAM, measures what they actually do on
this box, and refuses to compare results that are not comparable.

A new model lands on your feed. The post skipped the quant. The leaderboard was
someone else's GPU. You still do not know whether this artifact fits in *your*
VRAM, or what it does once it is there.

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

No Python, no venv, no package manager. Installers bind the downloaded asset to
its exact `SHA256SUMS` entry. Details and build-from-source are in
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
following, refusal, tool use — graded in Go against computed answers, never by
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

## Documentation

**Start here** — [design](docs/design.md) for what a result means and why, then
[usage](docs/usage.md) for every command, flag and output mode.

**Going deeper** — [statistics](docs/statistics.md) (methods and rejected
alternatives), [tasks](docs/tasks.md) (the battery, and adding your own without
forking), [backends](docs/backends.md) (Ollama, llama-server,
OpenAI-compatible), [doctor](docs/doctor.md) (can this box be measured fairly
at all), [calibration](docs/calibration.md) (paired-quant protocol),
[TUI](docs/tui.md) (the opt-in monitor and its privacy contract).

**Project** — [roadmap](ROADMAP.md), [release
acceptance](docs/release-acceptance.md), [retonr](docs/retonr.md) (optional
sister project; fitr works without it).

Screenshots use demo data and regenerate from the real printers via
`make screenshots`.

## License

[Apache License 2.0](LICENSE). Dependency licenses are in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
