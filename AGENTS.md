# fitr

A Go CLI that measures whether one exact local model configuration works on one
exact machine, and keeps the evidence behind every claim. One static binary, no
telemetry, no Python runtime dependency.

Read before changing anything consequential: `README.md` for what ships today
and what fitr refuses to claim, `ROADMAP.md` for build order and shipped
history, `docs/design.md` for the evidence model and its known limits. Each
subsystem has one owning document under `docs/`.

## The law this project is built on

Missing evidence stays missing. Nothing unmeasured becomes a number, a ranking
or a recommendation. Before adding any output, ask which observation supports
it and what the surface says when that observation is absent.

These are enforced in code and tests. Do not weaken them for convenience:

- Unavailable, cancelled, unknown-accounting, contaminated and refused are
  distinct dispositions. None of them is a model-quality zero.
- A value that is present but not the shape its field expects is unmeasured,
  not coerced into one. Reading the first entry of a per-layer `head_count_kv`
  array as the model's scalar passed every bounds check, passed `KVReady`, and
  put a fit verdict tens of times off on current artifacts.
- Evidence compares only within one device fingerprint, runtime, artifact
  digest, effective context and placement. Do not add a cross-configuration
  ranking or a global score.
- A runtime capability declaration is routing evidence, never a behavioral PASS.
- An exploration cannot certify its own winner. Confirmation requires a fresh
  sealed plan and fresh evidence.
- fitr never mutates or restarts the user's serving runtime. `apply` prints a
  recipe. fitr may mutate or remove only what fitr created, and there is no
  delete path today at all: `cleanup` is read-only planning.
- Network access happens only for an explicit install, update, pull, source
  resolution or remote endpoint. No telemetry and no background checks.
- Exports and receipts omit raw model output, hostnames, local paths and the
  raw fingerprint key. Receipts are mode 0600 on Unix.
- A fault mid-battery abandons the run and names what was discarded. Silence is
  the bug; fail-closed is the contract.

## Where things belong

Grep for the existing mechanism before adding a second one. There is one HTTP
path per runtime, one atomic write, one lock, one strict-JSON gate. A second
copy drifts, and drift in this codebase means evidence that disagrees with
itself.

| Concern | Home | Rule |
|---|---|---|
| CLI dispatch, flags, exit codes | `cmd/fitr/main.go` | 0 ok, 1 error, 2 usage, 3 a measured need FAILED, 4 required decision evidence unresolved or blocked, 130 interrupt. Adding a code is a contract change. |
| Serving runtimes | `internal/llm` interface, adapters in `internal/ollama`, `internal/llamaserver`, `internal/openaicompat` | The measurement layer must not know which server it is talking to. |
| Evidence schemas, sealing, signing, validation | `internal/record` | The only home. New fields carry `omitempty` so previously signed payloads keep their exact bytes and signatures; frozen fixtures prove it. |
| Derived renderer-neutral facts | `internal/analysis` | Reports are projections rebuilt from a validated record, never written back as evidence. |
| Presentation | `internal/render` (CLI, HTML), `internal/top` (TUI) | Renderers consume analysis. A renderer never recomputes a verdict, and every surface composes to the resolved width. |
| Battery, tasks, graders, execution adapters | `internal/eval` | Task definitions are canonical in `spec/` with an embedded copy under `internal/eval/tasks`. `make spec-sync` repairs drift; `TestEmbeddedSpecMatchesCanonical` fails the build without it. |
| Scoring policy | `internal/score` | Versioned. A sealed older scorecard must still validate exactly under its original policy. |
| Fit arithmetic and GGUF metadata | `internal/advise` | The fit verdict the README leads with, plus the largest untrusted binary surface fitr has. KV and parameter arithmetic is overflow-checked and an implausible dimension is unmeasured, never a smaller number. `FuzzReadMetadata` is a CI gate over both the whole-file and bounded-prefix entry points. |
| Device identity | `internal/device` | Any field added to the fingerprint changes what compares to what. Fingerprint errors corrupt comparison silently, which is the worst failure class here. |
| Untrusted JSON | `internal/strictjson` | Duplicate-key rejection runs before any typed decode. |
| Files and exclusion | `internal/atomicfile`, `internal/boundedio`, `internal/lock` | One way to write, one way to bound a read, one way to lock. |
| Long-context document pack | `internal/contextquality` (pure plan, generate, verify, analyze), `internal/eval/contexttask.go` (submission), `internal/record/context_quality.go` (sealing), `fitr run --context-tiers` (collection) | CLI and JSON project the sealed phase. HTML, TUI, role preferences and auto collection remain unconnected. See `docs/context-quality.md`. |

## Verify

The tracked tree is green. Keep it green. `make` is not installed on every
development machine used here, so these are the portable commands:

```bash
go build ./...
go vet ./...
go test ./... -count=1
golangci-lint run ./...
sh scripts/check-coverage.sh 80
go run ./cmd/fitr screenshots docs/assets && git diff --exit-code -- docs/assets
```

Formatting: check the tracked tree only. `gofmt -l .` also walks the gitignored
scratch directory, which holds stale copies of the whole repository.

