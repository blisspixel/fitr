#!/usr/bin/env python3
"""Exercise a real fitr stdio binary through the hash-pinned official MCP SDK.

Only fixture setup mutates the temporary evidence store. The SDK runs with
Python socket connections denied. This is a client acceptance test, not a
named-harness integration test or an OS sandbox for the child executable.
"""

import argparse
from contextlib import asynccontextmanager
from datetime import datetime, timedelta, timezone
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import sys
import tempfile
import time

import anyio
from mcp import Client, StdioServerParameters
from mcp.client.stdio import get_default_environment, stdio_client
from mcp.shared.exceptions import MCPError
from mcp.types import DiscoverResult


ROOT = Path(__file__).resolve().parent.parent
LOCK = ROOT / "scripts/mcp-sdk-requirements.txt"
FIXTURE = ROOT / "internal/record/testdata/schema5-signed-v0.9.8.json"
HELPER = ROOT / "scripts/mcp-sdk-fixture.go"
SELECTION_HELPER = ROOT / "scripts/mcp-selection-fixture.go"
PROTOCOL = "2026-07-28"
MODES = (PROTOCOL, "auto")
TOOL_NAMES = ["fitr_role_review", "fitr_role_status", "fitr_roles_list"]
MAX_TRANSCRIPT_BYTES = 1 << 20
INVALID_ROLE = "Provide role as 1 to 64 lowercase letters, digits or hyphens, starting with a letter or digit."
UNAVAILABLE = "Local evidence is unavailable, invalid or exceeds this profile's limits. Inspect it with fitr role review locally."
STATUS_UNAVAILABLE = "Local selection evidence is unavailable, invalid or exceeds this profile's limits. Inspect it with fitr role status locally."


class AcceptanceError(RuntimeError):
    """A required acceptance property failed."""


def require(condition, message):
    if not condition:
        raise AcceptanceError(message)


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()


def digest_bytes(value):
    return "sha256:" + hashlib.sha256(value).hexdigest()


def digest_file(path):
    with path.open("rb") as stream:
        return "sha256:" + hashlib.file_digest(stream, "sha256").hexdigest()


def snapshot(root):
    if not root.exists():
        return {}
    result = {}
    for path in sorted(root.rglob("*")):
        require(not path.is_symlink(), "fixture unexpectedly contains a symbolic link")
        result[path.relative_to(root).as_posix()] = digest_file(path) if path.is_file() else "directory"
    return result


def child_environment(temporary, results):
    # The SDK supplies its documented minimal OS environment. These additions
    # redirect home/config/temp state away from real accounts and model hosts.
    return {
        "FITR_RESULTS": str(results), "FITR_BACKEND": "invalid-must-not-be-used",
        "HOME": str(temporary), "USERPROFILE": str(temporary),
        "APPDATA": str(temporary), "LOCALAPPDATA": str(temporary),
        "XDG_CONFIG_HOME": str(temporary), "XDG_CACHE_HOME": str(temporary),
        "TEMP": str(temporary), "TMP": str(temporary), "TMPDIR": str(temporary),
    }


def run_local(arguments, environment=None, timeout=30, allowed=(0,)):
    process = subprocess.run(arguments, cwd=ROOT, env=environment, capture_output=True,
                             text=True, encoding="utf-8", timeout=timeout, check=False)
    require(process.returncode in allowed, f"fixture command exited {process.returncode}")
    return process.stdout


def fixture_environment(temporary, results):
    # Preserve the caller's build pins and resolve home-derived Go cache paths
    # before redirecting the helper's runtime home/config/temp to its fixture.
    environment = dict(os.environ)
    keys = ("GOCACHE", "GOMODCACHE", "GOPATH", "GOENV")
    build = json.loads(run_local(["go", "env", "-json", *keys], environment, timeout=10))
    require(all(isinstance(build.get(key), str) and build[key] for key in keys),
            "Go fixture build configuration is unavailable")
    environment.update(build)
    environment.update(child_environment(temporary, results))
    return environment


