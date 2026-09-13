# Resolve the exact files behind a model idea

Source resolution records public Hugging Face file metadata before any weight
download. It answers which commit and files were observed, their declared sizes
and hashes, and which dependencies still need investigation. On its own it does
not decide whether a model fits or qualifies for a role.

`--fit` projects declared weights plus modeled cache. `--screen` first applies
explicit publisher, license and architecture policies, then compares those
components with an operator ceiling. Both leave runtime allocation unmeasured.

```bash
fitr source resolve hf --repo owner/model --revision main \
  --file model-Q4_K_M.gguf --out resolution.json
fitr source show resolution.json
fitr source show resolution.json --display json

fitr source resolve hf --repo owner/model --revision main \
  --file model-Q4_K_M.gguf --out fit-source.json --fit --ctx 32768

fitr source resolve hf --repo owner/model --revision main \
  --file model-Q4_K_M.gguf --out screen-source.json --screen \
  --ctx 8192 --fit-budget-gb 16 --allow-license apache-2.0 \
  --allow-architecture qwen3 --require-first-party
```

## Who published it, and from what

The receipt records the repository's author, every base model it declares, and
whether the publisher is the author of all those bases. That value is arithmetic
rather than a judgement, and it is deliberately not a quality claim: a third-party conversion
is frequently the only one that exists, and is not thereby worse.

It is recorded because the obvious way to choose among candidates is the wrong
one. Download counts rank a repository by how long it has existed and how well
it is known, which is exactly backwards for the new releases someone is asking
about, and in practice they place third-party requantizations above the model
author's own conversion. Lineage answers the same question without that bias.

An absent or malformed base-model declaration never reads as first-party
authorship. A mixed-author merge cannot become first-party by inspecting only
its first parent or dropping an invalid parent. An author declaration that
conflicts with the pinned repository owner also remains unmeasured. The
declared license and gating state are recorded beside it; a
gated repository can answer metadata while refusing the files, so gating is a
fact about what comes next rather than a failure.

All of it comes from the response the resolver already fetched for the file
metadata, so it costs no additional request and no additional trust.

## Screen in order

`--screen` requires explicit positive `--ctx` and `--fit-budget-gb` values. The
latter is a ceiling in GiB for modeled components, not a safe runtime budget.
Repeat `--allow-license` and `--allow-architecture` for up to 32 unique lowercase
identifiers each. Omitting either accepted list leaves its gate unresolved,
without fetching a header that cannot yet resolve the screen.

| Gate | What clears it | What remains missing |
|---|---|---|
| Publisher | Consistent selected metadata and a known publisher; optional `--require-first-party` additionally requires all declared base authors to match | Independent publisher authentication and conversion quality |
| License | A specific declared identifier occurs in the operator's accepted list | Legal interpretation, license terms and dependency licenses |
| Architecture | Complete, consistent GGUF metadata supports fitr's cache arithmetic and its architecture identifier occurs in the accepted list | Support by the intended runtime build, modality, template and parser |
| Fit | Declared weights plus the f16 cache projection at the requested context do not exceed the component ceiling | Runtime buffers, companion allocation, placement and measured resident memory |

Each gate reports `clear`, `blocked`, `unresolved` or `not_checked`. The first
blocked or unresolved gate becomes the next investigation; later gates remain
`not_checked`. A declaration outside an accepted list is blocked; missing
evidence is unresolved. A license declared as `other` remains unresolved. No
popularity statistic or quality score enters this sequence. Clearing all four
gates does not qualify the candidate for a role.

## Project components before a complete download

The declared size gives the weights and the artifact's opening bytes give the
architecture, so the ordinary weights-plus-cache arithmetic applies to a model
that is not on the machine. `--fit` reads the first 32 KiB of each selected file
by default. `--header-bytes N` explicitly changes that per-file bound, up to
8 MiB. There is one bounded read per file and no automatic retry with a larger
allowance. A short metadata section can mean the permitted prefix also includes
leading tensor bytes; this is bounded artifact access, not a promise that every
transferred byte is metadata.

