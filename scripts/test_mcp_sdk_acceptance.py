"""Offline regressions for the acceptance assertions, run with the locked venv."""

import copy
from datetime import datetime, timedelta, timezone
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


def status_interval():
    return datetime(2026, 9, 5, tzinfo=timezone.utc), datetime(2026, 9, 5, 0, 0, 1, tzinfo=timezone.utc)


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
    def test_selected_identity_and_stale_state_must_match_cli(self):
        expected = {"revision": "sha256:current", "_selection_status": {
            "role": "coding", "scope": "battery_screening", "state": "stale",
            "lifecycle_digest": "sha256:life", "receipt_sha256": "sha256:receipt",
            "selection": {"spec_sha256": "sha256:original", "expires_at": "2026-09-06T00:00:00Z",
                          "selected": {"attachment": {"evidence_sha256": "sha256:evidence"}}}}}
        actual = {"role": "coding", "revision": "sha256:current", "scope": "battery_screening",
                  "state": "stale", "lifecycle_sha256": "sha256:life", "adoption_authorized": False,
                  "evaluated_at": "2026-09-05T00:00:00Z", "selection": {
                      "receipt_sha256": "sha256:receipt", "revision": "sha256:original",
                      "evidence_sha256": "sha256:evidence", "expires_at": "2026-09-06T00:00:00Z"}}
        smoke.validate_selection_status(actual, expected, *status_interval())
        for field in ("state", "revision", "lifecycle_sha256", "adoption_authorized", "selection"):
            broken = copy.deepcopy(actual)
            broken[field] = "qualified" if field == "state" else True
            with self.subTest(field=field), self.assertRaises(smoke.AcceptanceError):
                smoke.validate_selection_status(broken, expected, *status_interval())
        for field in actual["selection"]:
            broken = copy.deepcopy(actual)
            broken["selection"][field] = "substituted"
            with self.subTest(field=field), self.assertRaises(smoke.AcceptanceError):
                smoke.validate_selection_status(broken, expected, *status_interval())

    def test_unselected_cannot_gain_a_selection(self):
        expected = {"revision": "sha256:current", "_selection_status": {
            "role": "coding", "scope": "battery_screening", "state": "unselected",
            "lifecycle_digest": "sha256:life"}}
        actual = {"role": "coding", "revision": "sha256:current", "scope": "battery_screening",
                  "state": "unselected", "lifecycle_sha256": "sha256:life", "adoption_authorized": False,
                  "evaluated_at": "2026-09-05T00:00:00Z"}
        smoke.validate_selection_status(actual, expected, *status_interval())
        actual["selection"] = {"evidence_sha256": "invented"}
        with self.assertRaises(smoke.AcceptanceError):
            smoke.validate_selection_status(actual, expected, *status_interval())

    def test_status_timestamp_must_be_observed_during_the_actual_call(self):
        expected = {"revision": "sha256:current", "_selection_status": {
            "role": "coding", "scope": "battery_screening", "state": "unselected", "lifecycle_digest": "sha256:life"}}
        actual = {"role": "coding", "revision": "sha256:current", "scope": "battery_screening", "state": "unselected",
                  "lifecycle_sha256": "sha256:life", "adoption_authorized": False, "evaluated_at": "2026-09-05T00:00:00.123456789Z"}
        start, end = status_interval()
        smoke.validate_selection_status(actual, expected, start, end)
        # Two clocks read by two runtimes disagree by more than a serialization
        # precision, so the window is widened by a fixed tolerance. Readings
        # inside it stay acceptable; a replayed or mis-zoned timestamp does not.
        for value in ((start - timedelta(seconds=1)).isoformat(), (end + timedelta(seconds=1)).isoformat()):
            actual["evaluated_at"] = value
            with self.subTest(inside=value):
                smoke.validate_selection_status(actual, expected, start, end)
        for value in ("1900-01-01T00:00:00Z", "2999-01-01T00:00:00Z", "2026-09-05T00:00:00",
                      (start - timedelta(seconds=30)).isoformat(), (end + timedelta(seconds=30)).isoformat()):
            actual["evaluated_at"] = value
            with self.subTest(value=value), self.assertRaises(smoke.AcceptanceError):
                smoke.validate_selection_status(actual, expected, start, end)
        actual["evaluated_at"] = start.isoformat()
        with self.assertRaises(smoke.AcceptanceError):
            smoke.validate_selection_status(actual, expected, end, start)

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


