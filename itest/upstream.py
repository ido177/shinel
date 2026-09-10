"""Local stand-in for the LLM API.

Records the body shinel forwarded (so the test can see the mask) and echoes
it back, either as JSON or as an SSE stream when the request asked to stream.
"""

from __future__ import annotations

import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HOST, PORT = "0.0.0.0", 8080


class Handler(BaseHTTPRequestHandler):
    last = b""
    lock = threading.Lock()
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        print(self.address_string(), "-", fmt % args, flush=True)

    def do_GET(self):
        if self.path.split("?", 1)[0].rstrip("/") != "/last":
            self.send_error(404)
            return
        with self.lock:
            body = self.last
        self._send(200, "text/plain; charset=utf-8", body)

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(n)
        with self.lock:
            Handler.last = body
        stream = False
        try:
            stream = json.loads(body).get("stream") is True
        except (json.JSONDecodeError, TypeError, AttributeError):
            pass
        if not stream:
            self._send(200, "application/json", body)
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(b"data: ")
        view = memoryview(body)
        for i in range(0, len(body), 4):
            self.wfile.write(view[i : i + 4])
            self.wfile.flush()
        self.wfile.write(b"\n\n")
        self.wfile.flush()

    def _send(self, code, ctype, body):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


if __name__ == "__main__":
    ThreadingHTTPServer((HOST, PORT), Handler).serve_forever()
