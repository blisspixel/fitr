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
   unknown fields, and trailing JSON are hard errors.

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
2. Every repeat is a genuinely independent trial, which is what the Wilson
   intervals want. `-k 3` does not re-ask the same question three times; it
   asks three different instantiations of the same family.
3. Each result records its seed, so any instance can be regenerated exactly,
   and two runs sharing `--seedset` face byte-identical instances - the basis
   for paired comparison (see [statistics.md](statistics.md)).

The battery also self-tests without a model: every family's computed
canonical answer must pass its own grader, and a grader that rejected right
answers would fail the build. A harness bug reported as a model weakness is
the sin this repo exists to avoid.

Grading is strict where strictness is the point - a reply that wraps JSON in
commentary fails `structured_output`, because no pipeline can consume it -
and lenient where it is not: reasoning tasks only require a final `Answer:`
line, so a chatty-but-correct model is not punished for chattiness there.

The battery is deliberately weighted toward structured output, because that
is what quantization breaks first: JSON validity and tool-argument fidelity
degrade well before prose does.

## Tool withdrawal

Restraint at rest is the plumbing irrelevance rung: no tool calls on an
unrelated question. Restraint under *change* is what long agent sessions
actually face. The withdrawal task lists a tool, lets the model use it, then
drops it from the `tools` parameter mid-loop. The model is told what exists
every turn; calling a tool that is no longer listed is a hallucinated
capability. One grace call is tolerated (discovering the removal); persisting
past the error fails `tool_restraint`.

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
dropping your own task would defeat the point of having one.

User tasks may also use any generated family (`json_object`, `math_chain`,
...) with custom `params` - the same contamination-resistant machinery the
built-ins use.
