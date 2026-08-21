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

`run` is the engine of that loop, not the product. The product is the loop,
backed by two commitments that make it worth trusting: measurements that are
honest about their own resolution, and **a remedy attached to every negative
verdict** (design rule 7).

The default path is that loop on a Thursday night when a new 30B MoE drops.
The interior is strict so the surface can be trusted. You should not need the
evidence-contract vocabulary to get a keep / drop / try-this-flag answer.
You should be able to see it when you look.

---

## Now / next / later

| Horizon | Tag | What | Why this, not something else |
|---|---|---|---|
| **Shipped** | 0.4.0 | TUI, history, advise, apply, backends | Tagged. |
| **Shipped** | **0.5.0** | Evidence contract (A/B), lineage, family-stratified aggregation | Tagged with this docs pass. Install `latest` matches the contract the README describes. |
| **Now** | **0.6.0** | Installed inventory | After install, the runtime already knows the names. The stranger should not have to. |
| **Next** | 0.7.x | Context-fit table, then one next-command grammar | People actually choose among 4k / 8k / 16k, not one window. |
| **1.0** | 1.0 | A stranger closes advise → run → apply from inventory, on their machine | Still no public leaderboard, no LLM-as-judge, no invented SKU gates. |
| **Later** | Trust C and loop extras | Isolation, signing, catalog, vision, community profiles | Real work. None of it is the next product week. |

---

## Now (0.6.0): installed inventory

With no model argument, show what is already available locally, what has
current fitr evidence, what is stale, and the cheapest next measurement that
would resolve a decision. Unmeasured means candidate, never recommended.

Bare `fitr` becomes that table. `fitr advise` with no model is the same table.
`fitr advise <model>` stays the one-artifact fit verdict. There is no
`fitr inventory` command.

| State | Meaning | Next command |
|---|---|---|
| `measured` | Canonical current result, uncontaminated, fingerprint still matches | `fitr view <model>` |
| `unproven` | Installed, no rankable current result | `fitr advise <model>` then `fitr run <model>` |
| `incompatible` | Weights exceed the measured budget, or a load refusal | the advise remedy; do not suggest `run` |
| `stale` | A result exists, but fingerprint / runtime / schema no longer matches | `fitr run <model>` |

Calendar age is not staleness. Color does not carry state. Inventory is the
runtime's installed list (`Tags()`), not a disk crawl of GGUFs and not a
Hugging Face catalog.

- [ ] Bare `fitr` / no-arg `advise` list installed models joined to current evidence
- [ ] Four plain states, one remedy command per row (design rule 7)
- [ ] No ranking of unproven rows; no pull/load/run from this screen
- [ ] JSON / plain / rich; screenshot `docs/assets/inventory.svg`; README hero leads with `fitr`

**Not in 0.6.0:** `--load`/`--fit` across the library, a TUI fifth view, live
discovery, quality ranking, isolation, community SKU profiles, gate edits from
calibration pairs.

This is not blocked on battery calibration. Listing installed models is not a
recommendation. Calibration is required to change built-in gates and to claim
directional quant damage. It is not required to print what is already serving.

---

## Next (0.7.x): context visible, then loop cohesion

### Context made visible

Present weights, KV or recurrent state, runtime buffers, and safety headroom
as separate measured or estimated components. Show how memory, prefill,
decode, and usable task depth change at several context points. Hybrid
attention and incomplete split GGUFs stay SKIP until their architecture and
complete shard set are verified.

- [ ] Context-fit table at 2k / 4k / 8k / 16k / 32k / architecture max
- [ ] Buffers only when `--load` or `--fit` measured them; otherwise `n/a`
- [ ] Overlay decode/prefill only from saved runs at that ctx
- [ ] Low-memory remedy is a marked row, not a magic number in prose

### One story

- [ ] Inventory rows that already have architecture show fit tier
- [ ] `fitr top` Inventory view (or idle default); do not start native desktop
- [ ] One next-command grammar on CLI, TUI, and HTML: inventory → advise/run → view → apply
- [ ] `board` stays the measured comparable surface; inventory stays installed including unproven
- [ ] Stop advertising `--full` as the first measurement; `--full` remains the optional 40-turn loop, coding still SKIP

