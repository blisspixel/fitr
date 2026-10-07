#!/bin/sh
set -eu

# Measured with Go 1.27.0, CGO_ENABLED=0, -trimpath and -ldflags="-s -w"
# on windows/amd64, the largest target. Guided fitting and the associated
# evidence/privacy fixes in 0.11.2 added 249,856 bytes over 0.11.0:
# 15,815,680 -> 16,065,536. No new Go dependency was added. This deliberate
# release-envelope update retains the existing 38,824-byte headroom.
# CI and releases use this single measured gate.
max_bytes=16104360
dist_dir="${1:-dist}"
for binary in "$dist_dir"/fitr-*; do
  size="$(wc -c < "$binary")"
  printf '%-32s %10d bytes\n' "$binary" "$size"
  test "$size" -le "$max_bytes"
done
