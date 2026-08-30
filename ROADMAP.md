# Roadmap

`fitr` answers one question: **what local AI actually works for my workload on
this machine, and what evidence proves it?**

The product is the local decision loop, not a benchmark score:

```text
inventory -> advise -> run -> apply -> compare
what exists   fits?    verify   persist   decide
```

Every step follows the same evidence contract. Measurements stay attached to
the artifact, runtime, context, and device that produced them. Unsupported or
uncertain facts remain visible without becoming rankings. Every negative fit
verdict carries a remedy.

The broader 1.0 thesis has seven evidence layers:

| Layer | Question |
|---|---|
| FIT | Can this artifact, context, and placement run within the measured capacity? |
| BEHAVIOR | Does it perform the required primitives correctly? |
| PERFORMANCE | How long do load, prompt processing, first response, and generation take? |
| EXPLAIN | Which observed constraint is most likely to matter, and how strong is that evidence? |
| VALIDATED WORK | Does speed survive correctness checks, retries, and independent verification? |
| TRADEOFFS | Which configurations are dominated, and which are genuine choices? |
| COVERAGE | Which declared workloads have earned local trust, and which still need evidence or fallback? |

There is no global score across these layers. A configuration can fit without
being fast, be fast without being correct, or be reliable for one bounded
workflow without earning unattended authority for another.

The rationale and invariants live in [design](docs/design.md). Commands and
flags live in [usage](docs/usage.md). Hardware decision evidence lives in
[choosing hardware](docs/choosing-hardware.md). Bounded workflow and proof
contracts live in [workload evidence](docs/workload-evidence.md). Statistical
methods live in [statistics](docs/statistics.md).

## Status

| Horizon | Release | Outcome |
|---|---|---|
| Shipped | 0.9.8 | Output that fits the terminal, and verdicts the renderer can lay out |
| Now | 0.9.9 | Evidence correctness, tool-channel behavior, internal decomposition, and the native matrix |
| Then | 0.10 | Explain and choose: central analysis, capacity versus performance, calibration, context and quant experiments |
| Then | 0.11 | Validated workload evidence: per-trial receipts, bounded workflow contracts, and local coverage |
| Then | 1.0 | A clean-machine, evidence-backed local decision system with native acceptance |
| Next | Trust C | Stronger confinement, release provenance, and calibrated profile provenance |
| Later | Candidate discovery | Find models worth measuring, then measure them; plus loop extensions |

Progress is counted in releases, not dates. Each pre-1.0 release below states
its own exit criterion. 1.0 ships when those criteria are met and not before:
the point of the version is that it is trustworthy, not that it is reached.

## What the second machine changed

Running 0.9.6 on a second Windows box surfaced three defects, one of which
disabled the headline command:

| Defect | Consequence |
|---|---|
| `advise` sourced weight size from Ollama `/api/show`, which has no size field | Every Ollama model returned SKIP "model weights were not measured" while bare `fitr` printed that model's size on the same screen. The fit verdict was unavailable on the primary backend. |
| Windows GPU detection took the first enumerated video controller | A headset or remote-desktop virtual adapter outranked the real card. GPU name is part of the device fingerprint key, so evidence was sealed to the wrong device and would silently stop comparing when that adapter came or went. |
| The GPU probe used a PowerShell 7 flag | On Windows PowerShell 5.1 the probe failed silently and the GPU line vanished rather than reporting an unmeasured device. |

None were caught by CI, and none were visible on the development machine.
That is the finding that reshapes the plan below. Two conclusions:

**Acceptance is not a formality that follows the work; it is what finds the
work.** The previous claim that 1.0 functionality already existed and only
needed hardening was wrong on its own terms.

**The automated gates cannot see the product.** CI has no GPU and no serving
runtime, so it covers protocol and rendering logic thoroughly and covers the
decision loop not at all. The opt-in live smoke in `cmd/fitr/live_test.go`
exercises check machinery only -- it never calls advise, inventory, board, or
apply. A one-line live assertion that advise does not SKIP on an installed
model would have caught the first defect immediately.

A fourth item is a contract decision rather than a defect: an intermittent
transport error in the final battery step aborted a run and discarded every
completed measurement. It reproduced once and not on retry. Fixed in 0.9.8 by
retrying exactly the case that produced it: a chat reply carrying neither a
generation nor a terminal receipt.

## Pre-1.0 releases

The three milestones below were planned as separate releases. They were cut as
one, 0.9.7, because the work turned out to be a single thread: taking the
product to a machine that was not the development box, and fixing everything
that fell out of it. The headings are kept because the exit criteria are still
what the work has to be judged against.

### 0.9.7 - second-machine correctness [shipped]

- [x] Source advise weight size from the runtime model list, the same reading
      inventory already uses. Fit verdict and the context-fit table work on
      Ollama.
- [x] Select the GPU that serves inference: prefer the card `nvidia-smi`
      sized, then the largest adapter. Virtual displays no longer name the
      device fingerprint.
- [x] Parse both JSON shapes Windows PowerShell 5.1 emits, and keep an
      unmeasured device explicitly unmeasured instead of silently blank.
- [x] Bound the count knobs in user task files. A task file is user input
      whose parameters index fixed pools, and `~/.fitr/tasks` accepted one
      asking for a negative or impossible row count, then crashed generating
      it: a negative slice length, and a permutation sliced past its end. The
      count is now rejected at load with a diagnostic naming the field, the
      draw is bounded by its pool, and the float-to-int read is clamped rather
      than converted. The decode, validate and generate chain is a CI fuzz
      gate holding one invariant: whatever validation accepts, generation
      survives and can grade its own canonical answer.
