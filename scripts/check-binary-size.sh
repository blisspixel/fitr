#!/bin/sh
set -eu

# Measured with Go 1.27.0, CGO_ENABLED=0, -trimpath and -ldflags="-s -w"
# on windows/amd64, the largest target at 0.10.15. Owned document-context
# collection and the pinned pi-workspace session added 149,504 bytes:
# 15,666,176 -> 15,815,680. No new Go dependency was added. This cap
# retains 38,824 bytes of headroom, the same margin as the 0.10.15 cap.
# CI and releases use this single gate so a measured update cannot
# leave a stale release cap behind.
max_bytes=15854504
dist_dir="${1:-dist}"
for binary in "$dist_dir"/fitr-*; do
  size="$(wc -c < "$binary")"
  printf '%-32s %10d bytes\n' "$binary" "$size"
  test "$size" -le "$max_bytes"
done
