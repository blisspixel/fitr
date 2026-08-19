# Backends

`fitr` auto-detects what is running, in this order:

| Backend | Default endpoint | Override |
|---|---|---|
| Ollama | `http://127.0.0.1:11434` | `OLLAMA_BASE_URL` |
| llama.cpp `llama-server` | `http://127.0.0.1:8080` | `LLAMA_SERVER_URL` |
| any OpenAI-compatible server (LM Studio, vLLM, SGLang) | `http://127.0.0.1:1234` | `FITR_OPENAI_URL` |

Force one with `--backend ollama|llama-server|openai` or `FITR_BACKEND`.

Auto-detect identifies the *runtime* by response shape, not by port:
`/api/tags` is Ollama, `/props` with a build or model path is llama-server,
`/v1/models` is OpenAI-compatible. llama-server also speaks `/v1/models`,
so the `/props` hit wins on the same URL. Extra ports (vLLM 8000, Jan 1337,
SGLang 30000, koboldcpp 5001, ...) are probed only as fallbacks; a forgotten
vLLM does not steal the measurement from a running Ollama. Add more with
`$FITR_DISCOVER_URLS` (comma-separated). If more than one runtime answers,
fitr uses the first and prints the others.

## Hugging Face GGUF links

Paste a Hugging Face repo or blob URL as the model argument. fitr rewrites
it to the `hf.co/{user}/{repo}[:quant]` form Ollama pulls natively, then
fetches it if it is not already installed:

```bash
fitr run https://huggingface.co/bartowski/Qwen2.5-Coder-7B-Instruct-GGUF
fitr run https://huggingface.co/bartowski/Qwen2.5-Coder-7B-Instruct-GGUF/blob/main/Qwen2.5-Coder-7B-Instruct-Q4_K_M.gguf
fitr run hf.co/bartowski/Qwen2.5-Coder-7B-Instruct-GGUF:Q4_K_M
```

That is an Ollama feature. llama-server and OpenAI-compatible servers already
have a model loaded; pointing them at an HF URL is a mismatch, not a download.
For those, pass the served name (or start llama-server yourself with `-hf`).

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

The GPU compute API (CUDA / Metal / Vulkan / ROCm) is read from `/props`
when the build exposes it, and is part of the device fingerprint: a Vulkan
run and a CUDA run of the same binary are not comparable.

**`fitr advise`** sizes a model from GGUF architecture: a local `.gguf`,
Ollama `/api/show` `model_info`, or llama-server's `model_path` when that
file is readable. OpenAI-compatible servers do not expose GGUF metadata, so
advise SKIPs the KV cache (and says so) unless you pass a `.gguf` path.
Unmeasured GPU memory is SKIP, never a card-from-name number.

**Generic OpenAI-compatible** covers roughly ten of the twelve relevant
local runtimes with one adapter. Its honesty notes: timings are
**client-derived** (token counts from usage, rates from wall clock) and the
scorecard labels them so they cannot be mistaken for Ollama or llama-server
counters; tool support is claimed optimistically and then **verified by the
plumbing diagnostic** before any tools verdict is issued; vision is never
claimed unverifiably; resident-memory needs SKIP.

Across all three, tool-call arguments are normalized to one shape, and
malformed ones survive as malformed so the tool loop counts them instead of
laundering them into empty objects. Reasoning content round-trips across
agentic turns on every backend - a harness that silently drops it records
its own loss as the model's failure.