def prepare_fixture(binary, temporary, results):
    results.mkdir()
    saved = json.loads(run_local([
        "go", "run", str(HELPER), str(results), str(FIXTURE),
    ], fixture_environment(temporary, results), timeout=60))
    environment = dict(get_default_environment(), **child_environment(temporary, results))
    spec = {
        "schema": "fitr.role.spec.v1", "name": "coding", "description": "private-role-canary",
        "max_age_days": 3650,
        "decision": {"schema": "fitr.decision.spec.v1", "name": "private-decision-canary", "evidence_level": "decide",
                     "requirements": [
                         {"id": "private-quality-canary", "behavior": {"need": "structured_output", "minimum_rate": 0.5}},
                         {"id": "context", "context": {"minimum_effective_tokens": 4096}},
                         {"id": "memory", "capacity": {"maximum_resident_bytes": 16 << 30}},
                     ]},
        "preferences": [{"requirement": "private-quality-canary", "weight": 1, "worst": 0, "best": 1}],
    }
    spec_path = temporary / "role-spec.json"
    spec_path.write_bytes(canonical(spec))
    run_local([str(binary), "role", "define", str(spec_path), "--display", "none"], environment)
    run_local([str(binary), "role", "attach", "coding", saved["CanonicalPath"], "--display", "none"], environment)
    review = json.loads(run_local([str(binary), "role", "review", "coding", "--display", "json"],
                                  environment, allowed=(0, 3, 4)))
    require(len(review["candidates"]) == 1, "sealed fixture was not attached through the canonical API")
    require(review["candidates"][0].get("evaluation") is not None,
            "fixture failed integrity or identity checks before decision evaluation")
    require(review["state"] == "single-qualified" and review["candidates"][0].get("preference") is not None,
            "synthetic fixture did not exercise qualification and preference bounds")
    review["_selection_status"] = json.loads(run_local(
        [str(binary), "role", "status", "coding", "--display", "json"], environment, allowed=(0, 3, 4)))
    return review


def prepare_selection_fixture(binary, temporary, results, state):
    require(state in {"qualified", "stale"}, "unknown managed selection fixture state")
    results.mkdir()
    saved = json.loads(run_local([
        "go", "run", str(SELECTION_HELPER), str(results), str(FIXTURE), state,
    ], fixture_environment(temporary, results), timeout=60))
    require(saved["state"] == state and saved.get("selection") is not None,
            "managed fixture did not produce the requested selection")
    selected = saved["selection"]["selected"]
    require(selected.get("store_ref", {}).get("schema") == "fitr.evidence.store.ref.v1"
            and selected.get("runtime_binding", {}).get("kind") == "owned_ollama",
            "managed fixture lacks sealed store and owned runtime evidence")
    require(selected["model"]["requested"] != selected["model"]["resolved"],
            "managed fixture must exercise a distinct private requested alias")
    environment = dict(get_default_environment(), **child_environment(temporary, results))
    review = json.loads(run_local([str(binary), "role", "review", "coding", "--display", "json"],
                                  environment, allowed=(0, 3, 4)))
    status = json.loads(run_local([str(binary), "role", "status", "coding", "--display", "json"],
                                 environment, allowed=(0, 3, 4)))
    require(review["candidates"] == [], "managed selection leaked into ordinary exploration attachments")
    require(status["state"] == state and status.get("selection") == saved["selection"]
            and status["receipt_sha256"] == saved["receipt_sha256"],
            "binary CLI disagrees with public-API managed selection fixture")
    require((review["revision"] != status["selection"]["spec_sha256"]) is (state == "stale"),
            "managed stale fixture did not exercise a changed role revision")
    review["_selection_status"] = status
    return review


class RecordedStream:
    """Observe SDK-decoded envelopes without changing requests or responses."""

    def __init__(self, stream, direction, transcript):
        self.stream, self.direction, self.transcript = stream, direction, transcript

    def record(self, message):
        require(not isinstance(message, Exception), "SDK could not parse a server stdout message")
        value = message.message.model_dump(by_alias=True, exclude_unset=True, mode="json")
        self.transcript.append({"direction": self.direction, "message": value})
        require(len(canonical(self.transcript)) <= MAX_TRANSCRIPT_BYTES, "SDK transcript exceeds its bound")

    async def send(self, message):
        self.record(message)
        await self.stream.send(message)

    async def receive(self):
        message = await self.stream.receive()
        self.record(message)
        return message

    def __aiter__(self):
        return self

    async def __anext__(self):
        try:
            return await self.receive()
        except anyio.EndOfStream:
            raise StopAsyncIteration from None

    async def aclose(self):
        await self.stream.aclose()

    async def __aenter__(self):
        return self

    async def __aexit__(self, *_):
        await self.aclose()


