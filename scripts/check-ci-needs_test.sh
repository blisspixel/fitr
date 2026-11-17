#!/bin/sh
# Exercises the aggregate CI gate. A timeout, a skip, a failure, or a
# missing required job is an incomplete gate, not a product measurement.
set -eu

checker="$(CDPATH= cd -- "$(dirname "$0")" && pwd)/check-ci-needs.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail=0

# The success payload is built below so a renamed job has one edit site
# in the checker and one matching object here.

success_json() {
  printf '%s' '{'
  sep=""
  for name in test dist coverage reference-python mcp-sdk installer security; do
    printf '%s"%s":{"result":"success","outputs":{}}' "$sep" "$name"
    sep=","
  done
  printf '%s' '}'
}

run_case() {
  name="$1"
  want="$2"
  kind="$3"
  input="$4"
  # set -e would abort on the failing cases this file exists to record.
  set +e
  printf '%s' "$input" | sh "$checker" >"$tmp/out" 2>"$tmp/err"
  status=$?
  set -e
  if [ "$status" -ne "$want" ]; then
    echo "FAIL $name: exit $status, want $want" >&2
    echo "stderr: $(cat "$tmp/err")" >&2
    fail=1
    return
  fi
  if [ -s "$tmp/out" ]; then
    echo "FAIL $name: wrote to stdout" >&2
    fail=1
    return
  fi
  if [ "$want" -eq 0 ]; then
    if [ -s "$tmp/err" ]; then
      echo "FAIL $name: wrote to stderr on success" >&2
      fail=1
    fi
    return
  fi
  if ! grep -q "$kind" "$tmp/err"; then
    echo "FAIL $name: stderr missing $kind" >&2
    echo "stderr: $(cat "$tmp/err")" >&2
    fail=1
  fi
}

run_case all_success 0 unused "$(success_json)"

run_case empty_stdin 1 "ci gate: missing job results" ""
run_case blank_stdin 1 "ci gate: missing job results" "   "
run_case array 1 "ci gate: missing job results" "[]"
run_case null_json 1 "ci gate: missing job results" "null"
run_case empty_object 1 "ci gate: missing job results" "{}"

run_case failure 1 "ci gate incomplete:" "$(printf '%s' "$(success_json)" | jq -c '.test.result = "failure"')"
run_case skipped 1 "ci gate incomplete:" "$(printf '%s' "$(success_json)" | jq -c '.dist.result = "skipped"')"
run_case cancelled 1 "ci gate incomplete:" "$(printf '%s' "$(success_json)" | jq -c '.security.result = "cancelled"')"
run_case timed_out 1 "ci gate incomplete:" "$(printf '%s' "$(success_json)" | jq -c '.coverage.result = "timed_out"')"
run_case missing_result 1 "ci gate incomplete:" "$(printf '%s' "$(success_json)" | jq -c '.installer |= del(.result)')"
run_case null_result 1 "ci gate incomplete:" "$(printf '%s' "$(success_json)" | jq -c '.["mcp-sdk"].result = null')"
run_case dropped_job 1 "ci gate incomplete:" "$(printf '%s' "$(success_json)" | jq -c 'del(.security)')"
run_case extra_failure 1 "ci gate incomplete:" "$(printf '%s' "$(success_json)" | jq -c '.extra = {"result":"failure"}')"
run_case weird_result 1 "ci gate incomplete:" "$(printf '%s' "$(success_json)" | jq -c '.test.result = "not a token"')"
# A result that is not a short token must not be copied into the log.
weird="$(printf '%s' "$(success_json)" | jq -c '.test.result = "not a token"')"
set +e
printf '%s' "$weird" | sh "$checker" >"$tmp/out" 2>"$tmp/err"
set -e
if grep -q "not a token" "$tmp/err" || ! grep -q "test=unreadable" "$tmp/err"; then
  echo "FAIL weird_result: result was echoed or not marked unreadable" >&2
  echo "stderr: $(cat "$tmp/err")" >&2
  fail=1
fi

# An extra successful job does not hide a required one, and it does not fail.
run_case extra_success 0 unused "$(printf '%s' "$(success_json)" | jq -c '.extra = {"result":"success"}')"

# A failed dependency skips the jobs that need it. Both have to be named.
mixed="$(printf '%s' "$(success_json)" | jq -c '.dist.result = "failure" | .["mcp-sdk"].result = "skipped" | .installer.result = "skipped"')"
run_case skipped_downstream 1 "dist=failure" "$mixed"
set +e
printf '%s' "$mixed" | sh "$checker" >"$tmp/out" 2>"$tmp/err"
set -e
if ! grep -q "mcp-sdk=skipped" "$tmp/err" || ! grep -q "installer=skipped" "$tmp/err"; then
  echo "FAIL skipped_downstream: did not name both skipped jobs" >&2
  echo "stderr: $(cat "$tmp/err")" >&2
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "ok"
