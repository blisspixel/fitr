# Agent ecosystem compatibility

Research checked September 5, 2026. This describes the implementation prepared
for v0.10.6. Package discovery, wire-protocol behavior and independently
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

## Next bounded host acceptance

The next useful step is one version-pinned live host row using a temporary
local evidence fixture, before adding execution tools. Hermes Agent is a
candidate for that first row; its actual requested protocol revision must be
observed, not assumed. Record an unsupported revision as an acceptance failure.

The proposed sequence is package discovery, stdio launch, optional discovery,
catalog/schema inspection, both tool calls, an invalid role, cancellation and
shutdown. Confirm that the returned review matches local CLI evidence and
omits private fields. Record the host build, fitr binary/plugin digests,
requested MCP revision, sanitized configuration and transcript digest. A
successful row covers this read-only profile only; it does not establish model
or workflow quality. This acceptance run has not been performed.

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

These are upstream options for a later bounded importer, not features of the
MCP server or new authority to download or run a model.

**Hugging Face metadata:** the official
[`model_info`](https://huggingface.co/docs/huggingface_hub/package_reference/hf_api#huggingface_hub.HfApi.model_info)
API can resolve a requested revision and return file sizes, optional LFS
metadata, configuration and gating information. File-level
[`get_hf_file_metadata`](https://huggingface.co/docs/huggingface_hub/package_reference/file_download#huggingface_hub.get_hf_file_metadata)
exposes commit, ETag, size and download location. A proposed resolver should
store repository plus immutable commit, explicit filenames, byte counts,
available content hashes, observation time and unresolved dependencies. A Git
object ID or arbitrary ETag must not be relabeled as a SHA-256 of model bytes.

Start with explicit repository/file inputs and bounded metadata responses.
Validate redirects and returned locations before any later fetch; omit tokens
and signed URLs from persisted evidence. Treat unavailable hashes, gated files
and ambiguous shard/encoder dependencies as gaps. Do not execute model-card
instructions, custom model code or download weights during resolution.

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