@asynccontextmanager
async def recorded_transport(parameters, stderr, transcript):
    async with stdio_client(parameters, errlog=stderr) as (received, sent):
        yield RecordedStream(received, "server", transcript), RecordedStream(sent, "client", transcript)


def validate_discovery(raw, version):
    require(raw["supportedVersions"] == [PROTOCOL], "server advertises an unexpected protocol revision")
    require(raw["capabilities"] == {"tools": {}}, "server capabilities changed")
    require(raw["_meta"]["io.modelcontextprotocol/serverInfo"] == {"name": "fitr", "version": version},
            "server discovery identity differs from the tested binary")
    require(raw["resultType"] == "complete", "discovery result is not complete")
    return DiscoverResult.model_validate(raw)


def validate_result(result, schema, expected_error=False):
    require(result.result_type == "complete", "tool result is not complete")
    require(result.is_error is expected_error, "unexpected tool error state")
    require(len(result.content) == 1 and result.content[0].type == "text", "tool text shape changed")
    if expected_error:
        require(result.structured_content is None, "tool error exposed structured evidence")
        return
    require(result.structured_content["schema"] == schema, "tool output schema changed")
    require(json.loads(result.content[0].text) == result.structured_content, "text and structured evidence disagree")


def validate_review(actual, expected):
    for field in ("role", "revision", "scope", "state"):
        require(actual[field] == expected[field], f"MCP and local CLI disagree on {field}")
    require(actual["adoption_authorized"] is False, "MCP authorized adoption")
    require(actual["gap_count"] == len(expected.get("gaps", [])), "MCP lost unresolved evidence gaps")
    require(actual["comparison_ready"] is bool(expected.get("comparison", {}).get("ready")), "comparison readiness changed")
    require(actual.get("exploration_lead") == expected.get("exploration_lead"), "exploration lead changed")
    require(len(actual["candidates"]) == len(expected["candidates"]), "candidate count changed")
    for candidate, original in zip(actual["candidates"], expected["candidates"], strict=True):
        require(candidate["evidence_sha256"] == original["id"] and candidate["state"] == original["state"],
                "MCP changed candidate identity or qualification")
        require(candidate["reason_count"] == len(original.get("reasons", [])), "candidate reasons were lost")
        preference = original.get("preference")
        displayed = {key: max(0, min(1, preference[key])) for key in ("estimate", "low", "high")} if preference else None
        require(candidate.get("preference") == displayed, "preference estimate or uncertainty bounds changed")


def validate_selection_status(actual, expected, observed_start, observed_end):
    status = expected["_selection_status"]
    for field in ("role", "scope", "state"):
        require(actual[field] == status[field], f"MCP and CLI selection disagree on {field}")
    require(actual["revision"] == expected["revision"], "MCP selection current revision changed")
    require(actual["lifecycle_sha256"] == status["lifecycle_digest"], "MCP selection lifecycle changed")
    require(actual["adoption_authorized"] is False, "MCP selection status authorized adoption")
    original = status.get("selection")
    projected = None if original is None else {
        "receipt_sha256": status["receipt_sha256"], "revision": original["spec_sha256"],
        "evidence_sha256": original["selected"]["attachment"]["evidence_sha256"],
        "expires_at": original["expires_at"],
    }
    require(actual.get("selection") == projected, "MCP selected evidence differs from CLI")
    evaluated = datetime.fromisoformat(actual["evaluated_at"].replace("Z", "+00:00"))
    require(evaluated.tzinfo is not None, "selection evaluation time is not timezone-aware")
    require(observed_start <= observed_end, "wall clock moved backward during status call")
    # RFC3339Nano has nanoseconds; Python datetime truncates to microseconds.
    # Permit at most one microsecond of serialization precision, not clock skew.
    precision = timedelta(microseconds=1)
    require(observed_start - precision <= evaluated <= observed_end + precision,
            "selection evaluation time is outside the observed status call")


