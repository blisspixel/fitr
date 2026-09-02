# Workload evidence and bounded workflows

The model is not always the largest useful unit to evaluate. Real work can
include research, context transfer, decisions, tools, execution, verification,
state updates, retries, and escalation. fitr's broader product question is:

> What local AI actually works for this workload on this machine, and what
> evidence proves it?

That does not require fitr to become an orchestration platform. It requires a
bounded workflow to become a first-class experiment with a stable contract,
an authority envelope, and independent proof.

## What exists today

The current task battery already provides strong foundations:

- deterministic Go graders for generated checks;
- explicit task families, needs, seeds, and immutable denominator plans;
- tool calls observed in the real tool channel;
- bounded turn counts, workspaces, filenames, and file sizes;
- tool withdrawal and call-restraint observations;
- terminal-state, call-count, malformed-call, and repetition receipts;
- unisolated executable observations kept INCONCLUSIVE rather than promoted to
  PASS or FAIL.

The current result also seals one aggregate battery-core duration. It does not
seal per-trial wall time, retry and fallback attempts, validator duration,
tool-step duration, human intervention, endpoint locality, or energy. Those
missing receipts are why current history cannot honestly produce time to valid
result or validated work rate.

## The worker does not own the evidence

Every workflow verdict should name its proof class:

| Proof class | Meaning | Can establish core PASS/FAIL? |
|---|---|---|
| deterministic | Exact assertion or mechanical grader owned by the harness | yes |
| external state | A named system-state observation outside the worker output | yes, within its stated boundary |
| independent verifier | A separately isolated verifier with a sealed identity | yes |
| harness state machine | The harness observed a bounded protocol property | only for that narrow property, not workflow completion |
| heuristic classifier | Reproducible rule without ground-truth verification | no; labeled observation only |
| model judged | Another model evaluated the output | no; labeled observation only |
| self reported | The worker claimed completion | no |
| none | No proof exists | no |

A model saying `DONE` is not evidence that the repository, file, ticket, or
system reached the required state. The validator identity, policy hash,
isolation status, and accepted artifact or state digest belong in the receipt.

## Workflow task contract

A bounded workflow specification should define the work before execution:

```text
intent
initial state
available context
allowed tools and scopes
forbidden actions
definition of done
independent verification
time, turn, write, and retry budgets
escalation conditions
checkpoint and resume policy
approval authority and denial, timeout, and revocation behavior
```

Example:

```text
WORKLOAD
  repository bug fix

AUTHORITY
  may inspect repository
  may edit src/
  may run tests
  may not modify tests

DONE WHEN
  target regression passes
  complete suite passes
  requested behavior exists
  unrelated files are unchanged

ESCALATE WHEN
  requested behavior conflicts with the public API
```

The workflow fingerprint must include the exact model artifact, quant, runtime,
device, effective context, scenario and fixture identity, seed, initial-state
digest, task and workflow specification hashes, tool definitions and service
versions, context-provider version, dynamic-context event manifest,
verification policy and execution environment, authority profile, state
schema, worker and harness builds, sampling, cache and residency state,
concurrency, retry and stopping policy, endpoint/data-boundary class, and
checkpoint lineage and resume environment. Changing one creates a different
experiment.

## Time to valid result

Two clocks answer different questions.

`workflow time to valid result` is:

> Monotonic time from scenario handoff or release until the named independent
> validator accepts a terminal result under the declared protocol.

It includes context acquisition, runner startup, queueing, invalid attempts,
repairs, tool execution, verification, and approval or escalation delay.
`attempt latency` starts immediately before the first inference and ends at the
terminal attempt. Load and cache state remain separate protocols. Explicit
invalid terminal outcomes are unsuccessful outcomes. Timeouts and cancellations
may be right-censored when the protocol supports that analysis.

```text
valid by 30s        17/20
time to valid       median 8.4s among 17 accepted
timeouts            2
invalid at budget   1
```

Reporting latency only among successes without the complete outcome counts is
selection bias. A p95 from a handful of cases is not useful evidence.

fitr decision latency is a different metric: how long the instrument needed
to produce sealed evidence. The current `wall_s` is only battery-core wall
time and must not be relabeled as either end-to-end run latency or workload
time to valid result.

## Validated work rate

Validated work rate is:

```text
accepted outcomes / complete scenario wall time
```

The time includes failures, repairs, timeouts, and operational faults. Under
concurrency it uses experiment exposure wall time, not the sum of overlapping
scenario durations. It is
not pass rate multiplied by decode speed. It is comparable only within one
homogeneous workload pack and the same validator, authority envelope,
protocol, context, cache state, stopping policy, and concurrency regime.

Different built-in battery tasks are not interchangeable work units. fitr
will not combine them into quality-adjusted tokens, a productivity score, or
a universal outcomes-per-hour number.

## Scenario coverage

Coverage is a matrix, not a percentage unless the user declares a finite
scenario census:

```text
invoice extraction
  ordinary cases       8/8 executed
  missing fields       4/4
  locale variants      3/3
  malformed inputs     5/5
  scanned documents    not represented
```

A future workload pack needs a version, scenario families, generators,
validators, budgets, privacy policy, and explicit omissions. One user prompt
proves one declared shape, not broad coverage of a profession or subscription.

