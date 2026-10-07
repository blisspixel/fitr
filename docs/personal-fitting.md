# A personal fitting for local AI

fitr is a tailor for local AI: it turns a general-purpose model into a tested
choice for a person's work and machine. The guided entry is `fitr tailor`.
It seals one plan for the work, the candidates, the context, the effective
task definitions and one capacity policy, then either measures that plan through an explicit local endpoint or,
when the policy is a reserve and an owned runtime is approved, hands the same
settings to `fitr auto`. It does not download models, stop an existing Ollama,
or translate an absolute ceiling into the owned runtime's reserve. A single
candidate stays a non-comparative assessment. The endpoint assessment measures
resident allocation at the planned context. Start and resume refuse task
changes after preview; draft a new plan to approve new task definitions.
Session changes hold one lease for the whole command. The results root may
have an operator-selected alias, while managed directories and session files
must remain physical. Text and JSON status name the delegated auto session
so its measured facts remain reachable.

The workload is sealed before the settings are treated as an answer. A preset
such as repository planning, agentic coding, code review, document research,
or structured extraction expands into visible mandatory requirements,
recommendations, and preferences. The preset name is not the requirement.
Desired context stays the operator's number: a successful 32768-token run does
not satisfy a 250000-token goal, and a 59392-token window is not eligible for
a harness floor of 64000. Meeting a harness floor does not establish a larger
desired workflow. Weight quantization, such as Q4_K_M, and KV-cache precision,
such as q8_0, are separate fields. q8_0 reduces cache memory relative to f16
and does not halve total model and runtime memory. `--kv q8_0` records that
cache type and records flash attention as required on the plan. Quantized
KV is refused without that request. The record does not enable flash
attention on a server that is already running, and a client environment
variable does not prove the server applied the cache type. A configured
window with the short battery prompt is not populated-context qualification. Document
tiers remain payload bytes. A predeclared search can reject rows, and it does
not walk the context downward or adopt another row. No eligible configuration
is a legitimate result. Cloud is not an automatic substitute.

`fitr tailor` status, in text and JSON, carries one recommendation receipt,
`fitr.recommendation.v1`. It currently describes the planned candidate names,
endpoint, configuration and workload. It does not yet project canonical
observations into the recommendation: device identity and tested context stay
absent, and the recommendation stays unresolved even after collection. A
completed point or delegated phase cannot establish those facts. Inspect the
canonical measurements or the delegated `fitr auto status` for measured facts. Projected memory fit,
loading, measured performance, behavioral reliability, and harness
qualification stay separate. A model that loads and answers a simple prompt
while failing a required tool or coding workflow stays unqualified. The
recommendation policy keeps passed, failed and unresolved classes separate;
the current status receipt leaves them unresolved until observations are wired
into that projection.
A weights-plus-KV projection is not tested long-context quality. Unknown
runtime overhead, an unverified cache type, a skipped executable test, and a
small sample stay on the receipt. Conflicting constraints are shown as
tradeoffs. The requested context is not lowered, paid compute is not
substituted, and a quality floor is not relaxed to produce a winner. No
qualified candidate is a result.

