# Confirm and retain a role selection

`role review` screens attached battery evidence under an explicit role policy.
`role confirm` freezes that policy and its selected candidate before collecting
fresh evidence. `role adopt` then records an explicit selection in fitr.
Confirmation does not automatically adopt a model.

The scope remains `battery_screening`. A fresh generated-instance seed does
not establish held-out task families, unseen training data, general coding
competence or reliable execution of an agentic workflow. Workload bundles and
ordinary narrow-objective `experiment confirm` bundles cannot be relabeled as
role confirmation.

## Commands

Start from a defined role with current canonical evidence attached. See
[roles](roles.md) for quality floors, preference anchors and attachment rules.

```bash
fitr role review coding
fitr role confirm coding
```

The live command prints the saved bundle path. Inspect that exact bundle
without running models, then adopt explicitly:

```bash
fitr role confirm /path/to/saved-bundle.json --display json
fitr role adopt coding /path/to/saved-bundle.json
fitr role status coding --display json
```

To request restoration of the previous adoption:

```bash
fitr role rollback coding
```

All four commands accept `--display auto|rich|plain|json|none`. Live
`role confirm <name>` also accepts `--backend auto|ollama|llama-server|openai`.
A backend selection does not waive artifact, protocol or memory-receipt
requirements. Reopening a JSON bundle does not accept a live backend override.

## Requirements before a live confirmation

The command uses **all attached candidates**, in their existing order. There
must be two to four distinct runtime-bound artifacts. Detach unwanted
references explicitly before planning; the command does not silently trim a
larger library. If a selection already exists, the attached set must include
its incumbent artifact.

Review must establish an `exploration-lead`, or a `single-qualified` candidate
with the other declared candidates conclusively ineligible. A library with
only one attached candidate cannot run confirmation. Stale, unresolved or
incompatible exploration must be resolved first.

Confirmation preserves the common exploration protocol:

- Full battery, execution disabled, three to twenty repeats, fixed generated
  checks, timing samples and memory measurement.
- Exact artifact identities, backend/runtime, device, requested and verified
  effective context, and selected profile.
- Task definitions, grading policy, fitr software build and provenance.

There are no confirmation flags for changing context, repeat count, profile,
quality floors or weights. Changing those inputs requires new exploration.
A new fitr build or changed user task pack also requires compatible exploration
before inference can proceed.

The issued plan seals the complete role revision, every preference weight and
anchor, the fixed preference policy with 20% relative weight sensitivity, the
ordered candidate identities, and the preselected exploration evidence digest.
It generates a fresh shared seed and seals the resulting fixed task schedule
and denominators. Issuance is persisted before collecting new evidence.

## Runtime and capacity checks

Each candidate's preparation is compared with the issued plan before
inference. A changed artifact, runtime, build, task schedule, profile or context
stops the attempt. Completed evidence must also match its exact plan position,
verified device/context and fresh seed.

Live confirmation uses the ordinary run lifecycle, including backend unload
attempts to establish uncontaminated measurements. Keeping an incumbent
reference does not keep that model resident during an experiment.

Capacity preparation preserves an exploration policy's explicit operator
budget or reserve. When neither exists, the plan derives an explicit budget
from the tightest role memory ceiling at the requested context. Available
memory is measured again; an old availability observation is not copied into
the fresh run.

Before loading the candidate, fitr requires its prior verified allocation in
the relevant resource domain to be **strictly below** the minimum of current
available memory, the usable policy budget and any container headroom. Missing
availability, an absent usable budget or no remaining headroom blocks the
attempt. An explicit operator budget is not itself a current availability
measurement.

Accelerator bytes are attributed separately from total residency. Host-only
placement uses host allocation; unified memory is counted once in its shared
domain. Partial offloading with a host remainder is currently blocked because
the command does not implement a combined host and accelerator memory policy.
The previous allocation is a minimum resource check for this configuration,
not a full peak-memory prediction or a guarantee that loading will succeed.

## Read confirmation and selection states