Built-in tasks are representative evidence. User-defined tasks can be direct
local evidence for the task they declare. Doctor is infrastructure evidence.
Concurrency, sustained operation, retrieval integration, and state resume
remain unmeasured until their own protocols run.

## Bounded autonomy

Autonomy is not one capability. A workflow report should keep separate:

```text
recommend only
act after approval
complete bounded work
recognize an escalation boundary
respect permission withdrawal
resume a checkpointed task
standing responsibility     out of scope for a bounded fitr experiment
```

Each row receives PASS, FAIL, INCONCLUSIVE, SKIP, or UNMEASURED from its own
test. Tool withdrawal is evidence about restraint under a changing tool set;
it is not yet evidence of human approval handling or escalation.

Approval receipts identify who may grant authority and record request, grant,
denial, timeout, and revocation events. They state what the worker did after a
denial or missing response, whether escalation consumed human intervention,
and whether every action stayed inside the original envelope. Earned authority
is valid only for the exact workflow fingerprint and permission profile. It
never generalizes to standing autonomy.

## State and dynamic context

A resumability experiment needs state hashes, checkpoint lineage, active
duration, downtime, recovery duration, and downstream assertions that prove
the objective and completed work survived the reset.
It also proves that already-committed side effects were not duplicated.

The current tool-loop `Compacted` field observes only that reported prompt
tokens later became smaller. That can reflect truncation, accounting changes,
runtime policy, or deliberate compaction. The safe name is `observed
prompt-token contraction` until an explicit context-action receipt and a
state-preservation verifier exist.

Dynamic-context tests should distinguish relevant facts, distractors, stale
and current facts, late-arriving facts, context actions, and verified
preservation. A context that fits is not automatically a context that helps.

## Operational evidence

Model validity and system reliability remain separate. Infrastructure errors
do not become model failures, but a device decision still needs to know that
the system failed to complete work.

Three evidence families answer different questions:

1. Run repeatability from short within-run observations.
2. Run-to-run drift only across exact artifact, placement, runtime, context,
   and protocol matches.
3. Sustained stability from a dedicated soak experiment.

A decline without temperature, power, or clock evidence is sustained
performance decay, not proven thermal throttling.

Scenario receipts preserve scenario release, worker start/end, tool intervals,
verifier queue/start/end, independent acceptance, approval, and escalation
timestamps. Analysis separates worker, tool, queue, verifier, and human-wait
time. If verification is limiting accepted throughput, fitr names that stage
instead of blaming model generation.

## Evaluator boundary

fitr owns contract validation, immutable experiment identity, receipt
ingestion and sealing, verifier policy, and renderer-neutral analysis. A
bounded external runner owns execution and scheduling. fitr does not become a
durable work queue, secret store, approval service, organizational state store,
or autonomous operator.

The 0.11 experiment may validate one fixed, harness-owned workflow in a
constrained fixture. Arbitrary generated code and executable user-task JSON
remain SKIP until the stronger cross-platform confinement contract passes.

## Data boundary

Use `data boundary`, not a privacy score. A future receipt should classify the
endpoint as loopback, LAN, or remote; identify whether TLS was used; and state
raw-storage, retention, and export policy without persisting credentials or a
full sensitive URL.

Current canonical results and private history can contain raw prompts,
responses, hostname, and device details. History has no automatic retention.
The presentation snapshot and HTML share view are allowlisted. HTML omits raw
model output, hostname, local paths, arbitrary environment values, and the raw
device key. A remote OpenAI-compatible endpoint still sends workload data
outside the machine; backend shape alone is not a privacy guarantee.

## Energy and economics

Energy evidence needs an explicit sensor source, measurement domain, method,
coverage, duration, and joules. Accelerator-only telemetry is not whole-system
energy. TDP is not a measurement. If no trustworthy sensor exists, energy is
UNMEASURED.

Economics is an optional local scenario with user-supplied, dated assumptions:
purchase price, currency, lifetime, utilization, electricity tariff, resale,
and alternative spend. Every result shows its formula and distinguishes
measured inputs from assumptions. Human time, subscription replacement, and
productivity are never inferred by fitr. Economics assumptions stay out of
shareable exports unless explicitly selected.

## Planned workload evidence report

```text
WORKLOAD EVIDENCE
  pack              invoice-extraction v3
  protocol          loaded / uncached, serial
  validator         invoice-schema v2, deterministic

SCENARIO COVERAGE
  planned           20
  executed          20
  omitted           scanned documents

OUTCOMES
  valid             17/20
  invalid            1
  timeout            1
  infrastructure     1

TIME TO VALID RESULT
  median             8.4 s among 17 accepted
  valid by 10 s      12/20

VALIDATED WORK RATE
  3.7 accepted outcomes/min
  includes failures, repairs, and timeout budgets

AUTHORITY
  bounded completion under invoice-worker-v2   passed
  escalation         unmeasured
  resume              unmeasured

DATA BOUNDARY
  endpoint           loopback local
  raw evidence       retained locally
  shareable export   aggregates only
```

No single score is produced. A configuration earns only the capabilities and
workflow states its independent evidence supports.
