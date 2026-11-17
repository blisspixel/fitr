#!/bin/sh
# Reject the floating ubuntu-latest label in workflow selectors.
#
# ubuntu-latest was moving from Ubuntu 24.04 to 26.04 through 2026-11-19,
# so one label could land on either image. Hosted Ubuntu jobs pin
# ubuntu-26.04. A full-line comment may still name the old label when it
# records a measurement taken before the pin. A selector may not.
#
# Checked against actions/runner-images Ubuntu2604-Readme.md on 2026-11-17:
# image 20261111.196.1, OS 26.04.1 LTS. The release binary stays
# CGO_ENABLED=0, so this pin is not a glibc floor.
set -eu

root="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"

if [ "$#" -eq 0 ]; then
  set -- "$root"/.github/workflows/*.yml
fi

fail=0
for file in "$@"; do
  if [ ! -f "$file" ]; then
    echo "runner image: missing $file" >&2
    fail=1
    continue
  fi
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      "") continue ;;
    esac
    number="${line%%:*}"
    content="${line#*:}"
    trimmed=$(printf '%s' "$content" | sed 's/^[[:space:]]*//')
    case "$trimmed" in
      \#*) continue ;;
    esac
    echo "runner image: $file:$number uses ubuntu-latest" >&2
    fail=1
  done <<EOF
$(grep -n 'ubuntu-latest' "$file" || true)
EOF
done

if [ "$fail" -ne 0 ]; then
  exit 1
fi
