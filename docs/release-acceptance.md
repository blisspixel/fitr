# Release acceptance

This document tracks the evidence required for the 1.0 release. Automated
protocol tests are necessary, but they do not replace a native binary running
against real serving runtimes on clean operating-system installs.

Last updated: 2026-09-05.

### 0.10.6 release receipt

Release 0.10.6 is bound to commit
`8622ba8d796484c87718e8968021cd9727b2c1d8`. The exact commit passed
[main CI 33957092930](https://github.com/blisspixel/fitr/actions/runs/33957092930)
with **83.44% aggregate coverage** and
[native acceptance 33957093133](https://github.com/blisspixel/fitr/actions/runs/33957093133)
before tagging. The
[release workflow 33957316679](https://github.com/blisspixel/fitr/actions/runs/33957316679)
repeated the test, coverage, race, vulnerability, fuzz, lint, reproducibility,
static binary, size and installer gates before publishing
[v0.10.6](https://github.com/blisspixel/fitr/releases/tag/v0.10.6).

An earlier candidate failed MCP tests on macOS and Windows because saved
attachment paths used platform aliases while the server pinned a physical
root. The fix resolves each attachment parent to that same root, preserves
the saved evidence, and still rejects escaping aliases and symlinked leaves.
The successful final CI covers this production fix and its regressions.

All ten public assets were downloaded independently and matched the nine-entry
checksum manifest. The public Windows amd64 binary is **13,257,216 bytes**,
reports `fitr 0.10.6`, and has SHA-256
`445de2129ca5ef89d0c5ddb656c079932a5caa9e95dc567da2e55e813643d872`.
The tagged PowerShell installer installed that asset into an isolated directory
with `FITR_NO_PATH=1` and left persistent user PATH unchanged. `update --check`
reported current. `update --reinstall` staged the verified asset and its hidden
helper completed replacement: the target write time advanced, no staged files
remained, and version plus checksum still matched the public release.

Native Linux/macOS acceptance and the downloaded public Windows binary passed
the real MCP stdio smoke: three typed request IDs, current protocol metadata,
discovery, exact tool catalog, empty-role output, clean framing and no evidence
writes. This is binary/profile acceptance, not live acceptance by a named agent
harness. A2A remains unimplemented. No paid model API calls were used.

### 0.10.5 release receipt

Release 0.10.5 is bound to commit
`2a4fc13beaa44399092aacb0d48fd6563340a9da`. The exact commit passed
[main CI](https://github.com/blisspixel/fitr/actions/runs/33953933128), including
83.10% aggregate coverage, and
[native Linux/macOS acceptance](https://github.com/blisspixel/fitr/actions/runs/33953933030)
before the
[release workflow](https://github.com/blisspixel/fitr/actions/runs/33954218252)
published [v0.10.5](https://github.com/blisspixel/fitr/releases/tag/v0.10.5).

All ten public assets were downloaded independently and the nine-entry
manifest matched every asset. The Windows amd64 binary is 12,983,808 bytes
and has SHA-256
`8ce6bc508468cc58dde75da4b400607a47c425085443ecb3b5ba253f13d73626`.
The tagged PowerShell installer verified and installed it to an isolated
directory with persistent user PATH unchanged. Public `update --check`
identified the current release; `update --reinstall` staged the verified asset
and the hidden helper completed replacement after exit. The replacement
reported `fitr 0.10.5`, matched the public hash and left no staged files.

The role and cleanup commands add about 1.25% to the Windows binary relative
to 0.10.4. CI and release retain a size ceiling of 13,140,000 bytes, roughly
1.2% above the measured candidate. Read-only storage acceptance also exercised
a local tree exceeding 100,000 files; no personal paths or inventory receipts
are published here.

## Automated gates

| Gate | Coverage |
|---|---|
| Public command contract | Help succeeds, malformed input fails before discovery, positional counts and numeric bounds are exact, and diagnostics include a next action. Runs on Windows, macOS, and Linux CI. |
| Backend wire contract | Mock HTTP servers cover discovery, generation, chat and tool calls, timing receipts, artifact identity, effective context, authentication, redirects, bounded responses, and error redaction. |
| Evidence contract | Tests cover runtime-bound model identity, context verification, device fingerprints, immutable history, signed completion records, contamination, and comparison refusal. |
| Native candidate acceptance | A manual clean-run workflow installs the candidate artifact on ephemeral Linux and macOS runners, starts a pinned llama-server with two independently hashed GGUFs, exercises the everyday loop, checks evidence refusals, verifies cleanup, and uploads the complete transcript. |
| Release binary smoke | The built Linux binary must pass global and subcommand help, then reject a malformed command with exit 2 and a useful hint. |
| Installer smoke | Windows, macOS, and Linux runners install locally served candidate artifacts, bind the native asset to its exact checksum entry, and validate its version command. |
| Updater | Unit and command-contract tests cover the six platform assets, canonical stable tags, duplicate or missing assets and checksums, bounded downloads and version output, hash mismatch cleanup, staged version identity, pre-replacement digest guards, JSON disclosure, and unsupported targets. A Windows subprocess test proves deferred replacement after the updating process exits. Public replacement is verified and recorded after release publication. |
| Release quality | Formatting, vet, unit tests, race detection, cross-compilation, reproducible Linux build comparison, static ELF verification, size limits, vulnerability scanning, fuzz smoke tests, and installer syntax checks must all pass. |

### 0.9.10 release receipt

Release 0.9.10 is bound to commit
`4c69505d4aad7a29bfbebb55cdd1d732e03248e8`. The exact commit passed
[aggregate CI run 33335116411](https://github.com/blisspixel/fitr/actions/runs/33335116411)
and
[native acceptance run 33335118599](https://github.com/blisspixel/fitr/actions/runs/33335118599)
before the tag was published. The
[release workflow run 33335321170](https://github.com/blisspixel/fitr/actions/runs/33335321170)
then published the public
[v0.9.10 release](https://github.com/blisspixel/fitr/releases/tag/v0.9.10).

All ten public assets were downloaded independently after publication. The
manifest contained exactly nine file entries, each downloaded asset matched
its SHA-256 entry, and the public Windows amd64 binary reported
`fitr 0.9.10`. The tagged PowerShell installer was then run against GitHub
with `FITR_VERSION=v0.9.10`, an isolated `FITR_BIN`, and `FITR_NO_PATH=1`.
It verified the public binary checksum, installed to the requested directory,
left the persistent user `PATH` byte-for-byte unchanged, made the isolated
directory first on the current process `PATH`, and installed a binary that
reported `fitr 0.9.10`.

### 0.9.11 release receipt and public updater finding

Release 0.9.11 is bound to commit
`e364299a5177d08992a90a9152ca2725728e03d5`. The exact commit passed
[aggregate CI run 33341787024](https://github.com/blisspixel/fitr/actions/runs/33341787024)
and
[native acceptance run 33341932166](https://github.com/blisspixel/fitr/actions/runs/33341932166)
before
[release workflow run 33342178090](https://github.com/blisspixel/fitr/actions/runs/33342178090)
published the public
[v0.9.11 release](https://github.com/blisspixel/fitr/releases/tag/v0.9.11).

The public PowerShell installer then installed the Windows amd64 asset into an
isolated directory, kept persistent `PATH` unchanged, and matched SHA-256
`de3e3477d4625d7ff3937126c8c744c5d33d009e1d34abeff753481360637684`.
`update --check` succeeded and `update --reinstall` downloaded, checked, and
staged that exact asset. The detached helper did not complete replacement:
Windows PowerShell did not auto-load the module providing `Get-FileHash` in
that hidden no-profile process. The candidate remained staged and the target
remained unchanged. This public-path finding is fixed in 0.9.12 with a
module-independent SHA-256 implementation and a subprocess replacement test.

### 0.9.12 release receipt

Release 0.9.12 is bound to commit
`ed7add5e0da44d99973f136c63cecc420fd640a5`. The exact commit passed
[aggregate CI run 33342996701](https://github.com/blisspixel/fitr/actions/runs/33342996701)
and
[native acceptance run 33343012240](https://github.com/blisspixel/fitr/actions/runs/33343012240)
before
[release workflow run 33343160364](https://github.com/blisspixel/fitr/actions/runs/33343160364)
published the public
[v0.9.12 release](https://github.com/blisspixel/fitr/releases/tag/v0.9.12).

The public PowerShell installer installed the Windows amd64 asset into a fresh
isolated directory with `FITR_NO_PATH=1`. The installed binary reported
`fitr 0.9.12` and matched the release manifest at SHA-256
`25dc60d226ee392b6531125441b0bb83dcf2261f1ae72dae260d3ef23ede41c0`.
`update --check` identified 0.9.12 as current. `update --reinstall` downloaded
and verified that same public asset, staged it beside the running executable,
and launched the hidden helper. After the updating process exited, the helper
removed the staged file and replaced the target. The replaced binary again
reported `fitr 0.9.12` and matched the same published SHA-256.

### 0.10.0 release receipt

Release 0.10.0 is bound to commit
`7bcf2a6991608e40111e0b4eae3604a9492f7f6f`. The exact commit passed
[aggregate CI run 33570791085](https://github.com/blisspixel/fitr/actions/runs/33570791085)
and
[native acceptance run 33570801126](https://github.com/blisspixel/fitr/actions/runs/33570801126)
before
[release workflow run 33571114233](https://github.com/blisspixel/fitr/actions/runs/33571114233)
published the public
[v0.10.0 release](https://github.com/blisspixel/fitr/releases/tag/v0.10.0).

The release workflow re-ran the full test, coverage, race, vulnerability,
fuzz, lint, file-length, reproducibility, static-binary, size, CLI, checksum,
and three-platform installer gates. It then downloaded all ten release assets,
verified the nine-entry manifest, and published only after every asset matched.

After publication, GitHub's latest stable release metadata reported 0.10.0 as
non-draft and non-prerelease with exactly ten assets. The public manifest was
downloaded independently. The public Windows amd64 binary was also downloaded
independently, matched SHA-256
`4031c03e0d899146c17314ed219bfc89fe50c8cb88e6fd6e360b77d20a6b4304`,
reported `fitr 0.10.0`, and identified 0.10.0 as current through
`update --check`.

### 0.10.1 release receipt

Release 0.10.1 is bound to commit
`6ee1fef77e7c5f7e780b2d72189b242ad1d23607`. The exact commit passed
[aggregate CI run 33574512752](https://github.com/blisspixel/fitr/actions/runs/33574512752)
and
[native acceptance run 33574737854](https://github.com/blisspixel/fitr/actions/runs/33574737854)
before
[release workflow run 33575013788](https://github.com/blisspixel/fitr/actions/runs/33575013788)
published the public
[v0.10.1 release](https://github.com/blisspixel/fitr/releases/tag/v0.10.1).

The release workflow re-ran the full test, coverage, race, vulnerability,
fuzz, lint, file-length, reproducibility, static-binary, size, CLI, checksum,
and three-platform installer gates. It verified every release asset before
publishing. GitHub then reported the stable release as non-draft and
non-prerelease with exactly ten public assets.

The public manifest and Windows amd64 binary were downloaded independently
after publication. The executable matched SHA-256
`8fb3c2eb408894f75d6f5cd1ad46f1b7e264c8c4522a1215dc3323a555c7b632`,
reported `fitr 0.10.1`, and identified 0.10.1 as current through
`update --check`.

### 0.10.2 release receipt

Release 0.10.2 is bound to commit
`beba22cb50ccb4db0ce578e5a3e248430b2e8b91`. The exact commit passed
[aggregate CI run 33583896986](https://github.com/blisspixel/fitr/actions/runs/33583896986)
and
[native acceptance run 33583901290](https://github.com/blisspixel/fitr/actions/runs/33583901290)
before
[release workflow run 33584236132](https://github.com/blisspixel/fitr/actions/runs/33584236132)
published the public
[v0.10.2 release](https://github.com/blisspixel/fitr/releases/tag/v0.10.2).

The release workflow re-ran the full test, coverage, race, vulnerability,
fuzz, lint, file-length, reproducibility, static-binary, size, CLI, checksum,
and three-platform installer gates. It downloaded and verified the published
asset set before exposing the release. GitHub then reported the stable release
as non-draft and non-prerelease with exactly ten public assets.

The public manifest and Windows amd64 binary were downloaded independently
after publication. The executable matched SHA-256
`3f3ec62e60fdd6e01e926523a50e57eb2cf1739fa4d7d5934fa7b8f78ff56cea`,
reported `fitr 0.10.2`, and identified 0.10.2 as current through
`update --check`. The checksum-verifying public installer then replaced the
existing Windows PATH installation; the installed executable matched the same
public checksum and version.

### 0.10.3 release receipt

Release 0.10.3 is bound to commit
`4724971d07a94b6a36622f08d6ed9760fb022c15`. The exact commit passed
[aggregate CI run 33943207005](https://github.com/blisspixel/fitr/actions/runs/33943207005)
and
[native acceptance run 33943209271](https://github.com/blisspixel/fitr/actions/runs/33943209271)
before
[release workflow run 33943415531](https://github.com/blisspixel/fitr/actions/runs/33943415531)
published the public
[v0.10.3 release](https://github.com/blisspixel/fitr/releases/tag/v0.10.3).

The release workflow re-ran the full test, coverage, race, vulnerability,
fuzz, lint, file-length, reproducibility, static-binary, size, CLI, checksum,
and three-platform installer gates. It downloaded and verified the published
asset set before exposing the release. GitHub then reported the stable release
as non-draft and non-prerelease with exactly ten public assets.

The public manifest and Windows amd64 binary were downloaded independently
after publication. The executable matched SHA-256
`b6644a8d9195e8233251d7045304200f64f30bbb127877e86cecee303b95095e`,
reported `fitr 0.10.3`, and identified 0.10.3 as current through
`update --check`. The checksum-verifying public installer then replaced the
existing Windows PATH installation; the installed executable matched the same
public checksum and version.

### 0.10.4 release receipt

Release 0.10.4 is bound to commit
`bee470363b3036dc4b9eb50218cdc80d042e884b`. The exact commit passed
[aggregate CI run 33951141026](https://github.com/blisspixel/fitr/actions/runs/33951141026)
with 82.46% merged coverage and
[native acceptance run 33951140512](https://github.com/blisspixel/fitr/actions/runs/33951140512)
on clean Linux and macOS runners. The
[release workflow run 33951311551](https://github.com/blisspixel/fitr/actions/runs/33951311551)
then published [v0.10.4](https://github.com/blisspixel/fitr/releases/tag/v0.10.4).
Local Windows merged coverage was 81.07%. Lint, reachable vulnerability checks,
race detection, fuzz smoke tests, six bounded-size builds, screenshot drift
and three-platform installer checks passed.

Publication exposed a draft-resume bug: the release-by-tag REST lookup
returned 404 for an existing draft. The empty draft was removed and only the
failed publication job was rerun against the already verified candidate.
The workflow now reads `isDraft` through `gh release view`, preserving its
guard against overwriting a published release.

All ten public assets were downloaded independently. The manifest contained
exactly nine unique expected entries, and every file matched its SHA-256.
The Windows amd64 binary reported `fitr 0.10.4` and matched SHA-256
`1b5c063e246886fa182a7d46c30f8d7d376f62ccbed9949dd5b9b74ddd4b3f0f`.
The tagged public PowerShell installer installed into an isolated directory
with `FITR_NO_PATH=1`, kept persistent user PATH unchanged, and verified the
same version and checksum. `update --check` reported current.
`update --reinstall` staged the public asset, and its hidden helper completed
replacement after exit: the staged file disappeared, the target write time
advanced, and the replaced executable retained the public version and checksum.

The scoped workload security review found model-controlled policy keys and
unknown tool names retained as raw text in otherwise hash-only receipts.
Regression tests now verify fixed verifier diagnostics, hashed unknown tool
identities and denied empty tool calls. Review also fixed contradictory
terminal outcomes, per-turn call limits and aggregate timing overflow.
This is a scoped review, not a claim of exhaustive repository security.

The portable AgentPlugins 1.0.0 manifest passed the official versioned schema,
and its skill passed local validation. MCP, A2A and harness documentation
records researched contracts; no live adapter conformance is claimed.
No paid model API calls were used for this release.

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
record, binary and model hashes, `/props`, launch and request logs, every fitr
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
| macOS arm64 | Pass, checksum-verified candidate | Pass | Pass with supplied 8 GB planning budget | Pass with llama-server; ranking refusal expected | Pass | Pass on clean ephemeral runner |
| Linux amd64 | Pass, checksum-verified candidate | Pass | Pass with supplied 8 GB planning budget | Pass with llama-server; ranking refusal expected | Pass | Pass on clean ephemeral runner |

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

#### 0.9.11 full-run validation

On 2026-08-30, a development 0.9.11 binary completed the full default battery
against `qwen3-coder:30b` Q4_K_M with Ollama 0.24.0 at a verified 8192-token
request context. Generated-code execution remained disabled, so coding and
long-horizon executable tasks correctly stayed SKIP rather than becoming
unverified PASS or FAIL evidence.

Three speed observations produced 119.64 tok/s decode with SD 1.74,
4801.81 tok/s prefill with SD 70.46, and 0.17 s request TTFT with SD 0.01. The
development binary did not retain a gated-request load receipt, so this value
does not establish loaded TTFT. The release candidate closes that gap. The
separate runtime-unloaded observation was 6.39 s and runtime-reported load was
6.30 s, each with `n=1`. The verified requested-32K load probe reported
20.49 GB resident and 20.49 GB runtime-classified accelerator allocation. The
0.00 GB non-accelerator value remained labeled as a derived remainder.

The behavior battery passed 60 of 66 generated checks. Tool-channel plumbing
and all 9 ordinary tool calls passed, while the independent tool-withdrawal
scenario caught repeated calls and failure to terminate after a tool vanished.
That produced a real tool-restraint FAIL despite high raw generation speed.
The result saved, reopened, and entered Inventory as measured. This run also
found and fixed an integration bug where `GPU 100%` and
`CUDA / NVIDIA GeForce RTX 4090` described equivalent full-accelerator
placement but caused fresh evidence to appear stale. A regression now preserves
the distinction between equivalent full-accelerator labels and a genuinely
partial `GPU 50%` placement.

The README run image is a deterministic reconstruction from selected values
above. It is not the original sealed native record and does not claim that
lineage.

After the residency contract was finalized, the release candidate completed a
second native quick run with three speed observations. The runtime status probe
reported the model resident immediately before every gated request, so 0.16 s
was correctly promoted to loaded TTFT. Decode was 136.55 tok/s with SD 1.34,
prefill was 5261.84 tok/s with SD 17.63, runtime-unloaded TTFT was 6.13 s, and
runtime load was 6.05 s. Cache state remained unknown and therefore did not
establish the loaded-uncached behavioral gate. This is the intended separation
between residency proof and cache proof.

### Windows amd64, host B (0.9.6, superseded)

The earlier row used Ollama 0.32.15 and `qwen3:0.6b-q4_K_M` at a requested
2048-token context, and recorded a passing live loop. The recorded path never
invoked `advise`, which was returning SKIP for every Ollama model at the time.
The run did resolve the served tag to a runtime-bound digest, verify 2048
effective tokens, complete speed, resident-memory and tool-plumbing checks,
save and reopen a schema 5 result, and produce a non-mutating apply recipe. It
left no model resident and did not read or modify existing user evidence.

### macOS arm64, clean candidate

Environment: macOS 26.5.2 arm64, Darwin 25.5.0, Bash 3.2.57, Apple M1
(Virtual) with 3 logical CPUs, and 7.0 GB system RAM. `fitr device` identified
the Apple processor as the GPU fallback rather than printing the `arm64`
architecture string, and kept the unavailable driver explicitly `unknown`.

The [native acceptance run 33331095501](https://github.com/blisspixel/fitr/actions/runs/33331095501)
tested commit `4bea4986f13fc1dd3e08f96a0ff23231268b7ebe` with fitr 0.9.9 and
llama-server b10700 (`bebc9350e`). The checksum-verifying installer installed
the candidate built by the workflow. Two independently hashed SmolLM2 GGUFs
then completed the recorded loop at an exact 8192-token server context.

Advise used an explicit 8 GB operator planning budget and labeled it as
`--vram-gb`; the live run did not turn that input into measured resident
memory. Both schema-6 result files passed structural validation, including
the native protocol, exact effective context, first-output receipt, uncached
decode and prefill receipts, and positive cache reuse on replay. Board and
Compare refused ranking because llama-server could not bind the readable file
hash to the served runtime artifact. That refusal is the expected identity
contract. The dedicated server process and listener were gone at completion.

### Linux amd64, clean candidate

Environment: Ubuntu 24.04.4 LTS amd64, Linux 6.17.0-1022-azure, Bash 5.2.21,
AMD EPYC 7763 with 4 logical CPUs, 15.6 GB system RAM, and no measured GPU or
driver. The same [native acceptance run 33331095501](https://github.com/blisspixel/fitr/actions/runs/33331095501)
installed and exercised the exact candidate, llama-server build, context, and
two model hashes used by the macOS row.

Inventory, advice, both measured runs, apply, Doctor, Diag, Device, View,
Export, and `top --snapshot` produced their recorded artifacts. The schema-6
and cache validation, expected Board and Compare refusals, and exact listener
cleanup all passed. The 8 GB advice budget was operator supplied; live resident
memory remained SKIP because the runtime supplied no matching allocation
receipt.

## Live backend matrix

A backend row and an operating-system row prove different things. An operating
system row must execute the complete everyday loop. A backend row needs one
positive measured run with the backend selected explicitly, an isolated
results directory, the backend's identity and context receipts, saved evidence
reopened by `view`, and server request logs. Honest SKIP fields remain SKIP and
do not fail the row. The exact launch command, runtime version, model digest,
device state, fitr command, result path, and cleanup must be recorded.

For llama-server, lifecycle cleanup is external because the backend cannot
unload a server-owned model. Acceptance must start a dedicated loopback server,
record `/props`, run with `--backend llama-server`, stop that exact process,
and confirm the listener closed. Do not start the run while unrelated GPU work
would contaminate timing or require killing another process. A server preloads
its model, so its probe wall time is not an unloaded-runtime measurement.

| Backend | Discovery and inventory | Positive measured run | Identity gate | Context gate | Status |
|---|---:|---:|---:|---:|---|
| Ollama | Pass on two Windows hosts | Pass | Runtime digest pass | `/api/ps.context_length` pass | Pass on native hosts |
| llama-server | Pass on clean Linux amd64 and macOS arm64 | Pass on both native runners | Observed file hash remains unrankable; Board and Compare refuse | Exact `/props` receipt at 8192 | Pass for measurement; ranking unavailable by design |
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
2. Complete a positive native run for llama-server. Completed on clean Linux
   amd64 and macOS arm64 in run 33331095501. A generic
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

The release workflow requires successful aggregate CI and native acceptance
for the exact tagged commit. It also uses the same checksum-manifest target as
aggregate CI and native acceptance, so candidate and release asset sets cannot
silently diverge.

Backend receipt requirements and failure semantics are defined in
[backends](backends.md). The product-level exit criteria remain in the
[roadmap](../ROADMAP.md).
