# Agent ecosystem compatibility

Research checked September 5, 2026. Compatibility has three separate levels:
package discovery, a tested wire-protocol profile, and independently validated
work through a complete model-plus-harness configuration.

## What ships

`plugins/fitr` is a skills-only Agent Plugins 1.0.0 package. It contains a root
manifest and one portable Agent Skill for discovery and evidence interpretation.
Use a client's documented local-package loading mechanism. The package needs
an installed fitr CLI; it does not install fitr, modify client configuration,
start an MCP server or expose an A2A endpoint. Client acceptance remains
client-specific until a live conformance row has been recorded.

The [Agent Plugins specification](https://agent-plugins.org/specification)
defines portable skills and MCP configurations. A2A is not a portable core
component. Its [manifest schema](https://agent-plugins.org/schemas/1.0.0/plugin.schema.json)
and [Agent Skills specification](https://agentskills.io/specification) define
the package used here.

## Protocol profiles to implement next

| Surface | Research target | Required acceptance boundary | Current status |
|---|---|---|---|
| MCP | 2026-07-28 | Versioned request metadata, tool catalog/schema binding, errors and input-required continuations, bounded fixture tools | Researched; no fitr MCP adapter |
| A2A | 1.0.0 specification, 1.0 wire version | Agent Card/interface binding, task state, artifacts, cancellation readback and local verification | Researched; no fitr A2A adapter |
| Agent Plugins | 1.0.0 | Manifest plus immediate skill directories; no implicit execution | Skills-only package |

The [MCP July release](https://blog.modelcontextprotocol.io/posts/2026-07-28/)
changes the lifecycle to stateless requests and moves Tasks into an extension.
A new adapter must not assume an old initialization/session handshake.
[Streamable HTTP](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http)
requires matching protocol and method metadata. The
[tool contract](https://modelcontextprotocol.io/specification/2026-07-28/server/tools)
distinguishes protocol errors, tool errors and input-required continuations.
Tool annotations do not grant authority.

[A2A 1.0](https://a2a-protocol.org/v1.0.0/specification/) distinguishes terminal
completion, failure, cancellation and rejection from interrupted input/auth
states. Cancellation is a request whose result needs observation. Retrying a
submission does not automatically prove deduplication. A completed remote task
can supply an artifact for local checking; its status alone cannot establish
fitr acceptance or reveal the identity of an opaque agent's underlying model.

Start with deterministic loopback policy-repair fixtures, version-pinned
adapters and offline CI. Test remote success with invalid artifacts, tool
errors, version/schema drift, interruptions, duplicate delivery and cancellation
races. Keep streaming subscriptions, webhooks, arbitrary plugin execution and
automatic cross-version fallback outside that first profile.

## Harnesses and model roles

| Harness | Official surface researched | Evidence that must be bound |
|---|---|---|
| Hermes Agent | [Main, auxiliary and fallback model configuration](https://hermes-agent.nousresearch.com/docs/user-guide/configuring-models) | Every effective model slot, session routing and actual fallback |
| Pi | [JSON/RPC/SDK interfaces](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/README.md) | Harness build, API adapter, skills/extensions, fresh or resumed state |
| OpenClaw | [Model selection and routing](https://docs.openclaw.ai/concepts/models) | Effective runtime, provider/model, utility slots and session overrides |

These are researched integration paths, not live fitr compatibility claims.
In particular, [Pi custom model configuration](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/models.md)
can resolve executable credential commands. Future importers must parse inert
data and omit secrets, not invoke the harness's configuration resolver.

Use harness-owned monotonic time for end-to-end observations. Keep internal
worker timing unavailable for an opaque agent. Record protocol success,
remote terminal status, independently verified outcome, authority observation,
and data boundary separately. A model-plus-harness result cannot become a
model-only score or prove simultaneous residency of its auxiliary models.
