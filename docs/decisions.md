# Decision specifications

A measurement says what happened. A decision says whether that evidence is
enough for one declared workload. fitr keeps those objects separate so a user
can change a requirement without rewriting history or pretending a new run
occurred.

```text
sealed result
    -> validated renderer-neutral analysis
    -> decision specification
    -> requirement evaluation
    -> eligible | ineligible | unresolved
```

The source scorecard remains the historical screening verdict under the
profile and scoring policy sealed with the run. `fitr decide` derives a new
`fitr.decision.evaluation.v1` document. The evaluation points back to the
source evidence digest and contains a typed `fitr.configuration.v1` subject.
It is analysis, not new measurement evidence.

## Command

```bash
fitr decide [model|result.json] --spec decision.json
fitr decide qwen3-coder:30b --spec local-coding.json --display json
```

With no model or path, the newest saved result is selected. A model selects
its newest canonical result. A path reads that exact saved result through the
normal record validator.

The command returns:

| Exit | Meaning |
|---|---|
| 0 | every requirement is established |
| 1 | the spec or sealed evidence could not be processed |
| 2 | invalid invocation |
| 3 | at least one requirement is disproven |
| 4 | no requirement is disproven, but required evidence is unresolved or blocked |

JSON output is the complete derived evaluation. Plain output shows the same
states, observed values and intervals, missing evidence, and at most one next
evidence action.

## Schema

Every requirement has an `id` and exactly one typed body.

```json
{
  "schema": "fitr.decision.spec.v1",
  "name": "local coding",
  "evidence_level": "decide",
  "requirements": [
    {
      "id": "tool reliability",
      "behavior": {"need": "tool_calling", "minimum_rate": 0.90}
    },
    {
      "id": "tool protocol",
      "capability": {"name": "tools", "minimum_support": "protocol_verified"}
    },
    {
      "id": "context",
      "context": {"minimum_effective_tokens": 16384}
    },
    {
      "id": "loaded response",
      "performance": {"metric": "loaded_ttft_seconds", "at_most": 1.0}
    },
    {
      "id": "resident budget",
      "capacity": {"maximum_resident_gb": 22, "requested_context": 16384}
    }
  ],
  "fallback": {
    "unresolved": "cloud",
    "disproven": "reject"
  }
}
```

The loader accepts exactly one JSON value, rejects unknown fields, and caps a
spec at 1 MiB. Duplicate IDs and a requirement containing more than one kind
are errors.

## Evidence levels

| Level | Meaning |
|---|---|
| `screen` | cheap broken-versus-plausible evidence |
| `decide` | evidence planned to establish or refute workload requirements |
| `confirm` | fresh evidence for a candidate selected during exploration |
| `calibrate` | paired evidence designed to estimate a treatment effect |
| `operate` | sustained or concurrent operational evidence |

An ordinary run does not carry exploration-to-confirmation lineage. A
`confirm` spec therefore remains unresolved even when its substantive
requirements clear. This prevents an exploratory winner from certifying
itself on the observations that selected it. `fitr experiment confirm` is the
decision-bearing path: it seals two to four exact runtime-backed candidates,
the device, context, protocol, spec digest, and a fresh shared task seed before
collecting a new full battery for each candidate.

## Requirement semantics

### Behavior

Without `minimum_rate`, behavior requires the sealed need verdict to be PASS.
With `minimum_rate`, the evaluator reconstructs the raw pool and applies the
same family-clustered 95 percent interval used by the scorer. Current rate
estimands are:

- `structured_output`
- `instruction_precision`
- `reasoning`
- `tool_calling`
- `tool_restraint`
- `user_tasks`

The lower bound must clear the declared minimum to establish the requirement.
The upper bound must fall below the minimum to disprove it. Otherwise it is
unresolved. Planned unscorable observations and a single repeated family
cannot establish a broader need. Tool restraint also honors a sealed protocol
failure such as continued calls to a withdrawn tool.

