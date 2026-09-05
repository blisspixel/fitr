# Context task evidence

The next Extended fit scorecard tests whether a model uses a long document
correctly at a fixed operating window. The deterministic task pack and opt-in
Ollama request accounting are implemented as internal foundations. Collection,
signed run evidence, role preferences and auto confirmation are not connected
yet. No command currently earns this scorecard.

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
evidence. The future execution adapter must establish those dispositions.
The pure analyzer accepts no claimed pass; it rechecks the actual answer.

This is a finite synthetic task-set result. It is not a statistical confidence
interval, maximum model context, general document benchmark, compaction test or
agent workflow qualification. All-pass means at least the largest declared
tier tested. Untested sizes and task families remain unknown. The internal
report explicitly leaves runtime unbound and native token accounting unknown.

## Runtime controls and token accounting

The opt-in client policy sends explicit `truncate:false` and `shift:false`
throughout a client lifetime, including load probes and bounded chat retries.
Ordinary requests retain their existing defaults. Client controls alone do
not prove that an arbitrary runtime honors them.

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

## Remaining connected acceptance

Before this scorecard can influence a personal role, fitr must collect it
inside the owned runtime and existing budgeted fitting, preserve old signed
evidence, replay every observation, and obtain fresh confirmation. Native
acceptance must show that an oversized prompt is refused without shrinking
the document, window or reserve. Missing accounting must block qualification.

Pi-backed workspace, compaction and restart evidence remains a separate
Extended fit scorecard. See [personal fitting](personal-fitting.md) and
[agent interoperability](agent-interop.md) for that workflow and its limits.
