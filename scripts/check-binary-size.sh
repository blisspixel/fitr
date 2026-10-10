#!/bin/sh
set -eu

# Measured with Go 1.27.2, CGO_ENABLED=0, -trimpath and -ldflags="-s -w"
# on windows/amd64, the largest target. The Omarchy bar widget and
# `fitr desktop` in 0.11.3 added 85,504 bytes over 0.11.2 on Go 1.27.0:
# 16,065,536 -> 16,151,040. Moving to Go 1.27.2 for its standard library
# security fixes added 20,480 more: 16,171,520. The embedded plugin is four
# small text files and no new Go dependency was added. This deliberate
# release-envelope update retains the existing 38,824-byte headroom.
# CI and releases use this single measured gate.
max_bytes=16210344
dist_dir="${1:-dist}"
for binary in "$dist_dir"/fitr-*; do
  size="$(wc -c < "$binary")"
  printf '%-32s %10d bytes\n' "$binary" "$size"
  test "$size" -le "$max_bytes"
done
