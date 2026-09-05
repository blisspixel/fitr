# fitr

[![CI](https://github.com/blisspixel/fitr/actions/workflows/ci.yml/badge.svg)](https://github.com/blisspixel/fitr/actions/workflows/ci.yml)
[![Native acceptance](https://github.com/blisspixel/fitr/actions/workflows/native-acceptance.yml/badge.svg)](https://github.com/blisspixel/fitr/actions/workflows/native-acceptance.yml)

**fitr determines what local AI actually works for your workload on this
machine, shows the evidence, and helps choose a configuration without reducing
everything to one benchmark score.**

Model names and public leaderboards do not answer the local questions. Will
this artifact fit at the context you need? Is the runtime really using the
accelerator? Are tool calls reaching the tool channel? Is the faster quant
still correct? Does a candidate complete a bounded workflow when another
system independently verifies the result?

fitr measures those questions against the model bytes, runtime, context,
placement, and device that produced the evidence. Missing evidence stays
missing. An unmeasured model stays a candidate, not a recommendation.

<img src="docs/assets/top.svg?v=0.10.10" alt="fitr top wide board with comparable configurations and selected evidence" width="1000">

The wide Board keeps the comparable configurations, selected evidence, exact
measurements, unresolved requirements, and one next action on one screen. The
same facts remain available as compact terminal output, JSON, and HTML.

## Install

The full loop needs a running supported backend with at least one model. See
[backend requirements and identity limits](docs/backends.md).

macOS and Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/blisspixel/fitr/main/install.sh | sh
```

Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/blisspixel/fitr/main/install.ps1 | iex
```

fitr is one static binary. It needs no Python environment and sends no
telemetry. Installers and `fitr update` verify the selected release asset
against its published SHA-256 checksum. Pinning a version, relocating the
binary, updating, and building from source are covered in the
[install guide](docs/usage.md#install).

## Start with the local decision loop

```bash
fitr                                # inventory, evidence state, one next action
fitr qwen3:30b                      # fit advice for one model
fitr run qwen3:30b --ctx 16384 --capacity-budget-gb 22
                                      # behavior, performance, and safe-budget fit
fitr apply qwen3:30b                # print, but do not execute, persistence steps
fitr board                          # compare compatible measured evidence
fitr decide qwen3:30b --spec local-coding.json
```

Every inventory row ends with one useful next command. A decision spec then
applies your workload requirements to sealed evidence without changing the
original measurement. Requirements may cover behavior, capability support,
effective context, a specific latency state, and exact-context resident
memory. The answer is eligible, ineligible, or unresolved, with exit code 4
reserved for required evidence that has not yet been established.

Read [usage](docs/usage.md) for all commands and flags, or
[decision specifications](docs/decisions.md) for the strict schema and
requirement semantics.

<img src="docs/assets/inventory.svg?v=0.10.10" alt="fitr inventory fixture with local evidence, memory limits and a remote model excluded from local measurement" width="820">

## Start with something you heard about

A post, video, podcast, model card, or selected email excerpt can become an
idea to investigate for coding, a daily driver, classification, or another
role. Capture the claim and intended harness without turning it into evidence:

```bash
fitr discover add https://huggingface.co/Qwen/Qwen3.8-27B --role coding --model qwen3.8:27b --harness Pi
fitr discover list
fitr discover plan --role coding
```

The private inbox deduplicates ideas and drafts the next evidence steps. It
keeps source claims unmeasured. For an explicit Hugging Face file selection,
resolve public metadata before considering a download:

```bash
fitr source resolve hf --repo owner/model --revision main --file model-Q4_K_M.gguf --out candidate.json
fitr source show candidate.json
fitr discover attach-source <idea-id> candidate.json
fitr discover plan <idea-id>
```

<img src="docs/assets/source.svg?v=0.10.10" alt="Source metadata fixture with a pinned file, declared size, a projector candidate and unresolved local fit" width="1000">

The receipt pins a commit, preserves declared file sizes and hashes, and
surfaces dependency gaps. It downloads no weights and does not qualify the
model for a role. See [source resolution](docs/source-resolution.md),
[source attachments and investigation plans](docs/source-attachments.md),
[discovery and the model library](docs/discovery.md) for the
flow, and [agent interoperability](docs/agent-interop.md) for the portable
Agent Plugins package, read-only MCP tools, and the researched A2A, Hermes,
Pi and OpenClaw integration boundaries.

<img src="docs/assets/discovery.svg?v=0.10.10" alt="Discovery inbox fixture with a linked metadata receipt, an unverified claim and an unmeasured classifier idea" width="820">

For files already on disk, an explicit mapping can compare their local hashes
with the pinned receipt before any runtime experiment:

```bash
fitr artifact bind --source candidate.json --mapping local-files.json --max-bytes 68719476736 --timeout 10m --out artifact.json
```

Read [artifact binding](docs/artifact-binding.md) for the mapping and I/O bounds.

<img src="docs/assets/artifact.svg?v=0.10.10" alt="Local artifact fixture with a whole-file hash match and separate unverified dependencies, unbound runtime and unmeasured quality" width="820">

Matching local bytes still leaves runtime unbound, and capacity and quality
unmeasured.

## Choose for a role, with quality first

Declare the quality, context and memory requirements for a job, then attach
canonical measurements. Each role keeps its own versioned preferences:

```bash
fitr role init coding --quality user_tasks --memory-gb 22 --ctx 8192
fitr role attach coding /path/to/canonical-result.json
fitr role review coding
```

<img src="docs/assets/roles.svg?v=0.10.10" alt="Role review fixture showing a qualified daily model and a smaller model that fails the quality floor" width="900">

A candidate must clear every floor before preferences matter. Comparisons
retain uncertainty and check sensitivity to weight changes; missing evidence
and `no-qualified-candidate` are useful answers. With two to four compatible
candidates, `fitr role confirm coding` seals the policy and collects fresh
evidence after runtime and memory preflight. Explicit `role adopt` records the
selection; `role status` rechecks its evidence and expiry. A failed challenger
keeps the incumbent reference. See [roles](docs/roles.md) for the battery
screening scope and rollback limits. Automatic serving changes remain future work.

## Review what occupies your model storage

```powershell
fitr cleanup plan D:\models
```

The read-only plan shows apparent storage, large files and aged partial
downloads worth investigating. It does not delete files or assume a large
encoder, projector, shard or cache is unused. Links, incomplete scans and
unresolved dependencies remain explicit. See [cleanup planning](docs/cleanup.md).

## What ships today

| Capability | What it establishes | Details |
|---|---|---|
| Inventory and fit advice | Installed artifacts, evidence freshness, projected context fit, and an exact remedy when supported evidence says a configuration does not fit | [Usage](docs/usage.md), [choosing hardware](docs/choosing-hardware.md) |
| Local measurement | Structured output, instruction following, refusal, tool-channel behavior, degeneration, load state, TTFT, prefill, decode, context, placement, and allocation when the backend exposes them | [Design](docs/design.md), [tasks](docs/tasks.md) |
| Capacity policy | A pre-load sealed resource domain, timestamped availability, explicit operator budget or reserve, exact usable-budget formula, component projection, and observed safe headroom | [Choosing hardware](docs/choosing-hardware.md#capacity), [usage](docs/usage.md#capacity-policy-for-a-run) |
| Workload decisions | Constraint-based eligibility under a versioned declaration, with no universal weighted score | [Decisions](docs/decisions.md) |
| Personal roles | Quality and resource floors, fixed preferences, fresh confirmation, explicit selection, evidence expiry and validated rollback | [Roles](docs/roles.md), [confirmation](docs/role-confirmation.md) |
| Source resolution | Commit-pinned public file metadata, distinct declared hashes and unresolved dependencies, with no weight downloads | [Source metadata](docs/source-resolution.md) |
| Discovery investigations | Private source attachments and separately stated metadata, dependency, runtime and quality gaps | [Source attachments](docs/source-attachments.md) |
| Local artifact observations | Bounded whole-file hashes for explicit mappings, source comparisons and change detection without runtime promotion | [Artifact binding](docs/artifact-binding.md) |
| Agent interoperability | Read-only MCP 2026-07-28 role tools, official SDK acceptance and an Agent Plugins 1.0.0 package | [Protocol and client limits](docs/agent-interop.md) |
| Cleanup planning | Read-only bounded storage inventory and aged partial-download review candidates, with no inferred deletion authority | [Cleanup](docs/cleanup.md) |
| Context experiments | A predeclared exploratory context plan with shared task seeds, point-specific allocation, required-equal factors, and replayable bundles | [Context experiment](docs/usage.md#context-experiment) |
| Configuration tradeoffs | Conservative frontiers across sealed candidates, optional same-base conversion lineage, and no point-estimate winner when intervals overlap | [Quant experiment](docs/usage.md#quant-configuration-experiment), [calibration](docs/calibration.md) |
| Fresh confirmation | A sealed candidate set, a fresh shared task seed, full paired runs, and confirmation only when requirements resolve and the objective separates | [Confirmation](docs/usage.md#fresh-configuration-confirmation) |
| Validated work | A sealed workflow contract, typed proof classes, per-trial timing partitions, fixed policy-repair tools, independent verification, and signed terminal outcomes | [Validated work](docs/usage.md#validated-work-experiment), [workload evidence](docs/workload-evidence.md) |

The broader 1.0 thesis has seven evidence layers:

```text
FIT          Can this exact configuration run within the relevant capacity?
BEHAVIOR     Does it perform the required primitives correctly?
PERFORMANCE  How long do distinct load and inference phases take?
EXPLAIN      What does the evidence support, contradict, or leave unresolved?
VALIDATED WORK
             Does speed survive retries and independent verification?
TRADEOFFS    Which configurations are dominated, and which remain choices?
COVERAGE     Which declared workloads have earned local trust or need fallback?
```

FIT, behavior, burst performance, typed capacity policy, direct receipt
diagnoses, typed context and configuration experiments, fresh confirmation,
and one bounded validated-work contract are implemented. Broader causal
explanation, operational experiments, and declared workload coverage remain
pre-1.0 work. The
[roadmap](ROADMAP.md) distinguishes shipped slices from planned contracts.

README screenshots are deterministic fixtures rendered through the real
presentation paths with `make screenshots`. Host identity and local paths are
omitted. Full receipts and the other command surfaces live in the linked docs,
where they can be explained without turning the front page into a transcript.

## Why the evidence is useful

- **Fit is not performance.** Artifact size, KV projection, addressable
  capacity, current availability, observed allocation, and sustained residency
  are different claims.
- **Speed is not correctness.** A fast model can emit malformed structure,
  call a tool in message content, repeat a call after the tool disappears, or
  degenerate into a loop.
- **Capability is not competence.** A runtime declaration can route a test. It
  cannot become a behavioral PASS.
- **Exploration is not confirmation.** The observations that selected an
  attractive context or quant cannot certify that choice. Confirmation uses a
  fresh sealed plan and fresh evidence.
- **The worker is not the verifier.** Validated work requires harness-owned
  state and independent proof of the final outcome.
- **Uncertainty is an answer.** Missing receipts, overlapping intervals, and
  blocked observations remain visible instead of becoming estimates.

One renderer-neutral analysis path rebuilds presentation claims from validated
records. CLI, TUI, JSON, and HTML consume those facts; a renderer does not get
to invent a verdict.

## What fitr refuses to claim

- It does not rank evidence across incompatible machines or configurations.
- It does not call an unmeasured model good or turn missing input into a
  number.
- It does not reuse a result after the runtime-bound artifact identity changes.
- It does not add isolated model measurements and call the sum co-residency.
- It does not infer a hardware root cause from a timing ratio.
- It does not certify an exploration winner on the data that selected it.
- It does not mutate or restart your serving runtime. `fitr apply` prints a
  recipe.
- It does not run generated code by default or silently score unavailable
  execution evidence.

These boundaries are part of the product. The detailed evidence model and
known limits are in [design](docs/design.md).

## Optional remote validation

OpenRouter and other OpenAI-compatible providers are optional. They can help
develop adversarial cases, calibrate heuristic graders, compare task-pack
behavior, or provide an explicitly labeled model-judged observation. They are
never required for installation, local measurement, decisions, exports, or
CI, and they cannot establish local fit, placement, residency, or performance.

The existing OpenAI-compatible backend can be used explicitly for protocol
diagnostics. Credentials come from environment variables and are not written
to a fitr result. Exact commands, current limits, and the future
experiment-scoped provider receipt are documented in
[optional external validation](docs/external-validation.md).

## Local, private, and inspectable

Installed-model evaluation talks only to the selected endpoint. With a local
backend and installed artifact, the measurement path can run offline. Network
access otherwise occurs only for an explicit install, update, pull, or remote
endpoint. Explicit source resolution also fetches public file metadata.

Ollama entries marked as remote are excluded from local measurement, even when
the daemon is on localhost. Absent markers are not proof of local execution;
see [execution provenance](docs/local-execution.md) for the checks and limits.

Results remain on your machine unless you explicitly export them. The HTML
export omits raw model output, hostnames, local paths, the raw device
fingerprint key, and arbitrary runtime configuration. Private workload bundles
retain hashes and deterministic verifier output rather than raw prompts,
replies, or tool contents. The resulting integrity receipt is intentionally
not described as full replayability.

Local storage permissions matter: source and artifact receipts use mode `0600`
on Unix and inherit the destination directory's ACL on Windows. Use a directory
restricted to your account. fitr does not encrypt receipts or replace Windows ACLs.

## Documentation

- [Usage](docs/usage.md): installation, commands, flags, output modes, storage,
  and exit codes.
- [Design](docs/design.md): the evidence model, scoring principles, trust
  boundaries, and known limits.
- [Decision specifications](docs/decisions.md): workload requirements,
  eligibility, uncertainty, objectives, and evidence levels.
- [Workload evidence](docs/workload-evidence.md): bounded workflows,
  independent verification, timing, coverage, authority, and retention.
- [Choosing hardware](docs/choosing-hardware.md): capacity, performance,
  workload fit, and honest hardware evidence.
- [Tasks](docs/tasks.md), [statistics](docs/statistics.md), and
  [calibration](docs/calibration.md): battery construction and inference.
- [Backends](docs/backends.md), [doctor](docs/doctor.md), and
  [TUI](docs/tui.md): runtime contracts, measurement readiness, and the
  optional terminal monitor.
- [Optional external validation](docs/external-validation.md): the narrow,
  opt-in OpenRouter role and its evidence boundary.
- [Discovery](docs/discovery.md) and [agent interoperability](docs/agent-interop.md):
  ideas, role-specific choices, bounded automation and integration contracts.
- [Terminal design language](docs/design-language.md): color, layout, live
  activity and reduced-motion behavior.
- [Roadmap](ROADMAP.md) and [release acceptance](docs/release-acceptance.md):
  what comes next and the receipts required to publish.

## License

[Apache License 2.0](LICENSE). Dependency licenses are in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
