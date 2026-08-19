# Roadmap

`fitr` answers one question today: **is this model any good on this machine?**

The larger goal is to close a loop nothing currently closes:

```
    advise  ──►  run  ──►  tune  ──►  apply
  what should    verify    find the    set them
  I even run?    it        settings
```

Most tools do exactly one of those steps. None connect them. Benchmarks tell you
a number and leave; catalogs tell you a model exists and leave; nothing says
*"run this, at this quant, with these settings, and here is the evidence."*

`run` is the engine of that loop, not the product. The product is the loop —
and the two commitments that make it worth trusting: measurements that are
honest about their own resolution, and **a remedy attached to every negative
verdict** (design rule 7).

---

## Where it is now (0.2.0-dev)

**Done and measured:**

- Device fingerprint embedded in every result; `board` refuses to rank across
  fingerprints
- Needs-based scoring — PASS / FAIL / SKIP / `n/a` / `BLKD` — instead of one number
- Repeats with Wilson intervals, flakiness flags, `INDISTINGUISHABLE` verdicts,
  first-run-slow detection
- Five-signal degeneracy detection
- Tool-use plumbing diagnostic that runs *before* capability is judged
- **16 generated check tasks** across 12 parameterized families, weighted toward
  structured output (quantization breaks JSON before prose). Instances are
  generated per run from a recorded seed; the correct answer is **computed,
  never stored** — there is no answer string in this repo to end up in training
  data, and every repeat is an independent trial. The whole battery self-tests
  without a model: each family's computed canonical answer must pass its own
  grader.
- Two new needs with their own gates: **structured output** and **instruction
  precision**, each scored as a pooled pass rate with its Wilson interval shown
- **`fitr doctor`** — can this box be measured fairly at all? Real-token
  preflight (HTTP 200 is not inference), N-run byte-identical determinism in
  plain text AND grammar-constrained JSON mode (a known local-stack
  reproducibility break), served-context probe, placement check (partial
  offload is a RAM benchmark wearing a GPU badge), config red flags. Nothing
  else in the ecosystem verifies any of this; every benchmark assumes it.
- **User tasks without forking** — `~/.fitr/tasks/*.json` (or `$FITR_TASKS`).
  Declarative only: a user task can prompt and grade (exact / contains / regex /
  json_object / number), it cannot execute anything. Malformed files are hard
  errors with the filename; id collisions are rejected.
- **Golden-corpus regression** — a frozen full-run result pins
  `measure → Score → render` end to end, including the FAIL paths
- The minimum detectable effect is now **printed on every run** — previously
  this roadmap claimed it was said out loud when it was only computed in a test
- Spec drift protection: `spec/` at the repo root is canonical, the embedded
  copies are compared byte-for-byte in tests, `make spec-sync` repairs
- Single static binary, 5 platforms, CI on 3 OSes

**Honest limitations right now:**

| Limitation | Consequence |
|---|---|
| ~23 binary trials per default run | MDE ≈ 29pp. Better than the ~33pp of six tasks, still separates *broken* from *working*, not *good* from *slightly better*. `-k 3` on checks triples the sample. |
| OpenAI-backend timings are client-derived | usage gives counts, not server timings; decode/prefill rates there are wall-clock estimates |
| Cold/warm TTFT split not yet applied | cached-token counts are now recorded on llama-server; the speed phase does not use them yet |
| No compaction watchdog in the agentic loop | 40 turns now, but a model that never manages its context is not yet caught before the window fills |
| 2 device profiles | `lappy` and an uncalibrated `default` |
| Terminal output only | no shareable artifact |

---

## 0.3 — Meet people where their models already are

Promoted above everything else that remained, for two reasons found in the
August 2026 research pass: it roughly **triples the addressable users**, and
several measurements were **blocked** on it — llama.cpp's server uniquely
exposes cached-token counts (the only honest cold/warm TTFT split), logprobs,
`tool_choice`, and a chat-template capability probe.

- [x] **llama-server backend** behind a serving-backend interface
      (`internal/llm`). Native `/completion` for generation (server timings +
      cached-token counts land in every Metrics), OpenAI `/v1/chat/completions`
      for tool calling — the code path real agent frameworks exercise. String
      -encoded tool arguments are normalized; *malformed* ones survive as
      malformed so the tool loop counts them instead of laundering them.
- [x] **Capability probe over hardcoded knowledge** — tools/vision read from
      `/props` (`chat_template_caps` when the build exposes it), never from
      the model's name. The case study in docs/ records a shipped bug from a
      name-prefix guess.