Behavior need names are validated. Runtime-declared `vision` cannot be used as
a behavioral requirement, including on legacy records where that declaration
historically appeared as PASS. Use a capability requirement for availability;
image competence remains unresolved until an image task protocol is measured.
Profile-bound `fast_and_decent` and `low_footprint` screening rows are also not
behavior requirements. Declare exact performance and capacity limits instead,
so changing an operator threshold reevaluates evidence rather than inheriting
the historical device profile.

### Capability

Capability support is not task competence:

| Support | Establishes |
|---|---|
| `declared` | the runtime says the capability is available |
| `protocol_verified` | fitr completed the capability's protocol plumbing probe |

Neither state proves that the model performs a real vision, tool, audio, or
other task correctly. That requires behavior or workload evidence.

### Context

Context uses runtime-verified effective tokens. The requested value alone
cannot establish a minimum. A runtime adjustment can therefore disprove or
leave unresolved a requirement even when the model metadata advertises a
larger window.

### Performance

Supported metrics are:

- `decode_tps`
- `prefill_tps`
- `request_ttft_seconds`
- `loaded_ttft_seconds`

Every performance requirement has exactly one `at_least` or `at_most` bound.
Loaded TTFT requires the gated request's residency support claim. A fast
request with unknown residency remains visible but cannot establish loaded
responsiveness. When repeated measurements carry a standard deviation, the
95 percent t interval must clear the bound. A point estimate on the preferred
side of the threshold is unresolved when its interval crosses that threshold.

### Capacity

Capacity accepts exactly one of `maximum_resident_gb` or
`maximum_resident_bytes`. If `requested_context` is present, the runtime
allocation receipt must be for that exact requested and verified effective
context. The requirement compares observed resident allocation with an
operator limit. It does not convert nominal device capacity, current free
memory, or a weights-plus-KV projection into observed fit.

## Configuration identity

The derived configuration binds:

- requested and runtime-resolved model identity
- content-addressed artifact identity and runtime binding
- backend and runtime
- quant, family, and parameter-size metadata when supplied
- requested and verified effective context
- conservative comparability key when available
- exact-context runtime placement attribution when available

Future experiment schemas will extend this typed identity with treatment and
required-equal factors such as KV type, reasoning policy, sampling, parser,
template, speculative decoding, and workflow authority. They will not place
those fields in an untyped universal map.

## Objective and next evidence

An objective such as minimizing latency or maximizing measured throughput is a
comparison across eligible candidates. One result can establish eligibility,
but it cannot establish a winner. A spec containing `objective` therefore
remains unresolved until a compatible candidate-set comparison exists.
Selection also remains unresolved while any candidate named by the experiment
has unresolved eligibility. fitr cannot discard an unmeasured or inconclusive
candidate merely because two other candidates are ready to compare.
Likewise, when several candidates are eligible, every one must carry a bounded
objective metric. If exactly one candidate clears all constraints after every
candidate is resolved, it is selected by eligibility alone. If none clears,
the experiment reports that there is no eligible candidate.

Configuration comparison currently accepts `decode_tps`, `prefill_tps`,
`request_ttft_seconds`, `loaded_ttft_seconds`, `resident_bytes`, and
`artifact_bytes` as objective metrics. An unknown or misspelled metric is
rejected instead of becoming a permanently unresolved experiment.

For an unresolved evaluation, fitr selects at most one next action in this
order:

1. establish effective context or exact-context capacity
2. establish required capability or behavior
3. establish the exact performance state
4. collect fresh confirmation evidence

The action is semantic and deterministic. It never changes the sealed source
record and never turns missing evidence into an estimate.

## Profile compatibility

The historical profile format carries several meanings in one file. The
internal compatibility adapter now separates them into:

- measurement protocol from the sealed manifest
- grading policy from behavioral and performance screening gates
- device calibration and selection hints
- resident-memory capacity policy
- presentation preset
- explicit decision specification

The last item is never inferred from the profile. A tool reliability threshold
is a workload requirement, not a property of a GPU, and changing it does not
change the raw tool observations.
