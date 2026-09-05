# Keep a pinned source with an idea

A discovery idea preserves what you heard and the role you want to investigate.
A source receipt preserves an explicit file selection observed at one immutable
commit. Attaching the receipt lets the investigation continue without losing
those details. It leaves the idea and its claim unmeasured.

```bash
fitr discover add https://example.com/model-review --role coding --model candidate
fitr discover list
fitr source resolve hf --repo owner/model --revision main --file model-Q4_K_M.gguf --out candidate.json
fitr discover attach-source <idea-id> candidate.json
fitr discover plan <idea-id>
```

Copy the full idea ID from `discover add` or `discover list`. These IDs remain
stable when an attachment changes. Existing inbox files, original capture times
and duplicate detection retain the Idea v1 contract.

The attachment copies the validated receipt into private fitr storage. Moving or
removing `candidate.json` does not break it. The original receipt path is not
retained. This is an operator association: it does not prove that the original
post or video recommended those exact files.

## Read the plan as separate questions

| Fact | What it means |
|---|---|
| Original claim and model hint | What the idea says, still unverified |
| Source metadata | The attached receipt's resolved, incomplete or unavailable state |
| Selected files | Explicit paths with provider-declared sizes and hashes, not verified local bytes |
| Dependencies | Missing or unselected shards, companion candidates and unresolved closure |
| Runtime | Unbound until the exact artifact and dependency set is mapped to a supported runtime |
| Quality | Unmeasured until local evidence and role confirmation establish the required outcomes |

One attachment can supply the plan's metadata. With multiple attachments, choose
one explicitly using its full resolution digest:

```bash
fitr discover plan <idea-id> --source <resolution-sha256>
fitr discover plan --role coding --display json
```

fitr never chooses the newest receipt or blends files across commits. Without a
selection, a multiple-receipt plan shows that selection is required. It remains
a valid investigation plan, not an eligible model configuration.

Plans no longer turn an unverified model hint into an `advise` command that might
pull a mutable alias. Investigate dependency closure and exact runtime binding
first. Source metadata alone cannot establish a safe memory budget or make a
smaller quantization an equivalent-quality substitute.

## Private storage and output

Up to four receipts can be attached to one idea. Versioned, integrity-checked
association envelopes live under a sibling `.discovery-sources` directory,
outside the 1,000-entry inbox. The source store bounds aggregate managed bytes
to 256 MiB. Writes use a store lock and exclusive publication; malformed,
renamed, symlinked or edited associations are errors, including in silent mode.

The attachment embeds the complete validated [source receipt](source-resolution.md).
Its digest detects edits, not publisher authenticity. Repository claims and
provider-declared hashes still require independent local validation.

```bash
fitr discover detach-source <idea-id> <resolution-sha256>
```

Detaching removes only the managed association copy. It does not remove the idea,
original receipt, model files or measured evidence. The same receipt can be
attached again later.

All commands support `--display auto|rich|plain|json|none`. JSON keeps Idea v1
unchanged and adds separately versioned `fitr.discovery.plan.v2` proposals.
Success means the requested association or plan was produced; it does not mean
the model fits or qualifies. Attachment and planning commands make no network,
backend, download, model-execution or adoption calls.

The next binding and preflight boundary is described in the
[artifact/runtime plan](artifact-runtime-plan.md).