async def exercise_client(client, version, expected):
    # Explicit Client mode installs synthetic discovery. send_discover forces
    # actual wire I/O; discover() alone would return that synthetic cached value.
    discovered = validate_discovery(await client.session.send_discover(PROTOCOL), version)
    client.session.adopt(discovered)
    require(client.protocol_version == PROTOCOL, "client negotiated an unsupported protocol")
    require(client.server_capabilities.tools is not None, "client failed to recognize the tool capability")
    catalog = await client.list_tools()
    require([tool.name for tool in catalog.tools] == TOOL_NAMES, "unexpected tool catalog")
    for tool in catalog.tools:
        require(tool.input_schema.get("additionalProperties") is False, "tool input schema is open")
        require(tool.output_schema.get("additionalProperties") is False, "tool output schema is open")
        require(tool.annotations.read_only_hint is True and tool.annotations.destructive_hint is False,
                "tool safety annotations changed")
    listed = await client.call_tool("fitr_roles_list", {})
    validate_result(listed, "fitr.mcp.roles.v1")
    reviewed = await client.call_tool("fitr_role_review", {"role": "coding"})
    validate_result(reviewed, "fitr.mcp.review.v1", expected_error=expected is None)
    status_started = datetime.now(timezone.utc)
    selected = await client.call_tool("fitr_role_status", {"role": "coding"})
    status_finished = datetime.now(timezone.utc)
    validate_result(selected, "fitr.mcp.role-status.v1", expected_error=expected is None)
    if expected is None:
        require(listed.structured_content["roles"] == [], "empty store exposed role evidence")
        require(reviewed.content[0].text == UNAVAILABLE, "unavailable evidence diagnostic changed")
        require(selected.content[0].text == STATUS_UNAVAILABLE, "unavailable selection diagnostic changed")
    else:
        require(listed.structured_content["roles"] == [{"name": "coding", "revision": expected["revision"], "candidate_count": len(expected["candidates"])}],
                "role list differs from the fixture")
        validate_review(reviewed.structured_content, expected)
        validate_selection_status(selected.structured_content, expected, status_started, status_finished)
        await client.session.validate_tool_result("fitr_role_review", reviewed)
        await client.session.validate_tool_result("fitr_role_status", selected)
    await client.session.validate_tool_result("fitr_roles_list", listed)
    bad = await client.call_tool("fitr_role_review", {"role": "../private-path-canary"})
    validate_result(bad, "", expected_error=True)
    require(bad.content[0].text == INVALID_ROLE, "invalid role diagnostic changed")
    bad_status = await client.call_tool("fitr_role_status", {"role": "../private-path-canary"})
    validate_result(bad_status, "", expected_error=True)
    require(bad_status.content[0].text == INVALID_ROLE, "invalid selection role diagnostic changed")
    try:
        await client.call_tool("does_not_exist", {})
    except MCPError as error:
        require(error.code == -32602, "unknown tool error contract changed")
    else:
        raise AcceptanceError("unknown tool did not produce a protocol error")
    summary = {"listed_roles": len(listed.structured_content["roles"]),
               "review_state": reviewed.structured_content["state"] if expected else "tool_error",
               "selection_state": selected.structured_content["state"] if expected else "tool_error"}
    if expected:
        summary.update(role_revision=expected["revision"],
                       evidence_sha256=[candidate["id"] for candidate in expected["candidates"]],
                       cli_selection_sha256=digest_bytes(canonical(expected["_selection_status"])))
        if selected.structured_content.get("selection"):
            summary["selection"] = selected.structured_content["selection"]
            summary["lifecycle_sha256"] = selected.structured_content["lifecycle_sha256"]
    return summary


