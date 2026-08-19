# Roadmap

`fitr` answers one question today: **is this model any good on this machine?**

The larger goal is to close a loop nothing currently closes:

```
    advise  -->  run  -->  tune  -->  apply
  what should    verify    find the    set them
  I even run?    it        settings
```

Most tools do exactly one of those steps. None connect them. Benchmarks tell you
a number and leave; catalogs tell you a model exists and leave; nothing says
*"run this, at this quant, with these settings, and here is the evidence."*

`run` is the engine of that loop, not the product. The product is the loop -
and the two commitments that make it worth trusting: measurements that are
honest about their own resolution, and **a remedy attached to every negative
verdict** (design rule 7).

---

## Where it is now (0.2.0-dev)

**Done and measured:**

- Device fingerprint embedded in every result; `board` refuses to rank across
  fingerprints
- Needs-based scoring - PASS / FAIL / SKIP / `n/a` / `BLKD` - instead of one number
- Repeats with Wilson intervals, flakiness flags, `INDISTINGUISHABLE` verdicts,
  first-run-slow detection
- Five-signal degeneracy detection
- Tool-use plumbing diagnostic that runs *before* capability is judged
- **16 generated check tasks** across 12 parameterized families, weighted toward
  structured output (quantization breaks JSON before prose). Instances are
  generated per run from a recorded seed; the correct answer is **computed,
  never stored** - there is no answer string in this repo to end up in training
  data, and every repeat is an independent trial. The whole battery self-tests
  without a model: each family's computed canonical answer must pass its own
  grader.
- Two new needs with their own gates: **structured output** and **instruction
  precision**, each scored as a pooled pass rate with its Wilson interval shown
- **`fitr doctor`** - can this box be measured fairly at all? Real-token
  preflight (HTTP 200 is not inference), N-run byte-identical determinism in
  plain text AND grammar-constrained JSON mode (a known local-stack
  reproducibility break), served-context probe, placement check (partial
  offload is a RAM benchmark wearing a GPU badge), config red flags. Nothing
  else in the ecosystem verifies any of this; every benchmark assumes it.
- **User tasks without forking** - `~/.fitr/tasks/*.json` (or `$FITR_TASKS`).
  Declarative only: a user task can prompt and grade (exact / contains / regex /
  json_object / number), it cannot execute anything. Malformed files are hard
  errors with the filename; id collisions are rejected.
- **Golden-corpus regression** - a frozen full-run result pins
  `measure -> Score -> render` end to end, including the FAIL paths
- The minimum detectable effect is now **printed on every run** - previously
  this roadmap claimed it was said out loud when it was only computed in a test
- **A statistics engine built for n=3 to n=50**, documented with references in
  docs/statistics.md: Newcombe difference intervals as the sole arbiter of comparisons
  (the intervals-overlap rule is an effective alpha of ~0.006 and was
  retired), Fieller ratio intervals with Welch df for "how many times faster",
  McNemar's exact test on paired instances via `--seedset` (with the
  fewer-than-six-flips impossibility stated instead of a doomed p-value),
  Wald SPRT adaptive repeats (`--adaptive`), exact zero-event bounds behind
  doctor's determinism claims, MAD outlier annotation on timing repeats, CV
  in every spread line, and borderline-gate annotations when a gate sits
  inside the pool's interval. Every formula pinned to published reference
  values in tests.
- Spec drift protection: `spec/` at the repo root is canonical, the embedded
  copies are compared byte-for-byte in tests, `make spec-sync` repairs
- Single static binary, 6 platforms (linux/darwin/windows × amd64/arm64),
  CI on 3 OSes
- **One-command install** matching the grok/codex shape:
  `curl -fsSL .../install.sh | sh` and
  `irm .../install.ps1 | iex`. Checksums on tagged releases; `FITR_VERSION`
  to pin, `FITR_BIN` to relocate.
- **Pasted Hugging Face GGUF URLs** rewrite to `hf.co/{user}/{repo}[:quant]`
  and pull through Ollama - blob/resolve links keep the quant from the
  filename. Regular Ollama tags still need `--pull` so a typo does not start
  a multi-gigabyte download.
- **Tool withdrawal** - a tool vanishes from the `tools` list mid-loop;
  `tool_restraint` now covers rest (irrelevance) and change (one grace call
  tolerated, persistence fails)
- Cold TTFT captured from the loading warm-up (when it actually loaded) and
  **disclosed** next to the loaded/uncached figure the gate still judges
- **Three-way TTFT split on backends that report cache receipts** (llama-server):
  load (cold start), loaded/uncached (the gate), cached-prefix (warm). A
  gated TTFT that was actually a cache hit is labeled contaminated rather
  than published as a new-question number.
- **Compaction watchdog is a FAIL**: hitting 80% of the context window with
  a transcript that never shrank fails `unattended_agentic`. Peak-then-compact
  is disclosed, not failed.
- **Runtime discovery by signature, not port** - `/api/tags` / `/props` /
  `/v1/models`, extra well-known local ports, `$FITR_DISCOVER_URLS`
