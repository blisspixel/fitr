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

The graphs show observations, not certainty. Confidence intervals, repeat
counts, minimum detectable effects, and the refusal to rank across device
fingerprints remain more important than visual density.

## The opt-in full-screen TUI

After the calibration evidence and inventory-first recommendation loop are
working, add an opt-in `fitr top` interface with four views:

1. Live run: current phase, elapsed time, device placement, decode/prefill
   samples, memory, and warnings.
2. Result: independent need verdicts, raw observations, uncertainty, and the
   next useful command.
3. Board: filter and sort within one comparable device/config block, with an
   explicit boundary between blocks.
4. History: inspect configuration changes and open a valid pairwise compare.

The TUI must restore the terminal after interrupts and panics, remain fully
keyboard-driven, work on macOS, Linux, and Windows terminals, and degrade at
narrow widths. Color is redundant with text and shape. Animation is optional
and disabled when it obscures measurements or causes avoidable CPU use.

[Bubble Tea](https://github.com/charmbracelet/bubbletea) is the leading
high-level candidate because it supports inline and full-window Go TUIs and a
state/update/view architecture. [tcell](https://github.com/gdamore/tcell) is
the lower-level fallback with pure-Go support across mainstream Unix systems
and Windows. Neither belongs in the dependency graph until a prototype proves
terminal restoration, Windows behavior, accessibility, binary-size impact,
and compatibility with the repository's Go floor. The current graph renderers
use the standard library and establish the presentation model first.

## One core, several native surfaces

Measurement, scoring, calibration, and recommendation stay in Go packages
with no UI assumptions. Before a desktop client, extract a versioned,
read-only presentation schema and a structured event stream from the CLI.
Every interface consumes those contracts and must produce the same verdicts.
No frontend is allowed to reimplement scoring.

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

- the result and live-event schemas are versioned and have compatibility tests;
- the TUI has made the core navigation and data hierarchy routine;
- calibration supports honest recommendations rather than a prettier guess;
- the CLI remains independently complete and fully scriptable;
- packaging, signing, updates, and accessibility have owners on all three
  platforms.

Until then, terminal UX is product work, not a placeholder.
