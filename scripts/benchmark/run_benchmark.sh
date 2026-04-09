#!/usr/bin/env bash
# run_benchmark.sh — Run the full ddx-agent benchmark subset and emit a report.
#
# Usage:
#   ANTHROPIC_API_KEY=sk-... ./scripts/benchmark/run_benchmark.sh
#   OPENROUTER_API_KEY=sk-or-... ./scripts/benchmark/run_benchmark.sh
#
# Output:
#   benchmark-results/report-<TIMESTAMP>.json
#
# The report includes:
#   - git SHA and ddx-agent version
#   - model, provider, config snapshot
#   - per-task pass/fail/timeout outcomes
#   - aggregate resolved-task rate and metric summary
#
# Prerequisites:
#   pip install harbor-framework
#   harbor dataset pull terminal-bench/terminal-bench-2
#   Docker running locally
#
# See scripts/benchmark/README.md and SD-009 §3–§5 for full documentation.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
DIST_DIR="${REPO_ROOT}/dist"
BINARY="${DIST_DIR}/ddx-agent-linux-amd64"
SUBSET_FILE="${SCRIPT_DIR}/task-subset-v1.yaml"
RESULTS_DIR="${REPO_ROOT}/benchmark-results"
TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
REPORT_FILE="${RESULTS_DIR}/report-${TIMESTAMP}.json"
AGENT_CONFIG="${SCRIPT_DIR}/harbor_agent.py"

echo "=== ddx-agent benchmark run ==="
echo "Repo:    ${REPO_ROOT}"
echo "Subset:  ${SUBSET_FILE}"
echo "Report:  ${REPORT_FILE}"
echo ""

# ---------------------------------------------------------------------------- #
# Step 1: Build linux/amd64 binary
# ---------------------------------------------------------------------------- #
echo "[1/5] Building linux/amd64 binary..."
mkdir -p "${DIST_DIR}"
GOOS=linux GOARCH=amd64 go build \
    -ldflags "-X main.GitCommit=$(git -C "${REPO_ROOT}" rev-parse --short HEAD 2>/dev/null || echo dev)" \
    -o "${BINARY}" "${REPO_ROOT}/cmd/ddx-agent"
echo "      Built: ${BINARY}"

# ---------------------------------------------------------------------------- #
# Step 2: Validate prerequisites
# ---------------------------------------------------------------------------- #
echo "[2/5] Checking prerequisites..."
if ! command -v harbor &>/dev/null; then
    echo "ERROR: 'harbor' not found. Install with: pip install harbor-framework"
    exit 1
fi
if ! command -v python3 &>/dev/null; then
    echo "ERROR: 'python3' not found."
    exit 1
fi
echo "      harbor: $(harbor --version 2>/dev/null || echo 'found')"

