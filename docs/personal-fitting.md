# A personal fitting for local AI

fitr is a tailor for local AI: it turns a general-purpose model into a tested
choice for a person's work and machine. This document describes the next
product direction. Context-quality attributes, harness task scorecards and the
guided fitting flow below are not yet implemented.

## From an idea to an earned choice

1. Capture a model from a post, video, podcast, email or repository. Keep the
   source claim and what the person hopes to use it for.
2. Establish the exact artifact, required components and runtime compatibility.
   Check the machine and reserve before a bounded load probe.
3. Build a small shortlist for a named role. Public evaluations, popularity and
   provider telemetry can help find candidates; they remain source evidence.
4. Run the same declared tasks and configuration policy for each candidate.
   Show quality, usable context, reliability, latency and resource use separately.
5. Apply mandatory floors, then personal preferences with fixed anchors and
   uncertainty. Collect fresh confirmation before changing the selection.
6. Revisit the fitting when the workload, runtime, harness or machine changes.
   Preserve the selected evidence and explain what needs to be measured again.

The inbox, source receipts, local artifact observations and role requirements
already support the beginning of this flow. The connected bounded auto cycle
adds owned Windows runtime collection and confirmation in 0.10.11.
Search-driven shortlisting, automatic discovery and
scheduled reassessment remain future work.

See [model comparison](model-comparison.md) for how public discovery signals,
personal quality gates, uncertainty and cost fit into this flow.

## Your attributes, in meaningful units

A person who works with large repositories or long discussions can prefer
more usable context. A classifier can prioritize consistent labels and low
latency. An unattended coding agent can prioritize verified completion,
retained instructions and recovery. There is no universal distribution of
preference points.

| Layer | Example | Decision rule |
|---|---|---|
| Must have | Complete required task families; preserve instructions; fit the available memory reserve | Every floor must pass |
| Prefer | Larger demonstrated usable context; higher verified completion rate; lower latency | Apply declared weights only to qualified candidates |
| Evidence | Trials, task families, configuration, uncertainty and age | Missing observations stay unknown |
| Confirmation | A new fixed collection for the preselected choice | An uncertain or failed challenge preserves the selection |

The current role schema has behavioral, performance and capacity preferences.
Its context requirement verifies a runtime window; it does not measure how
well the model uses a long prompt. A usable-context preference needs a typed
measurement and schema extension before the interface offers that control.

## Measure usable context

Keep advertised maximum, runtime-accepted window, submitted task content and
task success separate. Count tokens with the applicable tokenizer and record
system/tool overhead plus the output reserve. A short probe accepted by a 128K
window establishes no long-context quality.

At a fixed operating window, use declared payload tiers and rotate important
facts through the beginning, middle and end. Test indirect retrieval, distant
dependencies, instruction retention and a realistic role task. A required
family cannot be hidden inside a passing average. These patterns are informed
by [RULER](https://github.com/NVIDIA/RULER),
[NoLiMa](https://github.com/adobe-research/NoLiMa) and
[LongBench v2](https://github.com/THUDM/LongBench).

The usable-context attribute should be the largest declared, tested payload
tier whose required tiers and families pass the fixed quality rules. Untested
lengths stay unknown. No truncation, smaller window or reduced output reserve
may silently improve the score. Comparing different configured windows needs
a separate declared experiment and fresh confirmation.

## Treat compaction as part of the fitting

A long-running agent includes its model, harness, tools and memory policy.
Test earlier decisions, constraints, completed tool effects and pending work
across forced compaction and restart. Grade the resulting files or state with
an independent verifier. A summary that sounds plausible does not prove that
the agent retained what matters. This follows the outcome-oriented patterns in
[agent evaluation guidance](https://www.anthropic.com/engineering/demystifying-evals-for-ai-agents)
and [long-running harness guidance](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents).

Evidence must identify the harness build, mode, effective provider routes,
tools, tokenizer and compaction policy. A changed route cannot inherit a local
model's qualification. Auxiliary calls, retries and resumed work stay within
the same charged budget; a repeated external effect fails the verifier.

## Build order and acceptance

Build on the connected auto cycle with typed context-quality evidence
and one pinned named-harness workflow. Build the guided fitting interface on
those measurements. See [agent interoperability](agent-interop.md) for current
host boundaries and [auto mode](auto-mode.md) for the bounded execution contract.

The extension must reject a large-window model that loses required facts, a
fast model below any mandatory floor, changed harness evidence, lost state
after compaction, and duplicate effects after restart. Preference controls
must explain which gap blocks a choice and preserve uncertainty at every
weight allocation. Public ranking and model self-assessment cannot fill the
missing local evidence.
