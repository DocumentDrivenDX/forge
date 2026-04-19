#!/usr/bin/env python3
"""
harbor_agent.py — Harbor 0.3.x BaseInstalledAgent adapter for ddx-agent.

This adapter stages a prebuilt ddx-agent binary into the task environment,
writes a minimal config rooted under /installed-agent/home, runs ddx-agent in
the task workspace, and converts downloaded session logs into a trajectory file
that our benchmark scoring path can consume.
"""

from __future__ import annotations

import json
import os
import shlex
import uuid
from pathlib import Path
from typing import Any

from harbor.agents.installed.base import BaseInstalledAgent, with_prompt_template
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext

_INSTALL_ROOT = "/installed-agent"
_BINARY_TARGET = f"{_INSTALL_ROOT}/ddx-agent"
_HOME_DIR = f"{_INSTALL_ROOT}/home"
_CONFIG_TARGET = f"{_HOME_DIR}/.config/agent/config.yaml"
_SESSION_LOG_DIR = "/logs/agent/sessions"
_OUTPUT_LOG = "/logs/agent/ddx-agent.txt"


def _bench_env(name: str, default: str = "") -> str:
    return os.environ.get(name, default)


def _provider_headers_yaml() -> str:
    raw = _bench_env("DDX_BENCH_PROVIDER_HEADERS_JSON", "")
    if not raw:
        return ""
    headers = json.loads(raw)
    if not isinstance(headers, dict):
        raise ValueError("DDX_BENCH_PROVIDER_HEADERS_JSON must decode to an object")
    lines = ["    headers:"]
    for key, value in headers.items():
        lines.append(f"      {key}: {json.dumps(str(value))}")
    return "\n".join(lines) + "\n"


def _render_provider_config() -> str:
    provider_name = _bench_env("DDX_BENCH_PROVIDER_NAME", "benchmark")
    provider_type = _bench_env("DDX_BENCH_PROVIDER_TYPE", "anthropic")
    provider_model = _bench_env(
        "DDX_BENCH_PROVIDER_MODEL", "claude-haiku-4-5-20251001"
    )
    api_key_env = _bench_env("DDX_BENCH_PROVIDER_API_KEY_ENV", "ANTHROPIC_API_KEY")
    base_url = _bench_env("DDX_BENCH_PROVIDER_BASE_URL", "")

    provider_reasoning = _bench_env("DDX_BENCH_PROVIDER_REASONING", "")

    lines = [
        "providers:",
        f"  {provider_name}:",
        f"    type: {provider_type}",
        f'    api_key: "${{{api_key_env}}}"',
        f"    model: {provider_model}",
    ]
    if base_url:
        lines.append(f"    base_url: {base_url}")
    if provider_reasoning:
        lines.append(f"    reasoning: {provider_reasoning}")
    headers_yaml = _provider_headers_yaml().rstrip()
    if headers_yaml:
        lines.append(headers_yaml)
    lines.extend(
        [
            f"default_provider: {provider_name}",
            f"session_log_dir: {_SESSION_LOG_DIR}",
        ]
    )
    return "\n".join(lines) + "\n"


def _agent_flags() -> list[str]:
    flags = ["--json", "--preset", _bench_env("DDX_BENCH_PRESET", "benchmark")]
    system_append = _bench_env("DDX_BENCH_SYSTEM_APPEND", "")
    if system_append:
        flags.extend(["--system", system_append])
    return flags


