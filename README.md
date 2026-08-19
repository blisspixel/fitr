# fitr

**Is this local model any good - on your machine?**

A new model drops. You want one answer, in about 15 minutes, that is true for
*your* hardware rather than for someone's A100:

```bash
ollama pull some-new-model:tag
fitr run some-new-model:tag --full
fitr board
```

<img src="docs/assets/run.svg" alt="fitr run scorecard (mock data)" width="820">

One run tells you, for this device and this config:

- **What the model is actually for here** - independent needs with PASS/FAIL
  gates (fast chat, coding, structured output, exact instructions, unattended
  agent work, low refusal, memory footprint), never one number.
- **What is silently broken** - the Q4 that writes clean prose but emits
  malformed tool calls, the parser that swallows them, the loop your GPU
  triggers and nobody else's does. No other harness reports this.
- **How sure to be** - every rate carries its confidence interval, every run
  prints what it cannot resolve, and "cannot separate" is a real answer.

> `llmfit` tells you what fits. Leaderboards tell you what is smart on
> someone else's machine. **`fitr` tells you what is true on yours.**

Single static binary. No Python runtime, no venv, no package manager. Works
against **Ollama, llama.cpp's llama-server, or any OpenAI-compatible server**
(LM Studio, vLLM, SGLang), auto-detected.

## Install

```bash
git clone https://github.com/blisspixel/fitr && cd fitr
make install
# or grab a binary for your platform from Releases
```

## Commands

| Command | Does |
|---|---|
| `fitr run <model> [--quick\|--full] [-k N]` | measure a model on this device (~4 to ~18 min by level) |
| `fitr board [--current]` | compare everything, grouped by device |
| `fitr doctor <model>` | can this box be measured fairly at all? (~1 min) |
| `fitr compare <a> <b>` | difference/ratio intervals; paired flips on shared instances |
| `fitr diag <model>` | 5-rung tool-use plumbing diagnostic |
| `fitr device` / `fitr profiles` | fingerprint and gates |

## Three ideas carry everything

1. **A score is meaningless without the device it was measured on.** Every
   result embeds a hardware/runtime fingerprint, and `board` refuses to rank
   across fingerprints.
2. **Tasks are generated, answers are computed, never stored.** There is no
   answer string in this repo to leak into training data, and every repeat
   is an independent trial.
3. **The statistics are built for n=3 to n=50** - the regime where most
   benchmark statistics quietly stop being true. Wilson and Newcombe
   intervals, Fieller ratios, McNemar paired tests, Wald sequential
   decisions, exact zero-event bounds. Every formula pinned to published
   reference values in tests.

## Documentation

| Doc | Covers |
|---|---|
| [Design](docs/design.md) | the four commitments, the needs, how to read a result, known limits |
| [Statistics](docs/statistics.md) | every method, the rejected alternative, and the references |
| [Task battery](docs/tasks.md) | generated checks with computed answers; your own tasks without forking |
| [Doctor](docs/doctor.md) | determinism, served context, placement, config red flags |
| [Backends](docs/backends.md) | Ollama, llama-server, OpenAI-compatible - and what each can measure honestly |
| [Usage](docs/usage.md) | flags, output modes, exit codes, device profiles |
| [Roadmap](ROADMAP.md) | what is next and why |

Screenshots use mock data and regenerate from the real renderer via
`make screenshots`, so they cannot drift from what the tool prints.

## License

MIT