- [x] Fuzz the GGUF metadata decoder and refuse unbelievable architecture.
      A GGUF header is the largest untrusted binary surface fitr has and had
      no fuzz target. Four million executions found a header whose dimensions
      wrapped the KV arithmetic negative, so bytes-per-token came out below
      zero, need came out below weights, and advise answered *compatible* for
      a model that does not fit -- with a negative GB figure printed beside
      the verdict. Implausible dimensions are now unmeasured, which routes to
      SKIP; the KV and parameter arithmetic is overflow-checked; and display
      rounding no longer converts an oversized float through int64. The
      decoder is a CI fuzz gate alongside the other three.
- [x] Stop re-probing inference placement after the battery. Placement is
      derived from the resident VRAM split, so it moves as an allocation
      changes; it is observed once while the model is resident for context
      verification and sealed into the v2 fingerprint and the comparability
      key. Overwriting the sealed value afterwards made the legacy and v2
      device fields disagree, and the manifest check then refused the save --
      a completed measurement discarded at the last step, on a machine where
      the split happened to move.
- [x] Extend the live smoke from check machinery to the whole documented loop,
      so a human with a runtime can prove inventory, advise, run, apply, and
      board in one command: `FITR_LIVE=<model> go test ./cmd/fitr -run TestLive`.
      It isolates `FITR_RESULTS`, treats a failed gate as a real answer, and
      was verified to fail when the advise defect is reintroduced.

Exit criterion: met. The three defects have regression tests, and the live
smoke fails if any documented command stops working against a real runtime.

### The acceptance path covers the product [shipped in 0.9.7]

The Windows acceptance row recorded a passing live loop while advise was
broken, because the recorded path never invoked advise. An acceptance path
that skips the command the README calls the fit verdict is not acceptance.

- [x] Redefine the acceptance path as every command in the README's everyday
      loop table, with the observed output recorded per row. The live smoke
      now walks inventory, advise, run, apply, board, doctor, diag, device,
      view, export, `top --snapshot` and compare, and asserts each produced
      its documented artifact rather than merely exiting zero. Export is held
      to being self-contained; compare skips loudly when no second model is
      named, because a skipped row that reads as green is the failure this
      test exists to prevent.
- [x] Record environment depth, not just operating system: shell and probe
      tooling version, serving-runtime version, GPU vendor, and driver. The
      PowerShell 5.1 defect is invisible in a matrix whose finest grain is
      "Windows", so `fitr device` now prints the interpreter its probes run
      through. It is display-only: Detect is on the path of every command and
      does not need another round-trip, and the sealed fingerprint does not
      need a field that cannot change a measurement.
- [x] Add a device-identity gate. Fingerprint errors are the worst failure
      class this product has, because they corrupt comparison silently and
      produce no error text. `IdentityConflicts` cross-checks the parts of a
      fingerprint that were derived independently -- the vendor tool that
      sized memory, the compute API the runtime reports, and the name of the
      card -- and says so before a run is sealed, and in `fitr device`. It
      fires only on contradiction, never on absence: unmeasured is a
      legitimate state and must not be dressed up as a fault.

Exit criterion: met for the automated path. A reviewer can read the live smoke
and see which commands ran and what each is required to produce. The native
matrix rows in [release acceptance](docs/release-acceptance.md) still need a
clean install on each operating system.

### Run resilience and backend breadth [shipped in 0.9.7, except the native matrix]

- [x] Decide and document the partial-evidence contract. **Resolved as
      fail-closed, stated out loud.** A fault mid-battery still abandons the
      whole run and saves nothing: the steps that already ran were measured
      under conditions that stopped holding, so keeping them would keep
      evidence collected on a machine that changed underneath. What was wrong
      was the silence. A late failure discarded a minute of completed work
      with nothing said about it, which is indistinguishable from the tool
      losing the result. The run now names the step that failed, says nothing
      was saved, and lists what was completed and thrown away.
- [x] Stop a slow probe from cancelling a measurement. Four device probes
      shared one five-second budget, and an empty CPU name is fatal:
      fingerprint v2 refuses to seal without it, so a run died with
      "fingerprint is missing CPU". A loaded machine is exactly when someone
      measures, and this was first seen failing repeatedly on a busy CI runner
      rather than reasoned about.
      Host facts cannot change while one process runs, and `Detect` is on the
      path of nearly every command -- a run calls it several times -- so each
      call was re-running the same subprocesses and re-rolling the same dice.
      They are now read once per process, the CPU probe retries on its own
      budget, and the diagnostic says a probe returned nothing and to try
      again rather than reading like broken hardware. Only successful readings
      are cached: one slow moment must not pin an empty value for the life of
      the process.
- [x] Retry the one transport fault that measures nothing. The intermittent
      "missing the terminal receipt" failure recurred during a measurement
      campaign and was captured: the server returns a reply that is not
      finished, carries tool calls, and has evaluated **zero tokens on either
      side**. Nothing was generated, so nothing was measured and nothing can
      be contaminated; the shape is not one a model can produce. That case,
      and only that case, is now asked again once. Anything with a token count
      above zero is a real partial generation and still stops the run on the
      first attempt, because a retry that hides a real fault is worse than a
      run that stops. It had cost three completed batteries, each minutes in.
- [x] Offer KV cache dtype as a remedy. The contract says every negative fit
      verdict carries a remedy, and `q8_0` halves KV bytes per element, which
      buys back context that `f16` spends. `advise` now prints it as a second
      `or` line on every low-memory and cache-bound incompatible verdict,
      names the knob per backend, stays silent when the cache is already
      quantized or the runtime exposes no knob, and says that the dtype is
      part of the device fingerprint so the result must be re-measured.