The terminal views for this loop are an Inventory table, a Context fit graph,
a Candidate evidence pane (`view` / Result), and a Change log (History).
Every row carries one of four plain states: measured, unproven, incompatible,
or stale.

---

## Later

### Loop, later

These follow the evidence contract so candidate metadata can never be mistaken
for measured quality.

- [ ] **Replaceable live discovery.** Query a free public model index through an
      adapter, cache source timestamps and immutable artifact metadata locally,
      and rank only by fit eligibility plus the user's stated workflows. Catalog
      recency is not quality evidence. A candidate becomes a recommendation only
      after a local run clears the relevant gates.
- [ ] **Research receipts.** For each candidate, show why it entered the set,
      source freshness, license, architecture, quant and shard completeness,
      expected fit range, runtime support, missing evidence, and the exact command
      that would test it. No opaque "best model" answer.
- [ ] **Opt-in recurring reevaluation.** Print and install an OS-native scheduled
      command only after explicit confirmation. The default is a dry-run plan.
      A cycle refreshes metadata, detects new candidates or stale evidence, spends
      no cloud inference money, and asks before downloads or evaluations. Budgets
      cover network bytes, disk, wall time, thermals, and any external cost.
- [ ] **Truly native desktop clients** only after the terminal information
      architecture is proven. SwiftUI/AppKit on macOS, WinUI 3 on Windows,
      and GTK 4/libadwaita on Linux. WebView wrappers are not the desktop plan.
      Sequencing is in [docs/interface.md](docs/interface.md).

### Trust C (does not block Now)

Release A (evidence integrity) and B (reproducibility) shipped in 0.5.0.
Family-stratified aggregation and the same-base lineage receipt shipped with
them. Isolation is the next *trust* project. Inventory is the next *product*.
They are not the same week.

Every verdict must remain traceable to complete, correctly attributed
observations. An unavailable fact stays unavailable instead of quietly becoming
a score.

- [ ] Calibrated profile provenance and community calibration tooling
- [ ] Cross-platform isolated worker (executable coding stays SKIP until
      confinement is the same test on all six targets). Spare cores then
      execute generated programs while the GPU is busy on the next prompt;
      they still do not share the inference queue.
- [ ] Real vision tasks
- [ ] Privacy-safe share artifacts beyond the current opt-in HTML
- [ ] Signed releases, SBOMs, and attestations
- [ ] Externally anchored share provenance and a trust-root workflow for
      decision-grade community evidence
- [ ] Published repeatability and test-retest studies
- [ ] Formal backend-conformance corpus

Exit criterion: an independent user can reproduce the protocol and distinguish
unsupported, inconclusive, contaminated, and measured outcomes without reading
the source.

### Waiting on purpose

| Item | Why it waits |
|---|---|
| Community SKU profiles (`rtx-4090`, `m3-max`, `strix-halo`) | Would be invented GB. `fitr profiles new` already writes an UNCALIBRATED local copy. Collect measured gates on real boxes. |
| Battery calibration that rewrites `spec/` | Protocol is done. Changing items still needs lineage-verified pairs on two devices and two families. Collect in the background; do not hold the loop for a cull. |
| `fitr tune` sweeps | Needs server restart orchestration fitr does not have. llama-bench owns throughput-only. A flash-attention quality regression is why throughput-only is not enough. |
| Parallel scored inference | Would make tok/s a shared-GPU number. Doctor already warns on `OLLAMA_NUM_PARALLEL>1`. The process lock exists because concurrent evals produced plausible-and-wrong timings. |
| Live discovery / `internal/scout` | Catalog recency is not quality. Inventory before internet. |
| Exec-kind user tasks | Arbitrary code from JSON. Wait for the isolated worker. |
| Long-context / needle tests; decode at 3+ depths | Explicit later list; does not close advise → run → apply. |
| Sustained-throughput mode | Thermal reality vs burst. Homelab work, after context-at-depth. |
| Same-config pairwise matchups (Glicko-2 style) | Extracts signal our flat refusal-to-rank discards, but sits in tension with design rule 1; decide deliberately, not by drift. |

---

## Path to 1.0

