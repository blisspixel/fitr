"""Offline regressions for the acceptance assertions, run with the locked venv."""

import copy
import importlib.util
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, patch

import anyio


SPEC = importlib.util.spec_from_file_location("acceptance", Path(__file__).with_name("mcp_sdk_acceptance.py"))
smoke = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(smoke)


class PrivacyTests(unittest.TestCase):
    def test_decoded_windows_unicode_and_nested_text_paths(self):
        private = "C:\\Users\\Jóse\\Private evidence"
        spellings = [private, private.replace("\\", "/"), private.upper()]
        for spelling in spellings:
            for value in [spelling, {"content": [{"type": "text", "text": spelling}]},
                          {"content": [{"type": "text", "text": json.dumps({"detail": spelling})}]},
                          {spelling: "hidden in a key"}]:
                with self.subTest(value=value), self.assertRaises(smoke.AcceptanceError):
                    smoke.validate_redaction(json.loads(json.dumps(value)), [private])

    def test_private_fields_and_deep_json_are_rejected(self):
        for key in ("path", "model", "run_id", "requirement_id"):
            with self.subTest(key=key), self.assertRaises(smoke.AcceptanceError):
                smoke.validate_redaction({"text": json.dumps({key: "anything"})}, [])
        value = "safe"
        for _ in range(34):
            value = [value]
        with self.assertRaises(smoke.AcceptanceError):
            smoke.validate_redaction(value, [])

    def test_fixed_diagnostic_and_sanitized_summary_pass(self):
        smoke.validate_redaction({"content": [{"text": smoke.UNAVAILABLE}], "role": "coding"}, ["private-path"])
        smoke.validate_redaction("{not JSON", ["private-path"])


class EvidenceTests(unittest.TestCase):
    def test_preference_bounds_and_lead_must_match_cli(self):
        expected = {"role": "coding", "revision": "sha256:r", "scope": "battery_screening", "state": "lead",
                    "exploration_lead": "sha256:e", "candidates": [{"id": "sha256:e", "state": "eligible",
                    "preference": {"estimate": 0.7, "low": -1e-15, "high": 1 + 1e-15}}]}
        actual = {"role": "coding", "revision": "sha256:r", "scope": "battery_screening", "state": "lead",
                  "exploration_lead": "sha256:e", "adoption_authorized": False, "gap_count": 0,
                  "comparison_ready": False, "candidates": [{"evidence_sha256": "sha256:e", "state": "eligible",
                  "reason_count": 0, "preference": {"estimate": 0.7, "low": 0, "high": 1}}]}
        smoke.validate_review(actual, expected)
        for change in ("lead", "missing_preference", "low", "estimate", "high"):
            broken = copy.deepcopy(actual)
            if change == "lead":
                del broken["exploration_lead"]
            elif change == "missing_preference":
                del broken["candidates"][0]["preference"]
            else:
                broken["candidates"][0]["preference"][change] = 0.4
            with self.subTest(change=change), self.assertRaises(smoke.AcceptanceError):
                smoke.validate_review(broken, expected)

    def test_error_results_cannot_hide_nontext_content(self):
        result = SimpleNamespace(result_type="complete", is_error=True, structured_content=None,
                                 content=[SimpleNamespace(type="text", text=smoke.UNAVAILABLE)])
        smoke.validate_result(result, "", expected_error=True)
        for content in ([], [SimpleNamespace(type="image")], result.content * 2):
            result.content = content
            with self.assertRaises(smoke.AcceptanceError):
                smoke.validate_result(result, "", expected_error=True)

    def test_text_must_equal_structured_result(self):
        data = {"schema": "fitr.mcp.roles.v1", "roles": []}
        result = SimpleNamespace(result_type="complete", is_error=False, structured_content=data,
                                 content=[SimpleNamespace(type="text", text=json.dumps(data))])
        smoke.validate_result(result, data["schema"])
        result.content[0].text = '{"schema":"fitr.mcp.roles.v1","roles":[{}]}'
        with self.assertRaises(smoke.AcceptanceError):
            smoke.validate_result(result, data["schema"])