- [ ] Complete positive native runs for llama-server and a generic
      OpenAI-compatible endpoint against the documented identity and context
      gates.
- [ ] Resolve every release-blocking defect found by the matrix while keeping
      cross-platform CI, race detector, vulnerability scan, fuzz smoke tests,
      distribution build, and installer syntax checks green.

Exit criterion: an interrupted or degraded run produces an interpretable
result, and all three backends have a native positive run.

### 0.9.8 - output that fits [shipped]

The scorecard drew a 78-column rule and then crossed it on 7 of 10 verdict
rows, the longest at 224 characters. `doctor` reached 286, the inventory 117,
`compare` 107, and `board` drew a 104-column rule around 123-column rows. On an
ordinary terminal the thing a reader looks at most wrapped into mush.

- [x] Give `Verdict` a structure. The width was the symptom; the cause was one
      string assembled in the scorer with `"; "` and `", "` joins, which a
      renderer can only print or truncate. `Measure`, `Gate`, `Detail` and
      `Note` are now separate, `Why` is composed from them so the persisted
      contract is unchanged, and results saved before the parts existed still
      render their explanation.
- [x] Compose every surface to a resolved width: `FITR_WIDTH`, then the
      terminal if it is narrower than 80, then 80. The terminal may narrow but
      never widen, so piped output is byte-identical to what was on screen.
      Nothing is truncated to fit; long text wraps under its own column.
- [x] Stop drawing sparklines the data does not support. They normalise to the
      series min and max, so a 0.4% wobble drew the same zigzag as a tenfold
      swing. Below a 5% relative spread there is no line, the ASCII ramp
      `.:-=+*#@` is gone because nobody can order it by height, and where a
      line does draw its own min and max sit on the row as the scale. The
      monitor had a second copy of the same two faults; it has the same fix.
- [x] Retry the transport case that discarded three completed batteries: a
      chat reply with neither a generation nor a terminal receipt.
- [x] Say what is free, not only what the card holds. A fit verdict computed
      against total memory answers a question nobody has: nobody uses a machine
      only for inference. This box reports 24.0 GB total with 0.7 GB free,
      because a compositor and six unrelated processes hold the rest, and
      advise was cheerfully certifying a 17.3 GB model at a 73216 window.
      The verdict is unchanged, because the card is still capable, and a
      caveat now names the gap. Free memory is display-only and never enters
      the comparability key: it moves minute to minute, and a volatile value
      in the key would put every run in a block of its own.
- [x] Correct the README's positioning line. "`llmfit` tells you what fits"
      stopped being true: `llmfit bench` measures on your own machine and
      `bench --share` feeds a hardware-keyed leaderboard. Verified against its
      own docs rather than from memory.

Guards, because measuring this was worth doing and nobody was doing it: a test
renders all eight demo surfaces and fails on any line past the width, a second
one renders them with implausibly long device names so the guard does not
depend on whose hardware runs it, and a third asserts the design doc names
every need the code does.

### 0.9.9 - tool calls in the tool channel, and the native matrix

- [x] Measure tool calls where they actually happen. `tool_args` grades a JSON
      object written as text; it never populated the `tools` parameter and
      never looked at `message.tool_calls`, so the single most-reported local
      failure -- a well-formed call arriving in `content` with a normal stop
      reason -- was invisible to it. Four generated families now run through
      the real channel (`tool_call`, `tool_call_strict`, `tool_fanout`,
      `tool_irrelevance`) with a mechanical failure taxonomy, so the detail
      line distinguishes "buy a better model" from "your GGUF ships no PARSER
      directive".
- [x] Make `tool_restraint` interval-bearing. It rested on a single plumbing
      observation, which cannot carry an interval. Restraint under *change*
      stays a separate binary rather than being averaged in: a model that stops
      cleanly when a tool is withdrawn and one that keeps calling a dead tool
      are not the same model.
- [x] Stop abandoning a run over a fault in the last behavioural task. A
      transport fault inside the withdrawal loop now makes that task
      INCONCLUSIVE. The loop is self-contained -- its own conversation, its own
      workspace -- so a fault there cannot reach back into speed, memory or the
      check pools, which is the only thing that ever justified discarding a
      run. This closes the contract decision recorded above.
- [x] A model with no tool support is `n/a`, not a failure and not a fault.
      Runtimes signal it with a generic HTTP 400; reading that as transport
      discarded the whole battery for a model that is simply text-only.
- [x] Prove a candidate can be sized without downloading it. A 4 KiB HTTP
      range read returns a GGUF header, and every key the fit math needs --
      block_count, head counts, key and value length, context length, embedding
      length, and the MoE expert fields -- sits ahead of the tokenizer vocab
      array. Verified against a real 5 GB model: `arch=qwen3 layers=36
      kv_heads=8 ctx=40960` from 4,096 bytes, KV-ready.

      `ReadMetadata` discarded all of it, because an `io.ErrUnexpectedEOF` in
      the middle of the vocab array is the right answer for a local file and
      the wrong one for a deliberately bounded read. `ReadMetadataPrefix`
      returns what it decoded and distinguishes truncation from corruption,
      which is the distinction that keeps the two callers from sharing a
      failure mode. It is covered by the existing CI fuzz gate, which now
      exercises both entry points.

      This matters more than the bytes saved: an HTTP range body is an
      `io.Reader`, so discovery reuses the fuzz-hardened fit arithmetic instead
      of growing a second, weaker copy of it.