class ManagedFixtureTests(unittest.TestCase):
    @staticmethod
    def fixture_results(state="qualified"):
        selection = {"spec_sha256": "sha256:original", "points": [], "selected": {
            "model": {"requested": "private-alias", "resolved": "private-model"}, "store_ref": {"schema": "fitr.evidence.store.ref.v1"},
            "runtime_binding": {"kind": "owned_ollama"}}}
        saved = {"state": state, "selection": selection, "receipt_sha256": "sha256:receipt"}
        review = {"revision": "sha256:original" if state == "qualified" else "sha256:changed", "candidates": []}
        return saved, review, copy.deepcopy(saved)

    def test_managed_fixture_requires_same_sealed_selection_from_actual_cli(self):
        for state in ("qualified", "stale"):
            with self.subTest(state=state), tempfile.TemporaryDirectory() as raw:
                temporary = Path(raw)
                saved, review, status = self.fixture_results(state)
                with patch.object(smoke, "fixture_environment", return_value={}), patch.object(smoke, "run_local", side_effect=[json.dumps(saved), json.dumps(review), json.dumps(status)]) as commands:
                    result = smoke.prepare_selection_fixture(Path("binary"), temporary, temporary / "results", state)
                self.assertEqual(result["_selection_status"], status)
                self.assertEqual(commands.call_args_list[0].args[0][:3], ["go", "run", str(smoke.SELECTION_HELPER)])
                self.assertEqual(commands.call_args_list[-1].args[0][1:4], ["role", "status", "coding"])

    def test_unbound_managed_fixture_or_cli_substitution_cannot_pass(self):
        for failure in ("store", "runtime", "state", "receipt", "selected", "attachments", "unchanged_revision"):
            saved, review, status = self.fixture_results("stale")
            if failure == "store":
                del saved["selection"]["selected"]["store_ref"]
            elif failure == "runtime":
                del saved["selection"]["selected"]["runtime_binding"]
            elif failure == "state":
                status["state"] = "qualified"
            elif failure == "receipt":
                status["receipt_sha256"] = "substituted"
            elif failure == "selected":
                status["selection"]["spec_sha256"] = "substituted"
            elif failure == "attachments":
                review["candidates"] = [{}]
            else:
                review["revision"] = saved["selection"]["spec_sha256"]
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as raw:
                temporary = Path(raw)
                with patch.object(smoke, "fixture_environment", return_value={}), patch.object(smoke, "run_local", side_effect=[json.dumps(saved), json.dumps(review), json.dumps(status)]), self.assertRaises(smoke.AcceptanceError):
                    smoke.prepare_selection_fixture(Path("binary"), temporary, temporary / "results", "stale")

    def test_new_helper_bytes_are_bound_in_receipt_inputs(self):
        with tempfile.TemporaryDirectory() as raw:
            helper = Path(raw) / "helper.go"
            helper.write_text("first", encoding="utf-8")
            with patch.object(smoke, "SELECTION_HELPER", helper):
                first = smoke.input_identities(smoke.HELPER)
                helper.write_text("changed", encoding="utf-8")
                second = smoke.input_identities(smoke.HELPER)
        self.assertNotEqual(first["selection_fixture_helper_sha256"], second["selection_fixture_helper_sha256"])
        self.assertEqual(first["fixture_helper_sha256"], second["fixture_helper_sha256"])


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
    def test_fixture_environment_redirects_runtime_paths_preserving_go_build_pins(self):
        build = {"GOCACHE": "D:/build/go", "GOMODCACHE": "D:/build/mod", "GOPATH": "D:/build/path", "GOENV": "off"}
        inherited = {"HOME": "ambient-home", "USERPROFILE": "ambient-profile", "GOCACHE": "D:/build/go",
                     "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "PATH": "required-go-path"}
        observed = []
        def read_build(_arguments, environment, **_options):
            observed.append(dict(environment))
            return json.dumps(build)
        with patch.dict(smoke.os.environ, inherited, clear=True), patch.object(smoke, "run_local", side_effect=read_build):
            actual = smoke.fixture_environment(Path("private-temp"), Path("private-results"))
        self.assertEqual(observed, [inherited])
        for key in ("GOTOOLCHAIN", "GOPROXY", "GOSUMDB", "PATH"):
            self.assertEqual(actual[key], inherited[key])
        for key, value in build.items():
            self.assertEqual(actual[key], value)
        for key in ("HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "TEMP", "TMP", "TMPDIR"):
            self.assertEqual(actual[key], "private-temp")
        self.assertEqual(actual["FITR_RESULTS"], "private-results")
        with patch.object(smoke, "run_local", return_value="{}"), self.assertRaises(smoke.AcceptanceError):
            smoke.fixture_environment(Path("private-temp"), Path("private-results"))

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
