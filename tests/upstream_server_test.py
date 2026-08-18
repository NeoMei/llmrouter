#!/usr/bin/env python3

import json
import os
import socket
import subprocess
import sys
import time
import unittest
import urllib.error
import urllib.request
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SERVER = ROOT / "tests/upstream_server.py"


class UpstreamServerTest(unittest.TestCase):
    def test_serves_bounded_json_for_get_and_post(self) -> None:
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]

        env = os.environ.copy()
        env["UPSTREAM_PORT"] = str(port)
        process = subprocess.Popen(
            [sys.executable, str(SERVER)],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
            text=True,
            env=env,
        )
        self.addCleanup(self._stop_process, process)

        base_url = f"http://127.0.0.1:{port}"
        deadline = time.monotonic() + 5
        while True:
            try:
                with urllib.request.urlopen(base_url + "/healthz", timeout=1) as response:
                    self.assertEqual(200, response.status)
                    self.assertEqual("application/json", response.headers.get_content_type())
                    self.assertEqual({"status": "ok"}, json.load(response))
                break
            except (urllib.error.URLError, ConnectionError):
                if process.poll() is not None or time.monotonic() >= deadline:
                    error = process.stderr.read() if process.stderr else ""
                    self.fail(f"upstream server did not start: {error.strip()}")
                time.sleep(0.05)

        request = urllib.request.Request(
            base_url + "/v1/chat/completions",
            data=b'{"model":"test"}',
            method="POST",
        )
        with urllib.request.urlopen(request, timeout=1) as response:
            self.assertEqual(200, response.status)
            self.assertEqual("application/json", response.headers.get_content_type())
            self.assertEqual({"status": "ok"}, json.load(response))

    @staticmethod
    def _stop_process(process: subprocess.Popen[str]) -> None:
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=3)
        if process.stderr is not None:
            process.stderr.close()


if __name__ == "__main__":
    unittest.main()
