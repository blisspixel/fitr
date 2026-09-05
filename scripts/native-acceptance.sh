#!/usr/bin/env bash
set -euo pipefail

: "${FITR_UNDER_TEST:?set FITR_UNDER_TEST to the installed candidate binary}"
: "${LLAMA_SERVER_BIN:?set LLAMA_SERVER_BIN to a pinned llama-server binary}"
: "${FITR_ACCEPTANCE_MODEL_A:?set FITR_ACCEPTANCE_MODEL_A to the first pinned GGUF}"
: "${FITR_ACCEPTANCE_MODEL_B:?set FITR_ACCEPTANCE_MODEL_B to the second pinned GGUF}"

acceptance_dir="${FITR_ACCEPTANCE_DIR:-${RUNNER_TEMP:-/tmp}/fitr-native-acceptance}"
results_dir="${acceptance_dir}/results"
port="${FITR_ACCEPTANCE_PORT:-18080}"
ctx="${FITR_ACCEPTANCE_CTX:-8192}"
endpoint="http://127.0.0.1:${port}"
model_a_name="$(basename "${FITR_ACCEPTANCE_MODEL_A}")"
model_b_name="$(basename "${FITR_ACCEPTANCE_MODEL_B}")"
server_pid=""

mkdir -p "${acceptance_dir}/commands" "${acceptance_dir}/servers" "${results_dir}"
export FITR_RESULTS="${results_dir}"
export FITR_BACKEND="llama-server"
export LLAMA_SERVER_URL="${endpoint}"

hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1"
  else
    shasum -a 256 "$1"
  fi
}

record_environment() {
  {
    printf 'recorded_at_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'endpoint=%s\n' "${endpoint}"
    printf 'context=%s\n' "${ctx}"
    printf 'shell=%s\n' "${BASH_VERSION}"
    uname -a
    if command -v sw_vers >/dev/null 2>&1; then
      sw_vers
    elif [ -r /etc/os-release ]; then
      cat /etc/os-release
    fi
    "${FITR_UNDER_TEST}" version
    "${LLAMA_SERVER_BIN}" --version
    hash_file "${FITR_UNDER_TEST}"
    hash_file "${LLAMA_SERVER_BIN}"
    hash_file "${FITR_ACCEPTANCE_MODEL_A}"
    hash_file "${FITR_ACCEPTANCE_MODEL_B}"
  } >"${acceptance_dir}/environment.txt" 2>&1
  cat "${acceptance_dir}/environment.txt"
}

stop_server() {
  [ -n "${server_pid}" ] || return 0
  if kill -0 "${server_pid}" 2>/dev/null; then
    kill -INT "${server_pid}"
    for _ in {1..80}; do
      kill -0 "${server_pid}" 2>/dev/null || break
      sleep 0.25
    done
  fi
  if kill -0 "${server_pid}" 2>/dev/null; then
    kill -TERM "${server_pid}"
    for _ in {1..40}; do
      kill -0 "${server_pid}" 2>/dev/null || break
      sleep 0.25
    done
  fi
  if kill -0 "${server_pid}" 2>/dev/null; then
    echo "llama-server process ${server_pid} did not stop" >&2
    return 1
  fi
  wait "${server_pid}" 2>/dev/null || true
  server_pid=""
  for _ in {1..20}; do
    if ! curl --fail --silent --max-time 1 "${endpoint}/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.25
  done
  echo "llama-server listener remained reachable after its process stopped" >&2
  return 1
}

cleanup() {
  stop_server || true
}
trap cleanup EXIT INT TERM

