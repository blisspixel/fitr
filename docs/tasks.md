# The task battery

Two kinds of task make up a run: classic tasks with bespoke harness logic and
**generated checks**, the scalable half of the battery. Speed, memory, refusal,
plumbing, generated checks, and tool withdrawal do not execute generated code.
Coding fixtures and executable tool or agent loops are disabled by default.

## Executable task safety

The current cross-platform executor is not a sandbox. An explicit
`--allow-unsafe-exec` run preflights the interpreter and requires a successful
exit plus an exact final verifier receipt, but generated Python still has the
current user's normal filesystem, environment, credential, and network access.
For that reason:

1. Default runs record executable tasks as SKIP before a model call or work
   directory is created.
2. Unsafe opt-in observations are recorded as INCONCLUSIVE even when their
   assertions appear to pass or fail.
3. INCONCLUSIVE and executor errors never enter a scoring or comparison
   numerator or denominator.
4. User task files remain declarative. Executable kinds, executable fields,
   unknown fields, duplicate JSON object names, and trailing JSON are hard
   errors.

Verified executable PASS and FAIL evidence requires the isolated worker in the
benchmark-quality release. Until then, no generated model code is allowed to
make a ranking claim.

## Generated checks: no answer strings, anywhere

The check battery (JSON emission, schema conformance, tool-argument fidelity,
CSV, format constraints, computed math, date arithmetic, state tracking) is
**generated per run**. Each task is a parameterized family: names, numbers,
and dates come from a seeded RNG, and the correct answer is **computed by the
harness, never stored**.

Three things follow from that design:

1. There is no answer string in this repo to leak into training data. A
   static benchmark published on GitHub is future training data with a delay;
   a generated one is not.
2. Every repeat is a fresh generated instance. `-k 3` does not re-ask the same
   question three times; it asks three different instantiations of the same
   family. Outcomes from one family can still be correlated, so need-level
   intervals remain family-clustered rather than treating every instance as
   statistically independent.
3. Each result records its seed, so any instance can be regenerated exactly,
   and two runs sharing `--seedset` face byte-identical instances - the basis
   for paired comparison (see [statistics.md](statistics.md)).

The battery also self-tests without a model: every family's computed
canonical answer must pass its own grader, and a grader that rejected right
answers would fail the build. A harness bug reported as a model weakness is
the sin this repo exists to avoid.

Grading is strict where strictness is the point - a reply that wraps JSON in
commentary fails `structured_output`, because no pipeline can consume it -
and duplicate object names fail because different consumers can choose
different values. Grading is lenient where strictness is not the point:
reasoning tasks only require a final `Answer:` line, so a chatty-but-correct
model is not punished for chattiness there.

The battery is deliberately weighted toward structured output, because that
is what quantization breaks first: JSON validity and tool-argument fidelity
degrade well before prose does.

## Tool calls, in the tool channel

`tool_args` grades a JSON object the model writes **as text** when asked for
one. That is a real skill and it is not the same skill as being handed a tool
and calling it, which also exercises the chat template and the runtime's
tool-call parser.

The difference matters because the most-reported local tool failure is not bad
JSON. It is a perfectly well-formed call arriving in `content` with a normal
stop reason, which the model never routed through the tool channel. To an agent
harness that is silence; to a text grader it is indistinguishable from an
answer. Four generated families run through the real channel:

| Family | Need | Asks |
|---|---|---|
| `tool_call` | `tool_calling` | one tool, exact arguments, computed from the prompt |
| `tool_call_strict` | `tool_calling` | adds a closed enum and a bounded integer, so a syntactically perfect call can still be wrong |
| `tool_fanout` | `tool_calling` | the right tool among 12 plausible neighbours, shuffled so calling the first one never passes |
| `tool_irrelevance` | `tool_restraint` | tools are offered and none can answer; the model must call nothing |

The verdict is binary like every fitr trial, but the failure **mode** rides
along in the detail, because the modes have different fixes:

`prose_channel` (a call emitted as text - template or parser, not the weights),
`no_call`, `wrong_name`, `extra_calls`, `bad_json`, `missing_param`,
`wrong_type`, `extra_param`, `wrong_value`.

Two design notes:

- **A model with no tool support does not fail; it is `n/a`.** Runtimes signal
  this with a generic HTTP 400, and reading that as a transport fault would
  abandon the run and discard every completed measurement. It is a capability
  fact, like vision, and the scorecard says so.