- [x] Remove the unsound adaptive verdict path from current runs. A second
      statistical audit found that the sequential Bernoulli process and the
      scorecard's clustered-family estimand did not match, and several specs
      share a family. Current runs use fixed, sealed denominators and the
      cluster-adjusted fixed-sample interval. Legacy Wald receipts remain
      readable only in display-only schema-5 history.
- [ ] Design adaptive sampling around an explicit stratified estimand before
      exposing it again. The candidate protocol draws one fresh instance per
      distinct family per complete round and applies an anytime-valid bounded
      mean process to complete-round family averages. It must persist the
      stratum plan, observed and scorable counts, skips, round boundaries,
      stopping rule, and replay to the first crossed boundary.
- [x] Bind inventory reuse to the model artifact without putting the digest in
      the device comparability key. Different models still share a board block,
      while a saved run is reusable only when the current runtime-bound digest
      matches. Replacing the bytes behind `:latest` produces STALE, never
      MEASURED.
- [x] Say what went stale. Artifact changes, missing verified identity, stored
      evidence integrity, contamination, and field-level fingerprint drift now
      have distinct explanations and the same direct re-run remedy.
- [x] Reuse performance evidence only when the artifact, effective context,
      placement, device fields, runtime digest, and integrity all match. Advise
      no longer attaches timing from a mutable tag or a superficially similar
      device key.
- [x] Make TTFT cache state evidence rather than an assumption. The probe nonce
      now precedes the measured prompt, cache-known survives the evaluator and
      score receipt, and unknown or cache-hit observations cannot prove the
      uncached responsiveness gate. A prefill probe with unknown cache state
      or any observed cache hit likewise cannot establish an uncached prefill
      gate or comparison claim.
- [x] Seal the memory probe's exact bytes and requested/effective context. The
      low-footprint need is scored only when the runtime confirms the requested
      32K allocation. Output labels the observation as the requested 32K load
      probe instead of presenting it as a general memory number.
- [x] Make explicit HTML export privacy-safe by construction. The share model
      allowlists the comparison settings it needs and replaces the raw device
      key with an opaque ID. Raw outputs, hostnames, local paths, and arbitrary
      runtime configuration do not enter the artifact.
- [x] Version the changed evidence contract. Result schema 6 and completion
      receipt v2 bind replayable cache state and point-specific memory
      receipts. Schema-5 records remain readable but are
      display-only, so an older footprint or TTFT PASS cannot survive without
      the evidence now required for that claim. The exact legacy Wald JSON
      shape is retained and covered by a signed round-trip fixture.
- [x] Seal the remaining current-run denominators and timing receipts. The
      generated check schedule and exact refusal prompt IDs are digest-bound
      before inference; a speed observation proves that first output occurred;
      unplanned memory fields and missing planned evidence are rejected.
- [x] Make paired behavior claims respect both terminal outcomes and family
      clustering. SKIP and INCONCLUSIVE never become failures. Item flips stay
      descriptive, while the exact test gives each generated family at most
      one direction.
- [ ] Decompose the run pipeline far enough to turn on the complexity gates.
      `execute` went from 571 lines to 463 by extracting the measurement
      phases, and `main.go` from 4,074 to 1,199, but `funlen`, `gocognit`,
      `gocyclo`, `nestif` and `dupl` are still off in `.golangci.yml` with the
      reason recorded there. Turning them on is the exit criterion, not the
      line count.
- [ ] Complete a positive native run for llama-server, the one backend row
      still resting on automated tests alone.
- [ ] Run the acceptance path on clean macOS and Linux installs.

Exit criterion: every evidence-correctness item above is complete, the
complexity linters are enabled with no suppressions in the run pipeline, and
every backend row rests on a native run.

### 0.10 - explain and choose

The first two questions in a hardware decision are different: can the model
fit, and how does it perform once it does? This release makes that separation
structural and adds cautious explanation without turning vendor specifications
or arithmetic proxies into measured facts.

- [ ] Define one renderer-neutral analysis contract consumed by CLI, TUI, HTML,
      and later clients. It owns performance, capacity, model shape, placement,
      diagnoses, evidence gaps, and next actions. Clients render it and never
      recompute a verdict or bottleneck.
- [ ] Reorganize Result into PERFORMANCE and CAPACITY. Keep TTFT, prefill,
      decode, and load separate from resident allocation, remaining capacity,
      context, and placement. Preserve cold, loaded-uncached, and cache-hit
      observations as distinct evidence when the backend can identify them.
- [ ] Show dense versus MoE shape with total and active-per-token parameters
      only when artifact metadata supports those facts. Do not infer either
      from a model name.
- [ ] Add conservative diagnoses with enumerated evidence and confidence.
      Labels such as `consistent with memory-traffic pressure` are allowed;
      an unmeasured claim such as `memory-bandwidth bound` is not. Every
      diagnosis names contradictory or missing evidence and a useful next
      experiment.
- [ ] Make help selection-aware for TTFT, prefill, decode, resident memory,
      placement, model shape, and verdict uncertainty. Explain what each fact
      answers and what comparisons remain valid.
- [ ] Score fitr's own capacity predictions. Store predicted weights plus KV
      beside observed runtime allocation, accumulate residuals by architecture
      and context, and publish uncertainty. A correction never enters a fit
      verdict until its calibration and versioned provenance support that use.
- [ ] Prototype a context sweep as its own experiment schema. Each point binds
      requested/effective context, allocation, TTFT cache state, placement, and
      comparable performance. One failed point does not fabricate later ones.
- [ ] Prototype a quant and configuration frontier. Report dominance only when
      one measured option is no worse on all declared dimensions and strictly
      better on at least one. Otherwise show the tradeoff, not a winner.
