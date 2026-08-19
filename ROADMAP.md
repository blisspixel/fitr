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
| Ollama only | excludes llama.cpp server, LM Studio, vLLM, MLX users — likely the majority of serious local users |
| Cold/warm TTFT cannot be split on Ollama | the API exposes no cache counter; the fix is the llama-server adapter, not more code here |
| 20-turn agentic ceiling, `reasoning_content` dropped | both flagged in docs/inference-frontier as exposed; long-horizon agent verdicts are optimistic |
| 2 device profiles | `lappy` and an uncalibrated `default` |
| Terminal output only | no shareable artifact |

---

## 0.3 — Meet people where their models already are

Promoted above everything else that remains, for two reasons found in the
August 2026 research pass: it roughly **triples the addressable users**, and
several measurements are **blocked** on it — llama.cpp's server uniquely
exposes `timings.cache_n` (the only honest cold/warm TTFT split), logprobs,
`tool_choice`, and a chat-template capability probe. Ollama exposes none of
those, and some numbers cannot be measured correctly without them.

- [ ] **llama-server adapter first**, then one generic OpenAI-compatible
      adapter plus a capability probe — not N bespoke clients. Roughly 10 of 12
      relevant runtimes are OpenAI-shaped.
- [ ] **Capability probe over hardcoded knowledge.** Read what the endpoint
      says it supports (tools/thinking/vision); never infer from a model's
      name. The case study in docs/ records a shipped bug from a name-prefix
      guess.
- [ ] **Backend in the fingerprint.** Vulkan vs CUDA vs Metal vs ROCm is not a
      footnote; it is a different measurement.
- [ ] **Native runtime detection** — notice what is already running rather
      than demanding a flag; do not hardcode ports.

## 0.2.x — finish making the measurement trustworthy

- [ ] **Raise the agentic floor to 40 turns** with a compaction watchdog, and
      **round-trip `reasoning_content`** in the loop. Both are documented
      exposures that make `--full` optimistic for thinking models.
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