start_server() {
  model_path="$1"
  label="$2"
  stop_server
  stdout_log="${acceptance_dir}/servers/${label}.stdout.log"
  stderr_log="${acceptance_dir}/servers/${label}.stderr.log"
  "${LLAMA_SERVER_BIN}" -m "${model_path}" \
    --host 127.0.0.1 --port "${port}" --ctx-size "${ctx}" \
    --n-gpu-layers 0 --jinja >"${stdout_log}" 2>"${stderr_log}" &
  server_pid=$!
  ready=0
  for _ in {1..240}; do
    if ! kill -0 "${server_pid}" 2>/dev/null; then
      break
    fi
    if curl --fail --silent --max-time 2 "${endpoint}/health" >/dev/null; then
      ready=1
      break
    fi
    sleep 0.25
  done
  if [ "${ready}" -ne 1 ]; then
    cat "${stderr_log}" >&2
    echo "llama-server did not become ready for ${label}" >&2
    return 1
  fi
  curl --fail --silent "${endpoint}/props" >"${acceptance_dir}/servers/${label}.props.json"
}

run_fitr() {
  label="$1"
  accepted="$2"
  shift 2
  log="${acceptance_dir}/commands/${label}.log"
  {
    printf 'command:'
    printf ' %q' "$@"
    printf '\n'
  } >"${log}"
  set +e
  "$@" >>"${log}" 2>&1
  status=$?
  set -e
  printf '\nexit: %d\n' "${status}" >>"${log}"
  cat "${log}"
  case ",${accepted}," in
    *",${status},"*) ;;
    *)
      echo "${label} exited ${status}; accepted statuses: ${accepted}" >&2
      return 1
      ;;
  esac
}

validate_saved_run() {
  result_path="$1"
  expected_model="$2"
  log="${acceptance_dir}/commands/validate-$(basename "${result_path}" .json).log"
  python3 - "${result_path}" "${expected_model}" "${ctx}" >"${log}" 2>&1 <<'PY'
import json
import sys

path, expected_model, expected_ctx = sys.argv[1], sys.argv[2], int(sys.argv[3])
with open(path, "r", encoding="utf-8") as handle:
    run = json.load(handle)

assert run["schema_version"] == 6, run.get("schema_version")
assert run["model"] == expected_model, run.get("model")
manifest = run["manifest"]
assert manifest["model"]["backend"] == "llama-server", manifest["model"]
assert manifest["model"]["binding"] == "observed_only", manifest["model"]
assert manifest["provenance"]["backend_protocol"] == "fitr.backend.llama-server-native.v1"
context = run["device_fingerprint_v2"]["context"]
assert context["requested_tokens"] == expected_ctx, context
assert context["effective_tokens"] == expected_ctx, context
assert context["effective_source"] == "runtime_report", context
memory = run["memory"]
assert memory["outcome"] == "skipped", memory
assert "does not report resident allocation bytes" in memory["unavailable_reason"], memory

samples = run["speed_repeats"]
assert samples, "no speed samples"
for sample in samples:
    assert sample["decode_tps"] > 0, sample
    assert sample["prefill_tps"] > 0, sample
    assert sample["first_output_observed"] is True, sample
    assert sample["gated_cache_known"] is True, sample
    assert sample["gated_prompt_tokens"] + sample.get("gated_cached_tokens", 0) > 0, sample
    assert sample.get("gated_cached_tokens", 0) == 0, sample
    assert sample["prefill_cache_known"] is True, sample
    assert sample["prompt_tokens"] + sample.get("cached_prompt_tokens", 0) > 0, sample
    assert sample.get("cached_prompt_tokens", 0) == 0, sample
    assert sample["warm_cache_known"] is True, sample
    assert sample["warm_cached_tokens"] > 0, sample
    assert sample.get("warm_prompt_tokens", 0) + sample["warm_cached_tokens"] > 0, sample

print(f"validated {path}")
PY
  cat "${log}"
}

saved_result_for() {
  model_name="$1"
  result_path="$(find "${results_dir}" -maxdepth 1 -type f -name "${model_name}--*.json" -print -quit)"
  if [ -z "${result_path}" ]; then
    echo "no saved result found for ${model_name}" >&2
    return 1
  fi
  printf '%s\n' "${result_path}"
}

