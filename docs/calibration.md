# Calibrating the check battery

Calibration answers a narrower question than a benchmark run:

**Which generated checks detect damage between two quants of the same model on
the same machine?**

This must happen before fitr turns its gates into a broad recommendation. A
task that never distinguishes a healthy reference from a degraded candidate is
cost without evidence. One that flips only on one runtime or device may be
detecting a backend fault, which is useful but is not a universal quant signal.

## Run one controlled pair

Use the same model family and parameter size, a higher-precision reference, a
lower-precision candidate, one unchanged serving runtime, one request context,
and one shared seedset. The checks-only level skips speed, memory, coding,
tools, refusal, and the long agentic loop so a calibration campaign spends its
time on the generated battery. It also excludes local user tasks so independent
reports contain the same canonical task set.

Example with two published tags of the same 8B model:

```bash
ollama pull huihui_ai/dolphin3-abliterated:8b-llama3.1-q8_0
ollama pull huihui_ai/dolphin3-abliterated:8b-llama3.1-q4_K_M

fitr doctor huihui_ai/dolphin3-abliterated:8b-llama3.1-q8_0
fitr doctor huihui_ai/dolphin3-abliterated:8b-llama3.1-q4_K_M
fitr run huihui_ai/dolphin3-abliterated:8b-llama3.1-q8_0 \
  --checks-only --seedset dolphin3-8b-01 -k 5
fitr run huihui_ai/dolphin3-abliterated:8b-llama3.1-q4_K_M \
  --checks-only --seedset dolphin3-8b-01 -k 5

fitr calibrate \
  huihui_ai/dolphin3-abliterated:8b-llama3.1-q8_0 \
  huihui_ai/dolphin3-abliterated:8b-llama3.1-q4_K_M \
  --out dolphin3-8b-pair.json
```

Five repeats are the command default and a campaign floor, not enough evidence
to delete a task. Use `-k 10` for a pair that will inform a battery change.
Both sides must use the same fixed `-k`. Adaptive stopping is rejected in this
mode because it can make the two models see different instances.

`fitr calibrate` rejects results when any of these controls differ:

- seedset or generated instances
- result schema
- hardware, driver, GPU backend, runtime, or resolved server configuration
- request context or inference placement
- model family or parameter size

It orders known GGUF dtypes by precision, reports every item-level flip, and
does not rewrite the task specification.

## Combine independent evidence

Each `--out` file contains only the paired outcomes and the hardware fields
needed to interpret them. It omits the hostname, prompts, model responses,
local result paths, and raw output. The local result JSON under `~/.fitr` is not
a shareable artifact.

Combine pair reports with:

```bash
fitr calibrate merge pair-framework.json pair-rtx.json pair-mac.json \
  --out calibration-summary.json
```

Aggregation deduplicates a device through a stable pseudonymous identifier,
rejects mixed task-spec versions, and reports how many devices observed each
item flip. The identifier contains no hostname but can link reports from the
same device. Aggregation never labels an item safe to remove.

To contribute a decision-grade pair, open the repository's **Calibration
evidence** issue form and attach only the exported pair JSON. The form requires
the controls below and rejects raw local result files as evidence.

## Evidence required before changing the built-ins

A task becomes a removal candidate only after all of the following are true:

1. At least two physical devices and two model families have supplied valid
   pairs.
2. Each decision-grade pair used at least 10 instances per task.
3. The higher-precision reference was healthy across the battery.
4. The lower-precision candidate showed damage somewhere, proving the pair had
   enough contrast to test discrimination.
5. The task never flipped across the collected pairs, or a stronger task in the
   same need consistently subsumed it.

Those conditions make an item eligible for review, not automatic deletion.
The canonical files in `spec/tasks/checks/` change only with a written evidence
summary and the normal self-grading and golden-corpus tests.

Gate calibration is separate. Item flips establish whether a check detects a
difference. PASS and FAIL thresholds require distributions from models and
quants whose real usefulness is already known on each device class.
