#!/usr/bin/env python3
"""Probe model-side reasoning controls for beadbench arms.

This is a preflight for benchmark arms, not a replacement for execute-bead
runs. It sends small OpenAI-compatible requests and records whether reasoning
controls are merely accepted or actually visible in the response shape.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import pathlib
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Any


PROBES: list[dict[str, Any]] = [
    {"id": "none", "body": {}},
    {
        "id": "thinking_map_32",
        "providers": ["lmstudio", "omlx"],
        "model_family": "qwen",
        "body": {"thinking": {"type": "enabled", "budget_tokens": 32}},
    },
    {
        "id": "thinking_map_0",
        "providers": ["lmstudio", "omlx"],
        "model_family": "qwen",
        "body": {"thinking": {"type": "enabled", "budget_tokens": 0}},
    },
    {
        "id": "qwen_off",
        "providers": ["lmstudio", "omlx"],
        "model_family": "qwen",
        "body": {"enable_thinking": False, "thinking_budget": 0},
    },
    {
        "id": "qwen_budget_32",
        "providers": ["lmstudio", "omlx"],
        "model_family": "qwen",
        "body": {"enable_thinking": True, "thinking_budget": 32},
    },
    {
        "id": "qwen_template_off",
        "providers": ["lmstudio", "omlx"],
        "model_family": "qwen",
        "body": {"chat_template_kwargs": {"enable_thinking": False}},
    },
    {
        "id": "openrouter_effort_medium",
        "providers": ["openrouter"],
        "body": {"reasoning": {"effort": "medium", "exclude": False}},
    },
    {
        "id": "openrouter_max_tokens_32",
        "providers": ["openrouter"],
        "body": {"reasoning": {"max_tokens": 32, "exclude": False}},
    },
    {
        "id": "openrouter_off",
        "providers": ["openrouter"],
        "body": {"reasoning": {"effort": "none"}},
    },
    # gpt-oss-family controls. OMLX (and LM Studio) may serve gpt-oss-20b /
    # gpt-oss-120b, which follow OpenAI's Harmony response format and expect a
    # top-level `reasoning_effort` ("low"|"medium"|"high") rather than Qwen's
    # `enable_thinking`/`thinking_budget`. There is no documented "off" or
    # token-budget control for gpt-oss; "low" is the closest reasoning-minimizing
    # value. These probes verify whether the OMLX template actually honors the
    # field on a non-Qwen model, and record baseline `reasoning_content`
    # emission when no control is sent.
    {
        "id": "gptoss_effort_low",
        "providers": ["lmstudio", "omlx"],
        "model_family": "gpt-oss",
        "body": {"reasoning_effort": "low"},
    },
    {
        "id": "gptoss_effort_medium",
        "providers": ["lmstudio", "omlx"],
        "model_family": "gpt-oss",
        "body": {"reasoning_effort": "medium"},
    },
    {
        "id": "gptoss_effort_high",
        "providers": ["lmstudio", "omlx"],
        "model_family": "gpt-oss",
        "body": {"reasoning_effort": "high"},
    },
    {
        "id": "gptoss_reasoning_map_low",
        "providers": ["lmstudio", "omlx"],
        "model_family": "gpt-oss",
        "body": {"reasoning": {"effort": "low"}},
    },
    # LM Studio's native REST API exposes a first-class `reasoning` scalar on
    # `/api/v1/chat` ("off"|"low"|"medium"|"high"|"on"). This surface is
    # intentionally probed separately from OpenAI-compatible
    # `/v1/chat/completions`: native chat has the documented reasoning knob and
    # reports `stats.reasoning_output_tokens`, while OpenAI-compatible chat is
    # the tool-compatible execute-bead surface.
    {
        "id": "lmstudio_native_reasoning_off",
        "providers": ["lmstudio"],
        "endpoint": "lmstudio_native_chat",
        "body": {"reasoning": "off"},
    },
    {
        "id": "lmstudio_native_reasoning_low",
        "providers": ["lmstudio"],
        "endpoint": "lmstudio_native_chat",
        "body": {"reasoning": "low"},
    },
    {
        "id": "lmstudio_native_reasoning_high",
        "providers": ["lmstudio"],
        "endpoint": "lmstudio_native_chat",
        "body": {"reasoning": "high"},
    },
]


def main() -> int:
    args = parse_args()
    manifest_path = pathlib.Path(args.manifest).resolve()
    manifest = load_json(manifest_path)
    arms = select_arms(manifest.get("arms", []), args.arm)
    providers = provider_map()

    timestamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    results_dir = pathlib.Path(args.results_dir).resolve()
    results_dir.mkdir(parents=True, exist_ok=True)

    probe_filter = list(args.probe) if args.probe else None
    validate_probe_filter(probe_filter)

    report: dict[str, Any] = {
        "schema_version": "1",
        "captured": timestamp,
        "manifest_path": str(manifest_path),
        "prompt": args.prompt,
        "max_tokens": args.max_tokens,
        "timeout_seconds": args.timeout_seconds,
        "probe_filter": probe_filter,
        "results": [],
    }

    for arm in arms:
        result = probe_arm(arm, providers, args, probe_filter)
        report["results"].append(result)
        print_result(result)

    report["summary"] = summarize(report["results"])
    report_path = results_dir / f"reasoning-probe-{timestamp}.json"
    report_path.write_text(json.dumps(report, indent=2) + "\n")
    latest_path = results_dir / "reasoning-latest.json"
    try:
        if latest_path.exists() or latest_path.is_symlink():
            latest_path.unlink()
        latest_path.symlink_to(report_path)
    except OSError:
        latest_path.write_text(json.dumps({"report": str(report_path)}) + "\n")

    print(f"\nreasoning-probe: report {report_path}")
    return 0


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Probe reasoning controls for beadbench arms")
    parser.add_argument("--manifest", default="scripts/beadbench/manifest-v1.json")
    parser.add_argument("--results-dir", default="benchmark-results/beadbench")
    parser.add_argument("--arm", action="append", help="Arm id to probe; repeatable")
    parser.add_argument(
        "--probe",
        action="append",
        help="Probe id to run; repeatable. Defaults to all probes eligible for the provider/model.",
    )
    # Default timeout is 120s because slow local Qwen models (LM Studio /
    # OMLX) routinely produce their first 16 tokens in 30-50s even when the
    # server accepts the request shape; a 45s bound prematurely reports
    # "timeout" for behavior that is really just a slow generation.
    parser.add_argument("--timeout-seconds", type=int, default=120)
    parser.add_argument("--max-tokens", type=int, default=64)
    parser.add_argument(
        "--prompt",
        default="What is 37*42? Think briefly if needed, then answer with only the integer.",
    )
    return parser.parse_args()


def load_json(path: pathlib.Path) -> dict[str, Any]:
    try:
        return json.loads(path.read_text())
    except Exception as exc:
        raise SystemExit(f"reasoning-probe: read {path}: {exc}") from exc


def provider_map() -> dict[str, dict[str, Any]]:
    proc = subprocess.run(
        ["ddx", "agent", "providers", "--json"],
        text=True,
        capture_output=True,
        check=False,
    )
    if proc.returncode != 0:
        raise SystemExit(f"reasoning-probe: ddx agent providers failed:\n{proc.stderr}")
    try:
        providers = json.loads(proc.stdout)
    except json.JSONDecodeError as exc:
        raise SystemExit(f"reasoning-probe: provider JSON parse failed: {exc}") from exc
    return {p.get("name"): p for p in providers if isinstance(p, dict) and p.get("name")}


def select_arms(arms: list[dict[str, Any]], selected: list[str] | None) -> list[dict[str, Any]]:
    if not selected:
        return arms
    wanted = set(selected)
    found = [arm for arm in arms if arm.get("id") in wanted]
    missing = wanted - {arm.get("id") for arm in found}
    if missing:
        raise SystemExit(f"reasoning-probe: unknown arm id(s): {', '.join(sorted(missing))}")
    return found


def validate_probe_filter(probe_filter: list[str] | None) -> None:
    if not probe_filter:
        return
    known = {probe["id"] for probe in PROBES}
    unknown = [pid for pid in probe_filter if pid not in known]
    if unknown:
        raise SystemExit(
            f"reasoning-probe: unknown probe id(s): {', '.join(sorted(unknown))}. "
            f"Known ids: {', '.join(sorted(known))}"
        )


def probe_arm(
    arm: dict[str, Any],
    providers: dict[str, dict[str, Any]],
    args: argparse.Namespace,
    probe_filter: list[str] | None,
) -> dict[str, Any]:
    result: dict[str, Any] = {
        "arm_id": arm.get("id"),
        "harness": arm.get("harness"),
        "provider": arm.get("provider"),
        "model": arm.get("model"),
        "requested_effort": arm.get("effort"),
        "status": "skipped",
        "probes": [],
    }

    provider_name = arm.get("provider")
    if not provider_name:
        result["reason"] = "arm has no OpenAI-compatible provider pin"
        return result
    provider = providers.get(provider_name)
    if not provider:
        result["reason"] = f"provider {provider_name!r} not found in ddx agent providers"
        return result
    if provider.get("type") not in {"lmstudio", "omlx", "openai", "openrouter", "ollama"}:
        result["reason"] = f"provider type {provider.get('type')!r} is not probed"
        return result

    base_url = str(provider.get("base_url") or "").rstrip("/")
    model = str(arm.get("model") or provider.get("model") or "")
    if not base_url or not model:
        result["reason"] = "missing base_url or model"
        return result

    result["status"] = "probed"
    result["provider_type"] = provider.get("type")
    result["base_url"] = base_url
    result["model"] = model
    if server_meta := collect_server_metadata(str(provider.get("type")), base_url, model):
        result["server_metadata"] = server_meta
    arm_id = str(arm.get("id") or "")
    for probe in probes_for_provider(str(provider.get("type")), model, probe_filter):
        probe_result = send_probe(base_url, model, args.prompt, args.max_tokens, args.timeout_seconds, probe)
        print_probe_progress(arm_id, probe_result)
        result["probes"].append(probe_result)

    result["capability"] = classify(result["probes"])
    return result


def collect_server_metadata(provider_type: str, base_url: str, model: str) -> dict[str, Any]:
    """Capture server/model version evidence alongside each probe run.

    Provides the server/version context AC3 asks for when documenting an
    operational blocker (e.g. LM Studio accepts every control shape but the
    model's chat template still emits `reasoning_content`). LM Studio exposes
    `/api/v0/models/<model>` with arch, quantization, context length, and
    capabilities. Failures are recorded in the report rather than raised so a
    single slow or missing endpoint does not abort the probe run.
    """
    meta: dict[str, Any] = {}
    if provider_type != "lmstudio":
        return meta
    api_root = base_url.removesuffix("/v1") if base_url.endswith("/v1") else base_url
    endpoint = f"{api_root}/api/v0/models/{urllib.parse.quote(model, safe='')}"
    try:
        with urllib.request.urlopen(endpoint, timeout=10) as resp:
            payload = json.loads(resp.read().decode("utf-8", "replace"))
    except Exception as exc:
        meta["error"] = f"{type(exc).__name__}: {exc}"[:300]
        return meta
    for key in ("arch", "publisher", "quantization", "compatibility_type", "state",
                "max_context_length", "loaded_context_length", "capabilities"):
        if key in payload:
            meta[key] = payload[key]
    meta["source_endpoint"] = endpoint
    return meta


def probes_for_provider(
    provider_type: str,
    model: str,
    probe_filter: list[str] | None = None,
) -> list[dict[str, Any]]:
    selected = []
    model_lower = model.lower()
    wanted = set(probe_filter) if probe_filter else None
    for probe in PROBES:
        if wanted is not None and probe["id"] not in wanted:
            continue
        providers = probe.get("providers")
        if providers is not None and provider_type not in providers:
            continue
        family = probe.get("model_family")
        if family == "qwen" and "qwen" not in model_lower:
            continue
        if family == "gpt-oss" and "gpt-oss" not in model_lower and "gptoss" not in model_lower:
            continue
        selected.append(probe)
    return selected


def send_probe(
    base_url: str,
    model: str,
    prompt: str,
    max_tokens: int,
    timeout_seconds: int,
    probe: dict[str, Any],
) -> dict[str, Any]:
    if probe.get("endpoint") == "lmstudio_native_chat":
        return send_lmstudio_native_probe(base_url, model, prompt, max_tokens, timeout_seconds, probe)

    body = {
        "model": model,
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": max_tokens,
        "temperature": 0,
    }
    body.update(probe["body"])
    started = time.monotonic()
    req = urllib.request.Request(
        base_url + "/chat/completions",
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout_seconds) as resp:
            raw = resp.read().decode("utf-8", "replace")
            elapsed = round(time.monotonic() - started, 3)
            return parse_openai_response(probe["id"], resp.status, raw, elapsed)
    except urllib.error.HTTPError as exc:
        raw = exc.read().decode("utf-8", "replace")
        return {
            "id": probe["id"],
            "accepted": False,
            "status": exc.code,
            "seconds": round(time.monotonic() - started, 3),
            "error": raw[:500],
        }
    except Exception as exc:
        return {
            "id": probe["id"],
            "accepted": False,
            "seconds": round(time.monotonic() - started, 3),
            "error_type": type(exc).__name__,
            "error": str(exc)[:500],
        }


def send_lmstudio_native_probe(
    base_url: str,
    model: str,
    prompt: str,
    max_tokens: int,
    timeout_seconds: int,
    probe: dict[str, Any],
) -> dict[str, Any]:
    api_root = base_url.removesuffix("/v1") if base_url.endswith("/v1") else base_url
    body = {
        "model": model,
        "input": prompt,
        "max_output_tokens": max_tokens,
        "temperature": 0,
        "store": False,
    }
    body.update(probe["body"])
    started = time.monotonic()
    req = urllib.request.Request(
        api_root + "/api/v1/chat",
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout_seconds) as resp:
            raw = resp.read().decode("utf-8", "replace")
            elapsed = round(time.monotonic() - started, 3)
            return parse_lmstudio_native_response(probe["id"], resp.status, raw, elapsed)
    except urllib.error.HTTPError as exc:
        raw = exc.read().decode("utf-8", "replace")
        return {
            "id": probe["id"],
            "surface": "lmstudio_native_chat",
            "accepted": False,
            "status": exc.code,
            "seconds": round(time.monotonic() - started, 3),
            "error": raw[:500],
        }
    except Exception as exc:
        return {
            "id": probe["id"],
            "surface": "lmstudio_native_chat",
            "accepted": False,
            "seconds": round(time.monotonic() - started, 3),
            "error_type": type(exc).__name__,
            "error": str(exc)[:500],
        }


def parse_openai_response(probe_id: str, status: int, raw: str, elapsed: float) -> dict[str, Any]:
    try:
        data = json.loads(raw)
    except json.JSONDecodeError:
        return {"id": probe_id, "accepted": True, "status": status, "seconds": elapsed, "raw": raw[:500]}
    choice = (data.get("choices") or [{}])[0]
    message = choice.get("message") or {}
    content = message.get("content") or ""
    reasoning = message.get("reasoning_content") or ""
    usage = data.get("usage") or {}
    completion_tokens = usage.get("completion_tokens", usage.get("output_tokens"))
    details = usage.get("completion_tokens_details") or usage.get("output_tokens_details") or {}
    return {
        "id": probe_id,
        "surface": "openai_chat_completions",
        "accepted": True,
        "status": status,
        "seconds": elapsed,
        "finish_reason": choice.get("finish_reason"),
        "content_preview": content[:160],
        "content_chars": len(content),
        "reasoning_chars": len(reasoning),
        "reasoning_preview": reasoning[:160],
        "completion_tokens": completion_tokens,
        "reasoning_tokens": details.get("reasoning_tokens"),
        "looks_like_visible_thinking": looks_like_visible_thinking(content),
    }


def parse_lmstudio_native_response(probe_id: str, status: int, raw: str, elapsed: float) -> dict[str, Any]:
    try:
        data = json.loads(raw)
    except json.JSONDecodeError:
        return {
            "id": probe_id,
            "surface": "lmstudio_native_chat",
            "accepted": True,
            "status": status,
            "seconds": elapsed,
            "raw": raw[:500],
        }
    messages: list[str] = []
    reasoning_items: list[str] = []
    for item in data.get("output") or []:
        if not isinstance(item, dict):
            continue
        content = item.get("content") or ""
        if item.get("type") == "message":
            messages.append(str(content))
        elif item.get("type") == "reasoning":
            reasoning_items.append(str(content))
    content = "".join(messages)
    reasoning = "".join(reasoning_items)
    stats = data.get("stats") or {}
    return {
        "id": probe_id,
        "surface": "lmstudio_native_chat",
        "accepted": True,
        "status": status,
        "seconds": elapsed,
        "content_preview": content[:160],
        "content_chars": len(content),
        "reasoning_chars": len(reasoning),
        "reasoning_preview": reasoning[:160],
        "completion_tokens": stats.get("total_output_tokens"),
        "reasoning_tokens": stats.get("reasoning_output_tokens"),
        "looks_like_visible_thinking": looks_like_visible_thinking(content),
    }


def looks_like_visible_thinking(content: str) -> bool:
    lowered = content.lower()
    markers = ("<think", "thinking process", "step-by-step", "let me", "i need to")
    return any(marker in lowered for marker in markers)


def classify(probes: list[dict[str, Any]]) -> dict[str, Any]:
    by_id = {probe.get("id"): probe for probe in probes}
    qwen_budget = by_id.get("qwen_budget_32") or {}
    qwen_off = by_id.get("qwen_off") or {}
    thinking_map = by_id.get("thinking_map_32") or {}
    openrouter_effort = by_id.get("openrouter_effort_medium") or {}
    none = by_id.get("none") or {}
    gptoss_low = by_id.get("gptoss_effort_low") or {}
    gptoss_high = by_id.get("gptoss_effort_high") or {}
    native_off = by_id.get("lmstudio_native_reasoning_off") or {}
    native_low = by_id.get("lmstudio_native_reasoning_low") or {}
    native_high = by_id.get("lmstudio_native_reasoning_high") or {}

    # For gpt-oss, behavioral evidence that `reasoning_effort` is honored is a
    # measurable change in reasoning_chars or completion_tokens between low and
    # high. Mere acceptance of the field is not enough: Harmony-style
    # deployments silently ignore unknown top-level fields.
    gptoss_effort_changes_reasoning = bool(
        gptoss_low.get("accepted")
        and gptoss_high.get("accepted")
        and (
            abs((gptoss_low.get("reasoning_chars") or 0) - (gptoss_high.get("reasoning_chars") or 0)) > 16
            or abs((gptoss_low.get("completion_tokens") or 0) - (gptoss_high.get("completion_tokens") or 0)) > 4
        )
    )

    recommended = "unknown"
    if native_off.get("accepted") and native_off.get("reasoning_tokens") == 0:
        recommended = "lmstudio_native"
    elif qwen_budget.get("accepted") and (qwen_budget.get("reasoning_chars") or 0) > 0:
        recommended = "qwen"
    elif thinking_map.get("accepted") and (thinking_map.get("reasoning_chars") or 0) > 0:
        recommended = "thinking_map"
    elif openrouter_effort.get("accepted"):
        recommended = "openrouter"
    elif gptoss_effort_changes_reasoning:
        recommended = "gpt_oss_effort"
    elif qwen_off.get("accepted") and none.get("looks_like_visible_thinking") and not qwen_off.get("looks_like_visible_thinking"):
        recommended = "qwen_off_only"
    elif any(probe.get("id", "").startswith("gptoss_") for probe in probes) and (none.get("reasoning_chars") or 0) > 0:
        # gpt-oss family probed but no effort-level knob changed observable
        # behavior: the deployment emits reasoning_content unconditionally.
        recommended = "gpt_oss_unsupported"

    return {
        "recommended_wire_format": recommended,
        "qwen_budget_separates_reasoning": bool(qwen_budget.get("accepted") and (qwen_budget.get("reasoning_chars") or 0) > 0),
        "thinking_map_separates_reasoning": bool(thinking_map.get("accepted") and (thinking_map.get("reasoning_chars") or 0) > 0),
        "openrouter_effort_accepted": bool(openrouter_effort.get("accepted")),
        "qwen_off_suppresses_visible_thinking": bool(
            qwen_off.get("accepted") and none.get("looks_like_visible_thinking") and not qwen_off.get("looks_like_visible_thinking")
        ),
        "gptoss_effort_accepted": bool(gptoss_low.get("accepted") or gptoss_high.get("accepted")),
        "gptoss_effort_changes_reasoning": gptoss_effort_changes_reasoning,
        "baseline_emits_reasoning_content": bool((none.get("reasoning_chars") or 0) > 0),
        "lmstudio_native_reasoning_accepted": bool(
            native_off.get("accepted") or native_low.get("accepted") or native_high.get("accepted")
        ),
        "lmstudio_native_off_zero_reasoning_tokens": bool(
            native_off.get("accepted") and native_off.get("reasoning_tokens") == 0
        ),
        "lmstudio_native_reasoning_changes_tokens": bool(
            native_low.get("accepted")
            and native_high.get("accepted")
            and abs((native_low.get("reasoning_tokens") or 0) - (native_high.get("reasoning_tokens") or 0)) > 4
        ),
    }


def summarize(results: list[dict[str, Any]]) -> dict[str, Any]:
    return {
        "total_arms": len(results),
        "probed": sum(1 for result in results if result.get("status") == "probed"),
        "by_wire_format": count_by(
            (result.get("capability") or {}).get("recommended_wire_format", "skipped")
            for result in results
        ),
    }


def count_by(values: Any) -> dict[str, int]:
    counts: dict[str, int] = {}
    for value in values:
        key = str(value)
        counts[key] = counts.get(key, 0) + 1
    return counts


def print_probe_progress(arm_id: str, probe_result: dict[str, Any]) -> None:
    """Emit one progress line per probe, flushing so slow runs are visible."""
    accepted = probe_result.get("accepted")
    if accepted:
        status_word = f"accepted (HTTP {probe_result.get('status')})"
    else:
        err_type = probe_result.get("error_type")
        err = probe_result.get("error") or ""
        status_code = probe_result.get("status")
        if status_code:
            status_word = f"error (HTTP {status_code})"
        elif err_type:
            status_word = f"error ({err_type})"
        else:
            status_word = "error"
        if err:
            status_word = f"{status_word}: {err.splitlines()[0][:120]}"
    seconds = probe_result.get("seconds")
    print(
        f"  {arm_id} / {probe_result.get('id')}: {status_word} in {seconds}s",
        flush=True,
    )


def print_result(result: dict[str, Any]) -> None:
    if result.get("status") != "probed":
        print(f"{result.get('arm_id')}: skipped ({result.get('reason')})", flush=True)
        return
    capability = result.get("capability") or {}
    print(
        f"{result.get('arm_id')}: wire={capability.get('recommended_wire_format')} "
        f"qwen_reasoning={capability.get('qwen_budget_separates_reasoning')} "
        f"off_suppresses={capability.get('qwen_off_suppresses_visible_thinking')}",
        flush=True,
    )


if __name__ == "__main__":
    raise SystemExit(main())
