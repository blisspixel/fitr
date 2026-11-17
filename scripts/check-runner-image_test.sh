#!/bin/sh
# The floating Ubuntu label is a selector bug. A comment may name it.
set -eu

checker="$(CDPATH= cd -- "$(dirname "$0")" && pwd)/check-runner-image.sh"
root="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail=0

run_case() {
  name="$1"
  want="$2"
  shift 2
  set +e
  err=$(sh "$checker" "$@" 2>&1)
  status=$?
  set -e
  if [ "$status" -ne "$want" ]; then
    echo "FAIL $name: exit $status, want $want" >&2
    echo "$err" >&2
    fail=1
    return
  fi
  if [ "$want" -ne 0 ] && ! printf '%s\n' "$err" | grep -q "runner image:"; then
    echo "FAIL $name: missing runner image message" >&2
    echo "$err" >&2
    fail=1
  fi
  if [ "$want" -eq 0 ] && [ -n "$err" ]; then
    echo "FAIL $name: wrote output on success" >&2
    echo "$err" >&2
    fail=1
  fi
}

printf '%s\n' '    runs-on: ubuntu-latest' >"$tmp/selector.yml"
printf '%s\n' '# measured on ubuntu-latest: run 43946630808' '    runs-on: ubuntu-26.04' >"$tmp/comment.yml"
printf '%s\n' '    runs-on: ubuntu-26.04' >"$tmp/pinned.yml"
: >"$tmp/empty.yml"

run_case selector 1 "$tmp/selector.yml"
run_case comment 0 "$tmp/comment.yml"
run_case pinned 0 "$tmp/pinned.yml"
run_case empty 0 "$tmp/empty.yml"
run_case missing 1 "$tmp/does-not-exist.yml"
run_case workflows 0 "$root"/.github/workflows/*.yml

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo ok
