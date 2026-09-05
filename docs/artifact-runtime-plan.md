# Next: bind the files to a runnable configuration

Status: local [artifact binding](artifact-binding.md) implements bounded byte
observations in 0.10.9. Runtime binding remains a researched design.
Reviewed 2026-09-05.

Source attachments preserve a candidate investigation. The next step must
establish what would actually be loaded before estimating fit or running a
quality battery. A repository name and quantization label cannot establish this.

GGUF contains structured metadata as well as tensors. Hugging Face provides a
metadata viewer and documents remote GGUF parsing. This offers a bounded metadata
path to evaluate before complete weight downloads. It does not establish local
allocation or quality. [Hub GGUF documentation](https://huggingface.co/docs/hub/gguf)

llama.cpp describes multimodal execution as a language model plus a corresponding
projector, with architecture-specific preprocessing. A similarly named companion
is only a candidate until that relationship is established. The intended role's
modalities also determine which components are needed.
[Multimodal implementation](https://github.com/ggml-org/llama.cpp/blob/master/tools/mtmd/README.md)

Tool calling depends on a tool-aware template and parser. Upstream also documents
possible tool-call degradation from extreme KV-cache quantization. Template
overrides, cache precision and parsing options belong in the tested configuration.
[Function-calling guidance](https://github.com/ggml-org/llama.cpp/blob/master/docs/function-calling.md)

## Proposed first boundary

Create an explicit local artifact manifest. Each entry binds a component role,
source commit and path to a locally measured size and content digest. Shards,
tokenizer/template data, projector, encoder and optional draft model remain
distinct. An operator declaration describes an intended relationship; a
supported runtime profile and local checks must establish compatibility.

Bind the complete manifest to the runtime build, launch settings, context,
KV precision, placement, modality and tool parser. Keep the requested alias
separate from exact served identity. Unknown components prevent readiness.
Hashing large local files needs byte/time bounds, cancellation and checks for
files changing during observation. It must not scan unrelated storage.

A text-only role need not inherit a projector by filename convention. A vision
role cannot omit required companions to improve a fit estimate. The profile must
distinguish required, optional, disabled and unresolved components. This is a
proposed fitr policy, not a guarantee supplied by GGUF or a model publisher.

## Acceptance before automation

- Changed bytes, templates or cache settings require a new configuration
  identity; a different mutable alias alone does not identify new bytes.
- Missing shards, changed files, incompatible companions or unverified runtime
  identity prevent measurement without silently invoking a download.
- Fit accounts for companions, context, KV cache, buffers, placement and current
  usable capacity. Unknown allocation stays unresolved.
- A smaller quantization, cache or model must earn the role on task outcomes,
  including tool-channel behavior. Speed cannot override a failed quality floor.
- Failed challengers preserve the qualified incumbent. No-qualified-candidate
  remains valid.
- Local binding and inspection do not delete files, execute repository
  instructions, grant network authority or start serving processes.

Bounded download and experiment planning should follow this boundary. The
authority and budget contract remains in [discovery](discovery.md).
