# Discovery, experiments, and your model library

The product loop begins with an idea: a post, video, podcast, selected email
excerpt, model card, hosted article, or a model that another tool suggests.
The useful question is what that candidate can do for a particular role on
your machine, with your runtime and agent harness.

## Available in 0.10.4

Capture a private idea without contacting its source:

```bash
fitr discover add https://huggingface.co/Qwen/Qwen3.8-27B --role coding --model qwen3.8:27b --harness "Pi, version to pin" --claim "Reported strong coding; test local tool use"
fitr discover add "podcast episode 42, 18m30s" --role daily-driver --claim "Check the model and quant named in this segment"
fitr discover list
fitr discover plan --role coding
fitr discover list --role coding --display json
```

Model references are user declarations. Capture does not establish that the
alias exists, resolve an artifact, fetch a URL, install a model, or execute a
test. All ideas remain `unmeasured`. A plan prints the next evidence steps;
it does not claim an experiment has run. Different claims, roles, or harnesses
remain different ideas. Identical ideas retain their original capture time.

The inbox lives in `<results directory>/.discovery/`, with one digest-named
JSON file per idea and a 1,000-entry limit. Files use the private results
storage conventions. Source URLs and claims are retained locally, including
URL query strings; use a public permalink or a source label when a link
contains private information. Ordinary output omits the source; explicit JSON
includes it. There is no automatic expiration or mailbox connection.

The supplied source, candidate name, and claim are data. They cannot grant
permission, run a command, install a plugin, or become a recommendation.
Capture accepts HTTP(S) URLs without embedded credentials and plain source
labels. It rejects ambiguous JSON, modified identities, control characters,
oversized fields, and malformed inbox entries instead of silently dropping
them. A digest detects accidental edits; it is not external provenance.

## The next product milestone

The local role library comes before an unattended researcher. Its intended
flow is:

```text
idea -> source check -> exact artifact -> fit plan -> bounded experiment
     -> fresh confirmation -> role review -> adopt, retain, or retire
```

Source claims and popularity help choose what to investigate. Hugging Face
downloads, GitHub activity, an AI search answer, and a compelling video do
not establish local correctness. A source that cannot resolve stays visible
as an unresolved idea; it does not disappear or enter the measured Board.

A candidate configuration needs artifact revision and digest, quant and
shards, vision projector or draft model when used, runtime/build, context and
KV settings, device, and relevant harness configuration. An agentic test also
binds harness version, tools, skills, prompt, permission profile, memory state,
model routing and actual fallbacks. See [agent interoperability](agent-interop.md).

## Best for me, by role

Coding, daily interaction, classification, extraction, vision, research,
compression and fallback can have different incumbents. Eight installed models
do not imply eight useful roles, and eight individually fitting models do not
prove simultaneous residency. A role may share a model with another role.

First apply hard constraints: resource budget, data boundary, required
capabilities, minimum correctness, and authority. Then apply the user's
preferences among eligible, comparable candidates. Preference weights belong
to a versioned role, never to a universal leaderboard. Missing required
evidence remains unresolved; an unknown measurement is not zero.

Before weighted recommendations ship, require:

- Named metrics, direction, units and normalization anchors. Never normalize
  against only the current candidates, which makes adding an option change
  everyone else's apparent quality.
- Separate user preference judgments from deterministic task correctness.
  Refusal behavior, coding correctness and instruction following are distinct
  observations; reducing one behavior does not prove improvement in another.
- Full denominators, uncertainty and sensitivity to plausible weight changes.
  An unstable winner remains a tradeoff, with the next useful experiment shown.
- An incumbent, fallback, evidence links, last review and invalidation reasons
  for each role. Keep the incumbent when the challenger has not earned change.
- Fresh confirmation after exploratory tuning. The observations that selected
  a winner cannot also certify it.

The inbox ships now. Role assignment, weighted recommendations, evidence
attachment, source extraction and automatic adoption are not implemented yet.
Current `fitr decide` evaluates explicit requirements against sealed run
evidence; it does not provide the planned preference-ranking layer.

## Auto mode contract

Programmatic preflight establishes eligibility to attempt a workload, not
quality. A model that loads and produces tokens can still fail the role.
Never silently substitute a smaller model or more aggressive quantization
and carry over the larger configuration's quality evidence.

For long-running agent work, gate on independently verified task outcomes,
reliability across repeated trials, tool correctness, regressions and the
human corrections required. Report completion cost and time including failed
attempts, retries and verification. Tokens per second remains a diagnostic;
it cannot compensate for a failed quality floor. Continuous availability
does not make an unverified result useful.

The role selector needs an explicit `no-qualified-candidate` outcome. Keep a
qualified incumbent, or leave the role unfilled. Offer smaller context,
partial offloading, another artifact, a narrower role or an explicitly allowed
hosted fallback as new experiments. Each configuration must earn its own
quality evidence. An idle 24/7 loop should prioritize the experiment most
likely to resolve a consequential evidence gap, then stop when no useful
experiment fits the remaining budget. It should not keep benchmarking for
activity's sake.

Automation should compose the same inspectable steps. Before it executes,
seal an operator policy covering allowed sources and endpoints, paid API cap,
download bytes and ownership, disk reserve, memory reserve, time, concurrency,
idle-machine requirements and stopping conditions. Default paid spending to
zero. Check the remaining worst-case budget before each call or download.

Separate `research`, `download`, `measure`, `refine`, and `adopt` permissions.
Each phase needs resumable progress, idempotency, cancellation and an audit
record. A local timeout cannot claim remote work stopped. Cleanup may touch
only artifacts owned by that experiment, with ownership written before the
download. Existing user models remain outside automatic cleanup.

LLM-assisted search is an optional source adapter. It reads retrieved material
and produces source-bound claims; it does not answer from remembered model
names. LLMfit or another inventory/fit tool may contribute attributed external
observations after its schema and version are checked. Those observations
cannot replace fitr's runtime-bound local measurements. No such adapter ships
in 0.10.4.

## Qwen example and verification boundary

The official [Qwen3.8-27B card](https://huggingface.co/Qwen/Qwen3.8-27B)
describes a dense vision-language model with native 262,144-token context.
That context limit is not a 24 GB allocation receipt. Exact quant bytes,
projector, cache type, effective context, backend build, speculative draft and
observed residency must be measured together before making a local fit claim.

The [JonathanColetti model card](https://huggingface.co/JonathanColetti/Qwen3.8-27B-Uncensored-GGUF)
describes reduced refusal behavior and retained MTP. The
[Huihui card](https://huggingface.co/huihui-ai/Huihui-Qwen3.8-27B-abliterated)
describes a layer-selective modification. These are candidate-source claims,
not fitr results or a recommendation to choose either variant. Reported
refusal counts, benchmark changes, VRAM use and throughput need their exact
evaluation setup before they can guide a local experiment. Other named merges
remain unresolved until their exact repository, revision and artifact resolve.
