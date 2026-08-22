# Roadmap

`fitr` answers one question: **is this local model any good on this machine?**

The product is the local decision loop, not a benchmark score:

```text
inventory -> advise -> run -> apply -> compare
what exists   fits?    verify   persist   decide
```

Every step follows the same evidence contract. Measurements stay attached to
the artifact, runtime, context, and device that produced them. Unsupported or
uncertain facts remain visible without becoming rankings. Every negative fit
verdict carries a remedy.

The rationale and invariants live in [design](docs/design.md). Commands and
flags live in [usage](docs/usage.md). Statistical methods live in
[statistics](docs/statistics.md).

## Status

| Horizon | Release | Outcome |
|---|---|---|
| Shipped | 0.9.1 | Stable inventory, serving-context, resident-fit, and apply behavior across the closed loop |
| Now | 1.0 | A new user can install fitr and close the loop on a clean machine without reading source code |
| Next | Trust C | Isolated executable evidence, stronger release provenance, and calibrated profile provenance |
| Later | Loop extensions | Replaceable discovery, research receipts, recurring reevaluation, vision, and native clients |

## 1.0: clean-machine confidence

The functionality needed for 1.0 exists. The remaining work is acceptance,
hardening, and removing surprises at the edges.

### Acceptance outcome

A new user can:

1. Install one static binary on Windows, macOS, or Linux.
2. Run bare `fitr` to see installed models as `measured`, `unproven`,
   `incompatible`, or `stale`.
3. Follow one next command per row through fit advice and a local measurement.
4. Persist the measured context with `fitr apply`, which prints instructions
   and never restarts or mutates the server.
5. Reopen and compare only evidence that is valid for the current device and
   runtime fingerprint.

### Remaining release work

- [ ] Run the complete clean-machine acceptance path on all three supported
      operating systems.
- [ ] Exercise Ollama, llama-server, and a generic OpenAI-compatible endpoint
      against the documented identity and context gates.
- [ ] Resolve every release-blocking defect found by that matrix and keep the
      cross-platform CI, race detector, vulnerability scan, fuzz smoke tests,
      distribution build, and installer syntax checks green.
- [ ] Review every command's first-run error and next-action text from a new
      user's perspective.
- [ ] Cut a 1.0 release candidate, verify all six binaries and checksums through
      the installers, then publish 1.0.

### Product contract already in place

| Surface | Contract |
|---|---|
| Inventory | Runtime-installed models only. Unmeasured models are candidates, never recommendations. |
| Advise | Compatible, low-memory, incompatible, or SKIP, with measured and estimated components kept distinct. |
| Run | Independent needs, explicit uncertainty, no generated-code execution by default. |
| Apply | Prints a persistence recipe. It does not restart or mutate a server. |
| Board and compare | Refuse cross-fingerprint or context-unverified rankings. |
| Export | Opt-in, self-contained, fingerprint visible, raw model output omitted. |

Backend identity details are in [backends](docs/backends.md). The task and
execution-safety contract is in [tasks](docs/tasks.md). Doctor's measurement
preflight is in [doctor](docs/doctor.md).

## Honest limits for 1.0

| Limitation | Consequence |
|---|---|
| About 23 binary trials per default run | Minimum detectable effect is about 29 percentage points. The default battery separates broken from working, not good from slightly better. |
| OpenAI-compatible timings are client-derived | Usage supplies token counts, but the generic protocol does not expose server timing receipts. |
| Two device profiles | `lappy` is calibrated; `default` is explicitly uncalibrated. No hardware SKU gates are invented from names. |
| Conventional attention uses weights plus KV by default | Compute buffers require `--load` or `--fit`. Hybrid recurrent models stay SKIP without a measured allocation. |
| Inventory comes from the serving runtime | There is no disk crawl or internet catalog. A llama-server exposes one model row. |
| `--full` is a long agent loop | Executable coding evidence stays SKIP until the isolated worker exists. The default battery is the first real measurement. |
| Scored inference is single-flight | Parallel prompts would contaminate throughput and context measurements. |

These limits are part of the contract, not hidden release notes. More detail is
in [design](docs/design.md), [statistics](docs/statistics.md), and
[backends](docs/backends.md).

## Next: Trust C

Trust C strengthens what can become decision-grade evidence. It does not
change the 1.0 product loop.

- [ ] Build a cross-platform isolated worker. Executable coding tasks remain
      SKIP until confinement has the same testable guarantees on all six
      release targets.
- [ ] Add signed releases, SBOMs, and attestations.
- [ ] Define calibrated profile provenance and community calibration tooling.
- [ ] Add externally anchored share provenance and a trust-root workflow for
      decision-grade community evidence.
- [ ] Publish repeatability and test-retest studies.
- [ ] Build a formal backend-conformance corpus.

Exit criterion: an independent reviewer can reproduce the protocol and
distinguish unsupported, inconclusive, contaminated, and measured outcomes
without reading the implementation.

## Later: loop extensions

These features must preserve the evidence contract. Candidate metadata can
never be mistaken for measured local quality.

- [ ] **Replaceable live discovery.** Query a public model index through an
      adapter, cache source timestamps and immutable artifact metadata, and
      rank only by fit eligibility plus the user's stated workflows.
- [ ] **Research receipts.** Show why a candidate entered the set, source
      freshness, license, architecture, quant, shard completeness, expected
      fit range, runtime support, missing evidence, and the exact local test.
- [ ] **Opt-in recurring reevaluation.** Produce an OS-native scheduled plan
      only after explicit confirmation. Default to a dry run with network,
      disk, wall-time, thermal, and external-cost budgets.
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
| `fitr tune` sweeps | Reliable sweeps require server restart orchestration. llama-bench already owns throughput-only sweeps. |
| Parallel scored inference | Shared GPU work produces plausible but invalid timing data. |
| Executable user tasks | Arbitrary code from JSON requires the isolated worker. |
| Long-context needle tests | They add depth coverage but do not block the core local decision loop. |
| Public leaderboard | Cross-device ranking contradicts the product's evidence model. |
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

Release notes and artifacts are on the
[GitHub releases page](https://github.com/blisspixel/fitr/releases).
