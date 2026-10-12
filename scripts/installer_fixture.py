"""Serve exact installer artifacts on an owned loopback listener."""

import argparse
import faulthandler
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from socketserver import TCPServer
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
from urllib.error import URLError
from urllib.request import ProxyHandler, build_opener


class FixtureError(RuntimeError):
    pass


class ArtifactHTTPServer(ThreadingHTTPServer):
    def server_bind(self):
        # The listener is numeric loopback; reverse DNS adds an unbounded startup dependency.
        TCPServer.server_bind(self)
        self.server_name, self.server_port = self.server_address[:2]


def serve(directory, ready_file):
    handler = partial(SimpleHTTPRequestHandler, directory=str(directory))
    print("binding an assigned loopback artifact listener", flush=True)
    faulthandler.dump_traceback_later(10, repeat=True)
    with ArtifactHTTPServer(("127.0.0.1", 0), handler) as server:
        print("publishing the owned listener marker", flush=True)
        marker = {"port": server.server_port, "pid": os.getpid()}
        pending = ready_file.with_suffix(".pending")
        pending.write_text(json.dumps(marker), encoding="utf-8")
        pending.replace(ready_file)
        faulthandler.cancel_dump_traceback_later()
        print("serving installer artifacts on 127.0.0.1:" + str(server.server_port), flush=True)
        server.serve_forever()


def wait_ready(process, ready_file, timeout):
    deadline = time.monotonic() + timeout
    opener = build_opener(ProxyHandler({}))
    last_error = "listener marker not yet written"
    while time.monotonic() < deadline:
        status = process.poll()
        if status is not None:
            raise FixtureError("artifact server exited before readiness with status " + str(status))
        if ready_file.exists():
            marker = json.loads(ready_file.read_text(encoding="utf-8"))
            port = marker["port"]
            if marker["pid"] != process.pid or type(port) is not int or not 0 < port < 65536:
                raise FixtureError("artifact server wrote an invalid listener marker")
            url = "http://127.0.0.1:" + str(port)
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                break
            try:
                with opener.open(url + "/SHA256SUMS", timeout=min(1.0, remaining)) as response:
                    response.read(1)
                if process.poll() is None:
                    return url
            except (OSError, URLError) as exc:
                last_error = str(exc)
        time.sleep(min(0.05, max(0, deadline - time.monotonic())))
    raise FixtureError("artifact server did not serve /SHA256SUMS within "
                       + str(timeout) + " seconds; last check: " + last_error)


class ArtifactServer:
    def __init__(self, directory, log, timeout=30):
        self.directory = Path(directory).resolve()
        self.log = Path(log)
        self.timeout = timeout
        self.process = None
        self.output = None
        self.temporary = None

    def __enter__(self):
        try:
            if not self.directory.is_dir():
                raise FixtureError("artifact directory does not exist: " + str(self.directory))
            self.log.parent.mkdir(parents=True, exist_ok=True)
            self.output = self.log.open("wb")
            self.temporary = tempfile.TemporaryDirectory(prefix="fitr-installer-server-")
            ready_file = Path(self.temporary.name) / "ready.json"
            self.process = subprocess.Popen(
                [sys.executable, "-I", "-B", str(Path(__file__).resolve()), "--serve",
                 "--directory", str(self.directory), "--ready-file", str(ready_file)],
                stdout=self.output, stderr=subprocess.STDOUT,
            )
            return wait_ready(self.process, ready_file, self.timeout)
        except BaseException:
            self.__exit__(None, None, None)
            show_log(self.log)
            raise

    def __exit__(self, *_):
        try:
            if self.process is not None:
                if self.process.poll() is None:
                    self.process.terminate()
                try:
                    self.process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    self.process.kill()
                    self.process.wait(timeout=5)
        finally:
            if self.output is not None:
                self.output.close()
            if self.temporary is not None:
                self.temporary.cleanup()


def show_log(log):
    print("artifact server log: " + str(log), file=sys.stderr)
    if log.exists():
        print(log.read_text(encoding="utf-8", errors="replace"), file=sys.stderr)


def interrupted(*_):
    raise KeyboardInterrupt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", required=True, type=Path)
    parser.add_argument("--log", type=Path)
    parser.add_argument("--serve", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("--ready-file", type=Path, help=argparse.SUPPRESS)
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if args.serve:
        if args.ready_file is None:
            parser.error("server mode requires a ready file")
        serve(args.directory, args.ready_file)
        return 0
    command = args.command[1:] if args.command[:1] == ["--"] else args.command
    if args.log is None or not command:
        parser.error("a server log and an installer command are required")
    signal.signal(signal.SIGTERM, interrupted)
    try:
        with ArtifactServer(args.directory, args.log) as url:
            environment = dict(os.environ, FITR_RELEASE_BASE_URL=url)
            status = subprocess.call(command, env=environment)
    except (FixtureError, OSError, ValueError) as exc:
        print("installer fixture: " + str(exc), file=sys.stderr)
        show_log(args.log)
        return 125
    except KeyboardInterrupt:
        show_log(args.log)
        return 130
    if status != 0:
        show_log(args.log)
    return status


if __name__ == "__main__":
    sys.exit(main())
