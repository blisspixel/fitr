# Agent ecosystem compatibility

Research checked September 5, 2026. The MCP profile was introduced in v0.10.6.
Package discovery, wire-protocol behavior and independently
verified model-plus-harness work are separate acceptance boundaries.

## Implemented profile

| Surface | Version | Current fitr implementation |
|---|---|---|
| MCP | 2026-07-28 | Read-only evidence server over stateless stdio; three bounded tools |
| Agent Plugins | 1.0.0 | Portable skill plus root MCP configuration |
| A2A | 1.0.0 specification, `1.0` wire version | Researched future evaluation adapter; no endpoint or adapter implemented |

The [MCP July release](https://blog.modelcontextprotocol.io/posts/2026-07-28/)
replaces the initialization/session lifecycle with self-contained requests and
moves Tasks to an extension. fitr implements a specific stdio profile, not every
feature of that revision. Literal protocol fixtures and sealed local evidence
tests cover its behavior. **No named harness has passed complete live fitr
acceptance yet.** A pinned Hermes binary exchange below records a client trust
gate blocker after successful discovery.

## Load the portable package

[`plugins/fitr`](../plugins/fitr) contains `plugin.json`, the
`skills/fitr-evidence/SKILL.md` skill and this root `mcp.json` configuration:

```json
{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
  "mcpServers": {
    "fitr-evidence": {
      "type": "stdio",
      "command": "fitr",
      "args": ["mcp", "serve"]
    }
  }
}
```

Use the host's documented local-package loading mechanism. An installed fitr
executable must be resolvable by the host. Configure the child process's local
`FITR_RESULTS` directory in the host environment before loading the MCP
configuration. The package does not install fitr or supply credentials, hooks,
an executable or an A2A endpoint. The host can launch the configured subprocess;
reading the skill alone does not launch it.

[Agent Plugins 1.0.0](https://agent-plugins.org/specification) standardizes
skills and MCP configuration at fixed package locations. Its
[MCP schema](https://agent-plugins.org/schemas/1.0.0/mcp.schema.json) separates
the executable token from arguments. Package validity does not prove that a
host speaks the requested MCP revision, and package containment does not
sandbox the subprocess.

## Read-only MCP tools

The executable entry point is:

```bash
fitr mcp serve
```

It reads newline-delimited JSON-RPC from stdin and writes protocol messages
only to stdout. `server/discover`, `tools/list`, `tools/call` and client
`notifications/cancelled` are implemented. Every request carries
`io.modelcontextprotocol/protocolVersion` set to `2026-07-28` and
`io.modelcontextprotocol/clientCapabilities` in `params._meta`. Optional client
identity grants no authority. There is no `initialize` handshake. This follows
the current [stdio binding](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/stdio).

| Tool | Arguments | Returned evidence |
|---|---|---|
| `fitr_roles_list` | `{}` | Role names, revision digests and attachment counts |
| `fitr_role_review` | `{"role":"coding"}` | Rechecked battery-screening state, evidence digests, candidate states, reason/gap counts, preference bounds and comparison readiness |
| `fitr_role_status` | `{"role":"coding"}` | Rechecked current selection, including auto-managed evidence: current revision, lifecycle digest, evaluation time, unselected/qualified/stale state and optional selected receipt, original revision, evidence digest and expiry |

Review and status invoke existing local role checks and always return
`adoption_authorized: false`. It does not expose confirmation, adoption,
rollback, downloads, model execution, arbitrary paths or endpoint selection.
Model names, source paths, run IDs, descriptions and raw diagnostics are
omitted. Role names and digests are shared, so role names must not contain
secrets. The host selects the evidence root; tool input cannot change it.

Review reports manually attached candidates. Status checks the selected
incumbent and its complete confirmation set without reading obsolete former
stores. A stale incumbent's digests remain visible with `state: "stale"`;
neither that identity nor a successful MCP call authorizes adoption or execution.

Results use `resultType: "complete"`. Unknown methods/tools are protocol
errors; invalid tool arguments or unavailable evidence return `isError: true`
with a fixed diagnostic. The fixed input/output schemas follow the
[tools contract](https://modelcontextprotocol.io/specification/2026-07-28/server/tools).
Annotations describe behavior but do not authorize an action.

The profile bounds input lines to 64 KiB, concurrency to four tool calls and
rate to 60 calls per minute. One evidence review runs at a time. Cancellation
suppresses the request's response; an already executing verification may finish
internally before observing cancellation. Evidence access has stricter size
limits than the CLI. See the [implementation profile](../internal/mcp/README.md)
for storage limits, shutdown behavior and the local filesystem race boundary.

Unsupported surfaces are HTTP/SSE, OAuth, subscriptions, resources, prompts,
sampling, elicitation, progress, logging, input-required continuations, JSON-RPC
batches, the Tasks extension and legacy protocol fallback. Supplying client
capabilities does not enable them. A legacy-only host can therefore fail to
connect even when it recognizes the plugin package.

## Official SDK acceptance

[`scripts/mcp_sdk_acceptance.py`](../scripts/mcp_sdk_acceptance.py) runs a built
fitr executable through the official Python SDK **`mcp==2.0.0`**. Its
[test-only lock](../scripts/mcp-sdk-requirements.txt) pins every dependency and
accepted wheel hash for CPython 3.14. These packages do not become Go runtime or
plugin dependencies. This lock targets Linux x64, macOS arm64 and Windows x64,
matching the CI matrix. It does not support Intel macOS because the pinned
[cryptography release](https://pypi.org/pypi/cryptography/50.0.1/json) has no
matching wheel. A local Windows run with the published fitr 0.10.10 binary
passed both explicit `2026-07-28` and ordinary default client modes against
empty and synthetic current-schema evidence stores. The
[0.10.10 release receipt](release-acceptance.md#01010-release-receipt)
also records official SDK acceptance for the Linux x64, macOS arm64 and Windows
x64 CI binaries. Each receipt binds its own binary and inputs; these are not
named-host acceptance results.

The [0.10.12 release receipt](release-acceptance.md#01012-release-receipt)
records the expanded eight-case suite on all three CI platforms and the public
Windows binary, plus a read-only check of the retained real auto incumbent
through both client modes. The selected receipt and lifecycle remained
unchanged. This checks existing evidence compatibility without repeating model
inference or changing its original battery-screening scope.

The script uses the released SDK's `Client(stdio_client(...))` transport
contract. In explicit mode, `session.discover()` can return synthetic cached
discovery. The smoke instead calls `send_discover`, validates the real reply
and adopts it. The default client also performs its own initial discovery;
the transcript must contain both actual probes and no legacy `initialize`.
These details follow the pinned
[client implementation](https://github.com/modelcontextprotocol/python-sdk/blob/v2.0.0/src/mcp/client/client.py)
and [session implementation](https://github.com/modelcontextprotocol/python-sdk/blob/v2.0.0/src/mcp/client/session.py).

Each case validates the catalog, closed schemas, all three tools, exact response
IDs and complete results, an invalid role and an unknown tool. The synthetic
fixture passes through the canonical record store and real role CLI. Its
integrity-sealed evidence exercises qualification and preference bounds; it is
fabricated test data and establishes no model quality. MCP summaries must
match the local CLI, including optional exploration leads and clamped
preference intervals. Checks reject exposed paths, model/run identities,
descriptions and requirement identifiers, including escaped JSON text.

The suite now has eight cases: empty, manually attached, managed qualified and
managed stale evidence under both explicit and default client modes. The
[managed fixture helper](../scripts/mcp-selection-fixture.go) creates and closes
real stores, finishes fresh confirmation and adopts through public lifecycle
APIs using synthetic records. Its stale case changes the role revision after
adoption. Status must match CLI state, lifecycle, selected receipt, original
revision, evidence and expiry while keeping private aliases and store identity
out of the wire projection. Its helper hash is bound before and after the run.

After setup, each case snapshots the temporary home/config/results tree and
requires its file contents and directory entries to remain unchanged. This
does not observe transient writes or all filesystem metadata. The SDK receives a minimal child environment
with those locations redirected and no configured model backend. Python socket
connections are denied during the smoke; this is not an OS network sandbox
for subprocesses. No model requests are made. Temporary directory permissions
follow the host OS, including inherited Windows ACLs.

Every SDK phase has a 20-second deadline and its cleanup must finish within
five seconds. This establishes bounded SDK cleanup, not observed graceful
child exit: the official
[stdio transport](https://github.com/modelcontextprotocol/python-sdk/blob/v2.0.0/src/mcp/client/stdio.py)
can terminate an unresponsive child after its own two-second wait. The
separate [native raw-wire smoke](../scripts/mcp-acceptance.py) checks the child's
exit status. SDK acceptance does not test cancellation races or every protocol
feature; existing Go protocol fixtures cover those narrower server contracts.

Run from the repository using Go and CPython 3.14.7. On Linux x64/macOS arm64:

```bash
python3.14 -m venv .tmp/mcp-sdk-venv
.tmp/mcp-sdk-venv/bin/python -I -m pip install --require-hashes -r scripts/mcp-sdk-requirements.txt
.tmp/mcp-sdk-venv/bin/python -I -m pip check
go build -o .tmp/fitr-sdk ./cmd/fitr
.tmp/mcp-sdk-venv/bin/python -I -B -m unittest discover -s scripts -p test_mcp_sdk_acceptance.py
.tmp/mcp-sdk-venv/bin/python -I scripts/mcp_sdk_acceptance.py .tmp/fitr-sdk --out .tmp/mcp-sdk-receipt.json
```

On Windows, use `python -m venv .tmp/mcp-sdk-venv`, the interpreter
`.tmp/mcp-sdk-venv/Scripts/python.exe`, and binary `.tmp/fitr-sdk.exe` with the
same arguments. Keep `-I`; the test requires an isolated venv interpreter.
Use a new receipt path on each run. Dependency installation needs PyPI; the
acceptance cases use local stdio only. Update the lock deliberately from
official [PyPI release metadata](https://pypi.org/pypi/mcp/2.0.0/json), retain
exact versions and matching wheel hashes, and rerun installation with
`--require-hashes`, `pip check` and all platform acceptance jobs.

The receipt records binary, script, fixture, helper, lock and plugin digests;
installed dependency versions; OS/Python identity; per-case evidence and
transcript digests; and measured SDK cleanup. Input file identities are checked
before and after all cases so a concurrent rebuild cannot silently relabel a
pass. Raw transcripts and temporary private fixture paths are not published.

## Named-host compatibility

These assessments were checked September 5, 2026. Hermes has a recorded real
connector exchange; the other rows remain source-based. Official SDK acceptance
does not establish named-host compatibility.

| Host and inspected release | Current boundary with fitr |
|---|---|
| Hermes Agent v2026.8.31 / 0.21.0 | **Discovery accepted; tool calls blocked by the pinned client.** Explicit stateless mode discovers the public fitr 0.10.11 binary and its two-tool catalog. The [connector trust reader](https://github.com/NousResearch/hermes-agent/blob/29112bef099274229cadff79cdff7bf7b99c4b77/tools/mcp_tool.py#L4627-L4716) checks camelCase `readOnlyHint`, while its locked SDK exposes `read_only_hint`. The unchanged untrusted gate rejects dispatch before any `tools/call`. |
| Pi v0.85.1 | **Extension required, unaccepted.** The [released agent](https://github.com/earendil-works/pi/blob/d981de1229ef899957bbe968bc8dcda02a21f477/packages/coding-agent/README.md) leaves MCP to extensions. Pin and test a specific adapter; the portable package does not establish built-in support. |
| Official DeepSeek Harness `dsh-v0.1.3-alpha.1` | **Legacy protocol mismatch, unaccepted.** This is a [developer-preview harness](https://github.com/deepseek-ai/deepseek-harness/releases/tag/dsh-v0.1.3-alpha.1), distinct from DeepSeek models. Its [lock](https://github.com/deepseek-ai/deepseek-harness/blob/d347e703908d0406b7a7ef80e3a0e594d86b2215/pnpm-lock.yaml) selects SDK 1.29.0 and its [connector](https://github.com/deepseek-ai/deepseek-harness/blob/d347e703908d0406b7a7ef80e3a0e594d86b2215/packages/mcp/mcp-client/src/connection.ts) uses the legacy client handshake. |
| OpenClaw v2026.9.1 | **Legacy protocol mismatch, unaccepted.** Its [release manifest](https://github.com/openclaw/openclaw/blob/v2026.9.1/package.json) pins SDK 1.30.0, whose [protocol declarations](https://github.com/modelcontextprotocol/typescript-sdk/blob/2d889f2b329e46680ec9bdd565de4616c497825a/src/types.ts) stop at `2025-11-25`. A catalog probe cannot make that client speak fitr's stateless revision. |
| NVIDIA NemoClaw v0.0.120 | **Managed transport mismatch, unaccepted.** Its [managed MCP contract](https://github.com/NVIDIA/NemoClaw/blob/2444537f5a77c7b2789de4d59430e228328b8279/docs/deployment/set-up-mcp-bridge.mdx) accepts authenticated Streamable HTTP and explicitly excludes host stdio bridges. fitr currently exposes stdio only. |

### Recorded Hermes boundary

The isolated Windows acceptance used Hermes commit
`29112bef099274229cadff79cdff7bf7b99c4b77`, Python 3.13.15, its unchanged source
and lock, 80 hash-verified locked wheels, and the public fitr 0.10.11 executable
with SHA-256 `c59fb2e73010021fa38ee33159bab95343d666470722ef4146c71b9bafc64540`.
Actual wire discovery and catalog inspection succeeded. The wire annotation was
`readOnlyHint: true`; the locked `mcp-types==2.0.0` object exposed
`read_only_hint=True`. The connector registered the tools but classified them
as write-capable, so its normal trust gate blocked dispatch. No trust override
or upstream patch was used. The definitive stateless receipt digest is
`8c7a11bca1cae779e2d748f8e099d94b8b1b0b69b3db03811724bec198dc4ae6`.

The legacy negative case recorded three actual `initialize` attempts with
`2025-11-25`, each rejected with `-32602` for missing stateless metadata. All
three children exited cleanly. The successful stateless child also exited
cleanly; evidence contents stayed unchanged. Dependent sealed-evidence, idle
and invalid-argument positive cases remain unaccepted because dispatch was
blocked. No model requests were made. This is a bounded client compatibility
finding, not complete Hermes acceptance or an OS network-confinement claim.

The proposed [Fit and Extended fit scopes](personal-fitting.md#fit-and-extended-fit)
separate connector acceptance from model-plus-harness task evaluation. A Pi
SDK workflow with externally verified state, actual compaction and exact
checkpoint restart is being prototyped with fake model responses first.
The pinned Pi 0.85.1 prototype passed nine separate process cases covering
normal tool state, built-in compaction, exact reopening, four checkpoint-tamper
rejections, cancellation and provider errors. It uses restricted resources and
tools with a fake provider; this proves harness wiring, not model retention or
task quality. A native fitr adapter must still account for all requests: a
split compaction can make two summary calls, ordinary turns may omit an output
cap, and a canceled callback can arrive already aborted before admission.

The legacy SDK [initialization path](https://github.com/modelcontextprotocol/typescript-sdk/blob/v1.29.0/src/client/index.ts)
is incompatible with fitr's modern-only server. Hermes now has the negative
binary exchange above; other host paths still need their own acceptance. A
future legacy profile or transport adapter must be explicit and separately
tested.

## Ordered integration roadmap

1. **Resolve the Hermes trust-reader boundary, then finish acceptance.** Keep
   the failed pinned path as a regression. A separately pinned corrected client
   must accept read-only annotations without relaxing trust and pass an explicit
   three-tool allowlist. Exercise canonical and managed evidence, invalid
   arguments, idle keepalive, cancellation and bounded shutdown without an LLM.
2. **Prototype one Pi Extended fit workflow.** Use the pinned SDK with
   explicit resources, restricted tools and a native fitr model adapter.
   First prove normal turns, built-in compaction and exact session reopening
   with deterministic fake responses. Then evaluate a constrained workspace
   task through the owned local runtime, with an independent verifier and one
   budget across turns, summaries and restart. This is a model-plus-adapter
   workflow, separate from Pi's default provider or MCP-extension support.
3. **Explain the remaining host gaps.** Record OpenClaw and DeepSeek protocol
   failures honestly. Consider an explicit CLI integration while awaiting a
   compatible client, with a fixed redacted projection or explicit disclosure
   scope: ordinary CLI JSON is richer than MCP output. For NemoClaw, first
   consider an inert import of its versioned
   [host readiness report](https://github.com/NVIDIA/NemoClaw/blob/2444537f5a77c7b2789de4d59430e228328b8279/docs/reference/system-readiness.mdx).
   Preserve producer identity and inconclusive findings; imported readiness
   cannot establish local fit or justify starting a sandbox.
   A Pi MCP extension can separately expose the bounded read-only tools when
   that connector is needed; it requires its own binary acceptance.
4. **Evaluate one bounded model-plus-harness workflow.** Seal the harness
   build, effective provider configuration, tool surface, task/verifier,
   context and runtime/artifact identity. Count auxiliary requests and every
   retry, verify final effects independently, and test interruption and
   compaction. A tool connection, successful exit or model claim cannot earn
   workflow quality. Require fresh role confirmation before selection.
5. **Add deployment or A2A only for a demonstrated need.** HTTP service
   exposure, credentials, shared-route changes and abort semantics introduce
   separate authority boundaries. Keep them outside the read-only profile.

Each accepted host row must record host/dependency identities, fitr
binary/plugin/configuration digests, requested protocol and transcript digest.
Require exact agreement with local CLI evidence, redacted output and unchanged
fixture contents. Do not count synthetic cached discovery as a wire exchange.
SDK and adapter dependencies stay outside fitr's standalone runtime.

Future configuration imports must remain inert. Pi's pinned
[model configuration](https://github.com/earendil-works/pi/blob/d981de1229ef899957bbe968bc8dcda02a21f477/packages/coding-agent/docs/models.md)
allows executable credential commands. Hermes's
[model configuration](https://github.com/NousResearch/hermes-agent/blob/29112bef099274229cadff79cdff7bf7b99c4b77/website/docs/user-guide/configuring-models.md)
includes auxiliary and fallback routes. Neither executing a resolver nor
recording only the main model establishes a read-only, reproducible import.

## Next bounded A2A adapter

A2A remains future work. Propose one deterministic loopback policy-repair
fixture over JSON-RPC, pinned to the
[A2A 1.0.0 specification](https://a2a-protocol.org/v1.0.0/specification/).
Seal the Agent Card digest, interface URL, `protocolBinding`, wire version
`protocolVersion: "1.0"`, task fixture and local verifier before submission.

Support `SendMessage`, bounded `GetTask` polling and `CancelTask` with observed
readback. Bind returned task/context IDs. Distinguish completed, failed,
canceled and rejected terminal states from input-required/auth-required
interruptions. Treat a message-only response as unsupported in this first
task-based profile. Do not automatically retry submission; cap polling and
record cancellation races explicitly.

Accept an artifact only after local verification, including a test where remote
completion returns an invalid artifact. Use harness-owned monotonic time for
end-to-end latency. Keep remote status, verified outcome, observed authority
and data boundary separate; opaque worker timing and model identity remain
unknown. Exclude streaming, webhooks, credential flows, arbitrary remote tools
and cross-version fallback. This proposal is not implemented or live-tested.

OpenClaw v2026.9.1 is not a suitable execution target for this bounded profile:
its [A2A implementation contract](https://github.com/openclaw/openclaw/blob/v2026.9.1/docs/channels/a2a.md)
refuses `CancelTask` because dispatched work cannot be stopped through that
plugin. A reply timeout can leave the task working. Do not relabel either
condition as observed cancellation.

## Source resolution and preflight options

The public HF metadata resolver ships in v0.10.7; llmfit remains a proposed
importer. Neither is a feature of the MCP server or authority to run a model.

**Hugging Face metadata:** the official
[`model_info`](https://huggingface.co/docs/huggingface_hub/package_reference/hf_api#huggingface_hub.HfApi.model_info)
API can resolve a requested revision and return file sizes, optional LFS
metadata, configuration and gating information. File-level
[`get_hf_file_metadata`](https://huggingface.co/docs/huggingface_hub/package_reference/file_download#huggingface_hub.get_hf_file_metadata)
exposes commit, ETag, size and download location. fitr's
[metadata resolver](source-resolution.md) records an immutable commit, explicit
filenames, declared byte counts and content hashes, query observation times and
unresolved dependencies. A Git object ID is not relabeled as a SHA-256 of model bytes.

It uses explicit repository/revision/file inputs, at most two fixed-host
anonymous metadata requests and no redirects. Unavailable hashes, denied
metadata and ambiguous shard/encoder dependencies remain gaps. No model-card
instructions, custom code, weight downloads or returned download URLs are used.

**llmfit:** its [official CLI documentation](https://github.com/AlexsJones/llmfit/blob/main/docs/cli.md)
and [project overview](https://github.com/AlexsJones/llmfit) expose hardware/model
recommendations as JSON and describe the assumptions behind fit and speed
estimates. The smallest integration is an inert import of an explicitly
selected JSON report, retaining tool version, catalog identity when available,
hardware inputs, quantization/context assumptions and estimate provenance.
Separate estimates from reported measurements and do not inherit a quality
score as a fitr role pass. Avoid installer, download, benchmark and sharing
paths in that importer.

Neither source metadata nor an upstream fit estimate establishes runtime-bound
artifact identity, current free memory, peak allocation, simultaneous
residency or role quality. Use them to select the next measurement. Current
[role confirmation](role-confirmation.md) still requires its own verified
allocation, fresh capacity preflight and sealed quality evidence.