class DDXAgent(BaseInstalledAgent):
    SUPPORTS_ATIF: bool = False

    def __init__(self, *args: Any, **kwargs: Any):
        kwargs.setdefault("version", "ddx-agent-benchmark")
        super().__init__(*args, **kwargs)

    @staticmethod
    def name() -> str:
        return "ddx-agent"

    async def install(self, environment: BaseEnvironment) -> None:
        binary_src = Path(os.environ.get("HARBOR_AGENT_ARTIFACT", ""))
        if not binary_src.exists():
            binary_src = Path(__file__).parent / "ddx-agent-linux-amd64"
        if not binary_src.exists():
            raise FileNotFoundError(
                f"ddx-agent binary not found. Expected {binary_src} or set "
                "HARBOR_AGENT_ARTIFACT to the host binary path."
            )

        await self.exec_as_root(
            environment,
            command=(
                f"mkdir -p {_INSTALL_ROOT} {_HOME_DIR}/.config/agent /logs/agent "
                f"&& chmod 755 {_INSTALL_ROOT}"
            ),
        )

        await environment.upload_file(binary_src, _BINARY_TARGET)
        await self.exec_as_root(
            environment, command=f"chmod 755 {_BINARY_TARGET}"
        )

        local_config = self.logs_dir / "config.yaml"
        local_config.write_text(_render_provider_config(), encoding="utf-8")
        await environment.upload_file(local_config, _CONFIG_TARGET)
        await self.exec_as_root(
            environment,
            command=f"chmod 600 {_CONFIG_TARGET} && chown -R $(id -u):$(id -g) {_HOME_DIR}",
        )

    def _run_env(self, instruction: str) -> dict[str, str]:
        env = {
            "HARBOR_INSTRUCTION": instruction,
            "HOME": _HOME_DIR,
            "XDG_CONFIG_HOME": f"{_HOME_DIR}/.config",
        }
        api_key_env = _bench_env("DDX_BENCH_PROVIDER_API_KEY_ENV", "")
        if api_key_env:
            value = os.environ.get(api_key_env, "")
            if value:
                env[api_key_env] = value
        return env

    @with_prompt_template
    async def run(
        self,
        instruction: str,
        environment: BaseEnvironment,
        context: AgentContext,
    ) -> None:
        del context

        ddx_flags = " ".join(shlex.quote(flag) for flag in _agent_flags())
        command = (
            "set -euo pipefail; "
            "cd /testbed 2>/dev/null || cd /workspace 2>/dev/null || true; "
            f"{_BINARY_TARGET} {ddx_flags} "
            '--work-dir "$(pwd)" '
            '-p "$HARBOR_INSTRUCTION" '
            f'2>&1 | stdbuf -oL tee {_OUTPUT_LOG}'
        )

        await self.exec_as_agent(
            environment,
            command=command,
            env=self._run_env(instruction),
        )

    def populate_context_post_run(self, context: AgentContext) -> None:
        trajectory, totals = self._build_trajectory()
        trajectory_path = self.logs_dir / "trajectory.json"
        trajectory_path.write_text(json.dumps(trajectory, indent=2), encoding="utf-8")

        context.n_input_tokens = totals["input"]
        context.n_output_tokens = totals["output"]
        context.cost_usd = totals["cost"]

    def _build_trajectory(self) -> tuple[dict[str, Any], dict[str, float]]:
        session_files = sorted(
            (self.logs_dir / "sessions").glob("*.jsonl"),
            key=lambda p: p.stat().st_mtime,
        )
        if not session_files:
            return self._empty_trajectory(), {"input": 0, "output": 0, "cost": 0.0}

        events: list[dict[str, Any]] = []
        for line in session_files[-1].read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if not line:
                continue
            events.append(json.loads(line))

        steps: list[dict[str, Any]] = []
        session_id = session_files[-1].stem
        model_name = ""
        total_input = 0
        total_output = 0
        total_cost = 0.0
        step_id = 1

        for event in events:
            etype = event.get("type", "")
            data = event.get("data") or {}
            if isinstance(data, str):
                try:
                    data = json.loads(data)
                except json.JSONDecodeError:
                    data = {}
            timestamp = event.get("timestamp") or event.get("ts")
            session_id = event.get("session_id", session_id)

            if etype == "session.start":
                model_name = data.get("model", model_name)
                prompt = data.get("prompt", "")
                if prompt:
                    steps.append(
                        {
                            "step_id": step_id,
                            "timestamp": timestamp,
                            "source": "user",
                            "message": prompt,
                        }
                    )
                    step_id += 1
                continue

            if etype == "llm.response":
                usage = data.get("usage") or {}
                cost = data.get("cost_usd") or 0.0
                if cost == -1:
                    cost = 0.0
                prompt_tokens = usage.get("input", 0) or 0
                completion_tokens = usage.get("output", 0) or 0
                total_input += prompt_tokens
                total_output += completion_tokens
                total_cost += cost
                model_name = data.get("model", model_name)

                tool_calls = []
                for tc in data.get("tool_calls") or []:
                    name = tc.get("name", "")
                    tool_calls.append(
                        {
                            "tool_call_id": tc.get("id", ""),
                            "function_name": name,
                            "arguments": tc.get("arguments", {}),
                            "name": name,
                            "result": "",
                            "error": "",
                        }
                    )

                step: dict[str, Any] = {
                    "step_id": step_id,
                    "timestamp": timestamp,
                    "source": "agent",
                    "message": data.get("content", "") or "(tool use)",
                    "model_name": model_name,
                    "tool_calls": tool_calls or None,
                    "metrics": {
                        "prompt_tokens": prompt_tokens,
                        "completion_tokens": completion_tokens,
                        "cost_usd": cost,
                    },
                }
                steps.append(step)
                step_id += 1
                continue

            if etype == "tool.call":
                tool_name = data.get("tool", "")
                output = data.get("output", "")
                err = data.get("error", "")
                for step in reversed(steps):
                    if step.get("source") != "agent":
                        continue
                    tool_calls = step.get("tool_calls") or []
                    for tc in tool_calls:
                        if tc.get("name") == tool_name and not tc.get("result"):
                            tc["result"] = output
                            tc["error"] = err
                            observation = step.setdefault("observation", {"results": []})
                            observation["results"].append(
                                {
                                    "source_call_id": tc.get("tool_call_id"),
                                    "content": err or output,
                                }
                            )
                            break
                    else:
                        continue
                    break

        trajectory = {
            "schema_version": "ATIF-v1.6-ddx",
            "session_id": session_id,
            "agent": {
                "name": "ddx-agent",
                "version": self.version() or "unknown",
                "model_name": model_name,
            },
            "steps": steps,
            "final_metrics": {
                "total_prompt_tokens": total_input,
                "total_completion_tokens": total_output,
                "total_cost_usd": total_cost,
                "total_steps": len(steps),
            },
        }
        return trajectory, {
            "input": total_input,
            "output": total_output,
            "cost": total_cost,
        }

    def _empty_trajectory(self) -> dict[str, Any]:
        return {
            "schema_version": "ATIF-v1.6-ddx",
            "session_id": str(uuid.uuid4()),
            "agent": {
                "name": "ddx-agent",
                "version": self.version() or "unknown",
                "model_name": self.model_name or "",
            },
            "steps": [],
            "final_metrics": {
                "total_prompt_tokens": 0,
                "total_completion_tokens": 0,
                "total_cost_usd": 0.0,
                "total_steps": 0,
            },
        }