- [ ] Produce a device requirement brief from the user's declared workloads:
      capacity window, interactive latency, prompt-processing demand,
      sustained or concurrent regime, portability, software support, and
      missing evidence. Vendor bandwidth, TOPS, prices, and power limits remain
      labeled specifications rather than measurements.

Exit criterion: every shipped explanation is generated centrally, cites its
evidence, survives missing receipts without guesswork, and renders identically
in meaning across CLI, TUI, and HTML. The device requirement brief is usable
without turning specifications into measurements. Context and quant prototypes
either earn a versioned command contract or remain clearly named experiments.

### 0.11 - validated workload evidence

The model is not always the largest useful evaluation unit. A bounded workflow
can be measured when its authority, state, definition of done, and independent
verifier are explicit. fitr evaluates that system; it does not become its work
queue, scheduler, or organizational memory.

- [ ] Add sealed per-trial receipts for scenario release, worker start/end,
      model and tool intervals, verifier queue/start/end, verifier outcome,
      independent acceptance, approval and escalation events, retries, and
      terminal state. Analysis separates worker, tool, queue, verifier, and
      human-wait time. Aggregate result files alone must never be reverse-joined
      into a time-to-valid-result claim.
- [ ] Define evidence classes: deterministic assertion, external system state,
      independent verifier, harness state machine, heuristic, model judged,
      self-reported, and none. Core workflow PASS and FAIL require an allowed
      independent class; self-report never upgrades itself into proof.
- [ ] Version a bounded workflow contract with intent, initial state, supplied
      context, allowed tools, forbidden actions, definition of done, verifier,
      budgets, approval authority, denial/timeout/revocation behavior,
      escalation conditions, and optional checkpoint/resume rules.
- [ ] Bind comparability to a workflow fingerprint covering the workflow spec,
      scenario, seed, initial state, tools and service versions, context
      provider and event manifest, verifier environment, permission profile,
      state and checkpoint lineage, worker and harness builds, sampling, cache,
      concurrency, retry, stopping, and data-boundary policy in addition to the
      existing model and machine identity.
- [ ] Report pass rate and attempt latency side by side. Add time to valid
      result, attempts per accepted outcome, and validated work rate only when
      their required receipts exist. Do not produce a global productivity
      score or assign a monetary value to human time.
- [ ] Add local workload coverage from user-declared task groups. A workload is
      proven, unproven, blocked, inconclusive, or capacity-limited on this exact
      configuration. The report states what still needs a fallback.
- [ ] Prototype bounded-authority tests: recommend-only behavior, approval
      gates, permission removal, ambiguity escalation, scope restraint, and
      completion without authority expansion.
- [ ] Prototype checkpoint and resume tests plus context-robustness cases for
      distractors, stale versus current facts, late relevant facts, and excess
      irrelevant context. Resume verification also rejects duplicated side
      effects. Maximum context and effective context are different measurements.
- [ ] Build the minimum isolated verifier/worker boundary required for any
      executable workflow verdict. The 0.11 experiment may run one fixed,
      harness-owned workflow in a constrained fixture. Arbitrary generated code
      and executable user-task JSON remain SKIP until the stronger Trust C
      boundary passes the native safety matrix.
- [ ] Prototype sustained and concurrent operation as separate experiment
      families. Soak measures performance drift and accepted work over time.
      A soak reports validated-outcome drift only when an independently
      verified bounded workload is part of the experiment. Serving tests
      measure total and per-request throughput, accepted outcomes per exposure
      wall time, and TTFT distributions at declared concurrency. Neither
      contaminates `fitr run` or enters its rankings.
- [ ] Keep data-boundary, energy, and economics claims evidence-based. Report
      the endpoint and transport fitr observed without claiming to inspect the
      runtime's egress. Energy requires a trustworthy named sensor. Economics
      accepts user assumptions and never downloads prices or invents retained
      cloud spend.

Exit criterion: fitr can demonstrate one bounded workload with an explicit
definition of done and independent verifier, report accepted outcomes without
joining incompatible evidence, and state exactly how much autonomy that
configuration has earned. Experimental operation modes keep distinct schemas
and comparison domains.

### 1.0 - proven local decision system

- [ ] Run the complete acceptance path on clean Windows, macOS, and Linux
      installs.
- [x] Review every command's first-run error and next-action text from a new
      user's perspective. Cross-platform command-contract tests pin successful
      help, exact positional arguments, numeric validation, and useful hints.
- [ ] Complete the core 0.10 analysis contract and the core 0.11 workload
      receipt, independent-proof, and coverage contracts. Optional experiment
      commands need not all graduate, but their findings must be reflected in
      the final CLI and schema decisions.
- [ ] Cut a release candidate, verify all six binaries and checksums through
      both installers, then publish 1.0.

### Acceptance outcome

A new user can:

1. Install one static binary on Windows, macOS, or Linux.
2. Run bare `fitr` to see installed models as `measured`, `unproven`,
   `incompatible`, or `stale`.
3. Follow one next command per row through fit advice and a local measurement,
   with advise returning a real verdict rather than SKIP.
4. Persist the measured context with `fitr apply`, which prints instructions
   and never restarts or mutates the server.
5. Reopen and compare only evidence that is valid for the current device and
   runtime fingerprint.
6. Read capacity and performance separately, understand what evidence supports
   each diagnosis, and see the next experiment when the cause is uncertain.
7. Evaluate at least one declared bounded workload against an independent
   definition of done, then see which local work is proven and which still
   needs evidence or fallback.

The native and backend evidence matrix is tracked in
[release acceptance](docs/release-acceptance.md).

### Product contract already in place

