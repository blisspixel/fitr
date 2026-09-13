#!/bin/sh
set -eu

# Measured with CI's Go 1.27.0, CGO_ENABLED=0 and release flags on
# all six targets. Source policy screening, complete-header and
# shard validation, corrected cache arithmetic, and current MCP
# metadata checks add 77,824 bytes to windows/amd64 compared with
# 496e586: 15,341,056 -> 15,418,880. No new Go dependency was added.
# Windows amd64 remains largest. This cap leaves 36,120 bytes of
# headroom, close to the preceding cap's 33,944 on that baseline.
# CI and releases use this single gate so a measured update cannot
# leave a stale release cap behind.
max_bytes=15455000
dist_dir="${1:-dist}"
for binary in "$dist_dir"/fitr-*; do
  size="$(wc -c < "$binary")"
  printf '%-32s %10d bytes\n' "$binary" "$size"
  test "$size" -le "$max_bytes"
done
