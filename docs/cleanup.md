# Read-only cleanup planning

```powershell
fitr cleanup plan D:\models
fitr cleanup plan D:\models --min-age-days 14 --display json
```

This inventories local storage without deleting or moving files, reading model
contents, running a backend, or contacting a service. It groups apparent bytes by
top-level directory and filename category, lists the largest files, and surfaces
aged incomplete downloads for **REVIEW**. The default age threshold is seven days.

`REVIEW` means a filename and modification time matched a rule. It does not mean
the file is unused or safe to delete. A paused transfer can still need its partial
file. Check the downloading application, resumability and ownership first. Model
weights, shards, vision projectors, build outputs, runtime libraries and fallback
configurations keep `USAGE_UNRESOLVED` status. A directory named `cache` is only a
classification hint; it does not establish that its contents are disposable.

Apparent bytes sum regular file lengths. They are not recoverable disk bytes:
hard links can count one physical allocation more than once, while sparse files,
compression and deduplication can also change disk usage. Allocated bytes are a
separate total. They are present only when every accepted regular file reported
a stat block count and an inode, and they sum those counts once per inode. A
directory listing that does not include the count leaves the field null, and
the text says unmeasured. Zero is a measured empty sum. The scan does not open
file contents to obtain the count. Neither total is space a deletion would free,
and neither one makes a partial download redundant or a checkpoint a replacement.
`completed_file_integrity` and each file's `file_integrity` stay `unmeasured`:
the scan does not read bytes or manifests, and a sibling name is not a
completed copy. A component group is filenames within one directory. The same
shard name in another directory is a separate group, and `ownership` stays
`unmeasured`. The directory is not an owner, and a repeated name is not a
shared dependency.
No newer model or faster configuration qualifies as a replacement from this scan.
Establish acceptable outputs and role coverage before retiring an incumbent.

The scan visits at most 250,000 entries, descends at most 64 levels and checks a
30-second deadline between filesystem operations. An operating-system call that
stalls can outlast that deadline. Within that budget, known model-store
directories (`blobs`, `manifests`, `models`) are entered before cache trees, so
a large directory of small cache files cannot consume the deadline before the
weights are seen. An incomplete plan names directories that were listed and not
entered; a deadline or entry limit still does not invent the contents of those
directories. Each human-facing list contains at most 20
items, with omitted counts where relevant. The JSON plan includes the absolute
root, UTC as-of and cutoff times, limits, total counts, categories, relative paths,
candidate reasons, unentered directories and bounded scan issues. Text uses relative paths and sanitizes
terminal control sequences. All display modes use the same plan; `NO_COLOR`
disables colors.

Observed symbolic links and special files are skipped, including links to model
storage. Permission failures, skipped entries, depth limits, cancellation, a
deadline or the entry limit produce an `INCOMPLETE` plan and nonzero exit status.
The entries successfully observed remain available in text or JSON. A complete
inventory describes an observation, not an atomic filesystem snapshot: downloads
and other applications may change storage during or after the scan.

Cleanup planning is an optional stage in the discovery loop. It provides storage
evidence for a human decision. Dependency-aware retirement, quarantine and deletion
are not implemented.