def transcript():
    return [{"direction": "client", "message": {"jsonrpc": "2.0", "id": 1, "method": "server/discover",
             "params": {"_meta": {"io.modelcontextprotocol/protocolVersion": smoke.PROTOCOL,
                                  "io.modelcontextprotocol/clientCapabilities": {}}}}},
            {"direction": "server", "message": {"jsonrpc": "2.0", "id": 1, "result": {"resultType": "complete"}}}]


class ProtocolTests(unittest.TestCase):
    def test_real_discovery_metadata_correlation_and_completion(self):
        smoke.validate_transcript(transcript(), [], smoke.PROTOCOL)
        for problem in ("synthetic", "legacy", "id_type", "metadata", "incomplete", "duplicate"):
            rows = transcript()
            if problem == "synthetic":
                rows[0]["message"]["method"] = "tools/list"
            elif problem == "legacy":
                rows[0]["message"]["method"] = "initialize"
            elif problem == "id_type":
                rows[1]["message"]["id"] = "1"
            elif problem == "metadata":
                rows[0]["message"]["params"]["_meta"]["io.modelcontextprotocol/protocolVersion"] = "2025-11-25"
            elif problem == "incomplete":
                rows[1]["message"]["result"]["resultType"] = "input_required"
            else:
                rows.extend(copy.deepcopy(rows))
            with self.subTest(problem=problem), self.assertRaises(smoke.AcceptanceError):
                smoke.validate_transcript(rows, [], smoke.PROTOCOL)

    def test_default_mode_requires_its_extra_real_discovery(self):
        with self.assertRaises(smoke.AcceptanceError):
            smoke.validate_transcript(transcript(), [], "auto")
        rows = transcript() + transcript()
        rows[2]["message"]["id"] = rows[3]["message"]["id"] = 2
        smoke.validate_transcript(rows, [], "auto")

    def test_stdout_parse_errors_and_transcript_limit_fail(self):
        stream = smoke.RecordedStream(None, "server", [])
        with self.assertRaises(smoke.AcceptanceError):
            stream.record(ValueError("not JSON-RPC"))
        envelope = SimpleNamespace(message=SimpleNamespace(model_dump=lambda **_: {"data": "long"}))
        with patch.object(smoke, "MAX_TRANSCRIPT_BYTES", 1), self.assertRaises(smoke.AcceptanceError):
            stream.record(envelope)


class IsolationTests(unittest.TestCase):
    def test_read_only_snapshot_detects_create_change_and_delete(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            baseline = smoke.snapshot(root)
            target = root / "receipt.json"
            target.write_text("first", encoding="utf-8")
            created = smoke.snapshot(root)
            self.assertNotEqual(baseline, created)
            target.write_text("second", encoding="utf-8")
            self.assertNotEqual(created, smoke.snapshot(root))
            target.unlink()
            self.assertEqual(baseline, smoke.snapshot(root))

    def test_network_guard_and_dependency_pin(self):
        for event in ("socket.connect", "socket.getaddrinfo", "socket.sendto"):
            with self.subTest(event=event), self.assertRaises(smoke.AcceptanceError):
                smoke.deny_network(event, ())
        smoke.deny_network("open", ())
        self.assertEqual(smoke.installed_dependencies()["mcp"], "2.0.0")
        with patch.object(smoke.importlib.metadata, "version", return_value="different"), self.assertRaises(smoke.AcceptanceError):
            smoke.installed_dependencies()

    def test_changed_inputs_cannot_publish_passed_receipt(self):
        version = (smoke.ROOT / "internal/buildinfo/version.txt").read_text().strip()
        with patch.object(smoke, "input_identities", side_effect=[{"binary": "before"}, {"binary": "after"}]), \
             patch.object(smoke, "installed_dependencies", return_value={"mcp": "2.0.0"}), \
             patch.object(smoke, "run_local", return_value="fitr " + version), \
             patch.object(smoke.sys, "addaudithook"), patch.object(smoke, "run_case", new=AsyncMock(return_value={})), \
             self.assertRaisesRegex(smoke.AcceptanceError, "inputs changed"):
            anyio.run(smoke.accept, Path("unused-binary"))


if __name__ == "__main__":
    unittest.main()