Quality, context and capacity floors are evaluated before preferences. A
candidate cannot compensate for a failed floor with a higher utility value.
Preference bounds must establish the preselected choice across the sealed
weight sensitivity. A different fresh winner is reported without replacing
the preselected candidate after seeing the results.

| Confirmation report state | Meaning |
|---|---|
| `confirmed` | The preselected candidate clears the floors and the declared comparison. Explicit adoption is available. |
| `unexpected-winner` | A different candidate wins the fresh comparison. This bundle cannot adopt that replacement. |
| `overlap` | Preference bounds overlap or weight sensitivity changes the choice. |
| `no-qualified-candidate` | Every candidate conclusively fails at least one floor. |
| `unresolved` | A floor or required preference bound remains unresolved. |
| `incompatible` | The validated records do not establish a comparable candidate set. |
| `incomplete` | One or more declared positions or observations are missing. A partial bundle cannot complete an attempt. |

Malformed, substituted or integrity-failing records are rejected as errors,
not assigned a favorable report state. Infrastructure errors stop the attempt
and cannot become valid confirmation evidence.

Confirmation exits 0 for `confirmed`, 3 for `no-qualified-candidate`, and 4
for the other report states. Processing failures exit 1, usage errors exit 2,
and cancellation exits 130.

`role status` reports `unselected`, `qualified` or `stale`. It exits 0 only for
`qualified`; unselected and stale selections exit 4. JSON output includes the
attempt count and `last_attempt`, whose action may be `started`, `completed`,
`failed` or `cancelled`. A completed attempt need not have a confirmed result.

## Expiry, interruption and evidence changes

An issued plan has a **24-hour window**. Collection, completion and adoption
must occur before it expires. After adoption, the selection's evidence expiry
is the earliest confirmation point's start time plus the role's `max_age_days`.
Changing the role revision or losing an exact canonical evidence match can
make the selection stale earlier. Reopening a saved bundle validates its
historical report; it does not renew the plan or selection.

Each issued plan permits one attempt. A handled failure or cancellation records
that terminal state, preserves completed point records and keeps the incumbent
reference. A crash can leave the attempt recorded as `started`, including when
a bundle was saved before the completion event. That attempt cannot resume or
adopt. The CLI has no recovery/resume command; after resolving the cause and
restoring current exploration, issue a new confirmation with a new plan and
seed. `fitr role status coding --display json` exposes the recorded attempt state.

Saving a fresh run replaces that model's canonical current result. Old role
attachments then become stale by design. A retained incumbent can also become
stale when new runs overwrite evidence used by its original selection. Keeping
the incumbent reference does not preserve a qualification claim after its
evidence changes.

Detach stale references by their old evidence digests and attach the new
canonical results before a later exploration review. Prior confirmation
records may serve as exploration for a new plan under normal integrity,
freshness and compatibility checks. They cannot serve as the new plan's fresh
confirmation records. Repeatedly attaching a record does not create trials.

## Adoption, rollback and storage

Adoption revalidates the issued completed plan, the current role revision, the
confirmed report and exact canonical current evidence for **every compared
candidate**. A concurrent lifecycle change cannot silently replace the
incumbent that was present when the plan was issued.

`role adopt` stores fitr's selected configuration and its evidence receipt.
It does not change a serving alias, launch or unload a model, modify another
application's configuration, or delete model files.

`role rollback <name>` targets the previous adoption receipt. Restoration
requires its original role revision, unexpired evidence, exact canonical
records for the original comparison and a still-valid original confirmation.
It does not translate old evidence to a new runtime or policy. If those records
were overwritten, rollback fails; an archived success does not substitute for
current evidence.

Role libraries remain in `<results directory>/.roles/`. Lifecycle sidecars are
stored in `.roles/.lifecycle/` and immutable confirmation bundles in
`.roles/.confirmations/`. Each lifecycle is bounded to 32 issued plans, 256
events and eight MiB; each bundle is bounded to 64 MiB. Lifecycle updates use
locking, digest-checked transitions and atomic private writes. These limits
do not trigger automatic pruning of evidence or history.