- [x] **Runtime in the fingerprint** — "llama-server b6xxx" vs "0.32.14";
      `board` refuses to rank across them.
- [x] **Auto-detection** — probes Ollama then llama-server; `--backend` and
      `$FITR_BACKEND` to force.
- [ ] **Exploit the cold/warm receipt** — `CachedTokens` is recorded but the
      speed phase does not yet split cold vs warm TTFT on backends that
      report it. That split is the single biggest measurement error available.
- [x] **Generic OpenAI-compatible adapter** for LM Studio / vLLM / SGLang —
      `/v1/completions` streaming with a chat fallback, usage-derived token
      counts, client-derived timings labeled as such, and the shared OpenAI
      wire mapping (`internal/oai`) both it and llama-server use. Tool support
      is claimed optimistically and verified by the plumbing diagnostic;
      vision is never claimed unverifiably.
- [ ] **Wider runtime discovery** — more than three well-known ports; notice
      what is already running.
- [ ] **GPU backend (Vulkan/CUDA/Metal/ROCm) in the fingerprint** — the
      runtime version implies it poorly; read it from the server where exposed.

## 0.2.x — finish making the measurement trustworthy

- [x] **40-turn agentic floor** (was 20 — that measured early-abort behaviour)
      and **`reasoning_content` round-trip** in the loop; the Message struct
      simply had no field for it, so re-serializing the transcript deleted it.
- [ ] **Compaction watchdog** — flag a model that lets the transcript grow
      to the context limit without managing it.
- [ ] **Calibrate the check battery** by adversarial filtering: run it across
      known-good and known-degraded quants of the same model; drop items that
      never discriminate. (The Aider polyglot redesign kept 225 of 697
      exercises this way.)
- [ ] **Quant damage as correctness agreement.** Compare the same model at two
      quants on the pooled check trials — item-level flips against the
      higher-precision run, not accuracy deltas. Accuracy provably hides quant
      damage; flips expose it. This replaces the old "KL divergence" idea: KLD
      collapses to noise exactly in the near-baseline zone users care about.
- [ ] **`fitr tune`, re-scoped.** Sweep the *request-level* knobs first
      (`num_ctx`, `num_batch`, `num_gpu`) — no restart needed — and score
      **quality + degeneracy + throughput jointly** per point; llama-bench
      already owns throughput-only sweeps, and a documented flash-attention
      quality regression proves throughput-only sweeps miss what matters.
      Server-level env sweeps (`OLLAMA_FLASH_ATTENTION`, `KV_CACHE_TYPE`) need
      restart orchestration fitr does not have; until it does, `tune` prints
      the variant instructions and diffs the fingerprints it observes.

## 0.4 — `fitr advise`

The step that turns a chore into a product.

- [ ] Three-tier verdict with remediation in one line — Compatible / Low
      memory (`try num_ctx=4096 → fits in 21.3 GB`) / Incompatible. The one
      consumer-grade thing nobody ships: LM Studio says "Likely too large",
      `llmfit` says "Too Tight"; both are dead ends.
- [ ] **Measure fit, don't model it** — dummy-allocation style, vendoring
      `gguf-parser-go` for the arithmetic rather than rebuilding it.
- [ ] Memory arithmetic that gets **MoE right** — decode tracks *active*
      parameters, not total. A 30B MoE (~3B active) at 24.8 tok/s beat an 8B
      dense at 14.6 on the same box; naive total-parameter math recommends
      exactly the wrong thing.
- [ ] Honest open question, still deliberately open: a static binary cannot
      know what models exist. Curated catalog that ages vs live index vs model
      reasoning — pick deliberately; do not default.

## 0.5 — Share the map

- [ ] **Community device profiles** — `rtx-4090`, `m3-max`, `strix-halo`. The
      network effect: the repo answers *"I have an M3 Max with 36 GB, what
      should I run?"*, which no leaderboard can structurally answer.
- [ ] **Shareable result artifact** — self-contained HTML, or opt-in
      submission. Never automatic: results contain a hardware fingerprint.

## Later, if they earn it

- [ ] Exec-kind user tasks behind an explicit trust gate (they are arbitrary
      code execution from JSON; declarative stays the default)
- [ ] Long-context / needle tests; decode at 3+ context depths
- [ ] Sustained-throughput mode (thermal reality vs burst numbers)
- [ ] Same-config pairwise matchups (Glicko-2 style) — extracts signal our
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
  — the generated families make that a config change, not a rewrite.

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
