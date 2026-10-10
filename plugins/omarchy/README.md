# fitr evidence

Read-only Omarchy Quattro bar widget for sealed local evidence. Plugin version
0.1.0, embedded in fitr from 0.11.3. It is not listed on the Omarchy
marketplace, and it has not yet been accepted on a running Omarchy session.

The widget shows the model, role, requested context, measured and estimated
fit, evidence freshness, unresolved requirements, and the next action. Those
rows come from `fitr desktop status`. Opening the panel refreshes that
document. It does not download a model, reconfigure serving, or call a model.

A measurement starts only from a second click, and only when fitr has already
proven the run is an allowlisted local `ollama` or `llama-server` command with
no ambient OpenAI configuration and no non-loopback `OLLAMA_BASE_URL` or
`LLAMA_SERVER_URL`. The widget then runs:

```text
fitr desktop benchmark --role <role> --confirm
```

fitr checks the allowlist again before it starts anything.

## Install

From fitr 0.11.3 or later:

```sh
fitr desktop install
omarchy plugin enable dev.fitr.evidence --section right
omarchy restart shell
```

`fitr desktop install` copies files into `~/.config/omarchy/plugins/dev.fitr.evidence`.
It does not edit `shell.json`. On a platform other than Linux, pass
`--plugins-dir`. This repository is not an Omarchy plugin at its root, so
`omarchy plugin add` of this git URL will not find `manifest.json` there.

## Remove

Disable the widget before deleting its files. `omarchy plugin remove` on a
hand-made folder moves that folder to a timestamped backup. `fitr desktop remove`
deletes the directory only when fitr wrote its install marker and `--yes` is
set. It does not edit `shell.json`, and it does not delete models or results.

```sh
omarchy plugin disable dev.fitr.evidence
fitr desktop remove --yes
```

## Limits

Saving a QML file is not a reliable reload on Omarchy 4 with Quickshell
0.3.1, because `Qt.clearComponentCache` is not exposed. Restart the shell
after installing or changing the plugin. This package does not start a second
Quickshell. The widget is unsandboxed inside the shell process.

Settings on the bar entry, when the shell exposes them: `refreshIntervalSec`
(60 to 3600, default 300), `fitrCommand` (one path token, default `fitr`),
and `role` (one role name, default empty). An empty role uses the only stored
role. Several stored roles stay unresolved until one is named.