1.0 ships when a stranger can install in one command, point fitr at whatever
is already serving, and get a verdict they can act on - including *what to
run next* when the answer is no. That is the advise → run → tune → apply
loop, closable on someone else's machine.

The target user is someone who would rather buy a capable machine every few
years than keep several cloud subscriptions, while still preserving enough
detail for expert audit. The product question is workflow-first: which of this
person's real jobs does this exact artifact, quant, context, runtime, and device
cover, with what evidence?

| Bar | Status |
|---|---|
| One-command install (curl / irm) | **done** |
| Honest measurement on Ollama, llama-server, OpenAI-compat | **done with fail-closed identity gates** - generic OpenAI runs require an operator SHA-256 pin matching the endpoint assertion; post-load llama-server path hashes remain unrankable without a runtime binding receipt |
| Scorecard refuses to lie (doctor, degeneracy, compaction, cache-split TTFT, intervals, quant flips) | **mostly done** - check-battery calibration still needs hardware; flip machinery is in `compare` |
| `fitr advise <model>` returns a fit verdict + remedy | **done**; `--load` observes resident allocation and `--fit` dummy-allocates |
| Recommend what to try without naming a model first | **inventory is 0.6.0** - list what is serving without ranking unmeasured rows; calibrated ranking among measured jobs comes later. Not blocked on a hardware campaign. |
| Persist a measured setting without silent mutation | **done** - `fitr apply` prints the command; never restarts the server |
| A result can leave the terminal | **done** - `fitr export` / `fitr run --html`, opt-in, fingerprint in the page |
| Saved evidence is easy to inspect in the terminal | **done** - `fitr view`, graph-based `board`, and the opt-in `fitr top` Live/Result/Board/History monitor |
| Community device profiles beyond `lappy` | **scaffolded** - `fitr profiles new` writes an UNCALIBRATED local copy; no invented rtx-4090 numbers in the repo |

We do **not** ship 1.0 with an uncalibrated ranking, a public leaderboard,
or LLM-as-judge. Battery calibration still needs hardware. `tune` can trail
advise: a one-line remedy (`try num_ctx=4096`) is advise; `fitr run --ctx 4096`
measures it; `fitr apply` prints how to persist it (and never restarts the
server). Sweeping knobs is tune.

---

## Honest limitations right now

| Limitation | Consequence |
|---|---|
| ~23 binary trials per default run | MDE ~ 29pp. Better than the ~33pp of six tasks, still separates *broken* from *working*, not *good* from *slightly better*. `-k 3` on checks triples the sample. |
| OpenAI-backend timings are client-derived | usage gives counts, not server timings; decode/prefill rates there are wall-clock estimates |
| 2 device profiles | `lappy` and an uncalibrated `default` |
| `advise` default is still weights+KV for conventional attention | `--load` / `--fit` measure compute buffers. Hybrid recurrent models stay SKIP without that receipt, and incomplete split GGUFs are rejected. No model catalog. |
| Bare `fitr` is status, not inventory | Hardware, reachable runtimes, and a generic next command. It does not yet list installed tags joined to evidence. That is 0.6.0. |
| `--full` is a long loop, not a coding grade | The 40-turn agentic task still SKIPs executable evidence until isolation. First measurement should be the default battery, not `--full`. |
| Scored inference is single-flight | Using 16 cores to fire 16 prompts would make tok/s a shared-GPU number. The process lock and doctor's `OLLAMA_NUM_PARALLEL` warning exist because concurrent evals produced plausible-and-wrong timings. |

---

## Cores, GPUs, and honesty

Machines have 6–16+ logical CPUs. fitr uses them where that does not lie,
and refuses them where it would.

**Already concurrent:** runtime discovery (every well-known port at once),
the TUI and lock refresher in the background, hardware fingerprint probes
(GPU/CPU/RAM/VRAM overlap; on Windows those are separate PowerShell
launches), split-GGUF shard `stat`. Go itself schedules on every logical
CPU (`GOMAXPROCS`). `fitr` / `fitr device` print that count as display-only.
It is not in the fingerprint key.

**Deliberately single-flight:** the measurement. One model, one request at
a time. Concurrent models contaminate timings. Parallel slots divide the
context. The wall clock of `fitr run` is the model, not the harness.

