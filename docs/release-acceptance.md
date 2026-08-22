# Release acceptance

This document tracks the evidence required for the 1.0 release. Automated
protocol tests are necessary, but they do not replace a native binary running
against real serving runtimes on clean operating-system installs.

Last updated: 2026-08-22.

## Automated gates

| Gate | Coverage |
|---|---|
| Public command contract | Help succeeds, malformed input fails before discovery, positional counts and numeric bounds are exact, and diagnostics include a next action. Runs on Windows, macOS, and Linux CI. |
| Backend wire contract | Mock HTTP servers cover discovery, generation, chat and tool calls, timing receipts, artifact identity, effective context, authentication, redirects, bounded responses, and error redaction. |
| Evidence contract | Tests cover runtime-bound model identity, context verification, device fingerprints, immutable history, signed completion records, contamination, and comparison refusal. |
| Release binary smoke | The built Linux binary must pass global and subcommand help, then reject a malformed command with exit 2 and a useful hint. |
| Installer smoke | Windows, macOS, and Linux runners download the latest public release, bind the native asset to its exact checksum entry, and validate its version command. |
| Release quality | Formatting, vet, unit tests, race detection, cross-compilation, reproducible Linux build comparison, static ELF verification, size limits, vulnerability scanning, fuzz smoke tests, and installer syntax checks must all pass. |

## Native operating-system matrix

| Operating system | Clean install | Bare inventory | Isolated live loop | Reopen and apply | Status |
|---|---:|---:|---:|---:|---|
| Windows amd64 | Pending final clean VM | Pass | Pass with Ollama | Pass | Partial |
| macOS arm64 | Pending | Pending | Pending | Pending | Pending |
| Linux amd64 | Pending | Pending | Pending | Pending | Pending |

The Windows live loop used an isolated `FITR_RESULTS` directory, Ollama
0.32.15, and `qwen3:0.6b-q4_K_M` at a requested 2048-token context. The run:

- resolved the exact served tag to a runtime-bound SHA-256 digest;
- verified 2048 effective tokens from the runtime receipt;
- completed speed, resident-memory, and tool-plumbing checks;
- saved and reopened a schema 5 result; and
- produced a non-mutating Ollama apply recipe from the saved context.

The temporary run left no model resident. Existing user evidence was not read
or modified.

## Live backend matrix

| Backend | Discovery and inventory | Positive measured run | Identity gate | Context gate | Status |
|---|---:|---:|---:|---:|---|
| Ollama | Pass on Windows | Pass | Runtime digest pass | `/api/ps.context_length` pass | Pass on one native host |
| llama-server | Automated only | Pending native run | Observed file hash remains unrankable | Automated `/props` receipt | Pending |
| Generic OpenAI-compatible | Pass against Ollama's compatibility route | Pending a conforming endpoint | Live missing-pin refusal pass; positive receipt pending | Unknown remains unrankable by design | Partial |

The live OpenAI-compatible negative test confirmed that evaluation stops before
generation or result creation when `FITR_OPENAI_MODEL_SHA256` is absent. The
diagnostic names the independent digest pin and the matching endpoint assertion
required to continue.

## Completion rule

1. Finish the clean-install rows on all supported operating systems.
2. Complete positive native runs for llama-server and a generic
   OpenAI-compatible endpoint that can supply the documented receipts.
3. Resolve every blocker without weakening an identity, context, contamination,
   or comparison gate.
4. Run the full release workflow on a release-candidate tag.
5. Verify all six downloaded binaries and `SHA256SUMS` through both installers
   before promoting 1.0.

Backend receipt requirements and failure semantics are defined in
[backends](backends.md). The product-level exit criteria remain in the
[roadmap](../ROADMAP.md).