| Surface | Contract |
|---|---|
| Inventory | Runtime-installed models only. Unmeasured models are candidates, never recommendations. |
| Advise | Compatible, low-memory, incompatible, or SKIP, with measured and estimated components kept distinct. |
| Run | Independent needs, explicit uncertainty, no generated-code execution by default. |
| Apply | Prints a persistence recipe. It does not restart or mutate a server. |
| Board and compare | Refuse cross-fingerprint or context-unverified rankings. |
| Export | Opt-in and self-contained. Uses an opaque device ID and allowlisted comparison configuration; omits raw output, hostname, paths, raw fingerprint key, and arbitrary runtime configuration. |

Backend identity details are in [backends](docs/backends.md). The task and
execution-safety contract is in [tasks](docs/tasks.md). Doctor's measurement
preflight is in [doctor](docs/doctor.md).

## Honest limits for 1.0

| Limitation | Consequence |
|---|---|
| 22 generated task specs across 16 families and five needs | Uncertainty is per need and cluster-aware. Unrelated outcomes are never combined into a global quality sample or resolution claim. Thin evidence remains INCONCLUSIVE. |
| OpenAI-compatible timings are client-derived | Usage supplies token counts, but the generic protocol does not expose server timing receipts. |
| Two device profiles | `lappy` is calibrated; `default` is explicitly uncalibrated. No hardware SKU gates are invented from names. |
| Device detection is probe-derived | Vendor tools and OS inventories disagree about what a GPU is. The fingerprint is only as good as the probe, which is why device identity gets its own gate. |
| Conventional attention uses weights plus KV by default | `--load` or `--fit` can observe total allocation and derive an `other resident` remainder, which may include compute buffers and other overhead. It is not an independently measured buffer breakdown. Hybrid recurrent models stay SKIP without a measured allocation. |
| Inventory comes from the serving runtime | There is no disk crawl or internet catalog. A llama-server exposes one model row. |
| `--full` is a long agent loop | Executable coding evidence stays SKIP until the isolated worker exists. The default battery is the first real measurement. |
| Scored inference is single-flight | Parallel prompts would contaminate throughput and context measurements. |
| TTFT cache state may be unknown | The timing remains visible, but unknown or cache-hit TTFT cannot prove the uncached responsiveness gate. |
| Low-footprint is a requested 32K load probe | It is scored only when the runtime confirms that effective context. Older receipts remain visible but cannot support the claim. |
| Bottleneck explanations are hypotheses | A diagnosis cites observations and uncertainty. fitr does not turn a speed ratio or vendor bandwidth specification into a measured root cause. |
| Validated throughput needs per-trial receipts | Aggregate pass rates and token timings cannot honestly yield time to valid result, accepted outcomes per hour, retries, or escalation cost. |

These limits are part of the contract, not hidden release notes. More detail is
in [design](docs/design.md), [statistics](docs/statistics.md), and
[backends](docs/backends.md).

## Next: Trust C

Trust C strengthens provenance and generalizes the minimum safe execution
boundary required by the 1.0 workload experiment.

- [ ] Harden the cross-platform isolated worker beyond the minimum bounded
      verifier boundary. Executable coding tasks remain SKIP until confinement
      has the same testable guarantees on all six release targets.
- [ ] Add signed releases, SBOMs, and attestations.
- [ ] Define calibrated profile provenance and community calibration tooling.
- [ ] Add externally anchored share provenance and a trust-root workflow for
      decision-grade community evidence.
- [ ] Extend 0.10 measurement calibration into reviewable cross-device studies
      and externally reproducible calibration artifacts.
- [ ] Publish repeatability and test-retest studies. First measurements taken,
      on one host with a 7B model:
      | Condition | Decode CV | Note |
      |---|---|---|
      | Within one run, k=3, resident | 1.6% | the sampling noise the intervals model |
      | Across 5 separate runs, same placement | 0.6% | reload does not add much |
      | Across placements, resident vs `GPU 65%` | ~1000% | 179 tok/s against 16 tok/s |
      The dominant source of variance is not sampling, and it is not reload.
      It is machine state: the same model, the same binary and the same box
      differ by an order of magnitude depending on how much of it fits. That
      is why placement belongs in the comparability key, and why a run now
      says so while the operator can still act on it. A behavioural task is
      separately unstable -- ten seeded, temperature-zero runs of the tool
      withdrawal loop took four to eight turns and split nine to one on
      outcome, so turn counts are not a measurement.
- [x] Warn at measurement time when inference is not really on the
      accelerator. Doctor had this rule and run did not, so a partial-offload
      run was saved looking like any other. Found by accident: a repeatability
      study returned 16 tok/s for a model that had measured 179 an hour
      earlier, because something else had taken most of the VRAM.
- [ ] Build a formal backend-conformance corpus.

Exit criterion: an independent reviewer can reproduce the protocol and
distinguish unsupported, inconclusive, contaminated, and measured outcomes
without reading the implementation.

## Later: candidate discovery

Today fitr answers "is what I already have any good here?" The larger question
is "what should I get?" fitr is positioned to answer it with the same local
verifier rather than an unmeasured ranking. A candidate does not have to be
trusted; it has to be pulled and measured. A name that does not exist fails at
pull, loudly and cheaply.

The loop extends by one step at the front:

```text
discover -> advise -> pull -> run -> apply -> compare
what exists  fits?   fetch  verify  persist   decide
anywhere
```

Design constraints, in priority order:

1. **Fit is deterministic; interest is not.** A registry index plus the
   existing fit math answers "what fits here" with no model in the loop, and
   that path must stand alone: a new user has no local model to think with.
   But it does not answer "what is worth considering". Sorting a registry by
   downloads is a lagging indicator, structurally biased against exactly the
   new releases the user is asking about. That signal lives in release notes,
   model cards, and announcements -- unstructured text.
