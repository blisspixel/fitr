# Terminal monitor

`fitr top` is the full-screen terminal surface for saved evidence and live
measurements. It is an opt-in interface over the same result, scoring, and
comparison rules used by the ordinary CLI. It never owns a second scoring
implementation.

## Command contract

```text
fitr top
fitr top view [model|result.json]
fitr top run <model> [run flags]
fitr top history [path|clear --yes]
```

- `fitr top` opens Inventory when the runtime listed models, otherwise Board.
- `fitr top view` opens the newest or selected saved result.
- `fitr top run` starts the same measurement as `fitr run` and opens Live.
- `fitr top history` opens History; `path` locates its private archive and
  `clear --yes` removes archived copies while keeping canonical latest results.
- Opening the TUI never pulls a model, starts a runtime, or starts a run unless
  the user chose `top run`.
- A redirected stream or `TERM=dumb` never receives terminal control
  sequences. Use `fitr view`, `fitr board`, or the presentation snapshot
  instead.

Normal `run`, `view`, and `board` output remains the complete scriptable
interface. The TUI is not required for automation.

## Five questions, five views

### Inventory

Inventory answers: what is already serving, and what is the cheapest next command?

Installed models only. Each row is measured, unproven, incompatible, or stale.
Fit (compatible / low mem / skip) appears when architecture is already known
from a saved result or a GGUF path the runtime exposed. CTX is the measured
window, or measured/serving when a live process reports a different
allocation. The selected row shows the compact context-fit graph and whether
apply is still pending. Unmeasured is never ranked. Enter opens a measured
result; it does not pull or run.

### Live

Live answers: what is happening, and can this run be trusted?

It shows the current phase, bounded progress where the work has a known count,
elapsed time, placement and integrity warnings, observed metric samples, and
completed phases. It does not invent a global percentage or ETA. A partial run
never shows a final PASS or FAIL product verdict.

Pausing freezes the viewport, not the measurement. The UI says `VIEW PAUSED,
MEASUREMENT CONTINUES` while paused. Completion stays on the current view and
offers Result rather than moving the user without consent.

### Result

Result answers: what can this model do here, and what should happen next?

The order is deliberate:

1. The `use for` statement.
2. Independent need verdicts with explicit state tokens.
3. Performance observations and repeat shapes.
4. Uncertainty, minimum detectable effect, cache, repeat-count, and
   contamination disclosures.
5. Device, runtime, quant, context, and profile identity.
6. The next useful command.

Color never replaces `[PASS]`, `[FAIL]`, `[SKIP]`, `n/a`, or `[BLKD]`.

### Board

Board answers: what differs inside one comparable device and request
configuration?

Device/config blocks are stacked with explicit boundaries. Sorting and
relative bars are always local to one block. There is no global ranking,
cross-fingerprint aggregate, or visual scale across blocks.

Supported sort fields are model, start time, decode, prefill, resident memory,
and repeats. Stable tie breakers keep equal rows from jumping during refresh.

### History

History answers: what changed over time, and which pair is valid to compare?

Saved runs are newest first. Every row carries time, model and build, context,
run level, need summary, and a privacy-safe device/config group. A marked
baseline can be compared with the selected row only when the existing compare
rules accept the pair. The UI states the exact mismatch when they do not.

## Keyboard model

| Key | Action |
|---|---|
| `1` to `5` | Open Live, Result, Board, History, or Inventory |
| `Tab`, `Shift+Tab` | Move to the next or previous view |
| `j`, `k`, arrows | Move the selection |
| `h`, `l`, left, right | Move to the previous or next view |
| `PgUp`, `PgDn` | Move one viewport |
| `Home`, `End`, `g`, `G` | Move to the first or last row |
| `Enter` | Open the selected item |
| `Esc` | Cancel input, close an overlay, or return one level |
| `/` | Filter the current collection |
| `s` | Change the current block's sort |
| `Space` | Pause Live display or mark a History baseline |
| `c` | Compare the marked and selected History rows |
| `r` | Reload saved evidence |
| `?`, `F1` | Open contextual help |
| `Ctrl+L` | Redraw the terminal |
| `q` | Quit, with confirmation during an active run |
| `Ctrl+C` | Cancel an active run and exit 130 |

Filtering is incremental, case-insensitive substring matching. `Enter` applies
the filter, `Esc` restores the prior filter, and `Ctrl+U` clears the input.
Ordinary keys, including `q`, enter text while the filter editor is active.

Mouse input is not part of the 0.4 release contract. Complete keyboard
operation is.

## Responsive layout

Layout uses terminal-cell width, not byte or rune count.

