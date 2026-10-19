# Omarchy bar

The Omarchy surface is a thin Quattro bar widget and a detail panel around
fitr's existing JSON contracts. The chip is one state word. It is hidden when
no role is stored, and it is hidden on a vertical bar because that edge is
28px wide and this widget has no icon. The panel names that state, the model
when one is resolved, the unresolved reasons, and the next sentence. A run
control appears only when the status document says a local run is proven, and
the second click is the confirmation.

Backend decisions stay in fitr. The widget prints strings. It does not score,
rank configurations, or turn a missing observation into a number. The status
document still carries every row and the limit sentences. The panel does not
print them.

The widget ships embedded in the fitr binary from 0.11.3. A published
0.11.5 binary has been exercised on a running Omarchy 4.0.5 session. The
checks that session recorded, and the two it left open, are listed below.

## What the panel reads

`fitr desktop status` writes `fitr.desktop.status.v1`. The projection copies:

| Source | What it contributes |
|---|---|
| `fitr.role.review.v1` | Candidate state and the review's next sentence |
| `fitr.role.lifecycle.v1.status` | Selection state `unselected`, `qualified`, or `stale` |
| `fitr.analysis.run.v1` | Requested context, effective context, capacity prediction, capacity budget, and the analysis next action |

A row stays `unmeasured` when the source field is absent, when a requested
context is zero, when a prediction has no byte fields, or when a budget is
not available. `descriptive_only` stays descriptive. It is not rewritten as
`observed_fit`. Unresolved requirements come only from the matched candidate.
Several stored roles, and no `--role`, stay unresolved. fitr does not pick a
winner, and an exploration lead is not adoption.

The document is presentation. It is not signed, and it is not written back
into a sealed record. Hostnames, local paths, the raw fingerprint key, and
raw model output are omitted. A store error that contains a path or a digest
is replaced with a fixed sentence.

`fitr desktop status` exits 0 when it produced the document, including empty,
stale, and unresolved states. Exit 2 is usage. A non-zero exit, or a payload
that is not `fitr.desktop.status.v1`, is an unavailable or unsupported panel
state. It is not a quality zero. Urgent rows take `bar.urgent` when the host
bar exposes it. That property is `Commons.Color.bar.active` on Omarchy
quattro `shell/plugins/bar/Bar.qml`, read 2026-10-18. Otherwise the row keeps
`barForeground`. The state word stays in the text and the row is bold, so the
urgency does not depend on the colour. On 2026-10-18 a running bar showed
that on Hackerman and on Catppuccin Latte. The sampled urgent pixels were
each theme's `red` (`#50f872` and `#d20f39`). The row text still carried
`unresolved`, and those rows were bold. The plugin source contained no hex
literal. Details are in the session record below.

A status process that stops without `exited` did not start. Quickshell emits `runningChanged` and does not emit `exited`
for `QProcess::FailedToStart`. Checked 2026-10-18 in quickshell-mirror
`process.cpp` at `43d4fa9e883cb03239b3d578c9c57070f4fbd281`, and the same two
signals were still the master behavior that day. The bar then says
`unavailable` instead of `?`. The same session showed that sentence for a
missing command, and a different unavailable sentence when status exited 1.

## What the panel does not do

Opening the panel calls `fitr desktop status` only. Construction of the widget
also refreshes that status on a timer. Neither path downloads a model,
reconfigures serving, or calls a model.

The widget never executes the status document's next action. A measurement is
a second click inside the panel, and the process it starts is only:

```text
fitr desktop benchmark --role <role> --confirm
```

fitr loads the role again and plans `fitr.desktop.benchmark.v1`. The plan
starts a process only when all of these are true:

- `--confirm` was passed
- the analysis action is still an allowlisted `fitr run`
- the model token has no slash, URL, or `..`
- the backend is `ollama` or `llama-server`
- the sealed backend protocol is the matching local protocol and is not empty
- `FITR_OPENAI_URL`, `FITR_OPENAI_API_KEY`, and `OPENAI_API_KEY` are unset or empty
- `OLLAMA_BASE_URL` and `LLAMA_SERVER_URL` are unset, empty, or a plain `http`
  URL whose host is `127.0.0.1`, `localhost`, or `::1`. Those are the client
  URLs the stock run reads, and `--backend` does not override them. Loopback
  is not proof of local inference; this only refuses a run that is visibly
  remote
- the argv has no `--pull`, `--endpoint`, or extra flags

Anything else is shown as text and is not started. The command exits 4 when
no proven local measurement is available. An allowlisted success path is
covered by a unit test with a fake runner. That test does not measure a model.