**Where cores earn a later checkbox:**

- [x] Discover probes concurrently; fingerprint probes and split-GGUF stats overlap
- [ ] Isolated worker overlaps generated-code execution with the next prompt (Trust C)
- [ ] Inventory of many local GGUFs parses headers in parallel (with 0.6.0 listing, not a disk crawl)

Non-goal: `FITR_PARALLEL=N` for scored inference. Faster runs come from
`--quick`, fewer repeats, or a smaller model.

---

## Shipped history

Do not reopen these as current work. Detail stays here so the trust sequence
is not lost.

### 0.5.0 — evidence contract

Release A: no injected infrastructure failure can create or alter a model
PASS or FAIL.

- [x] Disable tasks that execute generated code by default and preflight every
      executable before any model output is requested.
- [x] Require successful process exit and an exact verifier receipt. Explicit
      unisolated diagnostics remain INCONCLUSIVE and unrankable.
- [x] Add typed task outcomes and typed infrastructure failures.
- [x] Propagate refusal, plumbing, transport, fixture, and tool-loop failures.
- [x] Resolve the runtime model strictly. Require a runtime artifact digest or
      an independently pinned remote digest; keep post-load local file hashes
      visible but unrankable without a runtime binding receipt.
- [x] Return a nonzero exit when result persistence fails.
- [x] Seal an immutable run manifest before measurement, including artifact,
      backend/runtime, device/config, task level, seed set, context, repeats,
      and execution policy.
- [x] Add INCONCLUSIVE and exclude contaminated evidence from score, board,
      history comparison, and CLI comparison claims.
- [x] Fail-closed infrastructure handling: typed failures abort or record
      INCONCLUSIVE/ERROR; plumbing, process-receipt, identity, transport,
      and execute-level injection tests prove those paths cannot mint a
      model PASS or FAIL.

Release B: a stored result contains enough information to reproduce the
task battery, scoring policy, backend protocol, model identity, and execution
environment.

- [x] Fingerprint v2 and effective-context verification.
- [x] Task, profile, scoring-policy, specification, and exact executable
      hashes, plus a versioned backend-protocol receipt.
- [x] Strict built-in, profile, result, and calibration schemas with
      unknown-field and trailing-value rejection.
- [x] Persist adaptive decisions, tool-loop clean-stop evidence, and loop
      scoring.
- [x] OpenAI protocol conformance, bounded responses, exact-origin credential
      binding, and independent artifact pinning.
- [x] Result migrations and collision-free filenames.
- [x] Fake-backend integration coverage, fuzzing, and vulnerability scanning.
- [x] Bind completed measurements to the sealed manifest, reconcile canonical
      results with private history, and keep external or archived imports
      display-only on ranking surfaces.

Also in 0.5.0:

- [x] Family-stratified statistical aggregation.
- [x] Same-base lineage receipt (`fitr.lineage.same-base.v1`) from a
      publisher conversion manifest or matching GGUF base-digest metadata.
      A pair signature still cannot manufacture lineage.

### 0.4.0 — TUI / history

- Device fingerprint embedded in every current result; requested and
  effective context stay separate, and `board` refuses unverified or
  cross-fingerprint ranking
- CLI data views: `fitr view` reopens the newest or selected saved run;
  scorecards and `board` show repeat-shape graphs, with color/Unicode fallbacks
  and unchanged plain/JSON channels
- **`fitr top` full-screen monitor** with Live, Result, Board, and immutable
  History views; keyboard-only navigation; responsive layouts; color, ASCII,
  and non-interactive fallbacks; and terminal restoration across macOS, Linux,
  and Windows
- **Versioned presentation contracts** for privacy-safe snapshots and typed
  live events. The reducer and canvas are independent of tcell so later native
  clients can consume decisions without reimplementing scoring.
- **Append-only local run history** alongside a collision-safe canonical
  latest result, with atomic writes, legacy migration, explicit retention
  disclosure, and a clear command that preserves canonical results

### Measurement engine (through 0.4)