| Terminal size | Behavior |
|---|---|
| Width 120 or greater | Full data table, graphs, and selected detail |
| Width 80 to 119 | One primary table with selected detail below |
| Width 56 to 79 | Stacked compact cards with exact values |
| Width below 56 or height below 14 | Tiny safe view with identity, state, `?`, and `q` |

Decoration disappears before measurements. Graphs disappear before exact
numbers. Tables never wrap one logical row into an ambiguous second row.
Hostile control sequences are removed before layout, and long names are
truncated by display cells.

`NO_COLOR` removes color while retaining text and selection shape. Any
non-empty `FITR_ASCII` replaces structural Unicode and graph glyphs. The
terminal's default background remains the default background.

## Presentation contracts

The monitor consumes versioned, privacy-safe presentation data rather than
mutable evaluator state.

- Snapshot schema: `fitr.presentation.snapshot.v1`
- Event schema: `fitr.presentation.event.v1`

`top view --snapshot` includes the selected opaque run ID while retaining the
full Board and History context. The ID is stable across refreshes and contains
no model, host, or path data.

Event kinds are `run_started`, `phase_started`, `phase_progress`,
`metric_sample`, `notice`, `phase_completed`, `run_completed`, `run_failed`,
and `run_cancelled`.

Every live event carries a run ID, monotonic sequence, and elapsed time. Events
may carry phase names, bounded counts, severity, and measured metrics. They
never contain prompts, raw responses, answer strings, hostnames, or local
paths. A slow display cannot block or change measurement timing.

The immutable reducer is the behavioral contract for both the terminal and
later native clients. No client may recompute a verdict.

## Storage

The canonical `~/.fitr/results/<model>.json` file remains for compatibility.
Each completed run is also written atomically to private append-only history.
Legacy result files continue to load. Malformed history entries do not hide
valid evidence; the monitor reports their count without exposing local paths.
Board is built only from reconciled canonical current records. History entries
and explicit external files can be opened for inspection, but cannot enter a
ranking unless they exactly match the canonical current record and its private
history twin.

History contains the same raw prompts, responses, hostname, and device details
as the canonical result and has no automatic retention limit. It remains local
and is never uploaded. `fitr top history path` prints its location;
`fitr top history clear --yes` removes archived copies while preserving the
canonical latest result for each model. Ordinary commands read only canonical
results, so deleting one does not silently resurrect it from History.

The completion receipt binds the sealed manifest and measured outcome. This
detects post-completion edits and, together with current/history
reconciliation, rejects ordinary crafted imports from ranking surfaces. It is
not external attestation against an actor who can replace the executable and
rewrite the entire local store. Signed artifacts and externally anchored
provenance remain Release C work.

Run selection uses stable opaque IDs rather than row positions, so refreshes
do not move the user's selection to a different result.

## Terminal architecture

The full-screen adapter uses
[tcell v3](https://github.com/gdamore/tcell/tree/v3.4.2) for terminal input,
resize events, cell output, and restoration. The application does not use
tcell's widget layer. State transitions and layout render into a small
renderer-neutral canvas first, then a thin adapter copies cells to tcell.

This keeps fitr's interaction model deterministic and portable. It also avoids
a current Windows resize limitation in
[Bubble Tea v2](https://github.com/charmbracelet/bubbletea/blob/v2.0.8/screen.go).
tcell v3.4.2 supports the repository's Go 1.25 floor and its current release
includes Windows input and screen lifecycle fixes.

Only the event-loop goroutine touches the terminal. Evaluation, persistence,
and refresh work return immutable events. A deferred terminal finalizer runs
on success, error, interrupt, and panic. Cancelling a run waits for bounded
backend cleanup and the evaluation lock before the shell regains control.

## Release gates

The terminal milestone is complete only when:

1. All five views work by keyboard.
2. A deterministic full-run replay can be followed from Live to Result.
3. A partial run cannot show a final verdict.
4. Board cannot sort or scale across device/config blocks.
5. History rejects invalid pairs with the exact reason.
6. Tiny, narrow, standard, and wide layouts render without panic or overlap.
7. Color and Unicode fallbacks retain all meaning.
8. Redirected commands emit no terminal control sequences.
9. Interrupt, error, cancellation, and panic restore the terminal.
10. Existing plain and JSON output contracts remain intact.
11. Go tests, race tests, six cross-builds, lifecycle simulations, and manual
    terminal restoration and resize checks on macOS, Linux, and Windows pass.
12. Binary growth stays bounded and is printed by CI for every target.

Reducer and canvas tests cover hostile text, CJK and combining characters,
resize sequences, stale selections, invalid comparisons, and all empty/error
states. The lifecycle adapter is tested separately so terminal cleanup does not
depend on a layout golden.