def validate_transcript(transcript, private_values, mode):
    requests = [row["message"] for row in transcript if row["direction"] == "client"]
    replies = [row["message"] for row in transcript if row["direction"] == "server"]
    require(requests and requests[0]["method"] == "server/discover", "SDK did not use discovery first")
    require(all(item["method"] != "initialize" for item in requests), "SDK fell back to the legacy handshake")
    require(sum(item["method"] == "server/discover" for item in requests) == (2 if mode == "auto" else 1),
            "real discovery count changed, possibly synthetic SDK cache use")
    ids = [(type(item["id"]).__name__, item["id"]) for item in requests if "id" in item]
    reply_ids = [(type(item["id"]).__name__, item["id"]) for item in replies]
    require(len(set(ids)) == len(ids) and sorted(ids) == sorted(reply_ids), "response correlation or count changed")
    for request in requests:
        meta = request["params"]["_meta"]
        require(meta["io.modelcontextprotocol/protocolVersion"] == PROTOCOL, "request version metadata missing")
        require(isinstance(meta["io.modelcontextprotocol/clientCapabilities"], dict), "client capabilities missing")
    validate_redaction(replies, private_values)
    for reply in replies:
        if "result" in reply:
            require(reply["result"]["resultType"] == "complete", "server emitted an incomplete result")


def validate_redaction(value, private_values, depth=0):
    # Compare decoded strings, including keys. JSON escapes Windows backslashes
    # and non-ASCII names, so matching the serialized JSON would miss leaks.
    require(depth <= 32, "server response nesting exceeds privacy-check bound")
    if isinstance(value, dict):
        require(not {"model", "run_id", "path", "requirement_id"}.intersection(value),
                "server exposed a private evidence field")
        for key, nested in value.items():
            validate_redaction(key, private_values, depth + 1)
            validate_redaction(nested, private_values, depth + 1)
    elif isinstance(value, list):
        for nested in value:
            validate_redaction(nested, private_values, depth + 1)
    elif isinstance(value, str):
        normalized = value.replace("\\", "/").casefold()
        for private in private_values:
            require(private.replace("\\", "/").casefold() not in normalized,
                    "server response exposed a private fixture value")
        # Text content can itself contain JSON, including in tool errors where
        # there is no structured result to compare against.
        if value.lstrip().startswith(("{", "[", '"')):
            try:
                decoded = json.loads(value)
            except ValueError:
                return
            validate_redaction(decoded, private_values, depth + 1)


async def run_case(binary, version, mode, fixture):
    with tempfile.TemporaryDirectory(prefix="fitr-sdk-acceptance-") as raw:
        temporary = Path(raw).resolve()
        results = temporary / "results"
        if fixture == "manual":
            expected = prepare_fixture(binary, temporary, results)
        elif fixture in {"managed-qualified", "managed-stale"}:
            expected = prepare_selection_fixture(binary, temporary, results, fixture.removeprefix("managed-"))
        else:
            require(fixture == "empty", "unknown SDK acceptance fixture")
            expected = None
        before = snapshot(temporary)
        transcript = []
        parameters = StdioServerParameters(command=str(binary), args=["mcp", "serve"],
                                           env=child_environment(temporary, results), cwd=temporary)
        # stderr is outside the monitored fixture tree and never published.
        with tempfile.TemporaryFile(mode="w+", encoding="utf-8") as stderr:
            started = time.monotonic()
            with anyio.fail_after(20):
                options = {} if mode == "auto" else {"mode": mode}
                async with Client(recorded_transport(parameters, stderr, transcript), **options) as client:
                    summary = await exercise_client(client, version, expected)
                    finishing = time.monotonic()
            shutdown_seconds = time.monotonic() - finishing
            require(shutdown_seconds < 5, "SDK subprocess shutdown exceeded five seconds")
            stderr.seek(0)
            require(stderr.read() == "", "server emitted unexpected stderr")
        require(snapshot(temporary) == before, "read-only SDK calls changed the fixture filesystem")
        private = [str(temporary), "private-role-canary", "private-decision-canary", "private-quality-canary",
                   "private-path-canary", "private-model-canary", "private-runtime-canary",
                   "private-confirmation", "private-auto-session"]
        if expected:
            private.extend(item["run_id"] for item in expected["candidates"])
            selection = expected["_selection_status"].get("selection")
            if selection:
                for point in selection["points"]:
                    private.extend([point["attachment"]["path"], point["attachment"]["run_id"],
                                    point["model"]["requested"], point["model"]["resolved"]])
        validate_transcript(transcript, private, mode)
        return {"mode": mode, "fixture": {"manual": "synthetic-current-schema-sealed-canonical",
                                         "managed-qualified": "synthetic-sealed-managed-qualified",
                                         "managed-stale": "synthetic-sealed-managed-stale-role-revision"}.get(fixture, fixture),
                "protocol": PROTOCOL, "state": "passed", "summary": summary,
                "transcript_kind": "sdk-decoded-jsonrpc", "transcript_sha256": digest_bytes(canonical(transcript)),
                "message_count": len(transcript), "evidence_unchanged": True,
                "elapsed_seconds": round(time.monotonic() - started, 3), "sdk_cleanup_seconds": round(shutdown_seconds, 3),
                "child_exit_status_observed": False}