record_environment
# This subprocess uses a separate empty result root and no model host. It checks
# the installed binary's stdio profile, not acceptance by a named MCP client.
python3 "$(dirname "${BASH_SOURCE[0]}")/mcp-acceptance.py" "${FITR_UNDER_TEST}" \
  >"${acceptance_dir}/commands/mcp-stdio.log" 2>&1
cat "${acceptance_dir}/commands/mcp-stdio.log"
start_server "${FITR_ACCEPTANCE_MODEL_A}" "model-a"

run_fitr inventory "0" "${FITR_UNDER_TEST}" --backend llama-server
run_fitr advise-a "0,3" "${FITR_UNDER_TEST}" advise "${model_a_name}" \
  --backend llama-server --ctx "${ctx}" --vram-gb 8 --display plain
run_fitr run-a "0,3" "${FITR_UNDER_TEST}" run "${model_a_name}" \
  --backend llama-server --ctx "${ctx}" --quick -k 1 --display plain
validate_saved_run "$(saved_result_for "${model_a_name}")" "${model_a_name}"
run_fitr apply-a "0,3" "${FITR_UNDER_TEST}" apply "${model_a_name}" \
  --backend llama-server
run_fitr doctor-a "0,3" "${FITR_UNDER_TEST}" doctor "${model_a_name}" \
  --backend llama-server
run_fitr diag-a "0,3" "${FITR_UNDER_TEST}" diag "${model_a_name}" \
  --backend llama-server
run_fitr device-a "0" "${FITR_UNDER_TEST}" device --display plain
grep -F "llama-server" "${acceptance_dir}/commands/device-a.log" >/dev/null
if grep -Eq '^  gpu[[:space:]]+(arm64|amd64|x86_64)$' "${acceptance_dir}/commands/device-a.log"; then
  echo "device output used a CPU architecture as the GPU identity" >&2
  exit 1
fi
run_fitr view-a "0,3" "${FITR_UNDER_TEST}" view "${model_a_name}"
run_fitr export-a "0" "${FITR_UNDER_TEST}" export "${model_a_name}" \
  --out "${acceptance_dir}/scorecard-a.html"
run_fitr top-snapshot "0" "${FITR_UNDER_TEST}" top --snapshot
grep -F "${model_a_name}" "${acceptance_dir}/commands/top-snapshot.log" >/dev/null
run_fitr board-refusal "1" "${FITR_UNDER_TEST}" board
grep -F "valid evidence contract" "${acceptance_dir}/commands/board-refusal.log" >/dev/null

stop_server
start_server "${FITR_ACCEPTANCE_MODEL_B}" "model-b"
run_fitr run-b "0,3" "${FITR_UNDER_TEST}" run "${model_b_name}" \
  --backend llama-server --ctx "${ctx}" --quick -k 1 --display plain
validate_saved_run "$(saved_result_for "${model_b_name}")" "${model_b_name}"
run_fitr view-b "0,3" "${FITR_UNDER_TEST}" view "${model_b_name}"
stop_server

# llama-server reports an observed file hash, not a receipt binding the loaded
# bytes. Board and Compare must refuse to rank these otherwise complete runs.
run_fitr compare-refusal "1" "${FITR_UNDER_TEST}" compare "${model_a_name}" "${model_b_name}"
grep -F "valid sealed evidence contract" "${acceptance_dir}/commands/compare-refusal.log" >/dev/null

result_count="$(find "${results_dir}" -type f -name '*.json' | wc -l | tr -d ' ')"
if [ "${result_count}" -lt 2 ]; then
  echo "acceptance produced ${result_count} result files, want at least 2" >&2
  exit 1
fi

printf 'result_count=%s\nstatus=pass\n' "${result_count}" >"${acceptance_dir}/summary.txt"
cat "${acceptance_dir}/summary.txt"
