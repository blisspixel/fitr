# A personal model library, by role

Roles make the quality and resource requirements explicit before comparing
preferences. A fast model that fails a quality floor is ineligible. A model
with missing, uncertain or stale evidence remains unresolved.

## Start with a job

```bash
fitr role init coding --quality user_tasks --memory-gb 22 --ctx 8192
fitr role attach coding /path/to/canonical-result.json
fitr role review coding
fitr role list
```

`--quality` selects the behavioral evidence that matters for the job.
`user_tasks` needs your independently checked task pack. For a classifier,
test held-out labels and error cases; `structured_output` checks output
structure and does not establish classification accuracy. Coding needs
correctness checks for the actual work, not only a tool-call or formatting
test. The initial minimum rate is 0.90, with evidence at most 30 days old.
Both are explicit options: `--minimum-rate` and `--max-age-days`.

Rate-based initialization supports `user_tasks`, `structured_output`,
`instruction_precision`, `reasoning`, `tool_calling` and `tool_restraint`.
Composite needs such as `coding` do not have a measured rate. Use `role define`
with a `required_state` requirement when you need a composite PASS gate, and
choose a separate numeric preference. A role name does not select its tests.

The context floor and memory ceiling are mandatory. Memory is an observed
resident-allocation limit at the declared context, expressed in GiB by
`--memory-gb`. It does not infer that several individually fitting models
can stay resident together.

Roles evaluate the existing battery evidence contract. They do not
establish broad coding competence, full agentic-workflow reliability or
compatibility with a named external harness. Fixed policy-repair workload
bundles remain separate evidence. [Role confirmation](role-confirmation.md)
can establish a fresh selection under the declared battery policy; it does
not turn those screens into workflow competence evidence.

## Tune preferences without rewriting evidence

```bash
fitr role show coding > coding-role.json
# Edit the requirements or preferences, then seal a new revision:
fitr role define coding-role.json
fitr role review coding --display json
```

`role show` emits the editable JSON definition. `role define` preserves
previous revisions and keeps the candidate set, then every review reevaluates
the original evidence under the selected revision. The shipped
[coding example](../examples/roles/coding.json) includes quality, latency and
memory preferences. Its limits are a starting configuration to edit, not
hardware detection or a recommendation for every machine.

Every preference references a numeric requirement and declares `weight`,
`worst` and `best` anchors. Values between the anchors map linearly to zero
through one, with values beyond them capped at the ends. Anchors stay fixed
when candidates enter or leave the library. Behavior uses fractions,
performance uses the named metric's units, and memory uses bytes even when
its requirement was written in GiB. No universal model score is produced.

Only candidates that clear every quality and resource floor can receive a
preference assessment. Missing metrics are not zero, and a single timing
sample does not gain an invented zero-width uncertainty interval. The review
propagates per-metric bounds and checks simultaneous independent weight
changes of up to 20% in either direction. These propagated bounds are not a
joint confidence interval.

## Read the result

| State | Meaning |
|---|---|
| `empty` | No candidate evidence is attached. |
| `no-qualified-candidate` | Every declared candidate definitively fails at least one floor. |
| `unresolved` | Some eligibility, freshness, comparison or preference evidence is missing. |
| `single-qualified` | Exactly one candidate clears the screening floors; this is not proof of a comparative winner. |
| `tradeoff` | Qualified comparable candidates overlap in their bounds or change order under weight sensitivity. |
| `exploration-lead` | One qualified candidate leads across bounds and all tested weight perturbations. Fresh confirmation is still required before adoption. |

The comparison retains the existing requirements for runtime, device,
requested and effective context, task and grading protocol. Adding an
unresolved candidate cannot silently improve the apparent certainty of a
winner. Different quantizations must earn their own evidence.

Review exits 0 for `single-qualified` or `exploration-lead`, 3 when every
candidate fails, and 4 for empty, unresolved or tradeoff states. Exit 0 does
not authorize an automatic model switch. Usage and processing errors retain
the usual exit codes 2 and 1.

## Evidence ownership and freshness

The library lives in `<results directory>/.roles/`. Each role keeps up to 32
definition revisions and 64 digest-pinned candidate references; the directory
supports 64 roles. Each library JSON is bounded to one MiB. Writes are locked
and atomic, with the private results-store permissions.

An attachment references the model's canonical current result and its exact
completion digest. It does not copy or weaken the evidence store's trust
contract. Repeated attachment does not create extra trials. Every review
reloads and validates the source; a replacement, deleted file, expired result
or copied archived success cannot silently become current qualification.
Absolute source paths appear in explicit library JSON, not ordinary output.

Remove a reference without deleting its source evidence:

```bash
fitr role detach coding sha256:<full-evidence-digest>
```

## Confirm and retain a selection

With two to four compatible full-run candidates attached, collect fresh
evidence after sealing the current role, preferences and selected candidate:

```bash
fitr role confirm coding
# Use the exact saved bundle path printed by the command:
fitr role adopt coding /path/to/saved-bundle.json
fitr role status coding
fitr role rollback coding
```

<img src="assets/selection.svg?v=0.10.9" alt="Role status fixture with a qualified incumbent and a separate failed challenger attempt" width="900">

Confirmation keeps the incumbent reference until explicit adoption. Adoption
stores fitr's selection; it does not change a serving configuration. Rollback
requires the previous adoption's original evidence to remain current and
valid. Fresh runs overwrite canonical results, so previous attachments or
selections can become stale even when their references are retained.

See [role confirmation and lifecycle](role-confirmation.md) for the fixed
protocol, runtime and memory preflight, 24-hour plan window, evidence expiry,
failure states and rollback restrictions. Existing narrow-objective experiment
bundles cannot be relabeled as confirmation of role weights chosen after a run.
