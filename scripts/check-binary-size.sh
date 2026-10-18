#!/bin/sh
set -eu

# Measured with Go 1.27.2, CGO_ENABLED=0, -trimpath and -ldflags="-s -w"
# on windows/amd64, the largest target. The Omarchy bar widget and
# `fitr desktop` in 0.11.3 added 85,504 bytes over 0.11.2 on Go 1.27.0:
# 16,065,536 -> 16,151,040. Moving to Go 1.27.2 for its standard library
# security fixes added 20,480 more: 16,171,520. Listening-process
# accelerator evidence, bubblewrap isolation, ranked cleanup, context
# boundary probes, and model suggestions added 77,312 bytes:
# 16,171,520 -> 16,248,832. The guided-fitting recommendation projection
# (validated records into evidence classes) and the cleanup component grouping
# added 48,640 bytes: 16,248,832 -> 16,297,472. One-session tailor, the
# usable-input token receipt, notice vocabulary, suggest weight partition,
# scoring policy v7, and the confirmed-winner projection added 186,368 bytes
# on the 2026-10-18 dist job: 16,297,472 -> 16,483,840. Suggest's measured
# memory selection, the empty-inventory and unmeasured-install next lines,
# the conversation preset, the role-review command, and the embedded Omarchy
# status and urgent-colour handlers added 49,664 bytes on the later dist job
# the same day: 16,483,840 -> 16,533,504. The configuration account, which
# names measured, declared, runtime-default, requested, historical,
# unobserved, and process-environment values, added 56,320 bytes on the
# following dist job: 16,533,504 -> 16,589,824. No new Go dependency was
# added. This deliberate release-envelope update retains the existing
# 38,824-byte headroom. The decision text page for a requested reasoning
# effort, and printing a next-action code instead of a blank experiment,
# added 38,912 bytes on the 2026-10-18 dist job: 16,589,824 -> 16,628,736.
# No new Go dependency was added. The same headroom stays.
# CI and releases use this single measured gate.
max_bytes=16667560
dist_dir="${1:-dist}"
for binary in "$dist_dir"/fitr-*; do
  size="$(wc -c < "$binary")"
  printf '%-32s %10d bytes\n' "$binary" "$size"
  test "$size" -le "$max_bytes"
done
