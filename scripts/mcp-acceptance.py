#!/usr/bin/env python3
"""Check an installed binary's bounded MCP stdio profile, without a model host."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


PROTOCOL = "2026-07-28"
META = {
    "io.modelcontextprotocol/protocolVersion": PROTOCOL,
    "io.modelcontextprotocol/clientCapabilities": {},
}


def request(request_id, method, **params):
    return {
        "jsonrpc": "2.0",
        "id": request_id,
        "method": method,
        "params": {"_meta": META, **params},
    }


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        assert key not in result, f"duplicate JSON key: {key}"
        result[key] = value
    return result


def main():
    assert len(sys.argv) == 2, "usage: mcp-acceptance.py <installed-fitr-binary>"
    binary = Path(sys.argv[1]).resolve(strict=True)
    version_file = Path(__file__).resolve().parent.parent / "internal/buildinfo/version.txt"
    version = version_file.read_text(encoding="utf-8").strip()
    requests = [
        request("discover-1", "server/discover"),
        request(23, "tools/list"),
        request("23", "tools/call", name="fitr_roles_list", arguments={}),
    ]
    wire = "".join(json.dumps(item, separators=(",", ":")) + "\n" for item in requests)
    with tempfile.TemporaryDirectory(prefix="fitr-mcp-acceptance-") as temporary:
        results = Path(temporary) / "missing-results"
        environment = dict(os.environ, FITR_RESULTS=str(results))
        process = subprocess.run(
            [str(binary), "mcp", "serve"],
            input=wire,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            encoding="utf-8",
            errors="strict",
            env=environment,
            cwd=temporary,
            timeout=15,
            check=False,
        )
        assert process.returncode == 0, (process.returncode, process.stderr)
        assert process.stderr == "", f"unexpected stderr: {process.stderr!r}"
        assert not results.exists(), "read-only MCP created the evidence directory"
        assert not list(Path(temporary).iterdir()), "MCP wrote into the isolated working directory"

    lines = process.stdout.splitlines()
    assert len(lines) == len(requests), f"expected exactly three JSON lines: {process.stdout!r}"
    assert process.stdout.endswith("\n"), "final protocol response lacks newline framing"
    replies = [json.loads(line, object_pairs_hook=unique_object) for line in lines]
    by_id = {}
    for reply in replies:
        assert set(reply) == {"jsonrpc", "id", "result"}, reply
        assert reply["jsonrpc"] == "2.0", reply
        key = (type(reply["id"]), reply["id"])
        assert key not in by_id, f"duplicate response ID: {reply['id']!r}"
        by_id[key] = reply["result"]
        assert reply["result"]["resultType"] == "complete", reply
        info = reply["result"]["_meta"]["io.modelcontextprotocol/serverInfo"]
        assert info == {"name": "fitr", "version": version}, info
    assert set(by_id) == {(str, "discover-1"), (int, 23), (str, "23")}, by_id

    discovery = by_id[str, "discover-1"]
    assert discovery["supportedVersions"] == [PROTOCOL], discovery
    assert discovery["capabilities"] == {"tools": {}}, discovery
    tools = by_id[int, 23]["tools"]
    assert [tool["name"] for tool in tools] == ["fitr_role_review", "fitr_roles_list"], tools
    for tool in tools:
        assert tool["annotations"] == {
            "readOnlyHint": True, "destructiveHint": False,
            "idempotentHint": True, "openWorldHint": False,
        }, tool
        assert tool["inputSchema"]["additionalProperties"] is False, tool
        assert tool["outputSchema"]["additionalProperties"] is False, tool
    listed = by_id[str, "23"]
    empty_roles = {"schema": "fitr.mcp.roles.v1", "roles": []}
    assert listed["isError"] is False, listed
    assert listed["structuredContent"] == empty_roles, listed
    assert len(listed["content"]) == 1 and listed["content"][0]["type"] == "text", listed
    assert json.loads(listed["content"][0]["text"]) == empty_roles, listed
    print(process.stdout, end="")
    print(f"MCP {PROTOCOL} stdio binary smoke passed: fitr {version}; three replies; no evidence writes.")


if __name__ == "__main__":
    main()
