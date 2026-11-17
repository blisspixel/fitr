#!/bin/sh
# Read a GitHub Actions needs object on stdin.
#
# Exit 0 only when the object is non-empty, every required job is
# present, and every result is the string success. The required names
# are the job ids listed in .github/workflows/ci.yml, not matrix legs.
#
# The previous inline check counted entries whose result was not success
# and compared that count with zero. An empty object has no such entry,
# so it passed. A missing required job passed the same way.
#
# Anything else is an incomplete gate. A timeout, a skip, a cancellation,
# a failure, or a missing result is not a product measurement.
set -eu

input=$(cat || true)

missing() {
  echo "ci gate: missing job results" >&2
  exit 1
}

if [ -z "$(printf '%s' "$input" | tr -d '[:space:]')" ]; then
  missing
fi

# jq prints one line. A non-object or an empty object is MISSING.
# A result that is not a short token is reported as unreadable so the
# log does not echo an unexpected payload.
if ! parsed=$(printf '%s' "$input" | jq -r '
  def required: ["test","dist","coverage","reference-python","mcp-sdk","installer","security"];
  def token:
    if type == "string" and test("^[a-z0-9_-]+$") then . else "unreadable" end;
  if type != "object" or length == 0 then
    "MISSING"
  else
    (
      [ required[] as $name |
          (
            (.[$name].result // "missing")
            | token
          ) as $result |
          select($result != "success") |
          "\($name)=\($result)"
      ]
      +
      [ to_entries[] |
          (
            if (.value | type) == "object" then
              (.value.result // "missing")
            else
              "missing"
            end
            | token
          ) as $result |
          select($result != "success") |
          "\(.key)=\($result)"
      ]
    ) | unique | join(" ")
  end
' 2>/dev/null); then
  missing
fi

if [ "$parsed" = "MISSING" ]; then
  missing
fi

if [ -n "$parsed" ]; then
  echo "ci gate incomplete: $parsed" >&2
  exit 1
fi
