"""Process and listener regressions for the installer artifact fixture."""

import importlib.util
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch
from urllib.error import URLError
from urllib.request import ProxyHandler, build_opener


SCRIPT = Path(__file__).with_name("installer_fixture.py")
SPEC = importlib.util.spec_from_file_location("installer_fixture", SCRIPT)
fixture = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(fixture)


class InstallerFixtureTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.artifacts = self.root / "artifacts"
        self.artifacts.mkdir()
        (self.artifacts / "SHA256SUMS").write_text("exact checksum fixture\n", encoding="utf-8")
        self.log = self.root / "server.log"

    def test_assigned_port_serves_exact_bytes_and_reaps_child(self):
        for port in (8765, 8766):
            occupied = socket.socket()
            try:
                occupied.bind(("127.0.0.1", port))
            except OSError:
                occupied.close()
            else:
                occupied.listen()
                self.addCleanup(occupied.close)
        server = fixture.ArtifactServer(self.artifacts, self.log)
        with server as url:
            self.assertNotIn(int(url.rsplit(":", 1)[1]), (8765, 8766))
            with build_opener(ProxyHandler({})).open(url + "/SHA256SUMS", timeout=1) as response:
                self.assertEqual(response.read(), b"exact checksum fixture\n")
            self.assertIsNone(server.process.poll())
        self.assertIsNotNone(server.process.returncode)
        with self.assertRaises((OSError, URLError)):
            build_opener(ProxyHandler({})).open(url + "/SHA256SUMS", timeout=0.2)
        self.assertIn("GET /SHA256SUMS", self.log.read_text(encoding="utf-8"))

    def test_missing_manifest_has_bounded_http_diagnostic_and_cleanup(self):
        (self.artifacts / "SHA256SUMS").unlink()
        server = fixture.ArtifactServer(self.artifacts, self.log, timeout=3)
        started = time.monotonic()
        with self.assertRaisesRegex(fixture.FixtureError, "last check: HTTP Error 404"):
            with server:
                self.fail("a missing checksum manifest became ready")
        self.assertLess(time.monotonic() - started, 8)
        self.assertIsNotNone(server.process.returncode)
        self.assertIn("404", self.log.read_text(encoding="utf-8"))

    def test_dead_child_names_actual_exit_and_reaps_it(self):
        spawn = subprocess.Popen

        def exit_before_marker(*_, **kwargs):
            return spawn([sys.executable, "-I", "-c",
                          "import sys; print('owned startup failure', flush=True); sys.exit(17)"],
                         **kwargs)

        server = fixture.ArtifactServer(self.artifacts, self.log, timeout=3)
        with patch.object(fixture.subprocess, "Popen", side_effect=exit_before_marker):
            with self.assertRaisesRegex(fixture.FixtureError, "status 17"):
                with server:
                    self.fail("an exited child became ready")
        self.assertEqual(server.process.returncode, 17)
        self.assertIn("owned startup failure", self.log.read_text(encoding="utf-8"))

    def test_command_exit_is_preserved_and_listener_stops(self):
        command = [sys.executable, "-I", "-c",
                   "import os,sys; print(os.environ['FITR_RELEASE_BASE_URL'], flush=True); sys.exit(7)"]
        result = subprocess.run(
            [sys.executable, "-I", "-B", str(SCRIPT), "--directory", str(self.artifacts),
             "--log", str(self.log), "--", *command],
            capture_output=True, text=True, timeout=10,
        )
        self.assertEqual(result.returncode, 7, result.stderr)
        url = result.stdout.strip()
        self.assertTrue(url.startswith("http://127.0.0.1:"))
        with self.assertRaises((OSError, URLError)):
            build_opener(ProxyHandler({})).open(url + "/SHA256SUMS", timeout=0.2)
        self.assertIn("GET /SHA256SUMS", result.stderr)

    def test_fixture_failure_cannot_masquerade_as_installer_rejection(self):
        marker = self.root / "installer-ran"
        result = subprocess.run(
            [sys.executable, "-I", "-B", str(SCRIPT), "--directory", str(self.root / "missing"),
             "--log", str(self.log), "--", sys.executable, "-I", "-c",
             "from pathlib import Path; Path(" + repr(str(marker)) + ").write_text('ran')"],
            capture_output=True, text=True, timeout=5,
        )
        self.assertEqual(result.returncode, 125, result.stderr)
        self.assertFalse(marker.exists())
        self.assertIn("artifact directory does not exist", result.stderr)


if __name__ == "__main__":
    unittest.main()
