#!/bin/sh
set -eu

# Measured with CI's Go 1.27.0, CGO_ENABLED=0 and release flags on
# all six targets. Serving experiments, owned download planning,
# generalized workload evidence classes, boolean recurrent layer
# detection, runtime support profiles, and usable-context scorecards
# added 247,296 bytes to windows/amd64 compared with 0.10.14:
# 15,418,880 -> 15,666,176. No new Go dependency was added.
# At that measurement Windows amd64 was largest. This cap retains
# 38,824 bytes of headroom, close to the preceding cap's 36,120.
# CI and releases use this single gate so a measured update cannot
# leave a stale release cap behind.
max_bytes=15705000
dist_dir="${1:-dist}"
for binary in "$dist_dir"/fitr-*; do
  size="$(wc -c < "$binary")"
  printf '%-32s %10d bytes\n' "$binary" "$size"
  test "$size" -le "$max_bytes"
done
