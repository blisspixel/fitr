"""Process and listener regressions for the installer artifact fixture."""

import hashlib
import importlib.util
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
from types import SimpleNamespace
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
        (self.artifacts / "SHA256SUMS").write_bytes(b"exact checksum fixture\n")
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

    def test_numeric_loopback_binding_does_not_resolve_hostnames(self):
        with patch("socket.getfqdn", side_effect=AssertionError("unexpected hostname resolution")) as resolve:
            with fixture.ArtifactHTTPServer(("127.0.0.1", 0), fixture.SimpleHTTPRequestHandler) as server:
                self.assertEqual(server.server_name, "127.0.0.1")
                self.assertGreater(server.server_port, 0)
        resolve.assert_not_called()

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

    def test_windows_powershell_child_resets_module_path_without_mutating_parent(self):
        parent = dict(os.environ, PSModulePath="incompatible PS7 modules",
                      pSmOdUlEpAtH="another incompatible module path", FITR_FIXTURE_SENTINEL="retained")
        before = dict(parent)
        url = "http://127.0.0.1:43210"
        for program in ("powershell.exe", r"C:\Windows\System32\WindowsPowerShell\v1.0\PowerShell.EXE"):
            with self.subTest(program=program):
                with patch.object(fixture, "os", SimpleNamespace(name="nt", environ=parent)):
                    environment = fixture.installer_environment([program], url)
                expected = {key: value for key, value in parent.items()
                            if key.upper() != "PSMODULEPATH"}
                expected["FITR_RELEASE_BASE_URL"] = url
                self.assertEqual(environment, expected)
                self.assertEqual(parent, before)
                result = subprocess.run(
                    [sys.executable, "-I", "-c",
                     "import json,os; print(json.dumps({"
                     "'module_keys':[k for k in os.environ if k.upper() == 'PSMODULEPATH'],"
                     "'url':os.environ['FITR_RELEASE_BASE_URL'],"
                     "'sentinel':os.environ['FITR_FIXTURE_SENTINEL']}))"],
                    env=environment, capture_output=True, text=True, timeout=5,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(json.loads(result.stdout),
                                 {"module_keys": [], "url": url, "sentinel": "retained"})

    def test_other_installer_children_keep_module_path(self):
        parent = dict(os.environ, PSModulePath="keep this module path")
        before = dict(parent)
        for platform, program in (("posix", "powershell.exe"), ("nt", "pwsh.exe"),
                                  ("nt", "sh"), ("posix", "/usr/bin/sh")):
            with self.subTest(platform=platform, program=program):
                with patch.object(fixture, "os", SimpleNamespace(name=platform, environ=parent)):
                    environment = fixture.installer_environment([program], "http://127.0.0.1:43210")
                self.assertEqual(environment, dict(parent, FITR_RELEASE_BASE_URL="http://127.0.0.1:43210"))
                self.assertEqual(parent, before)

    @unittest.skipUnless(os.name == "nt", "Windows PowerShell compatibility requires Windows")
    def test_windows_powershell_hashes_fixture_with_incompatible_inherited_modules(self):
        inherited = dict(os.environ)
        environment = {key: value for key, value in inherited.items()
                       if key.upper() != "PSMODULEPATH"}
        incompatible = self.root / "incompatible-modules"
        incompatible.mkdir()
        environment["PsMoDuLePaTh"] = str(incompatible)
        environment["FITR_FIXTURE_HASH_FILE"] = str(self.artifacts / "SHA256SUMS")
        expected = hashlib.sha256((self.artifacts / "SHA256SUMS").read_bytes()).hexdigest().upper()
        environment["FITR_FIXTURE_EXPECTED_HASH"] = expected
        code = (
            "$ErrorActionPreference='Stop'; "
            "if ($PSVersionTable.PSVersion.Major -ne 5) { throw 'Expected Windows PowerShell 5' }; "
            "$command = Get-Command Get-FileHash -ErrorAction Stop; "
            "$hash = Get-FileHash -LiteralPath $env:FITR_FIXTURE_HASH_FILE -Algorithm SHA256; "
            "if ($hash.Hash -cne $env:FITR_FIXTURE_EXPECTED_HASH) { throw 'Fixture hash mismatch' }; "
            "[ordered]@{version=$PSVersionTable.PSVersion.ToString(); source=$command.Source; "
            "modulePath=$command.Module.Path; hash=$hash.Hash; url=$env:FITR_RELEASE_BASE_URL} "
            "| ConvertTo-Json -Compress"
        )
        result = subprocess.run(
            [sys.executable, "-I", "-B", str(SCRIPT), "--directory", str(self.artifacts),
             "--log", str(self.log), "--", "powershell.exe", "-NoProfile", "-NonInteractive",
             "-ExecutionPolicy", "Bypass", "-Command", code],
            env=environment, capture_output=True, text=True, timeout=45,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        diagnostic = json.loads(result.stdout)
        print("Windows PowerShell fixture compatibility: " + json.dumps(diagnostic), flush=True)
        self.assertTrue(diagnostic["version"].startswith("5."))
        self.assertEqual(diagnostic["source"], "Microsoft.PowerShell.Utility")
        self.assertIn("WindowsPowerShell", diagnostic["modulePath"])
        self.assertEqual(diagnostic["hash"], expected)
        self.assertTrue(diagnostic["url"].startswith("http://127.0.0.1:"))
        self.assertEqual(dict(os.environ), inherited)
        with self.assertRaises((OSError, URLError)):
            build_opener(ProxyHandler({})).open(diagnostic["url"] + "/SHA256SUMS", timeout=0.2)

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