2. **A language model reads; it does not recall.** A small local model asked
   what is new answers from a stale cutoff, and the models worth finding are
   the new ones. Fetched text is the source; the model extracts and
   summarises it, and turns a stated workflow into filters. Reading fetched
   text is work a 4 GB-class model can do, and whether it fits is a question
   fitr already answers.
3. **Candidates never enter the evidence path.** Everything discovery produces
   is a candidate, and candidates are never recommendations. The measurement
   is what promotes one.
4. **Every candidate resolves before it is shown as anything but a guess.** An
   unresolvable artifact is dropped, not displayed.

- [ ] **A shipped starting point.** An empty machine is the moment of highest
      user intent and lowest product value: bare `fitr` has nothing to say to
      someone with no models. A curated catalog, grouped by memory budget and
      platform, converts that empty screen into a next command. It is dated by
      construction, and that is acceptable -- the facts it carries (params,
      quant, size, architecture) are immutable properties of an artifact, so
      the fit math never decays; only novelty does. Constraints that keep it a
      starting point rather than a ranking:
      - Curated and small enough that a human actually reviews each refresh.
        An unreviewed monthly agent PR is worse than no catalog, because it
        launders generated text through a trusted repo.
      - Grouped into buckets, never rank-ordered. A sorted list is read as a
        leaderboard whatever the header says.
      - Freshness shown, not hidden. Disclosed staleness is a known quantity.
      - Displaced by evidence. Suggestions fill the empty state and recede as
        measured rows accumulate. The catalog is scaffolding.
      - Split by platform for the budget math, not for the artifact. A GGUF is
        a GGUF; Apple unified memory is what changes the tier arithmetic.
- [ ] **Ship the artifact, not the agent.** Research fan-out runs in CI on a
      schedule and lands as a reviewable pull request, not inside the binary.
      That keeps generated text behind a human merge gate, adds no network
      surface to the release, and needs no local model -- so discovery works
      on the clean machine that has nothing installed yet. `spec/` already
      demonstrates the shape: canonical at the repo root, embedded copy,
      versioned, with drift tests failing the build.
- [ ] **Replaceable live discovery.** Query a public model index through an
      adapter, cache source timestamps and immutable artifact metadata, and
      rank only by fit eligibility plus the user's stated workflows.
- [ ] **Research receipts.** Show why a candidate entered the set, source
      freshness, license, architecture, quant, shard completeness, expected
      fit range, runtime support, missing evidence, and the exact local test.
- [ ] **Optional small-model assist.** Once any model is installed, use it to
      read fetched text and to turn a stated need into filters. Availability
      is an enhancement, never a requirement; it must work on a small machine
      across Windows, macOS, and Linux.

## Later: keeping a machine current

Discovery answers "what should I get?" once. The recurring form answers "am I
still running the right thing?" without the user having to care. A pile of
models collected whenever attention was last paid is the normal state of a
local setup, and nothing today tells someone that a newer release is better
*on their box*. A scheduled updater alone cannot: only measurement can, which
is why this is fitr's job and not a package manager's.

This also serves the second audience. The same engine has two surfaces: the
operator who tunes context, KV dtype, and quant reads the fit table and the
calibration pairs; the user who just wants a good model reads one line a week.
The second surface is only trustworthy because the first one exists.

Sequence matters: this composes the candidate catalog, the measurement queue,
and fingerprint-scoped comparison. It is post-1.0 work. Attempting it earlier
would spend the 1.0 polish budget on a second product.

- [ ] **Opt-in recurring reevaluation.** Produce an OS-native scheduled plan
      only after explicit confirmation. Default to a dry run with network,
      disk, wall-time, thermal, and external-cost budgets.
- [ ] **Report only what the instrument resolves.** The default generated
      battery spans 22 task specs, 16 families, and five independent needs.
      Each need has its own interval and gate; there is no defensible global
      quality denominator. Throughput is a different instrument: k=3 on one
      device measured decode at a 1.6 percent coefficient of variation. An
      unattended report may state fit, memory, speed, and established
      breakage. It must never call one model globally smarter. Complete paired,
      family-aware evidence may support only a scoped directional claim about
      the declared need and families it actually separates.
- [ ] **Spend background time on statistical power.** Wall-clock is the one
      resource a scheduled job has and an interactive user does not. Repeats
      that nobody would wait for are affordable overnight, and trials are what
      convert an unrankable pair into a real answer. Depth, not breadth, is
      what the background buys.
- [ ] **Never contaminate, never intrude.** Scored inference is single-flight,
      so a background run must not start while the GPU is busy. The courtesy
      constraint and the evidence-validity constraint are the same constraint.
- [ ] **Own the disk consequence.** Weekly candidate pulls grow without bound,
      so testing tidies up after itself by default. fitr currently has no
      delete path at all -- its most destructive act is declining to restart a
      server -- so this adds a capability class, not a flag, and the
      invariants come first:
      - **Remove only what fitr pulled.** A candidate fitr fetched is fitr's
        to clean up. A model the user already had is never eligible, even if
        the run just measured it. Deleting someone's existing 17 GB artifact
        because a test touched it is the one bug that would end the project's
        credibility.
      - **Record provenance before the pull, not after.** Ownership has to
        survive a crash mid-run, or the next run cannot tell a leftover
        candidate from a user's model. In-memory tracking is not enough.
      - **Default by who initiated the pull.** An explicit `fitr run x --pull`
        is the user asking for that model: keep it, and print the reclaim
        command the way `apply` prints a recipe. An unattended candidate the
        user never named is fitr's mess: clean it. Same option, different
        default, because the consent is different.
      - **Bound the worst case with a budget, not just a boolean.** A cap on
        candidate disk holds even when cleanup fails or a run is killed.
      - Evidence survives cleanup. A sealed result carries the digest, so
        removing the artifact costs re-download to re-verify, not the record.