- **`tool_restraint` is now pooled.** Restraint at rest used to rest on one
  plumbing observation, which cannot carry an interval. Restraint under
  *change* stays a separate binary rather than being averaged in: a model that
  stops cleanly when a tool is withdrawn and one that keeps calling a dead tool
  are not the same model, and pooling would let a good rate at rest hide it.

## Tool withdrawal

Restraint at rest is measured by the generated `tool_restraint` family: no tool
calls on unrelated questions across fresh instances. The older plumbing
irrelevance rung remains a diagnostic fallback only when that pooled family is
unavailable; it does not override the pooled result. Restraint under *change*
is what long agent sessions actually face. The withdrawal task lists a tool,
lets the model use it, then drops it from the `tools` parameter mid-loop. The
model is told what exists every turn; calling a tool that is no longer listed
is a hallucinated capability. One grace call is tolerated while discovering
the removal; persisting past the error fails `tool_restraint`.

File tools accept one ordinary portable filename, not a path. Windows device
names, NTFS alternate-stream syntax, separators, control characters, and other
non-portable names are rejected on every operating system. Model-created files
and file reads are limited to 1 MiB so a malformed tool call cannot turn a run
into unbounded disk or transcript growth.

## Your own tasks, without forking

Drop declarative tasks in `~/.fitr/tasks/*.json` (or `$FITR_TASKS`):

```jsonc
{
  "id": "my_extraction",
  "kind": "check",
  "why": "the shape of my actual workload",
  "family": "static",
  "num_predict": 200,
  "params": {
    "prompt": "Extract the invoice total from: ... Reply with the number only.",
    "grader": { "type": "number", "expected_number": 1249.5, "tolerance": 0.01 }
  }
}
```

Graders: `exact`, `contains`, `regex`, `json_object`, `number`. A user task
can prompt and grade; it **cannot execute anything** - exec-style user tasks
stay out until they can be sandboxed honestly. They score into their own
`your tasks` row (all-must-pass by default), or pool into a built-in need via
`"need"`. Malformed files are hard errors with the filename in them: silently
dropping your own task would defeat the point of having one. Each task is
limited to 1 MiB and a directory may contain at most 1024 JSON task files.

User tasks may also use any generated family (`json_object`, `math_chain`,
...) with custom `params` - the same contamination-resistant machinery the
built-ins use.

## From tasks to bounded workloads

A prompt and grader can prove one model primitive. Real work is a sequence of
context acquisition, decisions, tool actions, state changes, verification,
and exceptions. The pre-1.0 workload experiment treats that bounded workflow
as another first-class evaluation unit without turning fitr into an agent
orchestrator.

A workflow contract must declare:

- its intent and initial state;
- available context and tools;
- allowed and forbidden actions;
- an explicit definition of done;
- independent verification;
- turn, attempt, time, and resource limits;
- escalation conditions;
- approval authority plus denial, timeout, and revocation behavior;
- checkpoint and resume rules where state continuity is under test.

The configuration identity expands with the experiment. In addition to model,
artifact, quant, runtime, device, placement, and context, a comparable workflow
receipt binds scenario and initial-state identity, workflow specification,
tools and service versions, context provider and context-event manifest,
permission profile, verifier environment, worker and harness builds, state and
checkpoint lineage, sampling, cache, concurrency, retry, stopping, and
data-boundary policies. A changed verifier or tool set is a different
experiment, not fresh evidence for the old one.

The worker does not own the evidence. A final message that says the task is
complete is self-report, not a PASS. Deterministic assertions, independently
observed final state, and separate verifier receipts can support core verdicts.
Model-judged and self-reported outcomes remain explicitly weaker evidence.

Current result records do not yet support time to valid result or validated
work rate. Those metrics require sealed per-trial receipts for attempt start,
attempt outcome, verifier result, acceptance time, retries, tool failures, and
escalations. Aggregate pass rates and aggregate token timings cannot be joined
after the fact without inventing evidence.

The full evidence taxonomy, metric definitions, autonomy boundaries, and
planned report are in [workload evidence](workload-evidence.md).

fitr owns contract validation, immutable experiment identity, receipt sealing,
verifier policy, and analysis. A bounded external runner owns execution and
scheduling. The 0.11 experiment may use one fixed harness-owned workflow in a
constrained fixture; arbitrary generated code and executable task JSON remain
SKIP until the stronger cross-platform confinement contract passes.
