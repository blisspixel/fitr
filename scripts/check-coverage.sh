#!/usr/bin/env sh
set -eu

threshold="${1:-80}"
case "$threshold" in
  ''|*[!0-9.]*|.*|*.*.*)
    echo "usage: scripts/check-coverage.sh [percentage]" >&2
    exit 2
    ;;
esac

cover_dir=$(mktemp -d "${TMPDIR:-/tmp}/fitr-coverage.XXXXXX")
trap 'rm -rf "$cover_dir"' EXIT HUP INT TERM

# GOCOVERDIR data is merged by the Go tool before text conversion. This avoids
# double-counting blocks from -coverpkg=./... when every package test binary
# instruments the same dependencies.
go test -count=1 -cover -coverpkg=./... ./... -args "-test.gocoverdir=$cover_dir"
go tool covdata textfmt -i="$cover_dir" -o="$cover_dir/coverage.out"

awk -v threshold="$threshold" '
  NR == 1 { next }
  {
    total += $2
    if ($3 > 0) covered += $2
  }
  END {
    if (total == 0) {
      print "coverage profile contained no statements" > "/dev/stderr"
      exit 1
    }
    percentage = 100 * covered / total
    printf "total coverage: %.2f%% (%d/%d statements); required: %.2f%%\n",
      percentage, covered, total, threshold
    if (percentage + 0.0000001 < threshold) exit 1
  }
' "$cover_dir/coverage.out"
