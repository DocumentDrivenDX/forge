#!/usr/bin/env python3
"""Deterministic fixtures for beadbench reasoning-control probes.

Run with ``python3 scripts/beadbench/test_probe_reasoning_controls.py``.
"""

from __future__ import annotations

import json
import pathlib
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))

import probe_reasoning_controls as probe  # noqa: E402


def test_lmstudio_native_probe_uses_api_v1_chat() -> None:
    captured: dict[str, Any] = {}

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self) -> None:  # noqa: N802
            length = int(self.headers.get("Content-Length", "0"))
            captured["path"] = self.path
            captured["body"] = json.loads(self.rfile.read(length))
            payload = {
                "model_instance_id": "qwen/qwen3.6-35b-a3b",
                "output": [
                    {"type": "message", "content": "1554"},
                ],
                "stats": {
                    "input_tokens": 12,
                    "total_output_tokens": 1,
                    "reasoning_output_tokens": 0,
                },
            }
            raw = json.dumps(payload).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def log_message(self, fmt: str, *args: Any) -> None:
            return

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        result = probe.send_probe(
            f"http://127.0.0.1:{server.server_port}/v1",
            "qwen/qwen3.6-35b-a3b",
            "What is 37*42?",
            64,
            5,
            {
                "id": "lmstudio_native_reasoning_off",
                "endpoint": "lmstudio_native_chat",
                "body": {"reasoning": "off"},
            },
        )
    finally:
        server.shutdown()
        thread.join(timeout=2)

    assert captured["path"] == "/api/v1/chat"
    assert captured["body"]["reasoning"] == "off"
    assert captured["body"]["max_output_tokens"] == 64
    assert "messages" not in captured["body"]
    assert result["surface"] == "lmstudio_native_chat"
    assert result["accepted"] is True
    assert result["reasoning_tokens"] == 0


def test_lmstudio_native_response_classification() -> None:
    probes = [
        {
            "id": "lmstudio_native_reasoning_off",
            "surface": "lmstudio_native_chat",
            "accepted": True,
            "reasoning_tokens": 0,
            "reasoning_chars": 0,
        },
        {
            "id": "lmstudio_native_reasoning_low",
            "surface": "lmstudio_native_chat",
            "accepted": True,
            "reasoning_tokens": 8,
            "reasoning_chars": 32,
        },
        {
            "id": "lmstudio_native_reasoning_high",
            "surface": "lmstudio_native_chat",
            "accepted": True,
            "reasoning_tokens": 48,
            "reasoning_chars": 160,
        },
    ]

    capability = probe.classify(probes)

    assert capability["recommended_wire_format"] == "lmstudio_native"
    assert capability["lmstudio_native_reasoning_accepted"] is True
    assert capability["lmstudio_native_off_zero_reasoning_tokens"] is True
    assert capability["lmstudio_native_reasoning_changes_tokens"] is True


def run_all() -> None:
    tests = [
        test_lmstudio_native_probe_uses_api_v1_chat,
        test_lmstudio_native_response_classification,
    ]
    for test in tests:
        test()
        print(f"ok  {test.__name__}")
    print(f"\n{len(tests)} tests passed")


if __name__ == "__main__":
    run_all()