The complete metadata section must fit inside the chosen bound. A truncated
header cannot establish absence of a sliding-window, recurrent or other layout
key that changes cache arithmetic. Malformed dimensions stay unmeasured, and
unsupported layouts remain unresolved. A partial architecture name can be shown
without clearing the gate. The official Qwen3-0.6B GGUF already exceeds the
default header bound; small parameter count does not imply a small vocabulary.
An explicit 8 MiB allowance completed that artifact's metadata and all four
declared screening gates in [live acceptance](release-acceptance.md#source-screening-acceptance-2026-09-13-working-tree).

One selected GGUF or one complete, consistent shard group can be projected.
Shard names, `split.no`, `split.count`, sizes and architecture declarations must
agree. Missing shards, independent models and selected projector or encoder
companions remain unresolved instead of becoming one misleading weight total.
Required but unselected companions remain a disclosed gap; dependency closure
has not been established.

`--fit-budget-gb N` supplies the component ceiling. For `--fit` without that
flag, detected addressable device capacity supplies the comparison, labeled as
addressable capacity rather than a safe runtime budget. `--ctx` defaults to the
artifact's declared maximum for `--fit`; `--screen` requires it explicitly.
An unknown ceiling, context or cache shape cannot clear the comparison.

That read has its own network boundary, wider than metadata resolution's, and
the output says so. The bytes of a large artifact are not on the host that
serves its metadata: the provider answers the canonical URL with a redirect to a
signed, expiring location on a content network it chooses, so refusing every
redirect refuses the bytes. The read follows at most one redirect, to an
absolute HTTPS location without credentials, requests a bounded range, sends
no credentials, and records the host that actually served it. The projection
cites that host.

What it establishes is bounded. The size is the provider's declaration rather
than bytes fitr hashed, the header is a prefix rather than a verified file, and
nothing here is a runtime observation. A component projection informs the next
investigation, never a model recommendation. HTTP range framing, encoding and
byte counts are checked. A response's complete file size, when supplied, must
agree with the pinned metadata before a component projection can clear.
An unreadable or incomplete header stays unresolved.

Use an existing physical output directory and a new filename. A source receipt
is immutable: the command will not overwrite an existing file. All commands
support `--display auto|rich|plain|json|none`; the saved path is printed to stderr.
The same validated metadata receipt supplies text and JSON.

`--out` stores metadata only. `source show` reopens it offline and does not
replay the screen. Fresh `--fit` and `--screen` output carries the source receipt,
policy where applicable, header observations, prefix digests and component
analysis in one JSON document. This output is not sealed into the metadata
receipt. It retains public filenames and serving hosts, not raw header bytes or
signed download URLs. `--display none` suppresses the report but still performs
the requested checks and returns their outcome.

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

Metadata-only resolution exits 0 when resolved. Incomplete and unavailable
metadata exit 4. `--screen` exits 0 only when all gates clear; `--fit` exits 0
only when its component projection is within the ceiling. Blocked and unresolved
screens or projections exit 4, including successful metadata followed by an
incomplete header. Exit 3 remains reserved for a measured need that failed. Usage
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
- Explicit `--fit` or `--screen` additionally permits one bounded artifact read
  per selected file, each with at most one HTTPS redirect and twenty seconds
  overall. The per-file allowance is 32 KiB by default and at most 8 MiB with
  `--header-bytes`; it never expands automatically. At 32 selected files, the
  maximum explicitly authorized body allowance is 256 MiB.
- At most 32 KiB of response headers, four MiB per response body, 4,096 inventory
  entries, 256 dependency findings and one MiB per saved receipt.
- Duplicate JSON keys, malformed sizes and hashes, conflicting metadata,
  traversal filenames and modified receipt semantics are rejected. New upstream
  fields are allowed; fitr's saved schema rejects unknown fields.
- Output uses a synced temporary file and exclusive publication.
  Existing targets, parent-traversal paths and symbolic-link components are rejected. On
  systems with directory aliases, use the physical directory path. Filesystems
  without hard-link support fail closed. These path checks do not sandbox a
  hostile local process with the same filesystem permissions.
- Unix writes use mode `0600`; Windows access follows the destination
  directory's ACL. Use a directory restricted to the intended account. fitr
  does not encrypt receipts or replace Windows ACLs. A writer can edit a saved
  receipt and recompute its unkeyed integrity seal.

Resolution does not download weights, execute repository code, alter a role,
grant adoption authority or delete files. Attach a receipt to a discovery idea
with [source attachments](source-attachments.md); the idea remains unmeasured.
Source extraction, download ownership, complete dependency graphs,
persisted screening observations, runtime support profiles and the larger
automation loop remain separate work. The next connected boundary is described
in the [artifact/runtime plan](artifact-runtime-plan.md).
