# Compare models for your work and machine

fitr's comparison design starts with a person's role, tasks and machine. Public
signals help build a shortlist; local evidence must establish which candidates
meet that role. Primary sources below were checked on September 5, 2026.

## Current behavior and next work

The shipped [role library](roles.md) applies mandatory behavioral, context and
capacity requirements before fixed preference weights and anchors. It retains
missing observations, uncertainty and freshness checks. [Role confirmation](role-confirmation.md)
seals the policy and preselected choice before collecting fresh battery
evidence; adoption records a selection in fitr.

The [connected auto cycle](auto-mode.md) adds bounded owned-runtime fitting in
0.10.11. Public catalog
shortlisting, usable-context attributes, compaction evaluation and named-harness
task scorecards remain future work. The design below does not imply an
OpenRouter, Artificial Analysis or Hugging Face ranking import already exists.

## Use public signals for discovery

| Signal | What it measures | Use in a personal fitting |
|---|---|---|
| OpenRouter rankings | Prompt and completion tokens routed through its platform, excluding private traffic, within a declared time window | Find actively used candidates; preserve the variant, period and population |
| Hugging Face downloads | Requests for designated repository files, including HEAD requests; multiple GGUF files can count separately | Discover repository activity without treating it as unique users or successful local runs |
| Artificial Analysis evaluations | Results under a published, versioned benchmark protocol and composite weighting | Find relevant strengths, then test the actual local artifact and task |

These distinctions follow the platforms' [ranking methodology](https://openrouter.ai/rankings),
[download counting rules](https://huggingface.co/docs/hub/models-download-stats)
and [benchmark methodology](https://artificialanalysis.ai/methodology/intelligence-benchmarking).
Popularity cannot satisfy a quality floor. A hosted score cannot qualify a
similarly named local quantization. Future source displays should retain the
source URL, observation date, methodology version, metric, units and identity
match, with unavailable fields visible.

## Gates first, preferences second

Define required task outcomes, operating context and memory ceiling before
running the shortlist. A faster candidate below any floor stays ineligible.
Among qualified, comparable candidates, apply the person's fixed preference
anchors and weights. Adding a candidate must not rescale everyone else's score.

Keep actual units beside preference values and show overlapping bounds. The
current role sensitivity check is separate from statistical uncertainty; its
propagated bounds are not a joint confidence interval. A public benchmark's
reported uncertainty belongs to its own protocol and evaluated population.
Artificial Analysis explicitly distinguishes composite uncertainty from wider
individual-evaluation intervals. [Uncertainty methodology](https://artificialanalysis.ai/methodology/intelligence-benchmarking)

Missing results remain unknown. An interrupted candidate must stay visible,
without a zero-cost or zero-latency placeholder. Ori Eval provides a useful
pattern: project-specific criteria, comparisons using the same evaluation files,
and explicit unmeasured candidates. fitr's future harness scorecards should add
independent task-result verification and preserve the current fresh-confirmation
requirement. [Ori Eval](https://openrouter.ai/docs/guides/ori/eval)

## Fix the configuration that earned the result

Local comparisons need the exact artifact revision and quantization, runtime
build and settings, device, task definitions and grading policy. Future harness
evidence must additionally identify the harness build, tools, reasoning mode,
tokenizer and compaction policy. Changing those inputs requires new compatible
evidence. The existing [artifact binding](artifact-binding.md) observes local
files; it does not establish what a runtime loaded or how well a task performed.

For a future hosted experiment, pin the provider endpoint and disable fallback;
require support for the declared parameters. OpenRouter's default routing may
ignore unsupported parameters, and performance preferences are softer than
eligibility filters. A requested alias alone cannot describe the configuration
that actually served a comparison. [Provider routing](https://openrouter.ai/docs/guides/routing/provider-selection)

## Measure usable context and retained state

A declared context window is a capacity limit. Input and output share it, so
tool instructions and output reserve reduce the available task payload.
[Context fields](https://openrouter.ai/docs/guides/overview/models)
The shipped context requirement checks the runtime window; usable-context
quality remains a separate proposed measurement.

Test fixed payload tiers and required distant dependencies, then expose the
largest tested tier that passes every required family. Untested lengths remain
unknown. Compaction and restart tests must verify retained constraints, earlier
decisions and completed tool effects under the same harness policy. Neither a
short successful probe nor a plausible summary earns long-context competence.
See the [personal fitting design](personal-fitting.md) for these proposed tests.

## Give cost an honest denominator

The auto cycle's requested-output allowance counts reserved token caps, including
retries. It is distinct from actual tokens, monetary charges and completed work.
Future paid comparisons need both an enforced total budget and recorded charges
for input, output, retries, auxiliary calls and judging. OpenRouter's
`provider.max_price` filters unit prices; prompt and completion limits use
dollars per million tokens, while the corresponding catalog prices use dollars
per token. Other priced components retain their own units.
[Price filters](https://openrouter.ai/docs/guides/routing/provider-selection),
[catalog pricing](https://openrouter.ai/docs/guides/overview/models)

For task comparisons, report total measured cost divided by independently
verified successful tasks, including failed attempts in the numerator. With no
verified successes, that ratio is undefined. Unknown charges stay unknown.
For local runs, keep elapsed time and resource observations separate from any
future measured energy cost. A low token price or fast failed run cannot earn
a better fitting.
