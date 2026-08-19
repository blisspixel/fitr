# Backends

`fitr` auto-detects what is running, in this order:

| Backend | Default endpoint | Override |
|---|---|---|
| Ollama | `http://127.0.0.1:11434` | `OLLAMA_BASE_URL` |
| llama.cpp `llama-server` | `http://127.0.0.1:8080` | `LLAMA_SERVER_URL` |
| any OpenAI-compatible server (LM Studio, vLLM, SGLang) | `http://127.0.0.1:1234` | `FITR_OPENAI_URL` |

Force one with `--backend ollama|llama-server|openai` or `FITR_BACKEND`.

The serving runtime and its version are part of the device fingerprint: a
backend change is a different measurement, and `fitr board` will not rank
across it.

## What each backend can measure, honestly

**Ollama** is the native path: server-side timings, resident memory via
`/api/ps`, capabilities via `/api/show`, and config read from the server's
own log (authoritative over your shell's environment).

**llama-server** is not just reach - it is measurement surface Ollama does
not expose: per-request **cached-token counts** (the only honest way to
separate a warm prefill from a cold one; the two differ by 70-200x), and a
**capability probe** via `/props`, so tool and vision support is read from
the endpoint, never guessed from the model's name. Resident-memory needs
SKIP on llama-server - it does not report resident bytes, and a made-up
number would be worse than a gap.

**Generic OpenAI-compatible** covers roughly ten of the twelve relevant
local runtimes with one adapter. Its honesty notes: timings are
**client-derived** (the surface exposes token counts but no server timings,
so rates come from wall clock and usage counts), tool support is claimed
optimistically and then **verified by the plumbing diagnostic** before any
tools verdict is issued, vision is never claimed unverifiably, and
resident-memory needs SKIP.

Across all three, tool-call arguments are normalized to one shape, and
malformed ones survive as malformed so the tool loop counts them instead of
laundering them into empty objects. Reasoning content round-trips across
agentic turns on every backend - a harness that silently drops it records
its own loss as the model's failure.
