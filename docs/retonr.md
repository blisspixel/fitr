# Optional sister: retonr

[retonr](https://github.com/blisspixel/retonr) is a separate product: local-first,
fidelity-gated re-expression of drafts in your own writing style. **fitr does
not require it.** There is no dependency, no download, and no call into
retonr from a measurement.

The two questions are adjacent:

| | fitr | retonr |
|---|---|---|
| Asks | is this model any good *on this machine*? | can this exact stack reconstruct *this draft* without breaking claims? |
| Unit | one model, one device fingerprint | one artifact digest, one runtime build, one hardware class |
| Verdict | PASS/FAIL/SKIP on independent needs | accept the candidate, or keep the original |
| Does not | qualify, activate, or license a model for retonr | rank models on a public leaderboard |

Retonr qualifies an exact artifact + runtime + hardware class. A familiar
Ollama tag is not a qualification. fitr's job is the *measured hardware
class*: fingerprint, GPU backend, resident size, structured-output rate,
tool restraint, degeneracy. That is evidence a person can hand to retonr.
It is not retonr saying yes.

## How to hand a result over

```bash
fitr run qwen3:30b --full
fitr export qwen3:30b --retonr
```

That writes `~/.fitr/results/<model>.retonr.json` (schema
`fitr.retonr.evidence.v1`). The file names itself `device_measurement` and
says, in the `disclaimer` field, that it is not a qualification.

If `retonr` is already on your PATH, `fitr run` prints a one-line hint
pointing at `--retonr`. If it is not, fitr stays silent. Missing retonr is
not an error.

## What fitr will not do

- Install, start, or configure retonr
- Emit retonr's internal qualification-v2 records (those bind artifact
  identity, runtime-build identity, and license decisions fitr does not have)
- Treat a PASS scorecard as "good enough for editorial reconstruction"
- Call retonr, or fail a run because retonr is absent
