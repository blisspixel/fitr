# Read-only evidence server profile

`fitr mcp serve` speaks MCP **2026-07-28** over newline-delimited stdio. The
host chooses the installed executable and local `FITR_RESULTS` directory before
launch. No request can supply a path, endpoint, command, model or credential.
There are no network clients, model executions, file writes or mutation tools.
UNC, device, alternate-stream and parent-traversal paths are rejected before
filesystem resolution. Local aliases are resolved with bounded component and
link checks; a remote link target is rejected before following it. Hosts should
configure a local directory, not a network-mounted filesystem.

The profile implements `server/discover`, `tools/list`, `tools/call` and client
`notifications/cancelled`. Every request independently supplies
`params._meta["io.modelcontextprotocol/protocolVersion"]` and
`params._meta["io.modelcontextprotocol/clientCapabilities"]`. Client identity is
optional and never grants authority. Discovery is optional for clients. An
unsupported version returns `-32022` with `supported` and `requested`; missing
required metadata returns `-32602`. Every success includes `resultType` set to
`complete`. These follow the current [versioning contract](https://modelcontextprotocol.io/specification/2026-07-28/basic/versioning),
[request metadata](https://modelcontextprotocol.io/specification/2026-07-28/basic#meta)
and [discovery schema](https://modelcontextprotocol.io/specification/2026-07-28/server/discover).

Example request, as one line:

```json
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fitr_role_review","arguments":{"role":"coding"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}
```

The deterministic catalog contains three tools:

| Tool | Arguments | Structured output |
| --- | --- | --- |
| `fitr_roles_list` | Empty object | `fitr.mcp.roles.v1`: role names, revision digests, attachment counts |
| `fitr_role_review` | Required `role`, matching `[a-z0-9][a-z0-9-]{0,63}` | `fitr.mcp.review.v1`: rechecked screening state, evidence digests, candidate states, reason/gap counts, preference estimate/low/high, comparison readiness, exploration lead |
| `fitr_role_status` | The same required `role` | `fitr.mcp.role-status.v1`: current role revision, lifecycle digest, evaluation time, unselected/qualified/stale state and optional selected receipt, original revision, evidence digest and expiry |

`adoption_authorized` is always false. Reviews reuse the local role eligibility,
freshness, identity and comparison checks. They are observations at request
time, not approvals or permanent qualifications. Text content duplicates the
redacted structured JSON. Model names, paths, run IDs, descriptions, requirement
IDs and raw diagnostics are omitted. Role names and evidence digests are shared;
users must not put secrets in role names. Protocol IDs and unsupported version
strings are echoed as required correlation metadata.

Review concerns manually attached candidates. Status rechecks the current
incumbent, including selections from auto's closed managed stores. A stale
selection remains identifiable without being qualified. Status shares the CLI
lifecycle checks through an immutable snapshot; it acquires no filesystem lock
and writes no sidecar. It validates the full lifecycle metadata but reads only
the incumbent's confirmation dependencies. Removing an obsolete former store
does not invalidate a later selection.

Unknown methods and tools produce protocol errors. Tool argument violations or
unavailable local evidence produce a completed result with `isError: true` and
a fixed, non-sensitive diagnostic. Tool input and output schemas are fixed
JSON Schema 2020-12 objects. No client-provided schema or remote `$ref` is loaded.
This follows the [tools contract](https://modelcontextprotocol.io/specification/2026-07-28/server/tools).

Limits are 64 KiB per input line, 128-byte string IDs or signed 64-bit integer
IDs, four concurrent tool requests and 60 tool calls per minute. One evidence
review runs at a time. JSON must have unique keys and exact protocol field
spelling. Batches, unknown request parameters and continuation fields are
rejected. Role storage retains its 64-role/candidate limits. MCP additionally
limits inspected directories to 512 entries, individual evidence files to
4 MiB, role JSON to 1 MiB, and history plus attached evidence to 16 MiB. Selection
status also limits lifecycle JSON to 1 MiB and 64 events, referenced managed
stores to 16, each managed manifest to 64 KiB, and all inspected selection files
to 512 and 16 MiB combined. Larger stores remain available through the ordinary
CLI. Unavailable selection evidence returns a fixed diagnostic directing the
person to `fitr role status` locally.

Cancellation stops cancellable work and suppresses its response. A record
verification already executing can finish internally before its cancellation
check; it cannot mutate evidence. Closing stdin drains outstanding replies for
at most five seconds. Process cancellation closes stdin and returns promptly.
There are no unsolicited stdout messages. These behaviors follow the
[stdio binding](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/stdio)
and [cancellation rules](https://modelcontextprotocol.io/specification/2026-07-28/basic/patterns/cancellation).

The configured root is resolved at startup. Each call preflights role, history
and attachment directories and rejects external attachments, symlinks and
nonregular evidence. The existing record API subsequently opens by path, so a
hostile local process with the same filesystem permissions can race these
checks. This is not a filesystem sandbox against that process. A handle-based
record reader is required before making that stronger claim. Atomic concurrent
fitr writes may also make a review unresolved; retry after the write finishes.
Selection status rechecks metadata bytes, file identities, directory membership
and saved root aliases before returning. These checks detect ordinary atomic
publication; they do not attest against a writer changing and restoring paths
or contents during the call.

No legacy `initialize` handshake, HTTP, OAuth, SSE, subscriptions, resources,
prompts, sampling, elicitation, progress, logging or Tasks extension is offered.
Optional client capabilities do not enable those features. Literal protocol
fixtures and sealed local evidence tests cover this profile; they do not certify
all MCP features or prove live acceptance by any named host. A future host
acceptance test must record its exact version and requested protocol revision.

The portable plugin's root `mcp.json` uses the standard stdio variant: bare
`command` equal to `fitr`, and `args` equal to `["mcp", "serve"]`. It relies on an
existing installed executable and launches only when the host loads that
configuration. Agent Plugins **1.0.0** standardizes skills and MCP configuration;
packaging does not prove wire compatibility or sandbox a subprocess. See the
[package specification](https://agent-plugins.org/specification) and
[closed configuration schema](https://agent-plugins.org/schemas/1.0.0/mcp.schema.json).

A2A remains a future evaluation adapter, not a server feature. The next bounded
contract should pin an Agent Card interface, `protocolBinding`, URL and wire
version `1.0`; bind task and context IDs; poll task/artifact state; check a
cancellation outcome; and independently verify the artifact. The first adapter
should avoid automatic submission retries and record cancellation races.
Neither task completion nor an Agent Card identifies the underlying model and
harness. The [A2A 1.0.0 specification](https://a2a-protocol.org/v1.0.0/specification/)
defines those version, task and operation semantics.

An explicit Go implementation keeps this three-tool profile dependency-free.
The official [Go SDK dependency manifest](https://github.com/modelcontextprotocol/go-sdk/blob/main/go.mod)
includes broader schema, HTTP and authorization dependencies. Reconsider that
tradeoff when adding transports or client-side protocol evaluation.