- Needs-based scoring - PASS / FAIL / INCONCLUSIVE / SKIP / `n/a` / `BLKD` - instead of one number
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
- Two needs with their own gates: **structured output** and **instruction
  precision**, each scored as a pooled pass rate with its Wilson interval shown
- **`fitr doctor`** - can this box be measured fairly at all? Real-token
  preflight (HTTP 200 is not inference), N-run byte-identical determinism in
  plain text AND grammar-constrained JSON mode (a known local-stack
  reproducibility break), served-context probe, placement check (partial
  offload is a RAM benchmark wearing a GPU badge), config red flags.
- **User tasks without forking** - `~/.fitr/tasks/*.json` (or `$FITR_TASKS`).
  Declarative only: a user task can prompt and grade (exact / contains / regex /
  json_object / number), it cannot execute anything. Malformed files are hard
  errors with the filename; id collisions are rejected.
- **Golden-corpus regression** - a frozen full-run result pins
  `measure -> Score -> render` end to end, including the FAIL paths
- The minimum detectable effect is printed on every run
- **A statistics engine built for n=3 to n=50**, documented with references in
  docs/statistics.md: Newcombe difference intervals as the sole arbiter of comparisons
  (the intervals-overlap rule is an effective alpha of ~0.006 and was
  retired), Fieller ratio intervals with Welch df for "how many times faster",
  McNemar's exact test on paired instances via `--seedset` (with the
  fewer-than-six-flips impossibility stated instead of a doomed p-value),
  Wald SPRT adaptive repeats (`--adaptive`), exact zero-event bounds behind
  doctor's determinism claims, MAD outlier annotation on timing repeats, CV
  in every spread line, and INCONCLUSIVE when a gate sits inside the pool's
  interval. Every formula pinned to published reference
  values in tests.
- Spec drift protection: `spec/` at the repo root is canonical, the embedded
  copies are compared byte-for-byte in tests, `make spec-sync` repairs
- Single static binary, 6 platforms (linux/darwin/windows × amd64/arm64),
  CI on 3 OSes
- **One-command install** using the familiar single-command CLI shape:
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
- **`fitr apply`** - prints how to persist a measured `num_ctx`. Never
  restarts the server. Scorecard, HTML, and board all show the request
  context; a ctx-only split is this machine at a different window, not
  different hardware.
- **Shareable HTML scorecard** - `fitr export` / `fitr run --html`. Opt-in,
  self-contained, fingerprint on the page, raw model output omitted.

### Backends (shipped)

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

### Trust work already on main

- [x] **40-turn agentic floor** (was 20 - that measured early-abort behaviour)
      and **`reasoning_content` round-trip** in the loop; the Message struct
      simply had no field for it, so re-serializing the transcript deleted it.
- [x] **Compaction watchdog** - filling 80% of the window with a transcript
      that never shrank FAILs `unattended_agentic`. Peak-then-compact is
      disclosed, not failed.
- [x] **Calibrate machinery** - `fitr calibrate a b` on a shared seedset
      reports which check items flipped and which never did. It does **not**
      rewrite the spec: Aider kept 225 of 697 after many boxes; one pair is
      a lead, not a cull.
- [x] **Efficient calibration evidence** - `run --checks-only` skips unrelated
      phases; pair export omits raw output and pseudonymizes device, seedset,
      and local-model identifiers; multi-report merge rejects spec drift and
      normalizes legacy artifacts. Unsigned imports remain exploratory and
      cannot create verified readiness or automatic review candidates.
- [ ] **Calibrate the check battery and gates on hardware** - the same-base
      lineage receipt exists; collect reviewed, authenticated pairs that carry
      it across at least two physical devices and two model families. Only then
      review items that never discriminate and thresholds that do not match
      known usefulness. (Hardware campaign; does not block inventory.)
- [x] **Quant comparison as item-level agreement** (machinery). `fitr compare` on
      a shared seedset reports item-level flips, including when the two rates
      match - accuracy hid the disagreements. Matching family, parameter size,
      and ranked dtype does not prove same-base revision lineage, so directional
      quant attribution on `compare` remains INCONCLUSIVE. `fitr calibrate
      --lineage` can attach a sealed receipt that binds both runtime-bound
      artifact digests to one base revision; campaign readiness still needs
      trust plus the hardware coverage above.
