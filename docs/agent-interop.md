# Agent ecosystem compatibility

Research checked September 5, 2026. The MCP profile was introduced in v0.10.6.
Package discovery, wire-protocol behavior and independently
verified model-plus-harness work are separate acceptance boundaries.

## Implemented profile

| Surface | Version | Current fitr implementation |
|---|---|---|
| MCP | 2026-07-28 | Read-only evidence server over stateless stdio; two bounded tools |
| Agent Plugins | 1.0.0 | Portable skill plus root MCP configuration |
| A2A | 1.0.0 specification, `1.0` wire version | Researched future evaluation adapter; no endpoint or adapter implemented |

The [MCP July release](https://blog.modelcontextprotocol.io/posts/2026-07-28/)
replaces the initialization/session lifecycle with self-contained requests and
moves Tasks to an extension. fitr implements a specific stdio profile, not every
feature of that revision. Literal protocol fixtures and sealed local evidence
tests cover its behavior. **No named harness has a recorded live fitr acceptance
result yet.**

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

The review invokes existing local role checks and always returns
`adoption_authorized: false`. It does not expose confirmation, adoption,
rollback, downloads, model execution, arbitrary paths or endpoint selection.
Model names, source paths, run IDs, descriptions and raw diagnostics are
omitted. Role names and digests are shared, so role names must not contain
secrets. The host selects the evidence root; tool input cannot change it.

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
matching wheel. A local Windows run with the published fitr 0.10.9 binary
passed both explicit `2026-07-28` and ordinary default client modes against
empty and synthetic current-schema evidence stores. CI receipts must establish each later binary
and operating-system result; this is not a named-host acceptance result.

The script uses the released SDK's `Client(stdio_client(...))` transport
contract. In explicit mode, `session.discover()` can return synthetic cached
discovery. The smoke instead calls `send_discover`, validates the real reply
and adopts it. The default client also performs its own initial discovery;
the transcript must contain both actual probes and no legacy `initialize`.
These details follow the pinned
[client implementation](https://github.com/modelcontextprotocol/python-sdk/blob/v2.0.0/src/mcp/client/client.py)
and [session implementation](https://github.com/modelcontextprotocol/python-sdk/blob/v2.0.0/src/mcp/client/session.py).

Each case validates the catalog, closed schemas, both tools, exact response
IDs and complete results, an invalid role and an unknown tool. The synthetic
fixture passes through the canonical record store and real role CLI. Its
integrity-sealed evidence exercises qualification and preference bounds; it is
fabricated test data and establishes no model quality. MCP summaries must
match the local CLI, including optional exploration leads and clamped
preference intervals. Checks reject exposed paths, model/run identities,
descriptions and requirement identifiers, including escaped JSON text.

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

## Next bounded host acceptance

The next useful step is one version-pinned live host row using the same
temporary evidence fixture. SDK acceptance establishes a real client flow;
portable package metadata alone establishes no harness support. Record a host's
unsupported revision as an acceptance failure.

Hermes Agent's released
[`29112bef099274229cadff79cdff7bf7b99c4b77` MCP connector](https://github.com/NousResearch/hermes-agent/blob/29112bef099274229cadff79cdff7bf7b99c4b77/tools/mcp_tool.py)
provides an explicit `protocol: stateless` option. Its default `auto` path
starts with legacy initialization and has specific fallback error handling,
which differs from the Python SDK's default discovery-first behavior. A later
Hermes row should explicitly select `stateless`, restrict its tool allowlist
to these two tools and observe the actual wire exchange. This is a source-based
configuration proposal, not a live Hermes compatibility result.

Pi's [v0.85.0 coding-agent documentation](https://github.com/earendil-works/pi/blob/v0.85.0/packages/coding-agent/README.md)
states that MCP belongs in extensions rather than its built-in agent. Pin an
actual extension before claiming a Pi integration. OpenClaw's official
[`mcp probe` contract](https://docs.openclaw.ai/cli/mcp) provides a catalog probe;
a successful probe alone would not establish tool execution or evidence
semantics. Neither host has been installed or configured by this acceptance.

The proposed sequence is package discovery, stdio launch, optional discovery,
catalog/schema inspection, both tool calls, an invalid role, cancellation and
shutdown. Confirm that the returned review matches local CLI evidence and
omits private fields. Record the host build, fitr binary/plugin digests,
requested MCP revision, sanitized configuration and transcript digest. A
successful row covers this read-only profile only; it does not establish model
or workflow quality. A named-host acceptance run has not been performed.

| Harness candidate | Official configuration surface | Bind before a later workflow experiment |
|---|---|---|
| Hermes Agent | [Model configuration](https://hermes-agent.nousresearch.com/docs/user-guide/configuring-models) | Main, auxiliary and fallback model slots; actual routing |
| Pi | [Custom model configuration](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/models.md) | Harness build, API adapter, extensions, model resolution and session state |
| OpenClaw | [Model selection](https://docs.openclaw.ai/concepts/models) | Effective provider/model, fallback and session overrides |

Pi configuration can contain executable credential commands. A future importer
must inspect inert data and omit secrets; invoking a configuration resolver is
not a read-only import.

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
