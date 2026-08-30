# Choosing hardware with fitr

fitr is useful for a hardware decision only when it keeps three questions
separate:

1. Can the exact artifact fit at the required context?
2. How did the measured configuration perform?
3. Did it complete the behavior and workload the user actually needs?

Memory capacity, token speed, and validated behavior are different facts. A
large-memory device can fit a model that generates slowly. A smaller model can
generate quickly and still fail the tool or structured-output behavior that
made it worth running. fitr does not combine those facts into a hardware score.

## What fitr can and cannot decide today

fitr can measure the machine in front of it and attach the result to the exact
artifact, runtime, effective context, placement, and configuration that
produced it. `advise` can turn model metadata and a device memory budget into a
context-capacity envelope. Saved runs add exact-context timing observations
only when the artifact and current placement still match.

fitr cannot honestly predict the speed of an unmeasured device from advertised
TOPS, TFLOPS, or memory bandwidth. It also does not know current prices,
resale value, electricity rates, warranty terms, or the value of the user's
time. Those can become explicit scenario inputs later, but they are not
benchmark evidence.

The safe near-term product is a device requirement brief, not a hardware
leaderboard:

```text
TARGET
  artifact       runtime-bound digest
  quant          Q5_K_M
  context        32768
  workload       tool-assisted coding

CAPACITY REQUIREMENT
  artifact       observed
  KV             derived for the named context and dtype
  other resident unknown until measured at that context

BEHAVIOR REQUIREMENT
  coding         required
  tool calling   required
  tool restraint required

PERFORMANCE TARGET
  generation     at least 20 tok/s
  loaded TTFT    at most 1.0 s

CANDIDATE DEVICE
  capacity       eligible / insufficient / unknown
  speed          unknown until this workload is measured
```

## Read the evidence in layers

### Capacity

Capacity answers whether a configuration can run. The components have
different evidence strength:

| Component | Meaning | Evidence label |
|---|---|---|
| Artifact bytes | File or runtime-listed artifact size | observed |
| KV cache | Architecture, context, and KV dtype arithmetic | derived |
| Total allocation | Runtime or allocator observation at a named point | observed |
| Other resident | Total allocation minus artifact bytes minus modeled KV | derived remainder |
| Capacity margin | Configured device budget minus the modeled or observed need | derived or projected |
| Free memory | A transient live device reading | observed now, not a stored constant |

`other resident` is not an independently measured buffer breakdown. It can
include runtime overhead, mappings, allocator effects, and compute buffers.
When total allocation was not observed, the context table projects from the
components it has and keeps the missing remainder visible.

The standard run currently performs a separate requested-32K load probe. New
receipts always retain its requested context and outcome. When the runtime
reports them, they also retain effective context, total bytes, and
accelerator-resident bytes. Older results have only a rounded resident value.
They remain readable, but they cannot establish a verified 32K footprint. The
Result screen labels a verified observation as a requested-32K load probe and
does not present it as memory at the run's ordinary 8K or 16K context.

### Performance

Performance answers how the measured configuration felt:

| Metric | User question |
|---|---|
| Decode | How quickly does the answer stream after prompt processing? |
| Prefill | How quickly does the runtime ingest the prompt? |
| TTFT | How long until the first non-empty output? |
| Load probe wall time | How long did the complete runtime-unloaded probe take? |
| Repeat dispersion | How much did the observation move within this run? |

TTFT needs an explicit cache and residency state. The useful categories are:

- runtime unloaded;
- loaded with a verified cache miss;
- loaded with a verified cache hit;
- loaded with cache state unknown.

Unknown cache state is not the same as an observed zero cached-token count.
fitr now preserves that distinction and will not certify a loaded/uncached
latency gate from an unknown receipt. Runtime-unloaded is also not machine
cold: the operating system may still cache model pages.

### Behavior and workload

Capacity says the model can load. Performance says how quickly it ran.
Behavior says whether the output was usable for the declared need. A hardware
purchase intended for local tool agents needs tool-channel, restraint,
context, and workflow evidence, not just decode speed.

User-defined tasks are the bridge from a generic battery to actual work. A
future workload evidence report will add end-to-end trial timing and bounded
workflow contracts. Until those receipts ship, do not divide the current
mixed battery pass count by total run time and call it productivity or useful
throughput. See [workload evidence](workload-evidence.md).

## Explanation without fake certainty

The explanation layer uses five evidence grades:

| Grade | Meaning |
|---|---|
| observed | Present in a sealed measurement receipt |
| derived | Deterministic arithmetic from observed facts |
| projected | A model-based estimate with named assumptions |
| hypothesis | Consistent with a cause, with alternatives still unresolved |
| unavailable | Required evidence is missing |

Current evidence can support statements such as:

- token generation was the slower observed phase;
- the runtime reported partial accelerator placement;
- decode varied by a named amount across the recorded repeats;
- the projected capacity envelope crosses the configured budget between two
  context points;
- a runtime-unloaded probe took a named wall time;
- a saved timing belongs to this exact artifact and placement.

A large prefill-to-decode ratio does not, by itself, prove a memory-bandwidth
bottleneck. Prefill and decode have different algorithms and parallelism. The
honest explanation is:

> Generation was the slower observed phase. This run does not distinguish
> memory traffic, compute, kernel efficiency, contention, or power limits.

Controlled interventions or hardware counters are required before a stronger
root-cause claim.

## Model shape and placement

Dense and mixture-of-experts models need different labels:

```text
MODEL SHAPE
  architecture       MoE
  total parameters   metadata-derived
  active per token   nominal estimate from routing metadata
  quant              Q5_K_M
```

Total parameters help explain artifact capacity. Nominal active parameters
help describe the compute path. Neither directly predicts speed.

Placement is similarly specific. `GPU 100%` means the runtime reported that
share of resident bytes on the accelerator at the observation point. It does
not prove an exact layer count, exclusive residency on unified memory, or the
absence of every host-memory transfer.

## Experiments that answer different purchase questions

These experiments must remain separate evidence families:

### Context sweep

A controlled sweep needs a sealed point plan, verified effective context,
point-specific placement and allocation, unique prompt prefixes, and repeated
speed observations. Changing the allocated maximum while sending the same
short prompt does not test long-context effectiveness.

### Quant tradeoff

A quant view should show the nondominated choices rather than one winner.
Behavioral attribution requires verified same-base lineage, identical task
instances, matching runtime and context, and enough trials to resolve the
difference.

### Sustained run

A soak experiment reports throughput and validated-outcome drift over time.
Without temperature, clock, or power evidence it may say sustained performance
declined. It may not call the cause thermal throttling.

### Concurrent serving

Concurrency intentionally changes the single-flight protocol. It needs a
separate serving record with aggregate throughput, queueing-inclusive latency,
completion counts, errors, and scheduler configuration. It never enters the
ordinary model Board.

## Buying checklist

Before spending money, pin the decision inputs:

1. Exact target artifact and quant.
2. Required requested and effective context.
3. Workload families and independent validators.
4. Latency and throughput targets.
5. Single-user, sustained, or concurrent operating regime.
6. Required runtime and backend support.
7. Full-accelerator or partial-placement tolerance.
8. Unmeasured requirements that still need a controlled experiment.

Advertised bandwidth, TOPS, price, power, and availability remain sourced
specifications or user assumptions. They never become fitr measurements by
appearing beside them.
