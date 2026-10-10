# Omarchy bar

The Omarchy surface is a thin Quattro bar widget and a detail panel around
fitr's existing JSON contracts. It shows, for one declared role, the resolved
model, requested and effective context, estimated fit, measured fit, evidence
freshness, unresolved requirements, and the next action.

Backend decisions stay in fitr. The widget prints strings. It does not score,
rank configurations, or turn a missing observation into a number.

The widget ships embedded in the fitr binary from 0.11.3. This document is
not a claim that a real Omarchy session has accepted the panel; the checks
that establish that are listed below and are still open.

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
state. It is not a quality zero.

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
| Real Omarchy | Not accepted from this workspace. `omarchy plugin validate`, `qmllint`, panel summon and hide, click, Escape, disable, enable, shell restart, a missing fitr binary, a non-zero status, and removal on a running shell still have to be done on an Omarchy machine |

A green CI run, including the built-binary script, does not accept the
rendered panel. Do not describe the widget as working on Omarchy until those
session checks have been recorded.

## What this still does not establish

The status document does not establish that a model fits, that a role is the
right role, or that the next command would succeed. Qualified means the
existing selection is qualified and the matched candidate is eligible. It is
not a new verdict. The panel is not a ranking and not the native desktop
application in [the interface direction](interface.md). Rows 7 through 12 of
the roadmap stay where they are.
