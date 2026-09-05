# Resolve the exact files behind a model idea

Source resolution records public Hugging Face file metadata before any weight
download. It answers which commit and files were observed, their declared sizes
and hashes, and which dependencies still need investigation. It does not decide
whether a model fits or qualifies for a role.

```bash
fitr source resolve hf --repo owner/model --revision main \
  --file model-Q4_K_M.gguf --out resolution.json
fitr source show resolution.json
fitr source show resolution.json --display json
```

Use an existing physical output directory and a new filename. A source receipt
is immutable: the command will not overwrite an existing file. All commands
support `--display auto|rich|plain|json|none`; the saved path is printed to stderr.
The same validated receipt supplies text and JSON.

## Select explicitly and pin once

Repository, revision and one to 32 unique `--file` values are mandatory.
Repeat `--file` for every explicitly selected shard or companion. Inputs are
identifiers and relative filenames, not arbitrary URLs. Copy the repository,
revision and exact file path from the candidate's source. A quantization label
does not identify every file sharing that label.

The resolver makes at most two anonymous metadata requests. The first resolves
the requested branch, tag or full commit. The second queries the returned full
commit and checks repository identity and selected-file metadata again. A branch
can advance after the first request without changing the pinned selection.
Inconsistent responses produce an unavailable receipt rather than mixed evidence.

The implementation follows the official
[Hub metadata API](https://huggingface.co/docs/huggingface_hub/package_reference/hf_api#huggingface_hub.HfApi.model_info)
and its [wire implementation](https://github.com/huggingface/huggingface_hub/blob/main/src/huggingface_hub/hf_api.py).
Repository renames, case differences and redirects require explicit correction.
Files remain case-sensitive. Gated, private and missing repositories can be
indistinguishable to an anonymous request; fitr preserves that uncertainty.

## Read the receipt

| State | Meaning |
|---|---|
| `resolved` | Every explicitly selected file has consistent metadata, a declared size and a provider-declared content SHA-256. |
| `incomplete` | Selected-file metadata is missing a file, size or content SHA-256. |
| `unavailable` | The bounded request or consistency checks could not establish the metadata. |

Resolved metadata exits 0. Incomplete and unavailable metadata exit 4. Usage
errors exit 2, and local processing or storage failures exit 1. Exit 0 does not
establish dependency closure, runtime compatibility, free memory or quality.
Operator cancellation exits 130. If a query had already begun, its completed
failure receipt may be saved; cancellation before resolution creates no receipt.

A receipt separates Git blob object IDs from provider-declared LFS SHA-256
values. Neither has been checked against downloaded model bytes. The receipt's
own digest detects edits; it is not a signature or independent authentication
of the publisher. It includes observation times for each query, response-body
digests, the resolved commit, selected metadata and the bounded filename inventory.
It does not retain response bodies, credentials or signed download locations.

Known numbered GGUF shard names are inspected as groups. Missing or unselected
members remain visible. Projector and encoder filenames are candidates only;
the resolver does not choose one or infer compatibility. Tokenizer and
cross-repository dependency closure remain unknown. It does not read model-card
instructions, configuration files or index contents in this first profile.

An 18 GB weight file is not an 18 GB runtime budget. Fit planning must account
for context, KV cache, runtime buffers, placement and required companions. Local
measurement and [role confirmation](role-confirmation.md) must then establish
the actual behavior and quality floors for that exact configuration.

## Request and storage bounds

- Fixed HTTPS `huggingface.co`, two metadata GETs at most, ten seconds per
  request and twenty seconds overall. No redirects, retries, proxy discovery,
  ambient tokens, cookies or custom endpoints.
- At most 32 KiB of response headers, four MiB per response body, 4,096 inventory
  entries, 256 dependency findings and one MiB per saved receipt.
- Duplicate JSON keys, malformed sizes and hashes, conflicting metadata,
  traversal filenames and modified receipt semantics are rejected. New upstream
  fields are allowed; fitr's saved schema rejects unknown fields.
- Output uses a synced private temporary file and exclusive publication.
  Existing targets, parent-traversal paths and symbolic-link components are rejected. On
  systems with directory aliases, use the physical directory path. Filesystems
  without hard-link support fail closed. These path checks do not sandbox a
  hostile local process with the same filesystem permissions.

Resolution does not download weights, execute repository code, alter a role,
grant adoption authority or delete files. Saved discovery ideas remain
unmeasured. Source extraction, download ownership, complete dependency graphs,
automatic fit planning and the larger automation loop remain separate work.