- [x] **`fitr tune`, first honest cut.** Prints the request-level knobs
      (`num_ctx`, `num_batch`, `num_gpu`) and diffs two saved fingerprints
      (including a `num_ctx` split). It does **not** sweep: llama-bench owns
      throughput-only points, and a flash-attention quality regression is why
      throughput-only is not enough. Server-level env (`OLLAMA_FLASH_ATTENTION`,
      `KV_CACHE_TYPE`) still needs restart orchestration fitr does not have.
- [x] **`fitr apply`.** Prints the copy-paste to persist a measured context
      (Ollama Modelfile / llama-server `--ctx-size` / openai-compat launch
      flags). Never mutates or restarts the server. The scorecard, HTML
      artifact, and board all show `num_ctx`; a ctx-only split is labeled as
      this machine at a different context, not as different hardware.
- [ ] **`fitr tune` sweeps** quality + degeneracy + throughput jointly per
      request-level point, once restart orchestration exists for server env.

### `fitr advise` (single-model, shipped)

The step that turns a chore into a product.

- [x] Three-tier verdict with remediation in one line - Compatible / Low
      memory (`try num_ctx=4096 -> fits in 21.3 GB`) / Incompatible. SKIP
      when VRAM, weights, or architecture cannot be measured - never a
      fabricated GB number, never a guess from the GPU's name. Next line is
      `fitr run --ctx N`; `fitr apply` prints how to persist N.
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
- [ ] **Inventory-first listing** - 0.6.0 above. Unmeasured models are
      candidates to measure, never confidently ranked.
- [ ] **Live discovery only after evidence-backed local recommendation.** Use a
      replaceable GGUF index adapter, not a curated list baked into the binary.
      Every recommendation must name its fit source, measured gate evidence,
      and remedy. Until then, `advise` answers "does THIS fit", not "what
      should I download".

### Share the map

- [x] **Local profile scaffold** - `fitr profiles new` copies default into
      `~/.fitr/profiles`, matched to this GPU, marked UNCALIBRATED. User
      files override embedded profiles of the same name.
- [ ] **Community device profiles** - `rtx-4090`, `m3-max`, `strix-halo`
      with measured gates. The network effect: the repo answers *"I have an
      M3 Max with 36 GB, what should I run?"*. Do not invent those numbers.
- [x] **Shareable result artifact** - self-contained HTML via `fitr export`
      or `fitr run --html`. Never automatic: the page contains a hardware
      fingerprint and is not uploaded. Raw model output is omitted.

### Optional sister: retonr

- [x] **Opt-in handoff, no dependency.** `fitr export --retonr` writes
      `fitr.retonr.evidence.v1` device-measurement JSON. A PATH hint appears
      only if `retonr` is already installed. Missing retonr is never an
      error. The file is not a qualification, activation, or license.

### Interface path: CLI first, native later

- [x] **Replayable terminal data view.** `fitr view [model|result.json]` opens
      the newest or selected run through the scorecard renderer. `fitr board`
      adds per-block throughput bars and repeat-shape graphs without a new
      dependency. Plain output stays ASCII and pipe-safe; JSON stays complete.
- [x] **Opt-in full-screen TUI.** Live run, result, board, and immutable
      history views share one keyboard-first responsive surface. Normal
      commands never take over the terminal, and redirected streams remain
      control-sequence free.
- [x] **Versioned presentation contract.** A read-only privacy-safe snapshot
      and typed event schema let every interface consume the same scoring
      decisions. Frontends never reimplement gates or comparisons.
- [ ] **Truly native desktop clients only after the terminal information
      architecture is proven.** See Later, above.

---

## Non-goals

- **A public leaderboard.** The whole thesis is that cross-device numbers are
  not comparable. Publishing a ranking would contradict the tool.
- **LLM-as-judge for correctness.** Checks are graded by computed ground truth.
  Built-in executable diagnostics can observe assertions only under an explicit
  unsafe opt-in, remain INCONCLUSIVE, and cannot become scored coding evidence
  until the isolated worker exists. Judges get added only where nothing
  mechanical can reach, and only with known bias mitigations.
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