def installed_dependencies():
    require(sys.flags.isolated == 1, "run acceptance with the isolated venv interpreter: python -I")
    require(sys.prefix != sys.base_prefix, "acceptance requires an isolated virtual environment")
    require(platform.python_implementation() == "CPython" and sys.version_info[:2] == (3, 14),
            "acceptance lock requires CPython 3.14")
    result = {}
    for name, wanted in re.findall(r"^([a-z0-9-]+)==([^;\s]+)", LOCK.read_text(encoding="utf-8"), re.MULTILINE):
        if name == "pywin32" and sys.platform != "win32":
            continue
        actual = importlib.metadata.version(name)
        require(actual == wanted, f"installed dependency differs from lock: {name}")
        result[name] = actual
    require(result.get("mcp") == "2.0.0", "official MCP SDK pin missing")
    return result


def deny_network(event, _arguments):
    if event in {"socket.connect", "socket.getaddrinfo", "socket.sendto"}:
        raise AcceptanceError("network access is forbidden during SDK acceptance")


async def accept(binary):
    identities = input_identities(binary)
    dependencies = installed_dependencies()
    version = run_local([str(binary), "version"]).strip().removeprefix("fitr ")
    require(version == (ROOT / "internal/buildinfo/version.txt").read_text(encoding="utf-8").strip(),
            "binary version differs from this checkout")
    sys.addaudithook(deny_network)
    cases = []
    # Preserve the original empty/manual four cases, then add both managed
    # selection states under both real SDK negotiation modes.
    for fixtures in (("empty", "manual"), ("managed-qualified", "managed-stale")):
        for mode in MODES:
            for fixture in fixtures:
                cases.append(await run_case(binary, version, mode, fixture))
    require(input_identities(binary) == identities, "acceptance inputs changed during the run")
    return {"schema": "fitr.acceptance.mcp-sdk.v1", "state": "passed", "scope": "official-sdk-stdio",
            "recorded_at": datetime.now(timezone.utc).isoformat(), "fitr_version": version,
            **identities,
            "python": platform.python_version(), "os": platform.system(), "architecture": platform.machine(),
            "dependencies": dependencies, "cases": cases,
            "limits": {"case_seconds": 20, "sdk_cleanup_seconds": 5, "transcript_bytes": MAX_TRANSCRIPT_BYTES},
            "named_harness_acceptance": False, "model_calls": 0,
            "network_boundary": "Python socket connections denied; fitr stdio-only profile, not an OS sandbox"}


def input_identities(binary):
    return {"binary_sha256": digest_file(binary), "dependency_lock_sha256": digest_file(LOCK),
            "script_sha256": digest_file(Path(__file__)), "fixture_sha256": digest_file(FIXTURE),
            "fixture_helper_sha256": digest_file(HELPER), "selection_fixture_helper_sha256": digest_file(SELECTION_HELPER),
            "plugin_files_sha256": {path.relative_to(ROOT / "plugins/fitr").as_posix(): digest_file(path)
                                     for path in sorted((ROOT / "plugins/fitr").rglob("*")) if path.is_file()}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("--out", type=Path, required=True, help="new acceptance receipt path")
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    require(not args.out.exists(), "acceptance output already exists")
    receipt = anyio.run(accept, binary)
    # Exclusive publication prevents accidentally overwriting an earlier row.
    with args.out.open("x", encoding="utf-8") as stream:
        json.dump(receipt, stream, indent=2)
        stream.write("\n")
    print(f"Official MCP SDK 2.0.0 acceptance passed: fitr {receipt['fitr_version']}; {len(receipt['cases'])} cases; no named harness claim.")


if __name__ == "__main__":
    main()