# ---------------------------------------------------------------------------- #
# Step 3: Extract task IDs from subset YAML
# ---------------------------------------------------------------------------- #
echo "[3/5] Parsing task subset..."
TASK_IDS=$(python3 - <<'PY' "${SUBSET_FILE}"
import sys, yaml  # type: ignore
with open(sys.argv[1]) as f:
    data = yaml.safe_load(f)
for t in data.get("tasks", []):
    print(t["id"])
PY
)
TASK_COUNT=$(echo "${TASK_IDS}" | wc -l | tr -d ' ')
SUBSET_VERSION=$(python3 -c "
import sys, yaml
with open('${SUBSET_FILE}') as f:
    data = yaml.safe_load(f)
print(data.get('version', 'unknown'))
")
echo "      Subset v${SUBSET_VERSION}: ${TASK_COUNT} tasks"

# ---------------------------------------------------------------------------- #
# Step 4: Run each task via Harbor
# ---------------------------------------------------------------------------- #
echo "[4/5] Running tasks..."
mkdir -p "${RESULTS_DIR}"

# Collect per-task results as JSON lines into a temp file.
TASK_RESULTS_FILE="$(mktemp /tmp/ddx-bench-tasks-XXXXXX.jsonl)"
trap 'rm -f "${TASK_RESULTS_FILE}"' EXIT

PASS=0
FAIL=0
TIMEOUT=0
ERROR=0

while IFS= read -r TASK_ID; do
    [[ -z "${TASK_ID}" ]] && continue
    echo "  → ${TASK_ID}"

    TASK_START="$(date -u +%s%3N)"
    HARBOR_OUT="$(harbor run \
        --dataset terminal-bench/terminal-bench-2 \
        --agent ddx-agent \
        --task-id "${TASK_ID}" \
        --runtime docker \
        --agent-config "${AGENT_CONFIG}" \
        2>&1)" || true
    TASK_END="$(date -u +%s%3N)"
    TASK_DURATION_MS=$(( TASK_END - TASK_START ))

    # Extract job ID.
    JOB_ID=$(echo "${HARBOR_OUT}" | grep -oP '(?<=job-id: )[a-z0-9-]+' || echo "")
    if [[ -n "${JOB_ID}" ]]; then
        JOB_DIR="${HOME}/.harbor/jobs/${JOB_ID}"
    else
        JOB_DIR=$(ls -td "${HOME}/.harbor/jobs/"*/ 2>/dev/null | head -1 || echo "")
    fi

    # Extract reward from the trial directory.
    REWARD=""
    STATUS="error"
    if [[ -n "${JOB_DIR}" && -d "${JOB_DIR}" ]]; then
        TRIAL_DIR=$(ls -d "${JOB_DIR}/trials/"*/ 2>/dev/null | head -1 || echo "")
        if [[ -n "${TRIAL_DIR}" ]]; then
            REWARD_FILE="${TRIAL_DIR}/verifier/reward.txt"
            if [[ -f "${REWARD_FILE}" ]]; then
                REWARD=$(cat "${REWARD_FILE}" | tr -d '[:space:]')
                if [[ "${REWARD}" == "1" ]]; then
                    STATUS="pass"
                    PASS=$(( PASS + 1 ))
                elif [[ "${REWARD}" == "0" ]]; then
                    STATUS="fail"
                    FAIL=$(( FAIL + 1 ))
                else
                    STATUS="unknown"
                    ERROR=$(( ERROR + 1 ))
                fi
            else
                # Check for timeout in harbor output.
                if echo "${HARBOR_OUT}" | grep -qi "timeout"; then
                    STATUS="timeout"
                    TIMEOUT=$(( TIMEOUT + 1 ))
                else
                    STATUS="error"
                    ERROR=$(( ERROR + 1 ))
                fi
            fi
        fi
    fi

    echo "    outcome=${STATUS} reward=${REWARD} duration=${TASK_DURATION_MS}ms"

    # Emit a JSON line for this task.
    python3 -c "
import json, sys
print(json.dumps({
    'task_id': sys.argv[1],
    'outcome': sys.argv[2],
    'reward': sys.argv[3] if sys.argv[3] else None,
    'duration_ms': int(sys.argv[4]),
    'job_id': sys.argv[5] if sys.argv[5] else None,
}))" \
        "${TASK_ID}" "${STATUS}" "${REWARD}" "${TASK_DURATION_MS}" "${JOB_ID}" \
        >> "${TASK_RESULTS_FILE}"
done <<< "${TASK_IDS}"

# ---------------------------------------------------------------------------- #
# Step 5: Assemble and write the report
# ---------------------------------------------------------------------------- #
echo "[5/5] Writing report..."

GIT_SHA=$(git -C "${REPO_ROOT}" rev-parse HEAD 2>/dev/null || echo "unknown")
GIT_SHA_SHORT=$(git -C "${REPO_ROOT}" rev-parse --short HEAD 2>/dev/null || echo "unknown")
AGENT_VERSION=$("${BINARY}" --version 2>/dev/null | head -1 || echo "unknown")

python3 - <<PY
import json, sys, os
from pathlib import Path

tasks_raw = Path("${TASK_RESULTS_FILE}").read_text().strip()
tasks = [json.loads(line) for line in tasks_raw.splitlines() if line.strip()]

total = len(tasks)
passed = sum(1 for t in tasks if t["outcome"] == "pass")
failed = sum(1 for t in tasks if t["outcome"] == "fail")
timed_out = sum(1 for t in tasks if t["outcome"] == "timeout")
errored = sum(1 for t in tasks if t["outcome"] in ("error", "unknown"))
resolved_rate = round(passed / total, 4) if total > 0 else 0.0

report = {
    "schema_version": "1",
    "captured": "${TIMESTAMP}",
    "git_sha": "${GIT_SHA}",
    "git_sha_short": "${GIT_SHA_SHORT}",
    "agent_version": "${AGENT_VERSION}",
    "subset_version": "${SUBSET_VERSION}",
    "model": "claude-haiku-4-5-20251001",
    "provider": "anthropic",
    "config": {
        "preset": "benchmark",
        "runtime": "docker",
        "dataset": "terminal-bench/terminal-bench-2",
    },
    "summary": {
        "total_tasks": total,
        "passed": passed,
        "failed": failed,
        "timed_out": timed_out,
        "errored": errored,
        "resolved_task_rate": resolved_rate,
    },
    "thresholds": {
        "resolved_task_rate_floor": 0.55,
        "resolved_task_rate_target": 0.70,
    },
    "threshold_check": {
        "resolved_task_rate_passes_floor": resolved_rate >= 0.55,
    },
    "tasks": tasks,
}

out_path = "${REPORT_FILE}"
Path(out_path).write_text(json.dumps(report, indent=2))
print(f"  Report: {out_path}")
print(f"  Resolved-task rate: {passed}/{total} = {resolved_rate:.1%}")
if resolved_rate >= 0.55:
    print("  [PASS] Meets regression floor (≥ 55%)")
else:
    print("  [FAIL] Below regression floor (< 55%)")
PY

echo ""
echo "=== Benchmark run complete ==="
echo "  Report: ${REPORT_FILE}"
echo ""
echo "To compare with another run:"
echo "  jq '{rate: .summary.resolved_task_rate, tasks: [.tasks[] | {id: .task_id, outcome}]}' \\"
echo "    benchmark-results/report-*.json"