`fitr desktop` does not call advise, does not probe a serving runtime while
building status, and does not edit the user's server.

## Plugin contract

Checked 2026-10-09 against the Quattro develop guide
(<https://plugins.omarchy.org/develop.html>, updated 13 Aug 2026),
`shell/services/PluginRegistry.qml`, and `shell/Ui/BarWidget.qml` on
`quattro`. Quickshell `Process.command` is a `QStringList`, confirmed at
quickshell-mirror `process.hpp` commit `43d4fa9e883cb03239b3d578c9c57070f4fbd281`.

| Item | Value |
|---|---|
| Plugin id | `dev.fitr.evidence` |
| Plugin version | `0.1.0`, independent of the fitr version |
| Kind | `bar-widget` only. The panel is a `Loader` inside `BarWidget.qml` |
| Entry point | `entryPoints.barWidget` = `BarWidget.qml` |
| Default section | `right` |
| License | Apache-2.0 |
| Location in this repo | `plugins/omarchy` |
| Install location | `~/.config/omarchy/plugins/dev.fitr.evidence` |

Third-party ids must not use the reserved `omarchy.` prefix. There is no
`omarchy.clonedFrom` field. The manifest is not at the repository root.
`omarchy plugin add` of this git URL will not see an Omarchy manifest there,
because this repository's root package is the Agent Plugins package in
`plugins/fitr`.

The widget is unsandboxed inside the long-running `omarchy-shell` process.
It must not start a second Quickshell. Qt 6.12 adds a `Color` type that
shadows `qs.Commons.Color`. This plugin does not reference `Color`.

Settings read with `BarWidget.setting`: `refreshIntervalSec` (clamped to 60
through 3600, default 300), `fitrCommand` (one path token), and `role`.
The integer setting matches the `defaults` plus `schema` shape used by the
Quattro local-ai refresh interval. The command and role are revalidated in
the widget before either process starts. fitr validates them again.

## Install and remove

`fitr desktop install` copies the files embedded in the fitr binary and writes
`fitr.desktop.install.v1` last. The marker records the plugin id, `installed_by`
`fitr`, and the fitr version that wrote it. Reinstall is allowed when the
marker matches, and it overwrites files inside that directory, including later
edits. A directory without the marker is left alone. A symlink is refused.
The command does not edit `shell.json`.

On Linux the default parent is `~/.config/omarchy/plugins`. On any other OS
the command returns exit 4 unless `--plugins-dir` is an absolute directory,
and it does not write a user profile. Exit 2 is a bad invocation. Exit 1 is
a failed copy.

Enable the copied directory with Omarchy, then restart the shell:

```sh
fitr desktop install
omarchy plugin enable dev.fitr.evidence --section right
omarchy restart shell
```

`fitr desktop remove --yes` deletes only the marked directory. It does not
remove the parent plugins directory, does not edit `shell.json`, and does not
delete models or results. Official `omarchy plugin remove`, checked in the
Quattro manual `32-shell-plugins.md` on 2026-10-09, disables first, deletes a
git checkout, unlinks a symlink, and moves a hand-made non-git folder to a
timestamped backup. Disable the widget before removing it if Omarchy still
lists it.

Saving a file under the plugin directory is not a reliable reload on Omarchy
4 with Quickshell 0.3.1: `Qt.clearComponentCache` is not exposed
(omacom/omarchy issue 8555). Restart the shell after install or after a QML
change until a real session shows otherwise.

## What has been checked, and what has not

| Check | What it establishes |
|---|---|
| Go tests of `internal/desktop`, `plugins/omarchy`, the renderer, and `fitr desktop` | Projection rules, the benchmark allowlist, marker install and remove, and that the QML source does not contain a shell string, a URL, or a benchmark inside `open()` |
| `scripts/check-desktop-plugin.sh` on a built binary | That binary can install its embed into a temp directory, emit an empty `fitr.desktop.status.v1`, and remove the marked directory. CI and the release workflow run this on the Linux amd64 dist binary |
| Published 0.11.3 | The first release that embeds the plugin. The release workflow runs the built-binary script above; that is not session acceptance |
| Real Omarchy, 2026-10-12 | Not a session. On Omarchy 4.0.4 the installed `fitr 0.11.3` binary installed the embedded plugin into a temporary directory. `omarchy plugin validate` exited 0. `qmllint` exited 0 and warned about dynamic members. The running bar was not changed |
| Real Omarchy, 2026-10-18 | Partial. Omarchy 4.0.5-1, Quickshell 0.3.2 (`2b4501a`), published `fitr 0.11.5`. The record is below. A qualified selection was not on this machine, and no allowlisted benchmark was started |

A green CI run, including the built-binary script, does not accept the
rendered panel. The 2026-10-18 session is the first running-bar record. It
does not by itself make the widget a measurement, and it leaves the two
checks named below open.

### Session on 2026-10-18

The binary was the published `fitr-linux-amd64` asset from
[0.11.5](https://github.com/blisspixel/fitr/releases/tag/v0.11.5). SHA-256
`09a67954aba9328645b1c084037d678edb6d7775a94f69e19613908d0585a589` matched
`SHA256SUMS`. It reports `fitr 0.11.5`, built with go1.27.2, `vcs.modified=false`,
revision `44604a3d541542c4c5c4e1e054921c28e2b62f21`. It replaced the `fitr`
on `PATH`. The previous binary was kept beside it. `fitr desktop install`
wrote plugin 0.1.0 over the existing marked directory and left `shell.json`
unchanged. `omarchy plugin validate` exited 0. `qmllint` from
`/usr/lib/qt6/bin/qmllint`, with an import root that exposes `qs.Ui` and
`qs.Commons`, exited 0. It warned about host-injected members and unqualified
ids in nested delegates. Neither QML file contains a hex colour literal.

The shell was restarted once after that install. Summon, toggle, hide, and
Escape were then used on the running bar. Toggle is the widget's left-click
handler, called through the shell IPC. A pointer event was not injected.
Escape closed the panel. The machine's own result store has no role, and the
bar read `empty`. The open panel repeated that status document: unmeasured
rows, unknown freshness, and the sentence that no role is stored. It did not
show a quality zero.

A temporary role store, separate from that result directory, supplied the
other documents through the widget's `fitrCommand` setting. Two roles and no
`role` setting put `unresolved` on the bar and "Name one role. The desktop
does not choose among roles." in the panel. The row states stayed
unmeasured. Naming one role, with one attached sealed run and its history
twin, left the role unresolved. The role row and the unresolved requirement
rows were bold and used the shell urgent colour. On Hackerman those pixels
matched `#50f872`, that theme's `red`, which `Color.bar.active` uses when
`bar.active` is absent. On Catppuccin Latte they matched `#d20f39`. The
previous panel literal `#c45c26` was not the row colour. The theme was put
back to Hackerman. The attached run did not become a qualified selection.
This machine has no qualified selection, and none was written for the panel.

A missing `fitrCommand` put `unavailable` on the bar and "fitr desktop status
did not start." in the panel. A command that exited 1 put `unavailable` on
the bar and "fitr desktop status did not produce a document." in the panel.
Neither state was a quality zero.

`fitr desktop benchmark --confirm` on the empty store exited 4 with
`started` false and the reason that no proven local measurement is available.
The same command with `OLLAMA_BASE_URL` set to a non-loopback address exited
4 with `started` false and the ambient-route reason. That refusal is the
release binary's plan. The running shell was not given that variable, and no
panel state had `local_proven`, so the second click was not an allowlisted
start. The panel showed "Request measurement" and no fitr process was
running. An allowlisted local run from the panel remains open.

`omarchy plugin disable` listed the widget disabled, and
`omarchy plugin enable dev.fitr.evidence --section right` listed it enabled.
`omarchy plugin remove --yes` unloaded it and moved the hand-made directory
to a dot backup beside the plugins directory. A following `fitr desktop install`
and `fitr desktop remove --yes` left `shell.json` unchanged, deleted only the
marked directory, and left both the plugins parent and that Omarchy backup in
place. The 0.11.5 plugin was installed and enabled again afterwards, and the
bar again read `empty`.

Still open after this session: a rendered qualified selection, and a second
click that starts an allowlisted local run. The off-host refusal inside a
panel process whose own environment carries the remote URL is the same gate
as that click.

The published 0.11.5 asset's embedded plugin readme still says a running
session has not been accepted. 0.11.6 changes that sentence so it points at
this record and leaves those two checks open. 0.11.6 also shortens the chip
to the state word and stops the panel from printing every row and the limit
sentences. That later panel was not part of this session.

## What this still does not establish

The status document does not establish that a model fits, that a role is the
right role, or that the next command would succeed. Qualified means the
existing selection is qualified and the matched candidate is eligible. It is
not a new verdict. The panel is not a ranking and not the native desktop
application in [the interface direction](interface.md). Rows 7 through 12 of
the roadmap stay where they are.
