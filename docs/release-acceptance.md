# Release acceptance

This document tracks the evidence required for the 1.0 release. Automated
protocol tests are necessary, but they do not replace a native binary running
against real serving runtimes on clean operating-system installs.

Last updated: 2026-08-30.

## Automated gates

| Gate | Coverage |
|---|---|
| Public command contract | Help succeeds, malformed input fails before discovery, positional counts and numeric bounds are exact, and diagnostics include a next action. Runs on Windows, macOS, and Linux CI. |
| Backend wire contract | Mock HTTP servers cover discovery, generation, chat and tool calls, timing receipts, artifact identity, effective context, authentication, redirects, bounded responses, and error redaction. |
| Evidence contract | Tests cover runtime-bound model identity, context verification, device fingerprints, immutable history, signed completion records, contamination, and comparison refusal. |
| Native candidate acceptance | A manual clean-run workflow installs the candidate artifact on ephemeral Linux and macOS runners, starts a pinned llama-server with two independently hashed GGUFs, exercises the everyday loop, checks evidence refusals, verifies cleanup, and uploads the complete transcript. |
| Release binary smoke | The built Linux binary must pass global and subcommand help, then reject a malformed command with exit 2 and a useful hint. |
| Installer smoke | Windows, macOS, and Linux runners install locally served candidate artifacts, bind the native asset to its exact checksum entry, and validate its version command. |
| Release quality | Formatting, vet, unit tests, race detection, cross-compilation, reproducible Linux build comparison, static ELF verification, size limits, vulnerability scanning, fuzz smoke tests, and installer syntax checks must all pass. |

### Reproducible native candidate acceptance

`.github/workflows/native-acceptance.yml` is a manual release gate, not a
mock-backend test. It builds the candidate once, installs it through the
checksum-verifying installer on clean ephemeral Linux and macOS runners, and
then runs `scripts/native-acceptance.sh` against a pinned llama-server build
and two pinned GGUF artifacts. Runtime archives, model bytes, and the candidate
binary are checked against exact SHA-256 values before any measurement.

The script isolates `FITR_RESULTS`, starts a dedicated CPU-only loopback
server at a fixed 8192-token context, and records inventory, advise, run,
apply, doctor, diag, device, view, export, and `top --snapshot`. It also proves
that Board and Compare refuse the observed-only llama-server artifact identity
rather than turning a readable local file hash into a runtime binding. Every
job stops the exact server process and verifies that its listener closed.
The saved schema-6 records must identify the native backend, verify the exact
context, keep resident memory SKIP, record first output, prove uncached decode
and prefill from `timings.cache_n`, and prove the replayed prefix was reused.
Operational exit 1 from Doctor or Diag fails the job.

The uploaded evidence artifact contains the operating-system and architecture
record, binary and model hashes, `/props`, launch and request logs, every FITR
command transcript, isolated result JSON, and a pass summary. A green job is
therefore a reviewable receipt for that candidate and runner, not a permanent
claim about all llama.cpp builds or accelerators.

## Native operating-system matrix

A row may only claim a passing live loop if every command in the README's
everyday loop table was invoked and its output recorded. The 0.9.6 Windows row
below claimed a passing loop while `advise` returned SKIP for every Ollama
model, because the recorded path never called `advise`. A partial path is
recorded as partial.

| Operating system | Clean install | Bare inventory | Advise | Live loop with isolated results | Reopen and apply | Status |
|---|---:|---:|---:|---:|---:|---|
| Windows amd64 (host A) | Pending final clean VM | Pass | Pass | Pass with Ollama, all documented commands | Pass | Pass on a used host |
| Windows amd64 (host B, 0.9.6) | Pending final clean VM | Pass | Failed: SKIP for every model | Recorded pass, advise not exercised | Pass | Superseded |
| macOS arm64 | Pending | Pending | Pending | Pending | Pending | Pending |
| Linux amd64 | Pending | Pending | Pending | Pending | Pending | Pending |

Rows must also record environment depth, not only the operating system: shell
and probe tooling version, serving-runtime version, GPU vendor, and driver. A
matrix whose finest grain is "Windows" cannot see a defect that only appears
on Windows PowerShell 5.1, which is how one shipped in 0.9.6. `fitr device`
prints the interpreter its probes run through so a row can record it.

### Windows amd64, host A

Environment: Windows 11, **Windows PowerShell 5.1.26100.9168**, Go 1.26.4,
Ollama 0.24.0, NVIDIA GeForce RTX 4090, driver 32.0.16.1047, 24.0 GB reported
by nvidia-smi, `OLLAMA_MODELS` on a separate volume.

