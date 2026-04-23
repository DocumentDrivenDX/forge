#!/usr/bin/env python3
"""Run DDx execute-bead benchmark tasks across harness/model arms.

The runner is intentionally dependency-free. It treats each task as:

  project root + bead id + base revision + verifier command

For real runs it clones the project into a disposable sandbox, reopens the
historical bead inside that sandbox, commits that tracker-only reopen, then
invokes:

  ddx agent execute-bead <id> --from <base> --no-merge --json ...

The source project and its tracker are never mutated.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import time
from typing import Any


def main() -> int:
    args = parse_args()
    manifest_path = pathlib.Path(args.manifest).resolve()
    manifest = load_json(manifest_path)
    tasks = select_by_id(manifest.get("tasks", []), args.task)
    arms = select_arms(manifest.get("arms", []), args.arm)
    tasks = filter_items(tasks, "project", args.project)
    tasks = filter_items(tasks, "category", args.category)
    tasks = filter_items(tasks, "difficulty", args.difficulty)
    tasks = filter_items(tasks, "tier", args.tier)
    arms = filter_items(arms, "tier", args.arm_tier)

    if args.limit_tasks:
        tasks = tasks[: args.limit_tasks]
    if args.limit_arms:
        arms = arms[: args.limit_arms]

    if not tasks:
        print("beadbench: no tasks selected", file=sys.stderr)
        return 2
    if not arms:
        print("beadbench: no arms selected", file=sys.stderr)
        return 2

    timestamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    results_dir = pathlib.Path(args.results_dir).resolve()
    run_dir = results_dir / f"run-{timestamp}-{os.getpid()}"
    run_dir.mkdir(parents=True, exist_ok=True)

    if not args.skip_preflight:
        preflight = run_preflight(tasks, arms)
        (run_dir / "preflight.json").write_text(json.dumps(preflight, indent=2) + "\n")
        print_preflight(preflight)
        if args.preflight:
            return 0 if preflight["ok"] else 3
        if not preflight["ok"]:
            print("beadbench: preflight failed; refusing to dispatch", file=sys.stderr)
            return 3
    elif args.preflight:
        print("beadbench: --preflight and --skip-preflight are mutually exclusive", file=sys.stderr)
        return 2

    report: dict[str, Any] = {
        "schema_version": "1",
        "captured": timestamp,
        "manifest_path": str(manifest_path),
        "manifest": {
            "version": manifest.get("version"),
            "created": manifest.get("created"),
            "description": manifest.get("description"),
            "selection_rule": manifest.get("selection_rule"),
        },
        "config": {
            "dry_run": args.dry_run,
            "verify": not args.no_verify,
            "repetitions": args.repetitions,
            "timeout_seconds": args.timeout_seconds,
            "filters": {
                "project": args.project or [],
                "category": args.category or [],
                "difficulty": args.difficulty or [],
                "tier": args.tier or [],
                "arm_tier": args.arm_tier or [],
            },
        },
        "arms": arms,
        "results": [],
    }

    for repetition in range(1, args.repetitions + 1):
        for task in tasks:
            for arm in arms:
                result = run_one(args, run_dir, task, arm, repetition)
                report["results"].append(result)
                print_result(result)

    report["summary"] = summarize(report["results"])
    report_path = run_dir / "report.json"
    report_path.write_text(json.dumps(report, indent=2) + "\n")

    latest_path = results_dir / "latest.json"
    try:
        if latest_path.exists() or latest_path.is_symlink():
            latest_path.unlink()
        latest_path.symlink_to(report_path)
    except OSError:
        latest_path.write_text(json.dumps({"report": str(report_path)}) + "\n")

    print(f"\nbeadbench: report {report_path}")
    print_summary(report["summary"])
    return 0


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run DDx bead benchmark manifests")
    parser.add_argument(
        "--manifest",
        default="scripts/beadbench/manifest-v1.json",
        help="Path to beadbench manifest JSON",
    )
    parser.add_argument(
        "--results-dir",
        default="benchmark-results/beadbench",
        help="Directory for run artifacts",
    )
    parser.add_argument("--arm", action="append", help="Arm id to run; repeatable")
    parser.add_argument("--task", action="append", help="Task id to run; repeatable")
    parser.add_argument("--project", action="append", help="Task project filter; repeatable")
    parser.add_argument("--category", action="append", help="Task category filter; repeatable")
    parser.add_argument("--difficulty", action="append", help="Task difficulty filter; repeatable")
    parser.add_argument("--tier", action="append", help="Task tier filter; repeatable")
    parser.add_argument("--arm-tier", action="append", help="Arm tier filter; repeatable")
    parser.add_argument("--limit-tasks", type=int, default=0)
    parser.add_argument("--limit-arms", type=int, default=0)
    parser.add_argument("--repetitions", type=int, default=1)
    parser.add_argument("--timeout-seconds", type=int, default=3600)
    parser.add_argument("--dry-run", action="store_true", help="Emit planned commands only")
    parser.add_argument("--no-verify", action="store_true", help="Skip verifier commands")
    parser.add_argument(
        "--preflight",
        action="store_true",
        help="Run preflight checks only and exit (no dispatch)",
    )
    parser.add_argument(
        "--skip-preflight",
        action="store_true",
        help="Do not run preflight checks before dispatch",
    )
    parser.add_argument("--keep-sandboxes", action="store_true")
    parser.add_argument(
        "--sandbox-root",
        default=os.environ.get("DDX_BEADBENCH_SANDBOX_ROOT", tempfile.gettempdir()),
    )
    return parser.parse_args()


def load_json(path: pathlib.Path) -> dict[str, Any]:
    try:
        return json.loads(path.read_text())
    except Exception as exc:
        raise SystemExit(f"beadbench: read manifest {path}: {exc}") from exc


def select_by_id(items: list[dict[str, Any]], selected: list[str] | None) -> list[dict[str, Any]]:
    if not selected:
        return items
    wanted = set(selected)
    found = [item for item in items if item.get("id") in wanted]
    missing = wanted - {item.get("id") for item in found}
    if missing:
        raise SystemExit(f"beadbench: unknown task id(s): {', '.join(sorted(missing))}")
    return found


def select_arms(arms: list[dict[str, Any]], selected: list[str] | None) -> list[dict[str, Any]]:
    if not selected:
        return arms
    wanted = set(selected)
    found = [arm for arm in arms if arm.get("id") in wanted]
    missing = wanted - {arm.get("id") for arm in found}
    if missing:
        raise SystemExit(f"beadbench: unknown arm id(s): {', '.join(sorted(missing))}")
    return found


def filter_items(items: list[dict[str, Any]], key: str, selected: list[str] | None) -> list[dict[str, Any]]:
    if not selected:
        return items
    wanted = set(selected)
    default = "core" if key == "tier" else ""
    return [item for item in items if str(item.get(key, default)) in wanted]


REQUIRED_EXECUTE_BEAD_FLAGS = (
    "--from",
    "--no-merge",
    "--json",
    "--project",
    "--harness",
    "--provider",
    "--model",
    "--model-ref",
    "--effort",
    "--context-budget",
)


def check_execute_bead_flags() -> dict[str, Any]:
    """Verify `ddx agent execute-bead --help` advertises every flag beadbench sends."""
    result: dict[str, Any] = {
        "ok": False,
        "missing_flags": [],
        "error": "",
    }
    try:
        proc = subprocess.run(
            ["ddx", "agent", "execute-bead", "--help"],
            text=True,
            capture_output=True,
            timeout=30,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired) as exc:
        result["error"] = f"ddx agent execute-bead --help unavailable: {exc!r}"
        return result
    if proc.returncode != 0:
        result["error"] = (
            f"ddx agent execute-bead --help exited {proc.returncode}: {proc.stderr.strip()}"
        )
        return result
    help_text = proc.stdout + proc.stderr
    missing = [flag for flag in REQUIRED_EXECUTE_BEAD_FLAGS if flag not in help_text]
    result["missing_flags"] = missing
    result["ok"] = not missing
    return result


def check_task_refs(task: dict[str, Any]) -> dict[str, Any]:
    """Verify project_root is a git repo and base_rev / known_good_rev / bead_id resolve."""
    out: dict[str, Any] = {
        "task_id": task.get("id"),
        "ok": False,
        "errors": [],
    }
    project_root_raw = task.get("project_root")
    if not isinstance(project_root_raw, str) or not project_root_raw:
        out["errors"].append("missing project_root")
        return out
    project_root = pathlib.Path(project_root_raw).expanduser()
    if not project_root.exists():
        out["errors"].append(f"project_root does not exist: {project_root}")
        return out
    if not (project_root / ".git").exists():
        out["errors"].append(f"project_root is not a git repository: {project_root}")
        return out

    for key in ("base_rev", "known_good_rev"):
        rev = task.get(key)
        if not isinstance(rev, str) or not rev:
            out["errors"].append(f"missing {key}")
            continue
        probe = subprocess.run(
            ["git", "-C", str(project_root), "rev-parse", "--verify", f"{rev}^{{commit}}"],
            text=True,
            capture_output=True,
        )
        if probe.returncode != 0:
            out["errors"].append(
                f"{key} {rev} not reachable in {project_root}: {probe.stderr.strip()}"
            )

    bead_id = task.get("bead_id")
    if not isinstance(bead_id, str) or not bead_id:
        out["errors"].append("missing bead_id")
    else:
        probe = subprocess.run(
            ["ddx", "bead", "show", bead_id, "--json"],
            cwd=str(project_root),
            text=True,
            capture_output=True,
            timeout=30,
        )
        if probe.returncode != 0:
            out["errors"].append(
                f"bead {bead_id} not found in {project_root}: {probe.stderr.strip() or probe.stdout.strip()}"
            )

    out["ok"] = not out["errors"]
    return out


def run_preflight(
    tasks: list[dict[str, Any]], arms: list[dict[str, Any]]
) -> dict[str, Any]:
    flags = check_execute_bead_flags()
    task_reports = [check_task_refs(task) for task in tasks]
    ok = flags["ok"] and all(t["ok"] for t in task_reports)
    return {
        "ok": ok,
        "execute_bead_flags": flags,
        "tasks": task_reports,
        "arm_count": len(arms),
        "task_count": len(tasks),
    }


def print_preflight(preflight: dict[str, Any]) -> None:
    flags = preflight["execute_bead_flags"]
    print("beadbench: preflight")
    if flags["ok"]:
        print("  execute-bead flags: ok")
    else:
        if flags.get("error"):
            print(f"  execute-bead flags: ERROR — {flags['error']}")
        if flags.get("missing_flags"):
            print(
                f"  execute-bead flags: missing {', '.join(flags['missing_flags'])}"
            )
    for task in preflight["tasks"]:
        if task["ok"]:
            print(f"  task {task['task_id']}: ok")
        else:
            print(f"  task {task['task_id']}: FAIL")
            for err in task["errors"]:
                print(f"    - {err}")
    status = "ok" if preflight["ok"] else "FAIL"
    print(f"  status: {status}")


def run_one(
    args: argparse.Namespace,
    run_dir: pathlib.Path,
    task: dict[str, Any],
    arm: dict[str, Any],
    repetition: int,
) -> dict[str, Any]:
    task_id = required(task, "id")
    bead_id = required(task, "bead_id")
    project_root = pathlib.Path(required(task, "project_root")).expanduser().resolve()
    base_rev = required(task, "base_rev")
    arm_id = required(arm, "id")

    run_id = f"{safe_name(task_id)}__{safe_name(arm_id)}__r{repetition}"
    artifact_dir = run_dir / run_id
    artifact_dir.mkdir(parents=True, exist_ok=True)

    result: dict[str, Any] = {
        "run_id": run_id,
        "task_id": task_id,
        "bead_id": bead_id,
        "project_root": str(project_root),
        "category": task.get("category"),
        "difficulty": task.get("difficulty"),
        "base_rev": base_rev,
        "known_good_rev": task.get("known_good_rev"),
        "arm_id": arm_id,
        "arm": arm,
        "repetition": repetition,
        "artifact_dir": str(artifact_dir),
        "status": "not_run",
        "exit_code": None,
        "duration_ms": 0,
        "verify": {"status": "skipped"},
        "phases": {
            "execute": {"status": "not_run"},
            "verify": {"status": "skipped"},
        },
    }

    command = execute_command(bead_id, base_rev, arm, project_root=None)
    result["planned_command"] = command

    if args.dry_run:
        result["status"] = "dry_run"
        result["exit_code"] = 0
        result["duration_ms"] = 0
        result["phases"]["execute"] = {"status": "dry_run"}
        (artifact_dir / "planned-command.json").write_text(json.dumps(command, indent=2) + "\n")
        return result

    sandbox = pathlib.Path(args.sandbox_root).resolve() / f"beadbench-{run_id}"
    if sandbox.exists():
        shutil.rmtree(sandbox)

    started = time.monotonic()
    try:
        run_cmd(["git", "clone", "--no-hardlinks", "--quiet", str(project_root), str(sandbox)], cwd=None)
        prepare_reopened_bead(sandbox, bead_id, artifact_dir)
        command = execute_command(bead_id, base_rev, arm, project_root=sandbox)
        result["planned_command"] = command

        proc = run_cmd(
            command,
            cwd=sandbox,
            timeout=args.timeout_seconds,
            check=False,
        )
        duration_ms = int((time.monotonic() - started) * 1000)
        result["exit_code"] = proc.returncode
        result["duration_ms"] = duration_ms
        (artifact_dir / "stdout.txt").write_text(proc.stdout)
        (artifact_dir / "stderr.txt").write_text(proc.stderr)

        parsed = parse_last_json_object(proc.stdout)
        result["execute_result"] = parsed
        if parsed:
            (artifact_dir / "execute-result.json").write_text(json.dumps(parsed, indent=2) + "\n")
        result["status"] = extract_status(proc.returncode, parsed)
        result["phases"]["execute"] = {
            "status": result["status"],
            "exit_code": proc.returncode,
            "duration_ms": duration_ms,
        }
        capture_result_artifacts(sandbox, artifact_dir, base_rev, parsed)

        if not args.no_verify:
            verify = verify_result(
                sandbox,
                artifact_dir,
                task,
                parsed,
                args.timeout_seconds,
                keep_worktree=args.keep_sandboxes,
            )
            result["verify"] = verify
            result["phases"]["verify"] = {
                "status": verify.get("status"),
                "exit_code": verify.get("exit_code"),
                "duration_ms": verify.get("duration_ms"),
            }
    except subprocess.TimeoutExpired as exc:
        result["status"] = "timeout"
        result["exit_code"] = None
        result["duration_ms"] = int((time.monotonic() - started) * 1000)
        result["timeout"] = record_timeout_evidence(exc, sandbox, base_rev, artifact_dir)
        result["phases"]["execute"] = {
            "status": "timeout",
            "timeout_seconds": exc.timeout,
            "progress_class": result["timeout"].get("progress_class"),
        }
    except Exception as exc:
        result["status"] = "runner_error"
        result["duration_ms"] = int((time.monotonic() - started) * 1000)
        (artifact_dir / "runner-error.txt").write_text(repr(exc) + "\n")
    finally:
        result["sandbox"] = str(sandbox)
        if not args.keep_sandboxes and sandbox.exists():
            shutil.rmtree(sandbox, ignore_errors=True)

    return result


def required(obj: dict[str, Any], key: str) -> str:
    value = obj.get(key)
    if not isinstance(value, str) or not value:
        raise SystemExit(f"beadbench: missing required field {key!r}: {obj}")
    return value


def execute_command(
    bead_id: str,
    base_rev: str,
    arm: dict[str, Any],
    project_root: pathlib.Path | None,
) -> list[str]:
    cmd = [
        "ddx",
        "agent",
        "execute-bead",
        bead_id,
        "--from",
        base_rev,
        "--no-merge",
        "--json",
    ]
    if project_root is not None:
        cmd.extend(["--project", str(project_root)])
    for key, flag in (
        ("harness", "--harness"),
        ("provider", "--provider"),
        ("model", "--model"),
        ("model_ref", "--model-ref"),
        ("effort", "--effort"),
        ("context_budget", "--context-budget"),
    ):
        value = arm.get(key)
        if value:
            cmd.extend([flag, str(value)])
    return cmd


def prepare_reopened_bead(sandbox: pathlib.Path, bead_id: str, artifact_dir: pathlib.Path) -> None:
    proc = run_cmd(["ddx", "bead", "reopen", bead_id], cwd=sandbox, check=False)
    (artifact_dir / "reopen-stdout.txt").write_text(proc.stdout)
    (artifact_dir / "reopen-stderr.txt").write_text(proc.stderr)

    status = run_cmd(["git", "status", "--short"], cwd=sandbox)
    if not status.stdout.strip():
        return

    run_cmd(["git", "add", ".ddx"], cwd=sandbox)
    run_cmd(
        [
            "git",
            "-c",
            "user.name=beadbench",
            "-c",
            "user.email=beadbench@example.invalid",
            "commit",
            "-m",
            f"beadbench: reopen {bead_id}",
        ],
        cwd=sandbox,
    )


def verify_result(
    sandbox: pathlib.Path,
    artifact_dir: pathlib.Path,
    task: dict[str, Any],
    execute_result: dict[str, Any],
    timeout_seconds: int,
    keep_worktree: bool,
) -> dict[str, Any]:
    command = task.get("verifier_command") or task.get("acceptance_command")
    if not command:
        return {"status": "skipped", "reason": "no verifier_command"}

    status = extract_status(0, execute_result)
    if status != "success":
        return {"status": "skipped", "reason": f"execute status {status}"}

    result_rev = first_string(execute_result, "result_rev", "resultRev", "commit", "commit_sha")
    if not result_rev:
        return {"status": "skipped", "reason": "execute result has no result_rev"}

    verify_dir = artifact_dir / "verify-worktree"
    add = run_cmd(
        ["git", "-C", str(sandbox), "worktree", "add", "--detach", "--quiet", str(verify_dir), result_rev],
        cwd=None,
        check=False,
    )
    if add.returncode != 0:
        return {
            "status": "error",
            "reason": "git worktree add failed",
            "stdout": add.stdout,
            "stderr": add.stderr,
        }

    started = time.monotonic()
    timed_out = False
    try:
        proc = subprocess.run(
            command,
            cwd=verify_dir,
            shell=True,
            text=True,
            capture_output=True,
            timeout=timeout_seconds,
        )
        stdout_text = proc.stdout
        stderr_text = proc.stderr
        returncode: int | None = proc.returncode
    except subprocess.TimeoutExpired as exc:
        timed_out = True
        stdout_text = _as_text(exc.stdout)
        stderr_text = _as_text(exc.stderr)
        returncode = None

    duration_ms = int((time.monotonic() - started) * 1000)
    (artifact_dir / "verify-stdout.txt").write_text(stdout_text)
    (artifact_dir / "verify-stderr.txt").write_text(stderr_text)
    if not keep_worktree:
        run_cmd(
            ["git", "-C", str(sandbox), "worktree", "remove", "--force", str(verify_dir)],
            cwd=None,
            check=False,
        )
    if timed_out:
        return {
            "status": "timeout",
            "command": command,
            "exit_code": None,
            "timeout_seconds": timeout_seconds,
            "duration_ms": duration_ms,
            "stdout_path": str(artifact_dir / "verify-stdout.txt"),
            "stderr_path": str(artifact_dir / "verify-stderr.txt"),
        }
    return {
        "status": "pass" if returncode == 0 else "fail",
        "command": command,
        "exit_code": returncode,
        "duration_ms": duration_ms,
        "stdout_path": str(artifact_dir / "verify-stdout.txt"),
        "stderr_path": str(artifact_dir / "verify-stderr.txt"),
    }


def capture_result_artifacts(
    sandbox: pathlib.Path,
    artifact_dir: pathlib.Path,
    base_rev: str,
    execute_result: dict[str, Any],
) -> None:
    result_rev = first_string(execute_result, "result_rev", "resultRev", "commit", "commit_sha")
    if not result_rev:
        return

    stat = run_cmd(
        ["git", "-C", str(sandbox), "diff", "--stat", f"{base_rev}..{result_rev}"],
        cwd=None,
        check=False,
    )
    (artifact_dir / "result.stat").write_text(stat.stdout + stat.stderr)

    diff = run_cmd(
        ["git", "-C", str(sandbox), "diff", "--binary", f"{base_rev}..{result_rev}"],
        cwd=None,
        check=False,
    )
    (artifact_dir / "result.diff").write_text(diff.stdout + diff.stderr)

    log = run_cmd(
        ["git", "-C", str(sandbox), "log", "--oneline", "--decorate", "--max-count=12", result_rev],
        cwd=None,
        check=False,
    )
    (artifact_dir / "result.log").write_text(log.stdout + log.stderr)


def record_timeout_evidence(
    exc: subprocess.TimeoutExpired,
    sandbox: pathlib.Path,
    base_rev: str,
    artifact_dir: pathlib.Path,
) -> dict[str, Any]:
    """Preserve as much evidence as possible from a timed-out execute-bead run.

    Writes partial stdout/stderr, any recoverable trailing JSON object, any
    `refs/execute-bead/preserve/*` references, and a sandbox status summary.
    Returns a dict describing what was captured and a progress classification:

    - ``no_output``          — the child emitted nothing on either stream.
    - ``read_only_progress`` — output captured but no commits or tracked
      writes were observed in the sandbox.
    - ``write_progress``     — commits, preserve refs, or tracked edits are
      present beyond ``base_rev``.
    """
    stdout_bytes = _as_text(exc.stdout)
    stderr_bytes = _as_text(exc.stderr)

    (artifact_dir / "timeout.txt").write_text(
        f"TimeoutExpired: {exc.timeout}s elapsed; command={exc.cmd!r}\n"
    )
    (artifact_dir / "stdout.txt").write_text(stdout_bytes)
    (artifact_dir / "stderr.txt").write_text(stderr_bytes)

    evidence: dict[str, Any] = {
        "timeout_seconds": exc.timeout,
        "partial_stdout_bytes": len(stdout_bytes),
        "partial_stderr_bytes": len(stderr_bytes),
    }

    parsed = parse_last_json_object(stdout_bytes)
    if parsed:
        (artifact_dir / "execute-result.json").write_text(json.dumps(parsed, indent=2) + "\n")
        evidence["partial_execute_result"] = parsed
        preserve_rev = first_string(parsed, "preserve_rev", "preserveRev", "result_rev", "resultRev")
        if preserve_rev:
            evidence["preserve_rev"] = preserve_rev

    sandbox_state = inspect_sandbox_state(sandbox, base_rev)
    if sandbox_state is not None:
        (artifact_dir / "timeout-sandbox-state.json").write_text(
            json.dumps(sandbox_state, indent=2) + "\n"
        )
        evidence["sandbox_state"] = sandbox_state
        for ref in sandbox_state.get("preserve_refs", []):
            evidence.setdefault("preserve_refs", []).append(ref)

    evidence["progress_class"] = classify_timeout_progress(
        stdout_bytes, stderr_bytes, sandbox_state
    )
    return evidence


def _as_text(value: Any) -> str:
    if value is None:
        return ""
    if isinstance(value, bytes):
        return value.decode("utf-8", errors="replace")
    return str(value)


def inspect_sandbox_state(sandbox: pathlib.Path, base_rev: str) -> dict[str, Any] | None:
    """Probe the sandbox git state after a timeout.

    Returns ``None`` if the sandbox directory no longer exists or is not a git
    repository — the caller treats that as missing evidence.
    """
    if not sandbox.exists() or not (sandbox / ".git").exists():
        return None

    state: dict[str, Any] = {
        "head": "",
        "commits_ahead_of_base": 0,
        "status_short": "",
        "tracked_diff_names": [],
        "preserve_refs": [],
    }

    head = run_cmd(["git", "-C", str(sandbox), "rev-parse", "HEAD"], cwd=None, check=False)
    state["head"] = head.stdout.strip()

    ahead = run_cmd(
        ["git", "-C", str(sandbox), "rev-list", "--count", f"{base_rev}..HEAD"],
        cwd=None,
        check=False,
    )
    try:
        state["commits_ahead_of_base"] = int(ahead.stdout.strip() or "0")
    except ValueError:
        state["commits_ahead_of_base"] = 0

    status = run_cmd(["git", "-C", str(sandbox), "status", "--short"], cwd=None, check=False)
    state["status_short"] = status.stdout

    tracked = run_cmd(
        ["git", "-C", str(sandbox), "diff", "--name-only", base_rev],
        cwd=None,
        check=False,
    )
    state["tracked_diff_names"] = [
        line for line in tracked.stdout.splitlines() if line.strip()
    ]

    refs = run_cmd(
        ["git", "-C", str(sandbox), "for-each-ref", "--format=%(refname) %(objectname)"],
        cwd=None,
        check=False,
    )
    for line in refs.stdout.splitlines():
        line = line.strip()
        if not line:
            continue
        name, _, sha = line.partition(" ")
        if "preserve" in name or name.startswith("refs/execute-bead/"):
            state["preserve_refs"].append({"ref": name, "sha": sha})

    return state


def classify_timeout_progress(
    stdout_text: str,
    stderr_text: str,
    sandbox_state: dict[str, Any] | None,
) -> str:
    """Bucket a timed-out run into progress classes for triage.

    The sandbox is consulted first so that silent child processes that still
    produced commits or preserve refs count as ``write_progress`` rather than
    ``no_output``.
    """
    if sandbox_state is not None:
        if sandbox_state.get("commits_ahead_of_base", 0) > 0:
            return "write_progress"
        if sandbox_state.get("preserve_refs"):
            return "write_progress"
        if sandbox_state.get("tracked_diff_names"):
            return "write_progress"
        if sandbox_state.get("status_short", "").strip():
            return "write_progress"

    if stdout_text.strip() or stderr_text.strip():
        return "read_only_progress"
    return "no_output"


def run_cmd(
    cmd: list[str],
    cwd: pathlib.Path | None,
    timeout: int | None = None,
    check: bool = True,
) -> subprocess.CompletedProcess[str]:
    proc = subprocess.run(
        cmd,
        cwd=cwd,
        timeout=timeout,
        text=True,
        capture_output=True,
    )
    if check and proc.returncode != 0:
        raise RuntimeError(
            f"command failed ({proc.returncode}): {' '.join(cmd)}\n"
            f"stdout:\n{proc.stdout}\nstderr:\n{proc.stderr}"
        )
    return proc


def parse_last_json_object(text: str) -> dict[str, Any]:
    decoder = json.JSONDecoder()
    objects: list[dict[str, Any]] = []
    for idx, char in enumerate(text):
        if char != "{":
            continue
        try:
            value, _ = decoder.raw_decode(text[idx:])
        except json.JSONDecodeError:
            continue
        if isinstance(value, dict):
            objects.append(value)
    return objects[-1] if objects else {}


def extract_status(exit_code: int | None, parsed: dict[str, Any]) -> str:
    for key in ("status", "final_status", "FinalStatus"):
        value = parsed.get(key)
        if isinstance(value, str) and value:
            return value
    if exit_code == 0:
        return "unknown_success"
    return "execution_failed"


def first_string(obj: dict[str, Any], *keys: str) -> str:
    for key in keys:
        value = obj.get(key)
        if isinstance(value, str) and value:
            return value
    return ""


def safe_name(value: str) -> str:
    out = []
    for char in value:
        if char.isalnum() or char in ("-", "_", "."):
            out.append(char)
        else:
            out.append("_")
    return "".join(out)


def summarize(results: list[dict[str, Any]]) -> dict[str, Any]:
    total = len(results)
    status_counts: dict[str, int] = {}
    verified_pass = 0
    verified_fail = 0
    verified_timeout = 0
    verified_skipped = 0
    execute_success = 0
    dry_runs = 0
    durations = []

    by_arm: dict[str, dict[str, Any]] = {}
    by_task: dict[str, dict[str, Any]] = {}

    for result in results:
        status = result.get("status", "unknown")
        status_counts[status] = status_counts.get(status, 0) + 1
        if status == "success":
            execute_success += 1
        if status == "dry_run":
            dry_runs += 1
        if isinstance(result.get("duration_ms"), int):
            durations.append(result["duration_ms"])

        verify_status = (result.get("verify") or {}).get("status")
        if verify_status == "pass":
            verified_pass += 1
        elif verify_status == "fail":
            verified_fail += 1
        elif verify_status == "timeout":
            verified_timeout += 1
        else:
            verified_skipped += 1

        add_group(by_arm, result.get("arm_id", "unknown"), result, verify_status)
        add_group(by_task, result.get("task_id", "unknown"), result, verify_status)

    executable = verified_pass + verified_fail
    return {
        "total_runs": total,
        "status_counts": status_counts,
        "execute_success": execute_success,
        "execute_success_rate": ratio(execute_success, total),
        "verified_pass": verified_pass,
        "verified_fail": verified_fail,
        "verified_timeout": verified_timeout,
        "verified_skipped": verified_skipped,
        "executable": executable,
        "verified_pass_rate": ratio(verified_pass, executable),
        "dry_runs": dry_runs,
        "avg_duration_ms": int(sum(durations) / len(durations)) if durations else 0,
        "by_arm": sorted(by_arm.values(), key=lambda item: item["id"]),
        "by_task": sorted(by_task.values(), key=lambda item: item["id"]),
    }


def add_group(
    groups: dict[str, dict[str, Any]],
    group_id: str,
    result: dict[str, Any],
    verify_status: str | None,
) -> None:
    group = groups.setdefault(
        group_id,
        {
            "id": group_id,
            "runs": 0,
            "execute_success": 0,
            "verified_pass": 0,
            "verified_fail": 0,
            "verified_timeout": 0,
            "verified_skipped": 0,
            "executable": 0,
            "verified_pass_rate": 0.0,
        },
    )
    group["runs"] += 1
    if result.get("status") == "success":
        group["execute_success"] += 1
    if verify_status == "pass":
        group["verified_pass"] += 1
    elif verify_status == "fail":
        group["verified_fail"] += 1
    elif verify_status == "timeout":
        group["verified_timeout"] += 1
    else:
        group["verified_skipped"] += 1
    group["executable"] = group["verified_pass"] + group["verified_fail"]
    group["verified_pass_rate"] = ratio(group["verified_pass"], group["executable"])


def ratio(num: int, den: int) -> float:
    if den <= 0:
        return 0.0
    return round(num / den, 4)


def print_result(result: dict[str, Any]) -> None:
    verify = (result.get("verify") or {}).get("status", "skipped")
    print(
        f"{result['run_id']}: status={result.get('status')} "
        f"verify={verify} duration={result.get('duration_ms')}ms"
    )


def print_summary(summary: dict[str, Any]) -> None:
    print("beadbench: summary")
    print(f"  total_runs: {summary['total_runs']}")
    print(f"  execute_success: {summary['execute_success']} ({summary['execute_success_rate']:.1%})")
    executable = summary["executable"]
    print(f"  verified_pass: {summary['verified_pass']}/{executable} ({summary['verified_pass_rate']:.1%})")
    print(f"  verified_timeout: {summary['verified_timeout']}")
    print(f"  status_counts: {json.dumps(summary['status_counts'], sort_keys=True)}")
    for arm in summary.get("by_arm", []):
        print(
            f"  arm {arm['id']}: executable={arm['executable']}/{arm['runs']} "
            f"pass={arm['verified_pass']}/{arm['executable']} "
            f"({arm['verified_pass_rate']:.1%})"
        )


if __name__ == "__main__":
    raise SystemExit(main())