```bash
git ls-files '*.go' | xargs gofmt -l
```

Two things that look like failures and are not. `golangci-lint` type-checks with
the Go toolchain it was itself built with, so a copy built by an older Go panics
with "file requires newer Go version" on this tree; reinstall it at the version
CI pins, using the current toolchain. And Windows Defender intermittently blocks
the `internal/updater` test binary, because that package replaces executables;
it fails identically on a clean checkout, so confirm against `main` before
treating it as a regression.

`.github/workflows/ci.yml` is the authority on the full gate set and on every
tool version. It additionally runs the race detector, ten fuzz smoke targets, a
1600-line cap on non-test `.go` files, a measured binary size cap in `dist`, a
reproducible-build comparison, installer smokes on three operating systems, and
`govulncheck`. Take Go and linter versions from `go.mod` and that workflow, not
from memory: `go.mod` states the minimum supported language version, and CI
builds on the current release while re-running the suite on the minimum.

CI has no GPU and no serving runtime, so every test there is pure logic. Real
evidence that a measurement path works comes from a live backend:

```bash
FITR_LIVE=<model> go test ./cmd/fitr -run TestLive
```

Native rows are recorded in `docs/release-acceptance.md`. Do not describe a
measurement path as working on unit tests alone.

A regression test is not finished until it has been seen to fail. Reintroduce
the defect, watch the new test catch it, then restore the fix. A test written
after a fix tends to pass for the wrong reason, and one that never failed is
evidence of nothing. Where several defects share a symptom, revert them one at
a time so each test is known to catch its own.

## Do not weaken a gate to pass

`.golangci.yml` is a hard gate with no path exclusions for production code, and
`nolintlint` requires both a specific linter and an explanation. The handful of
`//nolint` directives in the tracked tree each name one linter and say why in
one line. A broad ignore, a lowered coverage threshold, a deleted
assertion, an excluded file or a test edited to accept wrong behavior is not a
fix. If a gate is genuinely wrong, change it deliberately in its own commit with
the reason recorded: the comments in `.golangci.yml`, `Makefile` and the CI
size gate are the standard for how that is written down.

## Constants you do not own

fitr decodes GGUF, speaks MCP, and reads Ollama and llama.cpp response fields.
Those names belong to someone else's release. A test that repeats fitr's
spelling of one proves only that the code and the test agree: fitr read
`attention.recurrent_layer_count` for months, llama.cpp writes
`attention.recurrent_layers`, and the branch behind it could never fire while
the suite stayed green.

Check a borrowed name against the upstream source that defines it, and say in
a comment which version you checked. Prefer a fixture taken from a real
published artifact or a recorded wire exchange over a hand-built map, because
only the former can disagree with you.

## Adding a measurement

An evidence-producing feature is not done when it runs. Follow the shape
`internal/contextquality`, `internal/workload` and `internal/experiment`
already share rather than inventing a fourth:

1. Seal the plan into the run manifest before inference, so a finished phase
   cannot present a shorter or easier schedule than the run committed to.
2. Derive the verdict at load time from the observations rather than reading a
   stored conclusion, so a record cannot carry a verdict its own observations
   deny.
3. Keep transport faults, cancellation, unknown accounting and runtime refusal
   distinct from a wrong answer, and keep each one out of the quality numbers.
4. Add a fuzz target if it decodes anything untrusted, and a frozen fixture if
   it touches a signed payload.
5. Write down what the result does **not** establish, in the owning document,
   before claiming the feature ships.

## Documentation and durable state

Update the document a change makes untrue, in the same commit. `README.md` owns
product intent and shipped capability; `ROADMAP.md` owns planned work, per
release exit criteria and honest limits; `docs/design.md` owns the evidence
model; `docs/release-acceptance.md` owns acceptance receipts; one `docs/<topic>.md`
owns each subsystem. Do not add a second document for a topic that already has
an owner, and do not let the roadmap make planned behavior read as shipped.

A finding that outlives the session belongs in the issue tracker, not in a
summary. If you find a defect while doing something else, file it with enough
detail to act on months later, then carry on with the task you were given.

`.agents/` is gitignored scratch: analysis, archived worktrees, generated
fixtures, acceptance venvs, indexes and receipts. Never commit it, never put
credentials in it, and remember it contains whole copies of the tree, so
repository-wide greps will find stale matches there. Anything worth keeping
gets promoted into a tracked document, an issue, a test or code; nothing is
expected to survive there. `.tmp/` was the previous name and stays ignored, so
an older working tree keeps its contents rather than presenting them as new
untracked files.

## Writing

Lowercase `fitr` in prose. No emoji. No em or en dashes; the tree uses `--`. No
AI attribution, generated-by wording or coauthor trailers anywhere: commits,
comments, documentation, PR text.

Comments explain intent, constraints, invariants and non-obvious tradeoffs. The
existing ones are the standard: they say why a rule exists and what breaks
without it.

Commit messages follow the existing history: an imperative one-line title, then
a body saying what was wrong, what changed, why that boundary is the right one,
and what a test now proves. Read `git log` before writing one.
