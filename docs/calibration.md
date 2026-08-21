# Calibrating the check battery

Calibration answers a narrower question than a benchmark run:

**Which generated checks distinguish two artifacts on the same machine when
they face identical generated instances?**

This must happen before fitr turns its gates into a broad recommendation. A
task that never distinguishes a healthy reference from a candidate is
cost without evidence. One that flips only on one runtime or device may be
detecting a backend fault, which is useful but is not a universal quant signal.
Attributing a difference to quantization additionally requires sealed evidence
that both artifacts descend from the same exact base-model revision.

## Run one controlled pair

Use two artifacts the operator believes share one base revision, one unchanged
serving runtime, one request context, and one shared seedset. That belief is an
operator hypothesis until an authoritative derivation receipt proves it.
Matching model family and parameter size is only a coarse preflight check, not
lineage proof. The checks-only level skips speed, memory, coding, tools,
refusal, and the long
agentic loop so a calibration campaign spends its time on the generated
battery. It also excludes local user tasks so independent reports contain the
same canonical task set.

Example with two published tags that an operator might hypothesize share one
8B base revision:

```bash
ollama pull huihui_ai/dolphin3-abliterated:8b-llama3.1-q8_0
ollama pull huihui_ai/dolphin3-abliterated:8b-llama3.1-q4_K_M

fitr doctor huihui_ai/dolphin3-abliterated:8b-llama3.1-q8_0
fitr doctor huihui_ai/dolphin3-abliterated:8b-llama3.1-q4_K_M
fitr run huihui_ai/dolphin3-abliterated:8b-llama3.1-q8_0 \
  --checks-only --seedset dolphin3-8b-01 -k 10
fitr run huihui_ai/dolphin3-abliterated:8b-llama3.1-q4_K_M \
  --checks-only --seedset dolphin3-8b-01 -k 10

fitr calibrate \
  huihui_ai/dolphin3-abliterated:8b-llama3.1-q8_0 \
  huihui_ai/dolphin3-abliterated:8b-llama3.1-q4_K_M \
  --out dolphin3-8b-pair.json
```

Five repeats are the command default and an exploratory campaign floor, not
enough evidence to review a task. Use `-k 10` to meet the controlled-pair sample
floor, but do not treat an unsigned exported pair as authenticated evidence.
Both sides must use the same fixed `-k`. Adaptive stopping is rejected in this
mode because it can make the two models see different instances.

`fitr calibrate` rejects results when any of these controls differ:

- seedset or generated instances
- result schema
- hardware, driver, GPU backend, runtime, or resolved server configuration
- request context or inference placement
- model family or parameter size

It orders known GGUF dtypes for display, reports every item-level flip, and
does not rewrite the task specification. The order is not a causal claim.

Same-base lineage is a separate, independently checkable receipt. Matching
family, parameter size, and dtype rank is still only a preflight. A pair
becomes lineage-verified only when both runs have runtime-bound artifact
digests and a derivation binds those exact digests to one base revision:

```bash
fitr calibrate \
  huihui_ai/dolphin3-abliterated:8b-llama3.1-q8_0 \
  huihui_ai/dolphin3-abliterated:8b-llama3.1-q4_K_M \
  --lineage dolphin3-8b-conversion.json \
  --out dolphin3-8b-pair.json
```

The conversion manifest is `fitr.lineage.conversion.v1`. It names one
`base_revision` SHA-256 and every derived artifact digest:

```json
{
  "schema": "fitr.lineage.conversion.v1",
  "base_revision": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "artifacts": [
    {"digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "role": "base", "quant": "F16"},
    {"digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "role": "derived", "quant": "Q8_0"},
    {"digest": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "role": "derived", "quant": "Q4_K_M"}
  ]
}
```

Operator belief, Hugging Face names, and GGUF `base_model` URLs are not lineage. If both
Ollama blobs are local GGUFs whose metadata already stores the same
`general.base_model.0.sha256` (or `general.source.sha256`) and those files
hash to the runtime-bound digests, `fitr calibrate` attaches
`gguf_base_digest` lineage without `--lineage`. A pair signature still cannot
manufacture a missing receipt. Unsigned lineage-verified pairs remain
exploratory for campaign readiness.

## Combine independent evidence

Each `--out` file contains only the paired outcomes and the hardware fields
needed to interpret them. It omits the hostname, prompts, model responses,
local result paths, raw output, and the user-chosen seedset name. Device and
seedset identifiers are separate stable domain hashes, so aggregation can
deduplicate them without publishing the underlying local values. Absolute or
GGUF model paths receive a separate stable pseudonym, and configured helper
executables are recorded only as `configured`. The local result JSON under
`~/.fitr` is not a shareable artifact.

Combine pair reports with:

```bash
fitr calibrate merge pair-framework.json pair-rtx.json pair-mac.json \
  --out calibration-summary.json
```

Aggregation deduplicates a device through a stable pseudonymous identifier,
rejects mixed task-spec versions, and reports how many devices observed each
item flip. The identifier contains no hostname but can link reports from the
same device. Aggregation never labels an item safe to remove.

`fitr calibrate` reports whether a local pair meets the controlled sampling and
health criteria. The privacy-safe exported JSON is unsigned, so its device and
model-family assertions are unverified until an external trust policy names
the signer. `fitr calibrate merge` accepts unsigned files as exploratory leads
and never counts them toward verified campaign readiness. A trusted signature
seals the claims present in the report, including a lineage receipt when one
was attached. Decision-grade still requires that receipt, the k=10 floor, a
healthy higher-precision reference, contrast on the candidate, two devices,
and two model families. A signature without lineage stays exploratory.

To contribute an exploratory pair, open the repository's **Calibration
evidence** issue form and attach only the exported pair JSON. The form requires
the controls below and rejects raw local result files. An attachment is a lead
for written review, not authenticated evidence.

## Evidence required before changing the built-ins

A task becomes a removal candidate only after all of the following are true:

1. Every pair has a `fitr.lineage.same-base.v1` receipt binding both
   runtime-bound artifact digests to one exact base-model revision, verified
   under an external trust policy. Publisher conversion manifests and matching
   GGUF base-digest metadata are the accepted derivations.
2. At least two physical devices and two model families have supplied reviewed,
   authenticated pairs.
3. Each qualifying pair used at least 10 instances per task.
4. The dtype-ranked reference was healthy across the battery.
5. The candidate had fewer passes somewhere, proving the pair had enough
   contrast to test discrimination.
6. The task never flipped across the collected pairs, or a stronger task in the
   same need consistently subsumed it.

Those conditions make an item eligible for review, not automatic deletion.
The canonical files in `spec/tasks/checks/` change only with a written evidence
summary and the normal self-grading and golden-corpus tests.

Gate calibration is separate. Item flips establish whether a check detects a
difference. PASS and FAIL thresholds require distributions from models and
quants whose real usefulness is already known on each device class.