Run through `TestLiveLoopSmoke` with an isolated `FITR_RESULTS` directory. All
documented commands were invoked and each was required to produce its
artifact, not merely exit zero: inventory, advise, run, apply, board, doctor,
diag, device, view, export, `top --snapshot`, and compare across two measured
models. Advise produced a fit verdict and a context-fit table; export produced
self-contained HTML with no remote references; board and view reopened the run
just measured.

This host is the reason three defects are fixed in 0.9.7. It was not a clean
install, which is what made it useful: a used machine carries the virtual
display adapter, the older interpreter, and the mixed model store that a clean
VM does not.

### Windows amd64, host B (0.9.6, superseded)

The earlier row used Ollama 0.32.15 and `qwen3:0.6b-q4_K_M` at a requested
2048-token context, and recorded a passing live loop. The recorded path never
invoked `advise`, which was returning SKIP for every Ollama model at the time.
The run did resolve the served tag to a runtime-bound digest, verify 2048
effective tokens, complete speed, resident-memory and tool-plumbing checks,
save and reopen a schema 5 result, and produce a non-mutating apply recipe. It
left no model resident and did not read or modify existing user evidence.

## Live backend matrix

A backend row and an operating-system row prove different things. An operating
system row must execute the complete everyday loop. A backend row needs one
positive measured run with the backend selected explicitly, an isolated
results directory, the backend's identity and context receipts, saved evidence
reopened by `view`, and server request logs. Honest SKIP fields remain SKIP and
do not fail the row. The exact launch command, runtime version, model digest,
device state, FITR command, result path, and cleanup must be recorded.

For llama-server, lifecycle cleanup is external because the backend cannot
unload a server-owned model. Acceptance must start a dedicated loopback server,
record `/props`, run with `--backend llama-server`, stop that exact process,
and confirm the listener closed. Do not start the run while unrelated GPU work
would contaminate timing or require killing another process. A server preloads
its model, so its probe wall time is not an unloaded-runtime measurement.

| Backend | Discovery and inventory | Positive measured run | Identity gate | Context gate | Status |
|---|---:|---:|---:|---:|---|
| Ollama | Pass on two Windows hosts | Pass | Runtime digest pass | `/api/ps.context_length` pass | Pass on native hosts |
| llama-server | Automated only | Pending native run | Observed file hash remains unrankable | Automated `/props` receipt | Pending |
| Generic OpenAI-compatible | Pass against Ollama's compatibility route, 14 of 14 models listed | Blocked, not pending: see below | Both refusal modes pass live | Unknown remains unrankable by design | Partial by design |

Against Ollama's compatibility route the generic backend listed every served
model and produced correct inventory rows. Size and architecture are absent
from `/v1/models`, so `advise` returns SKIP rather than a fit verdict. That is
the documented contract, not a defect: the protocol does not carry the facts
the verdict needs.

Both identity refusals were exercised live on host A, and they are distinct:

- with no pin, evaluation stops at the identity step with *"openai-compat
  model identity requires FITR_OPENAI_MODEL_SHA256"*;
- with a pin set against an endpoint that reports no digest, it stops with
  *"endpoint did not report a model digest to match against
  FITR_OPENAI_MODEL_SHA256"*.

Neither refusal generated a token or wrote a result file. The isolated results
directory was empty afterwards.

A positive OpenAI-compatible run is **blocked rather than pending**. The gate
requires the endpoint to report an artifact digest, and the OpenAI model
schema has no field for one, so Ollama's compatibility route cannot satisfy
it and neither can any endpoint that speaks only the standard schema. Closing
this row needs an endpoint that publishes artifact identity as an extension.
Until then the honest status is that a generic OpenAI-compatible endpoint is
usable for inventory and unrankable for evidence, which is what the backend
contract already says.

## Completion rule

0. Every command in the README's everyday loop table appears in the recorded
   path for each row, with its observed output.
1. Finish the clean-install rows on all supported operating systems.
2. Complete a positive native run for llama-server. A generic
   OpenAI-compatible endpoint cannot supply the documented receipts through
   the standard schema, so that row closes as "usable for inventory,
   unrankable for evidence" unless a conforming endpoint appears; it does not
   block 1.0, because refusing to rank what cannot be identified is the
   contract working.
3. Resolve every blocker without weakening an identity, context, contamination,
   or comparison gate.
4. Run the full release workflow on the intended release tag.
5. Verify that all six binaries appear exactly once in `SHA256SUMS`, then run
   the POSIX installer against candidate artifacts on Linux and macOS and the
   PowerShell installer against the Windows candidate before promoting 1.0.

Backend receipt requirements and failure semantics are defined in
[backends](backends.md). The product-level exit criteria remain in the
[roadmap](../ROADMAP.md).