- **GPU backend in the fingerprint** (cuda/metal/vulkan/rocm/...); `board`
  refuses to rank a Vulkan run against a CUDA one of the same binary
- **`fitr advise`** - three-tier fit (Compatible / Low memory / Incompatible)
  with a remedy on the negative tiers. SKIP when VRAM, weights, or
  architecture cannot be measured; never a GB number guessed from the GPU
  name. MoE decode class uses active parameters, not total.
- **Shareable HTML scorecard** - `fitr export` / `fitr run --html`. Opt-in,
  self-contained, fingerprint on the page, raw model output omitted.

**Honest limitations right now:**

| Limitation | Consequence |
|---|---|
| ~23 binary trials per default run | MDE ~ 29pp. Better than the ~33pp of six tasks, still separates *broken* from *working*, not *good* from *slightly better*. `-k 3` on checks triples the sample. |
| OpenAI-backend timings are client-derived | usage gives counts, not server timings; decode/prefill rates there are wall-clock estimates |
| 2 device profiles | `lappy` and an uncalibrated `default` |
| `advise` default is still weights+KV | `--load` / `--fit` measure; without them, Compatible can still OOM on compute buffers. No model catalog. |

---

## 0.3 - Meet people where their models already are

Promoted above everything else that remained, for two reasons found in the
August 2026 research pass: it roughly **triples the addressable users**, and
several measurements were **blocked** on it - llama.cpp's server uniquely
exposes cached-token counts (the only honest cold/warm TTFT split), logprobs,
`tool_choice`, and a chat-template capability probe.

- [x] **llama-server backend** behind a serving-backend interface
      (`internal/llm`). Native `/completion` for generation (server timings +
      cached-token counts land in every Metrics), OpenAI `/v1/chat/completions`
      for tool calling - the code path real agent frameworks exercise. String
      -encoded tool arguments are normalized; *malformed* ones survive as
      malformed so the tool loop counts them instead of laundering them.
- [x] **Capability probe over hardcoded knowledge** - tools/vision read from
      `/props` (`chat_template_caps` when the build exposes it), never from
      the model's name. The case study in docs/ records a shipped bug from a
      name-prefix guess.
- [x] **Runtime in the fingerprint** - "llama-server b6xxx" vs "0.32.14";
      `board` refuses to rank across them.
- [x] **Auto-detection** - probes Ollama then llama-server; `--backend` and
      `$FITR_BACKEND` to force.
- [x] **Exploit the cold/warm receipt** - three TTFTs: load, loaded/uncached
      (gated), cached-prefix (disclosed when `tokens_cached` is a real
      receipt). The gate never judges a cache-hit figure.
- [x] **Generic OpenAI-compatible adapter** for LM Studio / vLLM / SGLang -
      `/v1/completions` streaming with a chat fallback, usage-derived token
      counts, client-derived timings labeled as such, and the shared OpenAI
      wire mapping (`internal/oai`) both it and llama-server use. Tool support
      is claimed optimistically and verified by the plumbing diagnostic;
      vision is never claimed unverifiably.
- [x] **Wider runtime discovery** - identify by response shape, not port;
      extra well-known local ports; `$FITR_DISCOVER_URLS` for the rest.
- [x] **GPU backend (Vulkan/CUDA/Metal/ROCm) in the fingerprint** - read
      from `/props` / the Ollama log `library=`; `board` refuses to rank
      across it.

## 0.2.x - finish making the measurement trustworthy

- [x] **40-turn agentic floor** (was 20 - that measured early-abort behaviour)
      and **`reasoning_content` round-trip** in the loop; the Message struct
      simply had no field for it, so re-serializing the transcript deleted it.
- [x] **Compaction watchdog** - filling 80% of the window with a transcript
      that never shrank FAILs `unattended_agentic`. Peak-then-compact is
      disclosed, not failed.
- [ ] **Calibrate the check battery** by adversarial filtering: run it across
      known-good and known-degraded quants of the same model; drop items that
      never discriminate. (The Aider polyglot redesign kept 225 of 697
      exercises this way.)
- [x] **Quant damage as correctness agreement** (machinery). `fitr compare` on
      a shared seedset reports item-level flips, including when the two rates
      match - accuracy hid the disagreements. Directional "quant damage"
      against the higher-precision run is claimed only when both results
      expose a comparable GGUF dtype of the same family. Which *items*
      discriminate still needs a live calibration pass.
- [ ] **Quant-flip calibration on hardware.** Drop check items that never
      flip between known-good and known-degraded quants of the same model.
- [x] **`fitr tune`, first honest cut.** Prints the request-level knobs
      (`num_ctx`, `num_batch`, `num_gpu`) and diffs two saved fingerprints.
      It does **not** sweep: llama-bench owns throughput-only points, and a
      flash-attention quality regression is why throughput-only is not
      enough. Server-level env (`OLLAMA_FLASH_ATTENTION`, `KV_CACHE_TYPE`)
      still needs restart orchestration fitr does not have.
- [ ] **`fitr tune` sweeps** quality + degeneracy + throughput jointly per
      request-level point, once restart orchestration exists for server env.

