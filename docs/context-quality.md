# Context task evidence

The next Extended fit scorecard tests whether a model uses a long document
correctly at a fixed operating window.

`fitr run <model> --context-tiers <bytes,bytes[,...]> [--ctx N]` collects one
phase. It is its own run level: the ordinary battery does not run, because the
pack needs a client that sends the declared overflow controls for its whole
lifetime and ordinary requests keep their existing defaults. There is no default
tier set. The declared sizes are sealed into the policy digest, so the operator
states what was tested rather than inheriting an untested product choice.

The result is an ordinary signed run record whose only planned work is the
phase. Role preferences, auto collection and fresh confirmation are not
connected yet, so nothing consumes this scorecard to qualify a model.

## What the document pack measures

A plan declares two to four increasing payload tiers between 2 and 64 KiB.
Each tier contains nine cells: indirect retrieval, distant dependencies and
instruction retention, with the principal fact placed near the beginning,
middle and end. Payload sizes are exact ASCII UTF-8 bytes, not token estimates.
Every cell reserves 128 output tokens.

The document contains competing records and varied rules. An answer must
follow the question's relationships and priority rules. Seeded station duties,
route overrides and service tickets prevent a single distinguished record or
constant action from answering every instance. An independent parser derives
the expected result from the visible document and checks the returned JSON.
No generated code or tool action runs in this pack.

The sealed plan identifies the policy, paired seed set, ordered cells, prompt
and payload digests, and fact offsets. Validation regenerates the cells;
changing a descriptor and recomputing its hash does not create a valid plan.
Expected answers are absent from submitted task objects.

## Qualification and limits

Every required cell in every lower tier must pass before a tier qualifies.
A later passing tier cannot bridge a failure. Any missing or unavailable cell
suppresses the verified prefix for the whole phase, including a missing cell
above an already failed tier. Diagnostic bounds remain available.

A wrong answer, declared output limit or verified context refusal differs from
unavailable transport, cancellation, unknown accounting or unverified runtime
evidence. The execution adapter establishes those dispositions. The pure
analyzer accepts no claimed pass; it rechecks the actual answer.

This is a finite synthetic task-set result. It is not a statistical confidence
interval, maximum model context, general document benchmark, compaction test or
agent workflow qualification. All-pass means at least the largest declared
tier tested. Untested sizes and task families remain unknown. The internal
report explicitly leaves runtime unbound and native token accounting unknown.

Four further limits follow from how the pack is built, and each is a property of
this instrument rather than of a measured model:

- **Three positions are a gate, not a curve.** Facts are placed near the
  beginning, middle and end. Published position studies use far finer sampling
  to resolve where degradation actually begins, and the middle of a document is
  not uniform. Nine cells per tier can establish that a tier failed; they cannot
  locate a narrow dead zone or support a claim about position sensitivity.
- **The verified prefix assumes larger is harder.** Requiring every lower tier
  to pass treats tier size as monotonically more difficult. That is a reasonable
  default and not a guaranteed property, so a lower tier failing while a higher
  one passes stays visible in the per-tier counts rather than being folded into
  a single verdict.
- **Bytes are the construction unit, not a window fraction.** Exact ASCII bytes
  keep the payload identical across models, which token-sized packs cannot do.
  They do not make the same payload occupy the same share of two models' windows
  once tokenized, so a passing tier is a claim about bytes. The native prompt
  token count is recorded beside it and is the only per-model token fact here.
- **Instruction retention has the thinnest external grounding.** Indirect
  retrieval and distant dependencies follow well-established task families.
  Standing-instruction retention over a long document is a real but sparsely
  studied category, so treat its results as the least corroborated of the three.

## Runtime controls and token accounting

The opt-in client policy sends explicit `truncate:false` and `shift:false`
throughout a client lifetime, including load probes and bounded chat retries.
Ordinary requests retain their existing defaults. Client controls alone do
not prove that an arbitrary runtime honors them.

Ollama resolves context shifting once, when it launches the runner for a model,
rather than per request. The policy is therefore adopted before the load probe,
not only on the graded requests.

A model whose runtime artifact format is `safetensors` is served by Ollama's MLX
runner, which reduces a requested output reserve to whatever remains beside the
accepted prompt and returns no field reporting that it did. The reserve gate
below cannot be established there at all, so such a model is refused before its
plan is sealed rather than measured into evidence whose central check was never
testable. The format is read from the runtime's own model details; an absent
format means GGUF, so absence is not treated as MLX.

