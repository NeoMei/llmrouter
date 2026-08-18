#!/usr/bin/env python3

import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


STATUS_BODY = b'{"status":"ok"}\n'


class StatusHandler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        self._write_status()

    def do_POST(self) -> None:
        self._write_status()

    def _write_status(self) -> None:
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(STATUS_BODY)))
        self.end_headers()
        self.wfile.write(STATUS_BODY)

    def log_message(self, format: str, *args: object) -> None:
        return


def main() -> None:
    port = int(os.environ.get("UPSTREAM_PORT", "8080"))
    server = ThreadingHTTPServer(("0.0.0.0", port), StatusHandler)
    server.serve_forever()


if __name__ == "__main__":
    main()
