# Terminal design language

fitr should make the next decision clear before exposing the full receipt.
The terminal is a product surface: role, configuration, evidence state and
next action lead; implementation details belong in inspection views.

## Product character

fitr is a tailor for local AI. The model is the starting material; the fitting
is specific to the person's machine, work and preferences. Express that care
through useful defaults, clear measurements, restrained choices and follow-up.
Avoid luxury decoration or confidence that the evidence has not earned.

The [mark](assets/fitr-mark.svg) is an open measuring frame with a separate
fitted block. The space around the block represents room for context and
runtime overhead; the opening represents a configuration that can be refined.
It is an identity mark, never a qualification badge. Use the transparent cyan
SVG on light or dark surfaces, preserve its clear space, and pair it with the
[lowercase wordmark](assets/fitr-lockup.svg) on introductions. Terminal output
keeps the text name instead of approximating the mark with ambiguous glyphs.

## Shared rules

- Use lowercase `fitr` product language, consistent gutters and a restrained
  palette. Color reinforces a written state and never carries meaning alone.
- Let role and model names lead. Keep hashes and internal schema identifiers
  in detailed receipts rather than competing for attention in the default view.
- Give each block a clear reading order and one next action. Use whitespace
  for grouping; avoid enclosing every metric in a box.
- Keep honest states visible: unmeasured, unresolved, blocked, stale, failed
  and independently accepted are different. An appealing layout cannot
  promote a claim or hide a missing denominator.
- Ordinary CLI reports use the shared 80-column default and adapt down to
  40 columns. Wide TUI views use master-detail layouts for comparison, while
  narrow views preserve the same facts in a single column.
- Preserve plain text, ASCII, `NO_COLOR`, pipes, explicit JSON and predictable
  exit codes. Do not add animation to completed reports or progress to stdout.
- Sanitize untrusted text before rendering. Command recipes must preserve
  argument boundaries for the target shell.

## Color and motion

Cyan emphasizes focus and active work, amber marks gaps and warnings, green
marks accepted outcomes, and red marks failures. Each state also has a text
label. The live TUI adds a four-frame ASCII activity marker while a run is
active. It indicates activity, not percentage complete or healthy output.
Terminal states stop it, and pausing the viewport freezes it while measurement
continues. Set `FITR_REDUCED_MOTION=1` for static status with elapsed-time
updates. Motion never enters JSON, saved receipts or ordinary CLI reports.

## Discovery and role-library direction

The current discovery cards show role, candidate, `[UNMEASURED]`, a source
claim, optional harness and the next planning command. A copyable idea ID lets
the user attach a receipt without searching JSON. Attached metadata uses separate
repository, commit and receipt fields; it does not replace the original model
hint. Plans expose dependency, runtime and quality gaps independently. Original
source URLs remain private unless explicitly requested as JSON. The same
proposal objects feed text and JSON plans; the renderer does not make a
compatibility decision.

Role review cards now lead with the role and screening state, followed by
each candidate's requirements, preference bounds and evidence gaps. Failed
quality remains visible beside a qualifying candidate. Explicit JSON carries
the full definition and receipts. `role status` leads with the selected model,
current qualification and expiry, then shows the latest attempt independently.
A failed challenger must not visually become a failed incumbent. Stale and
unselected roles retain a clear review action. Operational fallback and
multi-role placement views remain future work.
Avoid a hero score that mixes model quality, speed, fit and popularity.

Auto status uses the same field alignment and cyan focus as role views. Role,
current selection, candidate and acceptance state precede completed evidence
points and allowance consumption. Requested output-token caps are not actual
token use. The protected confirmation allowance is explicit. A session that
has expired must not offer first adoption, and historical outcomes remain
visible after their session deadline. JSON and plain output use the same exit
status. The TUI selected strip shows failed and unresolved needs before speed.

Personal preference controls should show meaningful units and fixed anchors.
An optional 100-point allocation can explain relative priorities, but mandatory
quality, context and resource floors remain separate. Never fill an unmeasured
attribute bar from a model card, parameter count or advertised context window.

README assets are rendered from deterministic fixtures through real display
paths. CI checks screenshot drift, output width and hostile text. Refresh
images whenever visible behavior changes and inspect the result at actual
reading size, not just the SVG source.

Source cards label their state `METADATA RESOLVED` or `METADATA INCOMPLETE`,
then show the selected file, declared size and dependency gaps. Amber remains
appropriate for resolved metadata because it is not a quality or fit pass.
The footer keeps local bytes, runtime fit and role quality explicitly unmeasured.