Native terminal observations retain presence of total prompt, cached prompt
and generated-token counts. Missing or null counts remain unknown; absent
cache counts cannot be treated as cache misses. Valid cache counts permit the
uncached count to be derived from the reported total. The full output reserve
must fit beside that total, even when the model returns a very short answer.
These semantics follow the pinned
[Ollama 0.33.3 request and metric types](https://github.com/ollama/ollama/blob/v0.33.3/api/types.go)
and its [prompt count derivation](https://github.com/ollama/ollama/blob/v0.33.3/llm/llama_server.go#L1408-L1413).

The parser rejects inconsistent counts and differently capitalized aliases
that could overwrite a receipt. Invalid streamed responses expose no partial
output under this policy. Cancellation is checked after body close. An
opt-in empty chat retry requires explicit consistent zero accounting and
passes through admission again.

The new accounting stays separate from legacy cache metrics and their replay
behavior. It is transient until a signed execution contract binds it to exact
input, model artifact, template, runtime build, placement and observed window.
It does not establish payload-only token counts or independent tokenizer
identity.

## Submitting a sealed plan

The execution adapter submits every sealed cell in plan order at one fixed
operating window, with deterministic sampling and the declared 128-token
reserve. It never lowers the window, the payload or the reserve, and never
retries a cell. It refuses a client that would not send the declared overflow
controls, and dispatches nothing at all when the plan, model or client is
unusable.

The adapter holds no expected answer, so it cannot leak one into a request or
grade its own work. It records a disposition per cell and the pure analyzer
re-derives the report from those records alone.

A terminal success qualifies only when the entire declared reserve fits beside
the accepted prompt tokens. A short answer that fits only because the model
stopped early fails the reserve gate rather than passing on its content.

That gate does its arithmetic against the operating window the plan was sealed
with, which is the requested context. The phase therefore refuses to dispatch
unless the runtime resolved exactly that window. A runtime that resolved a
smaller one would make the arithmetic true of a window that does not exist: the
prompt still fits the sealed figure, generation then exhausts the real window,
and Ollama reports the same terminal reason it reports for an ordinary output
cap, because llama.cpp's stop type covers both. The cell would be recorded as a
declared output limit, which is a measured failure, when the cause was
capacity. An adjusted or unreported window names the resolved value and asks
for a re-run at it, rather than measuring into evidence whose central check
rests on a window the runtime never granted.

A refused reservation, a cancelled context, a runtime that cannot be shown
local, or an invalid request policy ends the phase; the remaining cells are
recorded as not attempted rather than left silently missing. A per-cell
transport fault or unknown accounting is recorded and the phase continues,
because an incomplete phase already cannot qualify and the remaining cells
still carry their own diagnostics. None of these become model-quality zeroes.

The runtime refusing an oversized prompt is none of those. It is the behavior
the pack is built to elicit, and it is recognized specifically rather than as
any HTTP 400: llama.cpp answers with `exceed_context_size_error` carrying the
prompt size and the per-slot context it enforced, and Ollama copies that body
verbatim into its own error string, so both layers are decoded strictly and
anything that does not match exactly stays an ordinary transport fault. A
recognized refusal is a context limit, which fails its own cell. Read as
transport it was unavailable instead, and one unavailable cell suppresses the
verified prefix for the whole phase, so a model that refused correctly lost
the evidence its smaller tiers had already earned.

The reported window is the runtime's per-slot context, which is the server's
total divided by its slot count rather than the figure it was started with.

## Persisting a phase

A run seals the plan's digest and cell count into its task plan before the
manifest exists, so a finished phase cannot present a shorter or easier
schedule than the run committed to. The observations are then part of the
signed completion payload.

The stored report is never taken from a caller. It is derived from the plan and
the observations when the phase is attached, and derived again by every loader
before the evidence is accepted, so a record cannot carry a verdict its own
observations deny. A sealed plan with no observations, observations with no
sealed plan, and a plan that differs from the one sealed before inference are
each refused.

A run without a context phase omits these fields entirely rather than writing
an empty one, so every manifest, record and completion payload written before
the phase existed keeps its exact bytes and its signature.

## Remaining connected acceptance

The phase renders in the CLI and in JSON through the central analysis
projection. HTML export and the TUI result view do not yet render it, so
`--html` is refused rather than writing a scorecard that omits the only
planned work.

Before this scorecard can influence a personal role, fitr must collect it
inside the owned runtime and existing budgeted fitting, obtain fresh
confirmation, and expose the phase in HTML and the TUI. Missing
accounting must block qualification, which the reserve gate already enforces.

Native acceptance must show that an oversized prompt is refused without
shrinking the document, window or reserve. Two runtime details shape that test.
The refusal is raised by llama-server, not by Ollama's own pre-flight, because
`truncate:false` bypasses the Go-side length check entirely; the classifier
above matches that overflow error specifically rather than any HTTP 400, and
acceptance should assert the resulting context-limit disposition rather than a
status code. And which component would have enforced the limit
depends on whether the model resolves to a legacy Go template or a Jinja one,
so the test needs at least one model of each kind before the guarantee can be
called verified.

Pi-backed workspace, compaction and restart evidence remains a separate
Extended fit scorecard. See [personal fitting](personal-fitting.md) and
[agent interoperability](agent-interop.md) for that workflow and its limits.
