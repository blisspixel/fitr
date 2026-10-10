#!/bin/sh
# Installs one already-built fitr binary's embedded Omarchy plugin into a
# temporary directory, reads desktop status against an empty results
# directory, and removes that plugin. This checks the built binary. It does
# not start Omarchy, Quickshell, or a serving runtime, and it is not shell
# acceptance.
set -eu
binary=${1:?built fitr binary}
root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT
plugins="$root/plugins"
results="$root/results"
mkdir -p "$plugins" "$results"
"$binary" desktop install --plugins-dir "$plugins" --display json >/dev/null
test -f "$plugins/dev.fitr.evidence/manifest.json"
test -f "$plugins/dev.fitr.evidence/BarWidget.qml"
test -f "$plugins/dev.fitr.evidence/Panel.qml"
test -f "$plugins/dev.fitr.evidence/.fitr-install.json"
grep -F 'dev.fitr.evidence' "$plugins/dev.fitr.evidence/manifest.json" >/dev/null
grep -F 'BarWidget.qml' "$plugins/dev.fitr.evidence/manifest.json" >/dev/null
export FITR_RESULTS="$results"
export FITR_OPENAI_URL=
export FITR_OPENAI_API_KEY=
export OPENAI_API_KEY=
"$binary" desktop status --display json >"$root/status.json"
grep -F 'fitr.desktop.status.v1' "$root/status.json" >/dev/null
grep -F '"state":"empty"' "$root/status.json" >/dev/null
"$binary" desktop remove --plugins-dir "$plugins" --yes --display json >/dev/null
test ! -e "$plugins/dev.fitr.evidence"
test -d "$plugins"
