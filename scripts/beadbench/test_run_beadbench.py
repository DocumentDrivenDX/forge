#!/usr/bin/env python3
"""Deterministic fixtures for beadbench timeout artifact handling.

Run with ``python3 scripts/beadbench/test_run_beadbench.py`` — the script
exits non-zero on any failed assertion and zero on success.
"""

from __future__ import annotations

import json
import pathlib
import subprocess
import sys
import tempfile

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))

import run_beadbench as rb  # noqa: E402


def _git(cwd: pathlib.Path, *args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["git", "-C", str(cwd), *args], text=True, capture_output=True, check=True
    )


def _init_sandbox(root: pathlib.Path) -> str:
    root.mkdir(parents=True, exist_ok=True)
    _git(root, "init", "--quiet", "-b", "main")
    _git(root, "config", "user.email", "beadbench-test@example.invalid")
    _git(root, "config", "user.name", "beadbench-test")
    (root / "README").write_text("base\n")
    _git(root, "add", "README")
    _git(root, "commit", "--quiet", "-m", "base")
    base_rev = _git(root, "rev-parse", "HEAD").stdout.strip()
    return base_rev


def test_no_output_timeout(tmp: pathlib.Path) -> None:
    sandbox = tmp / "s1"
    base = _init_sandbox(sandbox)
    artifacts = tmp / "a1"
    artifacts.mkdir()

    exc = subprocess.TimeoutExpired(cmd=["ddx", "agent"], timeout=1.0, output=b"", stderr=b"")
    info = rb.record_timeout_evidence(exc, sandbox, base, artifacts)

    assert info["progress_class"] == "no_output", info
    assert info["partial_stdout_bytes"] == 0
    assert info["partial_stderr_bytes"] == 0
    assert (artifacts / "timeout.txt").exists()
    assert (artifacts / "stdout.txt").read_text() == ""
    assert (artifacts / "stderr.txt").read_text() == ""
    assert "partial_execute_result" not in info


def test_read_only_progress(tmp: pathlib.Path) -> None:
    sandbox = tmp / "s2"
    base = _init_sandbox(sandbox)
    artifacts = tmp / "a2"
    artifacts.mkdir()

    exc = subprocess.TimeoutExpired(
        cmd=["ddx", "agent"],
        timeout=2.0,
        output=b"thinking...\n",
        stderr=b"resolving model...\n",
    )
    info = rb.record_timeout_evidence(exc, sandbox, base, artifacts)

    assert info["progress_class"] == "read_only_progress", info
    assert info["partial_stdout_bytes"] > 0
    assert info["partial_stderr_bytes"] > 0
    assert (artifacts / "stdout.txt").read_text() == "thinking...\n"
    assert (artifacts / "stderr.txt").read_text() == "resolving model...\n"


def test_partial_json_recovered(tmp: pathlib.Path) -> None:
    sandbox = tmp / "s3"
    base = _init_sandbox(sandbox)
    artifacts = tmp / "a3"
    artifacts.mkdir()

    partial = (
        b'log line\n'
        b'{"status":"partial","preserve_rev":"deadbeef1234"}\n'
        b'still more lo'
    )
    exc = subprocess.TimeoutExpired(
        cmd=["ddx", "agent"], timeout=3.0, output=partial, stderr=b""
    )
    info = rb.record_timeout_evidence(exc, sandbox, base, artifacts)

    assert "partial_execute_result" in info
    assert info["partial_execute_result"]["preserve_rev"] == "deadbeef1234"
    assert info["preserve_rev"] == "deadbeef1234"
    recovered = json.loads((artifacts / "execute-result.json").read_text())
    assert recovered["status"] == "partial"