## Path to 1.0

1.0 ships when a stranger can install in one command, point fitr at whatever
is already serving, and get a verdict they can act on - including *what to
run next* when the answer is no. That is the advise → run → tune → apply
loop, closable on someone else's machine.

| Bar | Status |
|---|---|
| One-command install (curl / irm) | **done** (binary downloads need a `v*` tag) |
| Honest measurement on Ollama, llama-server, OpenAI-compat | **done** (0.3) |
| Scorecard refuses to lie (doctor, degeneracy, compaction, cache-split TTFT, intervals, quant flips) | **mostly done** - check-battery calibration still needs hardware; flip machinery is in `compare` |
| `fitr advise` names a model + settings with a remedy | **done** for the NIM-shaped verdict; `--load`/`--fit` dummy-allocate; still no catalog |
| A result can leave the terminal | **done** - `fitr export` / `fitr run --html`, opt-in, fingerprint in the page |
| Community device profiles beyond `lappy` | **not started** |

We do **not** ship 1.0 with an uncalibrated `advise`, a public leaderboard,
or LLM-as-judge. Battery calibration still needs hardware. `tune` can trail
advise: a one-line remedy ("try num_ctx=4096") is advise; sweeping knobs is
tune.

## 0.4 - `fitr advise`

The step that turns a chore into a product.

- [x] Three-tier verdict with remediation in one line - Compatible / Low
      memory (`try num_ctx=4096 -> fits in 21.3 GB`) / Incompatible. SKIP
      when VRAM, weights, or architecture cannot be measured - never a
      fabricated GB number, never a guess from the GPU's name.
- [x] **Measure fit, don't model it** - `advise --load` observes Ollama
      resident size (compute buffers included); `advise --fit` runs
      `llama-fit-params` on a GGUF when present. Live resident still beats
      dummy allocation, which still beats weights+KV. Missing
      `llama-fit-params` is a note, not a fabricated GB. `gguf-parser-go`
      is not vendored: the allocator binary is the measurement.
- [x] Memory arithmetic that gets **MoE right** - decode tracks *active*
      parameters, not total. A 30B MoE (~3B active) at 24.8 tok/s beat an 8B
      dense at 14.6 on the same box; naive total-parameter math recommends
      exactly the wrong thing. Pinned against Qwen3-30B-A3B architecture.
- [ ] Honest open question, still deliberately open: a static binary cannot
      know what models exist. Curated catalog that ages vs live index vs model
      reasoning - pick deliberately; do not default. `advise` answers "does
      THIS fit", not "what should I download".

## 0.5 - Share the map

- [ ] **Community device profiles** - `rtx-4090`, `m3-max`, `strix-halo`. The
      network effect: the repo answers *"I have an M3 Max with 36 GB, what
      should I run?"*, which no leaderboard can structurally answer.
- [x] **Shareable result artifact** - self-contained HTML via `fitr export`
      or `fitr run --html`. Never automatic: the page contains a hardware
      fingerprint and is not uploaded. Raw model output is omitted.

## Later, if they earn it

- [ ] Exec-kind user tasks behind an explicit trust gate (they are arbitrary
      code execution from JSON; declarative stays the default)
- [ ] Long-context / needle tests; decode at 3+ context depths
- [ ] Sustained-throughput mode (thermal reality vs burst numbers)
- [ ] Same-config pairwise matchups (Glicko-2 style) - extracts signal our
      flat refusal-to-rank discards, but sits in tension with design rule 1;
      decide deliberately, not by drift

---

## Non-goals

- **A public leaderboard.** The whole thesis is that cross-device numbers are
  not comparable. Publishing a ranking would contradict the tool.
- **LLM-as-judge for correctness.** Coding is scored by *executing assertions*;
  checks are graded by computed ground truth. Judges get added only where
  nothing mechanical can reach, and only with the bias mitigations known to work.
- **Replacing lm-evaluation-harness / Inspect AI.** Those answer "how good is
  this model." `fitr` answers "how good is it *here*." Different question.
- **Chasing benchmark scores.** If a task stops discriminating, it gets replaced
  - the generated families make that a config change, not a rewrite.

---

## Design rules

These are load-bearing. Changes that break them need a strong argument.

1. **A number without its device is meaningless.** Never rank across fingerprints.
2. **Never fail a test you did not run.** `SKIP` and `n/a` are not `FAIL`.
3. **Plumbing before capability.** A tools failure is uninterpretable until the
   template and parser are known good.
4. **Never print a fabricated precision.** No `± 0.00` from one observation; no
   ranking when intervals overlap.
5. **Execution over opinion.** Pass/fail comes from running code or computed
   ground truth wherever it can.
6. **Say what was not measured.** An honest gap beats a confident guess.
7. **A verdict without a remedy is half an answer.** "Too large" is a dead end.
   `try --max-model-len=4096 to reduce to >=30 GB` is a product. Every negative
   result should carry the flag that fixes it and the number that results.
8. **Never ship an answer string.** A check task's correct answer is computed
   at run time from a recorded seed. Static answers in a public repo are
   training data with a delay.
