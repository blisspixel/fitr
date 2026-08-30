# Interface direction

fitr is CLI-first. The terminal should become the fastest, richest way to
measure a model, understand the result, and decide what to do next. A desktop
application can come later, but it must be a real native application rather
than a web page packaged as one.

## What ships first

The default commands stay composable:

- `fitr run` prints progress to stderr and a final result to stdout.
- `fitr view [model|result.json]` reopens the newest or selected saved run as
  a compact data view.
- `fitr board` shows relative throughput bars and repeat-shape graphs inside
  each comparable device/config block.
- `--display plain`, `--display json`, `NO_COLOR`, and `FITR_ASCII` remain
  first-class. Visual polish must never break pipes, logs, CI, or terminals
  without color and Unicode.

The graphs show observations, not certainty. Per-need intervals, family and
repeat counts, and the refusal to rank across device fingerprints remain more
important than visual density. There is no global resolution statistic across
unrelated needs.

## Terminal UX quality bar

fitr is a decision interface, not a telemetry wallpaper. Every result surface
uses the same spine:

```text
MODEL / CONFIG / DEVICE
VERDICTS
NEXT
PERFORMANCE
CAPACITY
EVIDENCE AND LIMITS
```

Outcome and evidence source remain separate dimensions. `PASS`, `FAIL`,
`INCONCLUSIVE`, `SKIP`, `BLKD`, and `n/a` describe the decision state;
measured, runtime-reported, client-derived, projected, and unavailable
describe how a fact was obtained. Color reinforces those words but never
replaces them.

An observation that survives for context but cannot support a claim is marked
`descriptive only` beside its acquisition source in CLI, TUI, and HTML. It is
never rendered like an available observation merely because the numeric value
still exists.

Responsive layouts remove information by decision priority rather than
clipping a desktop table. Model, state, exact primary values, and the next
action survive first. Secondary identity, variability, timestamps, bars, and
repeat-shape graphs appear only when space supports them. Result uses a compact
full-battery summary below 32 rows and points to `fitr view` for the complete
evidence. Horizontal semantic truncation is marked with an ellipsis.

Help and the footer advertise only actions valid in the current view. Filter,
sort, pause, baseline, comparison, and error state are explicit text. Exact
values stay visually above bars and graphs, and graphs are withheld when the
sample shape cannot support them.

The next major interaction slice is a responsive master-detail layout for
Board, History, and Inventory: side-by-side at wide widths, stacked at medium
widths, and an explicit full-screen detail action when narrow. That work must
preserve the same decision spine and comparison boundaries rather than adding
a composite score or a second interpretation path.

This direction borrows focused-panel and width-sensitive split behavior from
[lazygit](https://github.com/jesseduffield/lazygit/blob/master/docs/Config.md),
preview disclosure from [fzf](https://github.com/junegunn/fzf.vim/blob/master/README.md),
and visible sort/follow state from
[htop](https://github.com/htop-dev/htop/blob/main/htop.1.in) and
[btop](https://github.com/aristocratos/btop/blob/main/README.md). It
deliberately does not copy dashboard density or a composite ranking. The
[llmfit TUI](https://github.com/AlexsJones/llmfit/blob/main/docs/tui.md) is a
useful catalog-navigation reference, while its broad shortcut surface and
primary composite score are not a fit for this evidence model.

## The opt-in full-screen TUI

The opt-in `fitr top` interface shipped in 0.4. Its explicit command contract
keeps browsing and measurement separate:

```text
fitr top
fitr top view [model|result.json]
fitr top run <model> [run flags]
fitr top --snapshot
fitr top history [path|clear --yes]
```

It has five views:

1. Live run: current phase, elapsed time, device placement, decode/prefill
   samples, memory, and warnings.
2. Result: independent need verdicts, distinct latency states, exact-context
   allocation attribution, uncertainty, and the next useful command.
3. Board: filter and sort within one comparable device/config block, with an
   explicit boundary between blocks.
4. History: inspect configuration changes and open a valid pairwise compare.
5. Inventory: installed models with measured/unproven/incompatible/stale
   state, optional fit tier, measured/serving ctx, a compact context-fit
   graph on the selected row, and one next command. Not a ranking.

The TUI must restore the terminal after interrupts and panics, remain fully
keyboard-driven, work on macOS, Linux, and Windows terminals, and degrade at
narrow widths. Color is redundant with text and shape. Animation is optional
and disabled when it obscures measurements or causes avoidable CPU use.

[tcell v3](https://github.com/gdamore/tcell/tree/v3.4.2) is the terminal
adapter. It provides cell rendering, input, resize events, and restoration on
the repository's Go 1.25 floor, including native Windows resize handling. A
renderer-neutral canvas and pure state reducer sit above it; fitr does not use
tcell's widget layer. This keeps the interaction contract testable without a
terminal and reusable by later native clients.

[Bubble Tea v2](https://github.com/charmbracelet/bubbletea/tree/v2.0.8) was
the high-level alternative. Its released screen implementation still lacks
Windows resize notifications, which conflicts with fitr's platform parity
requirement. The framework decision and complete interaction contract are in
[tui.md](tui.md).

## One core, several native surfaces

Measurement, scoring, calibration, and recommendation stay in Go packages
with no UI assumptions. Versioned read-only presentation snapshots and a
structured live-event stream now separate those decisions from the terminal
adapter. Every later interface consumes those contracts and must produce the
same verdicts. No frontend is allowed to reimplement scoring.

If native desktop applications earn their maintenance cost, use each
platform's native UI stack:

- macOS: [SwiftUI](https://developer.apple.com/swiftui/) with AppKit where
  platform integration requires it.
- Windows: [WinUI 3 and the Windows App SDK](https://learn.microsoft.com/windows/apps/),
  Microsoft's recommended stack for new native Windows desktop applications.
- Linux: [GTK 4](https://gnome.pages.gitlab.gnome.org/gtk/gtk4/getting_started.html)
  with [libadwaita](https://gnome.pages.gitlab.gnome.org/libadwaita/doc/main/)
  for the GNOME-quality application surface.

That means three thin frontends and one tested domain core. It costs more than
a WebView wrapper, so the terminal information architecture must prove itself
first. Electron, WebView2 shells, Wails, and Tauri are out of scope for the
native product direction.

## Gate before desktop work

Desktop work starts only when all of these are true:

- the result and live-event schemas remain versioned and compatibility-tested;
- real use of the TUI has validated the core navigation and data hierarchy;
- calibration supports honest recommendations rather than a prettier guess;
- the CLI remains independently complete and fully scriptable;
- packaging, signing, updates, and accessibility have owners on all three
  platforms.

Until then, terminal UX is product work, not a placeholder.
