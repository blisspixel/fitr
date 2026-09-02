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

1. Model, configuration, device identity, and the `use for` statement.
2. Independent need verdicts with explicit state tokens, kept above the longer
   diagnostic sections. Below 32 rows they become an explicit state-count
   summary with failed, blocked, and inconclusive needs called out, so a full
   battery cannot silently push performance and capacity out of view.
3. The next useful command, kept above secondary diagnostics.
4. Performance observations and repeat shapes, with request TTFT,
   receipt-proven loaded TTFT, runtime-unloaded TTFT, and verified loaded
   cache-hit TTFT kept separate.
5. The verified resident and allocation-attribution observation from the
   requested 32K load probe, when the runtime confirms that effective context.
   The non-accelerator value is a derived remainder, not a spill claim.
6. Direct receipt-state diagnoses and the selected evidence gaps that are most
   useful in the bounded TUI viewport. The full analysis contract remains
   available in CLI and HTML output.
7. Per-need uncertainty, family structure, cache, repeat-count, and
   contamination disclosures. Unrelated needs are never combined into one
   global resolution claim.

Color never replaces `[PASS]`, `[FAIL]`, `[SKIP]`, `n/a`, or `[BLKD]`.

### Board

Board answers: what differs inside one comparable device and request
configuration?

Device/config blocks are stacked with explicit boundaries. Sorting and
relative bars are always local to one block. There is no global ranking,
cross-fingerprint aggregate, or visual scale across blocks.

At 120 columns and above, Board becomes a master-detail surface. Comparable
configurations stay in the left pane while the selected configuration's need
verdicts, measurements, and next command remain visible in the right pane.
The selected row uses a full-row terminal selection style, and the active sort
is named with a direction marker. At narrower widths, Board returns to one
column without changing the evidence or inventing a shorter verdict language.

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
| `?`, `F1` | Open the help overlay |
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
| Width 120 or greater | Board uses the master-detail layout; other views show full width-qualified values and supported graphs |
| Width 80 to 119 | One primary column; Result uses a compact full-battery summary below 32 rows |
| Width 56 to 79 | Priority-trimmed rows with exact primary values and explicit truncation |
| Width below 56 or height below 14 | Tiny safe view with identity, state, `?`, and `q` |

Decoration disappears before measurements. Graphs disappear before exact
numbers. Tables never wrap one logical row into an ambiguous second row.
Hostile control sequences are removed before layout, and long names are
truncated by display cells with a visible ellipsis. Deliberate spacing remains
present in no-color output, so style is never the only field separator.

Board includes a compact measurement legend for decode tok/s, SD, and N.
Result labels every displayed performance observation with `n`; a single
runtime-unloaded sample is not visually equivalent to a repeated estimate.
The footer and `?` help are view-specific and never advertise a no-op key.

Board's selected-detail pane is shipped at 120 columns and above. History and
Inventory remain single-column collections until their detail panes can carry
the same selection, clipping, and evidence-integrity guarantees. Narrow Board
views open the complete selected Result with `Enter` rather than compressing
its evidence into an unreadable side pane.

## Visual language

The product name is always written `fitr`, including the terminal header and
sentence starts. Uppercase is reserved for machine-like state tokens such as
`PASS`, `FAIL`, and `INCONCLUSIVE`, evidence-layer names, schema constants, and
environment variables.

The interface uses dense facts and sparse chrome. Pane titles are lowercase;
table columns and verdict tokens remain uppercase. Exact values accompany
every relative bar. Cyan identifies structure, focus, active sorting, and
actions. Green means established, red means disproven, yellow means blocked or
warning, and inconclusive evidence remains neutral and dim. Selection also
has a text marker and full-row terminal treatment, so color never carries the
state by itself.

The compact scorecard tag `[INCL]` is only a width-safe rendering of the stored
`INCONCLUSIVE` verdict. Detail, JSON, and evidence records retain the complete
word. Inventory states, claim states, verdicts, and experiment stages are not
interchanged merely because two of them sound similar.

`NO_COLOR` removes color while retaining text and selection shape. Any
non-empty `FITR_ASCII` replaces structural Unicode -- rules, bars, selection
markers -- with stable ASCII. Repeat-shape graphs are *withheld* rather than
replaced: `.:-=+*#@` is not a perceptual ramp, since `#` and `@` are dense but
not tall, so a reader cannot order it by height and the drawn shape says
nothing. The numbers on the row carry it instead. The terminal's default
background remains the default background.

A shape is also withheld when there is nothing to show: fewer than two
observations reads `-`, and a series whose spread is under 5% of its mean
reads `flat`. Those are different statements and are never printed for each
other, because a graph normalised to its own min and max would otherwise draw
a 0.4% wobble as a dramatic zigzag.

## Presentation contracts

The monitor consumes versioned, privacy-safe presentation data rather than
mutable evaluator state.

- Snapshot schema: `fitr.presentation.snapshot.v2`
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

The exported HTML surface follows the same disclosure boundary. It uses an
opaque device ID and allowlisted comparison settings rather than the raw
fingerprint, hostname, local paths, or arbitrary runtime configuration.

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
