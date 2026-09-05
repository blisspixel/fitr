# Terminal design language

fitr should make the next decision clear before exposing the full receipt.
The terminal is a product surface: role, configuration, evidence state and
next action lead; implementation details belong in inspection views.

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
claim, optional harness and the next planning command. Sources remain private
unless explicitly requested as JSON. The same proposal objects feed text and
JSON plans; the renderer does not make a compatibility decision.

The future role library should add an incumbent, challenger, fallback and
compact evidence gaps. A detail pane explains the role's hard constraints,
preference weights, confidence and the reason a candidate is unresolved.
Avoid a hero score that mixes model quality, speed, fit and popularity.

README assets are rendered from deterministic fixtures through real display
paths. CI checks screenshot drift, output width and hostile text. Refresh
images whenever visible behavior changes and inspect the result at actual
reading size, not just the SVG source.
