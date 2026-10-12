# Ollama execution provenance

The serving-runtime version comes only from the configured endpoint's
`GET /api/version` response. An unreachable daemon, malformed response or
missing version leaves that identity unobserved. fitr does not substitute
`ollama --version`: the CLI selects an endpoint through its own `OLLAMA_HOST`
environment and can report an installed client or a different daemon. A stopped
daemon therefore has no measured runtime version, and its fingerprint cannot
stand in for an observed serving process. Existing sealed records retain their
original identity; fitr cannot repair older warning text into a daemon version.
This follows Ollama v0.40.3's
[version API](https://github.com/ollama/ollama/blob/v0.40.3/docs/api.md#version)
and [CLI handler](https://github.com/ollama/ollama/blob/v0.40.3/cmd/cmd.go),
reviewed October 12, 2026.

A local Ollama endpoint can route a model to a remote service. fitr preserves
the API's explicit `remote_model` and `remote_host` fields and checks selected
models before local run, diagnosis, advice and confirmation paths proceed.
Known remote entries remain visible in mixed inventory with state `remote`;
they receive no local capacity projection or reused local measurement.

Generate and chat frames are checked before output or timing is accepted.
A remote marker at any position discards the response's output and metrics.
It does not trigger an empty-response retry. Malformed provenance is a separate
error and also stops the local path. Differently cased JSON keys cannot erase a
positive marker, and default model aliases use the same case-insensitive
`:latest` matching rule in selection and inventory.

These checks follow the upstream
[Ollama API types](https://github.com/ollama/ollama/blob/main/api/types.go),
reviewed September 5, 2026. They detect explicit provenance and malformed
metadata. Absent markers do not prove local execution: a daemon can omit or
misreport them, and an alias or routing configuration can change after
inspection. A future owned-runtime experiment must bind its process, executable,
launch settings and served identity. The [artifact/runtime plan](artifact-runtime-plan.md)
keeps that stronger boundary separate.

Existing saved records without these optional fields retain their original
serialization and integrity checks. fitr cannot reconstruct remote provenance
that an older record did not capture. Explicit OpenAI-compatible remote
diagnostics remain separate from local fit; see
[external validation](external-validation.md).