- [ ] **End at a next command.** A finding is not a switch. Report the
      comparison and print the apply recipe; the user decides.

## Later: tune, and the mutation stance

`fitr does not restart or mutate the serving process` is right for `apply` and
was wrong as a blanket rule. Persisting a context genuinely needs no restart --
`num_ctx` is a per-request option and `ollama create` writes a derived tag --
so that command loses nothing by printing a recipe. But two knobs are read only
at server start, `OLLAMA_KV_CACHE_TYPE` and `OLLAMA_FLASH_ATTENTION`, and
declining to orchestrate a restart turns a five-second operation into a manual
chore. On the machine fitr is built for, the operator owns the server.

The distinction is consent and blast radius, not purity:

- [ ] **`fitr tune` restarts the server, once the user says so.** Set the
      knob, restart, measure, restore the prior value. Confirmed, never
      silent, never a default.
- [ ] **Say what a restart costs before asking.** In-flight requests from
      other clients die, and a server that does not come back is fitr breaking
      the machine it was asked to measure. That is a reason to ask once, not a
      reason to refuse forever.
- [ ] **Detect the shared case.** A system-level service or other active
      clients warrant a warning; a user-owned desktop process does not.
      `--no-mutate` keeps today's print-only behaviour for CI and shared hosts.
- [ ] **Prefer a fitr-owned instance where it is cheap.** A scratch server on
      its own port sweeps without disturbing anything, under the same rule
      that governs pulled models: fitr may mutate what fitr created.
- [ ] Fingerprinting is already correct here. Cache dtype is part of the
      device key, so a sweep across dtypes separates its own evidence without
      new machinery. This was never the obstacle.

## Later: loop extensions

These features must preserve the evidence contract.

- [ ] **Real vision tasks.** Add them only when the evidence and scoring path is
      as explicit as the text path.
- [ ] **Native desktop clients.** Consider SwiftUI/AppKit, WinUI 3, and GTK 4
      only after the terminal information architecture is proven. See
      [interface](docs/interface.md).

## Intentionally deferred

| Item | Why it waits |
|---|---|
| Community SKU profiles | Real measured gates are required. `fitr profiles new` already creates an explicitly uncalibrated local profile. |
| Battery changes in `spec/` | Gate changes need lineage-verified pairs across at least two devices and two model families. |
| Automatic server-mutating tune sweeps | Context and quant experiments begin in 0.10, but changing process-wide runtime settings still requires the explicit restart and restoration contract in the tune milestone. |
| Parallel ordinary scored inference | Shared GPU work produces plausible but invalid timing data. A separate serving-test schema may measure declared concurrency without entering ordinary rankings. |
| Arbitrary executable user tasks | The 0.11 bounded workflow prototype requires an isolated verifier boundary. Unrestricted code from task JSON remains out of scope. |
| Long-context needle tests | They add depth coverage but do not block the core local decision loop. |
| Public leaderboard | Cross-device ranking contradicts the product's evidence model. A shipped catalog is not this: it groups candidates by what fits, which is reproducible arithmetic, and never orders them by quality, which is not. |
| LLM-as-judge correctness | Computed ground truth remains the default wherever mechanical grading is possible. |

## Shipped history

| Release | Milestone |
|---|---|
| 0.2.0 | Calibration reports and local device-profile scaffolding |
| 0.3.0 | Closed advise, run, and apply basics; terminal data views and optional retonr export |
| 0.4.0 | Full-screen terminal monitor, immutable history, and presentation contracts |
| 0.5.0 | Evidence integrity, reproducibility receipts, same-base lineage, and family-stratified aggregation |
| 0.6.0 | Installed inventory as the default `fitr` surface |
| 0.7.0 | Context-fit table with weights, KV, buffers, and headroom |
| 0.8.0 | Inventory fit tiers, Inventory TUI view, and one next-command grammar |
| 0.9.0 | Serving versus measured context, compact inventory windows, and named advise through `fitr <model>` |
| 0.9.1 | Bug fixes for loaded resident fit, serving-context identity, backend selection, and inventory filtering |
| 0.9.2 | Exact saved-model identity, timezone-correct history selection, and fail-closed installer verification |
| 0.9.3 | Hardening source release; superseded because its Linux artifacts linked to glibc |
| 0.9.4 | Static Linux correction plus lock, probe, evidence recovery, atomic output, receipt, and reproducibility hardening |
| 0.9.5 | Backend and GGUF boundary hardening, bounded local inputs, deterministic profiles, cancellation fixes, and Apache License 2.0 |
| 0.9.6 | Duplicate-key JSON rejection, fail-closed model inventory, bounded discovery and control calls, and portable task-file boundaries |
| 0.9.7 | Second-machine correctness: the fit verdict restored on Ollama, device identity sealed and gated, untrusted GGUF and task-file input hardened and fuzzed, the KV dtype remedy, whole-loop live coverage, and a static analysis gate |
| 0.9.8 | Output that fits: every surface composed to a resolved width, verdicts given a structure the renderer can lay out, sparklines refused where the data cannot support them, and the transport retry that stopped discarding completed runs |

Release notes and artifacts are on the
[GitHub releases page](https://github.com/blisspixel/fitr/releases).