def test_write_progress_via_commit(tmp: pathlib.Path) -> None:
    sandbox = tmp / "s4"
    base = _init_sandbox(sandbox)
    (sandbox / "NEW").write_text("work\n")
    _git(sandbox, "add", "NEW")
    _git(sandbox, "commit", "--quiet", "-m", "agent work in progress")

    artifacts = tmp / "a4"
    artifacts.mkdir()
    exc = subprocess.TimeoutExpired(
        cmd=["ddx", "agent"], timeout=4.0, output=b"", stderr=b""
    )
    info = rb.record_timeout_evidence(exc, sandbox, base, artifacts)

    assert info["progress_class"] == "write_progress", info
    assert info["sandbox_state"]["commits_ahead_of_base"] == 1
    assert (artifacts / "timeout-sandbox-state.json").exists()


def test_write_progress_via_preserve_ref(tmp: pathlib.Path) -> None:
    sandbox = tmp / "s5"
    base = _init_sandbox(sandbox)
    _git(sandbox, "update-ref", "refs/execute-bead/preserve/agent-xyz", base)

    artifacts = tmp / "a5"
    artifacts.mkdir()
    exc = subprocess.TimeoutExpired(
        cmd=["ddx", "agent"], timeout=5.0, output=b"", stderr=b""
    )
    info = rb.record_timeout_evidence(exc, sandbox, base, artifacts)

    assert info["progress_class"] == "write_progress", info
    refs = [r["ref"] for r in info["sandbox_state"]["preserve_refs"]]
    assert "refs/execute-bead/preserve/agent-xyz" in refs


def test_missing_sandbox_is_tolerated(tmp: pathlib.Path) -> None:
    sandbox = tmp / "does-not-exist"
    artifacts = tmp / "a6"
    artifacts.mkdir()
    exc = subprocess.TimeoutExpired(
        cmd=["ddx", "agent"], timeout=6.0, output=b"line\n", stderr=b""
    )
    info = rb.record_timeout_evidence(exc, sandbox, "HEAD", artifacts)
    assert info["progress_class"] == "read_only_progress"
    assert "sandbox_state" not in info


def test_preflight_detects_unreachable_revs(tmp: pathlib.Path) -> None:
    project = tmp / "proj"
    _init_sandbox(project)
    task = {
        "id": "t-bad",
        "project_root": str(project),
        "bead_id": "nope-bead",
        "base_rev": "0" * 40,
        "known_good_rev": "1" * 40,
    }
    report = rb.check_task_refs(task)
    assert report["ok"] is False
    joined = " | ".join(report["errors"])
    assert "base_rev" in joined
    assert "known_good_rev" in joined


def test_preflight_passes_on_reachable_task(tmp: pathlib.Path) -> None:
    project = tmp / "proj2"
    base = _init_sandbox(project)
    # bead_id check requires ddx CLI; stub it by shimming PATH.
    shim = tmp / "bin"
    shim.mkdir()
    (shim / "ddx").write_text("#!/bin/sh\nexit 0\n")
    (shim / "ddx").chmod(0o755)
    import os

    old_path = os.environ.get("PATH", "")
    os.environ["PATH"] = f"{shim}:{old_path}"
    try:
        task = {
            "id": "t-good",
            "project_root": str(project),
            "bead_id": "fake-bead",
            "base_rev": base,
            "known_good_rev": base,
        }
        report = rb.check_task_refs(task)
    finally:
        os.environ["PATH"] = old_path
    assert report["ok"] is True, report
    assert report["errors"] == []


def test_check_execute_bead_flags_missing(tmp: pathlib.Path) -> None:
    shim = tmp / "bin"
    shim.mkdir()
    (shim / "ddx").write_text(
        "#!/bin/sh\ncat <<'EOF'\nUsage: ddx agent execute-bead <id>\nFlags:\n  --from\n  --json\nEOF\n"
    )
    (shim / "ddx").chmod(0o755)
    import os

    old_path = os.environ.get("PATH", "")
    os.environ["PATH"] = f"{shim}:{old_path}"
    try:
        flags = rb.check_execute_bead_flags()
    finally:
        os.environ["PATH"] = old_path
    assert flags["ok"] is False
    assert "--no-merge" in flags["missing_flags"]
    assert "--harness" in flags["missing_flags"]


