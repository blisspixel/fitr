# Verify the local files behind a source receipt

An artifact binding compares explicitly mapped local files with a pinned source
receipt. It records independently observed whole-file hashes, declared source
metadata and any mismatch or missing evidence. It does not establish what a
runtime loaded, whether the configuration fits, or whether it meets a role's
quality requirements.

Create a mapping using the full source receipt digest and absolute local paths:

```json
{
  "schema": "fitr.artifact.bind.spec.v1",
  "resolution_sha256": "sha256:<full source receipt digest>",
  "files": [
    {
      "source_path": "model-Q4_K_M.gguf",
      "local_path": "D:\\models\\model-Q4_K_M.gguf",
      "component_role": "weights"
    }
  ]
}
```

Use the platform's absolute path syntax. A mapping accepts one to 32 explicit
files from the same validated source selection. Component roles are operator
declarations, not inferred compatibility. No globs, sibling searches or automatic
dependency selection are performed.

Use physical paths without symlink components, parent traversal, network or
device paths. Historical receipts can retain another platform's absolute paths;
showing them does not resolve those paths on the current machine.

```bash
fitr artifact bind --source resolution.json --mapping local-files.json --max-bytes 68719476736 --timeout 10m --out artifact.json
fitr artifact show artifact.json
fitr artifact show artifact.json --display json
```

The default read budget is 64 GiB and the default elapsed-time limit is ten
minutes. Hard limits are one TiB and one hour. Preflight compares the total size
of all mapped regular files with the budget, including files whose declared
size already disagrees and which will not be hashed. `bytes_read` counts only
actual reads and can remain zero. Cancellation and timeouts are
cooperative between filesystem reads; a blocked filesystem call cannot be
guaranteed to stop at an exact wall-clock deadline.

## What the observation establishes

A provider-declared content SHA-256 can be compared with the independently
observed local SHA-256. Git blob object IDs remain separate. If the provider
content hash is absent, a local hash is useful but cannot establish a source
match. Unmapped selected files and unresolved dependencies remain visible.

The command checks opened-file identity, size and modification time before and
after reading, then rechecks the complete mapped set before sealing the result.
It detects ordinary concurrent changes and path replacement. This is not a
filesystem snapshot and does not prove that a writable file remains unchanged
after the observation. A later loader must verify the bytes it consumes.

The receipt embeds its source selection, mapping, limits, actual byte counts,
per-file outcomes and integrity seal. It is private and immutable; output must
use a new filename and cannot overlap a mapped input. Reopening a receipt
validates the saved observation without reopening the mapped model files.

For automation, exit code `0` means all selected local bytes matched their
declared source hashes. Other valid observations, including local hashes without
provider hashes or failed file observations, return `4`; argument errors return
`2`, invalid input/receipt or publication/output failures return `1`, and
interrupted commands return `130`. A started interrupted
observation may leave a sealed receipt. `--display none` still validates it and
preserves these exit states.

Local-byte matches remain separate from dependency compatibility. Runtime stays
unbound, capacity and quality stay unmeasured, and no binding outcome grants
ranking, measurement or adoption authority. The later runtime boundary is
described in the [artifact/runtime plan](artifact-runtime-plan.md).