Qwen3.6-27B card figures, when the candidate or `--model-card qwen3.6-27b`
names that card, stay labeled as card statements: 262144 native context,
extension toward 1010000 tokens with the appropriate setup, and a vendor
recommendation of at least 128K to preserve thinking. They are not proof that
a quantized build on one GPU delivers them. The Hermes Ollama floor recorded
here is the 64000-token minimum documented by
[Hermes Agent](https://hermes-agent.nousresearch.com/docs/guides/local-ollama-setup)
for agentic work. The card statements were checked against the
[Qwen3.6-27B publisher card](https://huggingface.co/Qwen/Qwen3.6-27B) on
2026-10-06.
Hardware and the serving runtime are discovered for the machine under test.
Context, weight precision, KV precision, and the capacity budget stay
operator settings. A measurement from one GPU is a validation case, not a
product default.

Usable-context floors and preferences are supported in roles and decisions.
`fitr run --context-tiers` collects the document scorecard as its own run, and
owned fitting can collect it beside the battery. `fitr experiment workload
--workflow pi-workspace` seals the pinned harness session. Neither scorecard
is a role floor by itself. See [context task evidence](context-quality.md) and
[workload evidence](workload-evidence.md) for what each measurement does and
does not establish.

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

A local decision model is not part of the fitting that ships. After the
fixed-configuration path, an optional assistant may explain tradeoffs among
options the plan already declared, ask which constraint the operator wants
to change, or draft a finite search for the operator to approve. A suggestion
is not a quality verdict, not fresh confirmation, and not a substitute for
the sealed plan. It does not certify a winner, rewrite the goal, walk the
context downward, or change the serving runtime. Fit arithmetic still runs
with no model in the loop. The assistant stays out of the open live checks
and out of the model-set, soak, coverage, and clean-machine acceptance work.

## Fit and Extended fit

The proposed interface offers two scopes within the same fitting:

| Scope | What it establishes |
|---|---|
| Fit | The current configuration's capacity, observed runtime window, supported behavioral screening and performance |
| Extended fit | Named document and harness workload scorecards, independently verified, with an explicit recovery policy for work spanning compaction or restart |

Fit keeps the behavioral checks already present today. Capacity advice alone
does not establish that full scope. The document scorecard and the pinned
pi-workspace session can be collected. Neither automatically qualifies a
model for unattended use. Each role can eventually require the scorecards
relevant to its work, then apply preferences only after every mandatory floor
passes.

Pi is the first named harness behind an Extended fit workflow: a constrained
workspace change, verified resulting files, forced split compaction, and
reopening the exact saved session without repeating a completed effect.
`fitr experiment workload --workflow pi-workspace` seals that session as
`fitr.pi-session.v1`. The pin is
[@earendil-works/pi-coding-agent 0.85.1](https://github.com/earendil-works/pi/blob/d981de1229ef899957bbe968bc8dcda02a21f477/packages/coding-agent/package.json)
and its [compaction contract](https://github.com/earendil-works/pi/blob/d981de1229ef899957bbe968bc8dcda02a21f477/packages/coding-agent/docs/compaction.md).
fitr checked the split-summary rule there and seals the schedule itself. The
process does not launch Pi, so the pin is not a claim that Pi executed the
trial. A fake provider can pass the file checks and still cannot establish
coverage. A recorded Pi process and a recorded native local-model trial remain
open. See [workload evidence](workload-evidence.md) and the
[recorded boundaries](agent-interop.md#named-host-compatibility).

Show separate outcomes for local screening, document context checks and each
harness workload. A failed challenger preserves a qualified incumbent. A
successful harness test cannot erase a failed capacity or behavioral floor.
Before work starts, `fitr tailor` previews the role, candidates, required
outcomes, context, capacity policy, schedule, repeats, retention, and
permitted actions. It reuses the role schema, the auto journal when an owned
runtime is approved, and the existing renderers. It is not a second
measurement engine. Fresh confirmation stays a later seed, and resume does
not mint one.

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

The role schema supports behavioral, performance, capacity and usable-context
preferences. Its context requirement can verify a runtime window, an explicit
usable-context floor in bytes (`minimum_usable_context_bytes`), or both.
Usable-context preferences evaluate demonstrated tier prefix evidence without
confusing basic runtime window with verified quality.

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

The sequence is the [roadmap](../ROADMAP.md#immediate-build-order). Owned
fitting already collects the document scorecard and confirms it from a fresh
plan. The pinned pi-workspace session is sealed and still lacks a recorded Pi
process and a recorded native local-model trial. `fitr tailor` is the guided
front door over the fitting that exists today. It does not close those live
checks. See [agent interoperability](agent-interop.md) for current host
boundaries and [auto mode](auto-mode.md) for the bounded execution contract.

The extension must reject a large-window model that loses required facts, a
fast model below any mandatory floor, changed harness evidence, lost state
after compaction, and duplicate effects after restart. Preference controls
must explain which gap blocks a choice and preserve uncertainty at every
weight allocation. Public ranking and model self-assessment cannot fill the
missing local evidence.
