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

---

## Where it is now (0.1.0)

**Done and measured:**

- Device fingerprint embedded in every result; `board` refuses to rank across
  fingerprints
- Needs-based scoring — PASS / FAIL / SKIP / `n/a` / `BLKD` — instead of one number
- Repeats with Wilson intervals, flakiness flags, `INDISTINGUISHABLE` verdicts,
  first-run-slow detection
- Five-signal degeneracy detection
- Tool-use plumbing diagnostic that runs *before* capability is judged
- Language-neutral task spec, generated not hand-written
- Single static binary, 5 platforms, 50 tests, CI on 3 OSes

**Honest limitations right now:**

| Limitation | Consequence |
|---|---|
| 6 tasks | minimum detectable effect ~33pp at `-k 3`. Separates *broken* from *working*, not *good* from *slightly better*. |
| Ollama only | excludes llama.cpp server, LM Studio, vLLM, MLX users |
| 2 device profiles | `lappy` and an uncalibrated `default` |
| No task extensibility | your tasks require a fork |
| Terminal output only | no shareable artifact |

---

## 0.2 — Make the measurement trustworthy

The current battery proves the *design*. It is not yet a *suite*.

- [ ] **More tasks (30-50).** The single biggest scientific weakness. Until this
      lands, `fitr` should not be used to separate two close models, and it
      currently says so out loud.
- [ ] **User tasks without forking** — `~/.fitr/tasks/*.json`. Your work is the
      point; the built-ins are defaults.
- [ ] **`fitr tune`** — sweep the settings that actually move numbers, with a
      restart between variants and replicate measurements. The methodology is
      already validated: on one box `KV_CACHE_TYPE=f16` beat `q8_0` by **+17%
      prefill** (251/256/253 vs 218/207/226, ranges non-overlapping) while the
      decode difference was **noise** — and reporting the second honestly matters
      as much as the first.
- [ ] **`fitr doctor`** — determinism check. Run one prompt N times and report
      whether *your box* even produces identical output. Nothing does this, and
      every benchmark silently assumes it.
- [ ] **Golden-corpus regression** — freeze fixtures so the writing stages can be
      tested without a model.

## 0.3 — Meet people where their models already are

- [ ] **OpenAI-compatible backend** (`/v1`) — covers llama.cpp server, LM Studio,
      vLLM, SGLang in roughly one adapter. Roughly triples the addressable users.
- [ ] **Native runtime detection** — notice what is already running rather than
      demanding a flag.
- [ ] **Backend in the fingerprint.** Vulkan vs CUDA vs Metal vs ROCm is not a
      footnote; it is a different measurement.

## 0.4 — `fitr advise`

The step that turns a chore into a product.

- [ ] Read the box → propose models, quants, and settings worth trying, **with
      the reasoning shown**, then verify by measuring.
- [ ] Honest open question: a static binary cannot know what models exist. The
      options are a curated catalog that ages, a live index, or a model that
      reasons about it — each with a real failure mode. **Pick deliberately;
      do not default.**
- [ ] Memory arithmetic that gets **MoE right** — decode tracks *active*
      parameters, not total. On one box a 30B MoE (~3B active) ran at 24.8 tok/s
      while an 8B dense ran at 14.6 and a 27B dense at 3.5. Any advisor doing
      naive total-parameter math will recommend exactly the wrong thing.

## 0.5 — Share the map

- [ ] **Community device profiles** — `rtx-4090`, `m3-max`, `strix-halo`,
      `dgx-spark`, `mac-mini-m4`. This is the network effect: the repo would
      answer *"I have an M3 Max with 36 GB, what should I run?"*, which **no
      leaderboard can structurally answer**. A profile PR is small, reviewable,
      and useful the moment it merges.
- [ ] **Shareable result artifact** — a self-contained HTML report, or an
      opt-in submission. Never automatic: results contain a hardware
      fingerprint.

## Later, if they earn it

- [ ] `fitr apply` — write the winning settings
- [ ] Quantization comparison (KL divergence against a higher-precision reference)
- [ ] Long-context / needle tests
- [ ] Sustained-throughput mode (thermal reality vs burst numbers)
- [ ] Structured-output / JSON-schema conformance as its own need

---

## Non-goals

- **A public leaderboard.** The whole thesis is that cross-device numbers are
  not comparable. Publishing a ranking would contradict the tool.
- **LLM-as-judge for correctness.** Coding is scored by *executing assertions*.
  Judges get added only where nothing mechanical can reach, and only with the
  bias mitigations that are known to work.
- **Replacing lm-evaluation-harness / Inspect AI.** Those answer "how good is
  this model." `fitr` answers "how good is it *here*." Different question.
- **Chasing benchmark scores.** If a task stops discriminating, it gets replaced.

---

## Design rules

These are load-bearing. Changes that break them need a strong argument.

1. **A number without its device is meaningless.** Never rank across fingerprints.
2. **Never fail a test you did not run.** `SKIP` and `n/a` are not `FAIL`.
3. **Plumbing before capability.** A tools failure is uninterpretable until the
   template and parser are known good.
4. **Never print a fabricated precision.** No `± 0.00` from one observation; no
   ranking when intervals overlap.
5. **Execution over opinion.** Pass/fail comes from running code wherever it can.
6. **Say what was not measured.** An honest gap beats a confident guess.
7. **A verdict without a remedy is half an answer.** "Too large" is a dead end.
   `try --max-model-len=4096 to reduce to >=30 GB` is a product. Every negative
   result should carry the flag that fixes it and the number that results.