def test_check_execute_bead_flags_ok(tmp: pathlib.Path) -> None:
    shim = tmp / "bin"
    shim.mkdir()
    help_text = "Flags:\n" + "\n".join(f"  {f}" for f in rb.REQUIRED_EXECUTE_BEAD_FLAGS) + "\n"
    (shim / "ddx").write_text(f"#!/bin/sh\ncat <<'EOF'\n{help_text}EOF\n")
    (shim / "ddx").chmod(0o755)
    import os

    old_path = os.environ.get("PATH", "")
    os.environ["PATH"] = f"{shim}:{old_path}"
    try:
        flags = rb.check_execute_bead_flags()
    finally:
        os.environ["PATH"] = old_path
    assert flags["ok"] is True, flags
    assert flags["missing_flags"] == []


def test_verify_result_records_verifier_timeout(tmp: pathlib.Path) -> None:
    """Verifier timeout must be recorded as verify.status=timeout and must not
    propagate to the caller where it would be mislabelled as an execute timeout."""
    sandbox = tmp / "sbx"
    base = _init_sandbox(sandbox)
    artifacts = tmp / "art"
    artifacts.mkdir()
    task = {
        "id": "t-vto",
        "verifier_command": "sleep 5",
    }
    execute_result = {"status": "success", "result_rev": base}
    verify = rb.verify_result(
        sandbox=sandbox,
        artifact_dir=artifacts,
        task=task,
        execute_result=execute_result,
        timeout_seconds=1,
        keep_worktree=False,
    )
    assert verify["status"] == "timeout", verify
    assert verify["exit_code"] is None
    assert verify["timeout_seconds"] == 1
    assert (artifacts / "verify-stdout.txt").exists()
    assert (artifacts / "verify-stderr.txt").exists()


def test_summary_counts_executable_over_non_skipped(tmp: pathlib.Path) -> None:
    results = [
        {
            "status": "success",
            "arm_id": "a1",
            "task_id": "t1",
            "duration_ms": 100,
            "verify": {"status": "pass"},
        },
        {
            "status": "success",
            "arm_id": "a1",
            "task_id": "t1",
            "duration_ms": 200,
            "verify": {"status": "fail"},
        },
        {
            "status": "success",
            "arm_id": "a1",
            "task_id": "t1",
            "duration_ms": 300,
            "verify": {"status": "timeout"},
        },
        {
            "status": "execution_failed",
            "arm_id": "a1",
            "task_id": "t1",
            "duration_ms": 50,
            "verify": {"status": "skipped"},
        },
    ]
    summary = rb.summarize(results)
    assert summary["executable"] == 2
    assert summary["verified_pass"] == 1
    assert summary["verified_fail"] == 1
    assert summary["verified_timeout"] == 1
    assert summary["verified_skipped"] == 1
    assert summary["verified_pass_rate"] == 0.5
    arm = summary["by_arm"][0]
    assert arm["id"] == "a1"
    assert arm["executable"] == 2
    assert arm["verified_pass_rate"] == 0.5


def main() -> int:
    cases = [
        test_no_output_timeout,
        test_read_only_progress,
        test_partial_json_recovered,
        test_write_progress_via_commit,
        test_write_progress_via_preserve_ref,
        test_missing_sandbox_is_tolerated,
        test_preflight_detects_unreachable_revs,
        test_preflight_passes_on_reachable_task,
        test_check_execute_bead_flags_missing,
        test_check_execute_bead_flags_ok,
        test_verify_result_records_verifier_timeout,
        test_summary_counts_executable_over_non_skipped,
    ]
    failures: list[str] = []
    for case in cases:
        with tempfile.TemporaryDirectory(prefix="beadbench-test-") as raw:
            try:
                case(pathlib.Path(raw))
                print(f"ok  {case.__name__}")
            except AssertionError as exc:
                failures.append(f"{case.__name__}: {exc}")
                print(f"FAIL {case.__name__}: {exc}")
            except Exception as exc:
                failures.append(f"{case.__name__}: {exc!r}")
                print(f"ERROR {case.__name__}: {exc!r}")
    if failures:
        print(f"\n{len(failures)} failure(s)", file=sys.stderr)
        return 1
    print(f"\n{len(cases)} tests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
