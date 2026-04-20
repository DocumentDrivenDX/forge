#!/usr/bin/env bash
# smoke_run.sh — Run a single Terminal-Bench task to validate the ddx-agent adapter.
# See docs/helix/02-design/solution-designs/SD-009-benchmark-mode.md §4 for the full
# smoke-run workflow and passing criteria.
#
# Usage:
#   ANTHROPIC_API_KEY=sk-... ./scripts/benchmark/smoke_run.sh
#   OPENROUTER_API_KEY=sk-or-... ./scripts/benchmark/smoke_run.sh
#
# Prerequisites:
#   pip install harbor-framework
#   harbor dataset pull terminal-bench/terminal-bench-2
#   Docker running locally
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
DIST_DIR="${REPO_ROOT}/dist"
DEFAULT_BINARY="${DIST_DIR}/ddx-agent-linux-amd64"
INPUT_BINARY="${DDX_AGENT_BINARY:-${DEFAULT_BINARY}}"
SMOKE_TASK="${DDX_BENCH_SMOKE_TASK:-break-filter-js-from-html}"
DATASET="${DDX_BENCH_DATASET:-terminal-bench@2.0}"
RUNTIME="${DDX_BENCH_RUNTIME:-docker}"
PRESET="${DDX_BENCH_PRESET:-benchmark}"
PROVIDER_NAME="${DDX_BENCH_PROVIDER_NAME:-openrouter}"
PROVIDER_TYPE="${DDX_BENCH_PROVIDER_TYPE:-lmstudio}"
PROVIDER_MODEL="${DDX_BENCH_PROVIDER_MODEL:-openai/gpt-oss-20b}"
PROVIDER_BASE_URL="${DDX_BENCH_PROVIDER_BASE_URL:-https://openrouter.ai/api/v1}"
PROVIDER_API_KEY_ENV="${DDX_BENCH_PROVIDER_API_KEY_ENV:-OPENROUTER_API_KEY}"
PROVIDER_HEADERS_JSON="${DDX_BENCH_PROVIDER_HEADERS_JSON:-}"
SYSTEM_APPEND="${DDX_BENCH_SYSTEM_APPEND:-}"
HARBOR_BIN=""

ensure_harbor() {
    if [[ -n "${HARBOR_BIN}" ]]; then
        return
    fi
    if command -v harbor &>/dev/null; then
        HARBOR_BIN="$(command -v harbor)"
        return
    fi
    if ! command -v uv &>/dev/null; then
        echo "ERROR: 'harbor' not found and 'uv' is unavailable for automatic install."
        exit 1
    fi

    echo "      harbor not found; installing via uv tool install harbor..."
    uv tool install harbor
    hash -r

    if command -v harbor &>/dev/null; then
        HARBOR_BIN="$(command -v harbor)"
        return
    fi
    if [[ -x "${HOME}/.local/bin/harbor" ]]; then
        HARBOR_BIN="${HOME}/.local/bin/harbor"
        return
    fi

    echo "ERROR: Harbor install completed but no executable was found on PATH or at ${HOME}/.local/bin/harbor."
    exit 1
}

resolve_provider_key() {
    if [[ -n "${!PROVIDER_API_KEY_ENV:-}" ]]; then
        return
    fi

    local config_path=""
    local config_key=""
    local -a config_candidates=(
        "${REPO_ROOT}/.agent/config.yaml"
        "${HOME}/.config/agent/config.yaml"
    )

    for config_path in "${config_candidates[@]}"; do
        [[ -f "${config_path}" ]] || continue
        config_key="$(awk -v provider="${PROVIDER_NAME}" '
            BEGIN { in_providers = 0; current = "" }
            /^[[:space:]]*providers:[[:space:]]*$/ { in_providers = 1; next }
            in_providers && /^[^[:space:]]/ { in_providers = 0; current = "" }
            !in_providers { next }
            /^  [^:#]+:[[:space:]]*$/ {
                current = $0
                sub(/^  /, "", current)
                sub(/:[[:space:]]*$/, "", current)
                next
            }
            current == provider && /^    api_key:[[:space:]]*/ {
                value = $0
                sub(/^    api_key:[[:space:]]*/, "", value)
                gsub(/^[[:space:]]+|[[:space:]]+$/, "", value)
                if (value ~ /^".*"$/ || value ~ /^'\''.*'\''$/) {
                    value = substr(value, 2, length(value) - 2)
                }
                print value
                exit
            }
        ' "${config_path}")"
        if [[ "${config_key}" =~ ^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$ ]]; then
            config_key="${!BASH_REMATCH[1]:-}"
        fi
        if [[ -n "${config_key}" ]]; then
            export "${PROVIDER_API_KEY_ENV}=${config_key}"
            echo "      sourced ${PROVIDER_API_KEY_ENV} from ${config_path}"
            return
        fi
    done
}

echo "=== ddx-agent benchmark smoke run ==="
echo "Repo: ${REPO_ROOT}"
echo "Task: ${SMOKE_TASK}"
echo ""

# Step 1: Prepare binary under test
echo "[1/4] Preparing binary under test..."
if [[ -z "${DDX_AGENT_BINARY:-}" ]]; then
    mkdir -p "${DIST_DIR}"
    GOOS=linux GOARCH=amd64 go build -o "${DEFAULT_BINARY}" "${REPO_ROOT}/cmd/agent"
    echo "      Built current checkout binary: ${DEFAULT_BINARY}"
else
    if [[ ! -f "${INPUT_BINARY}" ]]; then
        echo "ERROR: DDX_AGENT_BINARY does not exist: ${INPUT_BINARY}"
        exit 1
    fi
    echo "      Using supplied binary: ${INPUT_BINARY}"
fi

# Step 2: Validate Harbor is installed
echo "[2/4] Checking Harbor installation..."
ensure_harbor
resolve_provider_key
echo "      OK: $("${HARBOR_BIN}" --version 2>/dev/null || echo 'harbor found')"

# Step 3: Run single smoke task
echo "[3/4] Running smoke task '${SMOKE_TASK}'..."
SMOKE_JOB_NAME="smoke-${SMOKE_TASK}-$(date -u +%Y%m%dT%H%M%SZ)"
SMOKE_JOBS_DIR="${REPO_ROOT}/benchmark-results/harbor-jobs"
mkdir -p "${SMOKE_JOBS_DIR}"
AGENT_ENV_ARGS=()
if [[ -n "${!PROVIDER_API_KEY_ENV:-}" ]]; then
    AGENT_ENV_ARGS+=(--ae "${PROVIDER_API_KEY_ENV}=${!PROVIDER_API_KEY_ENV}")
fi
JOB_OUTPUT=$( \
    PYTHONPATH="${SCRIPT_DIR}${PYTHONPATH:+:${PYTHONPATH}}" \
    HARBOR_AGENT_ARTIFACT="${INPUT_BINARY}" \
    DDX_BENCH_PRESET="${PRESET}" \
    DDX_BENCH_PROVIDER_NAME="${PROVIDER_NAME}" \
    DDX_BENCH_PROVIDER_TYPE="${PROVIDER_TYPE}" \
    DDX_BENCH_PROVIDER_MODEL="${PROVIDER_MODEL}" \
    DDX_BENCH_PROVIDER_BASE_URL="${PROVIDER_BASE_URL}" \
    DDX_BENCH_PROVIDER_API_KEY_ENV="${PROVIDER_API_KEY_ENV}" \
    DDX_BENCH_PROVIDER_HEADERS_JSON="${PROVIDER_HEADERS_JSON}" \
    DDX_BENCH_SYSTEM_APPEND="${SYSTEM_APPEND}" \
    "${HARBOR_BIN}" run \
    --yes \
    --dataset "${DATASET}" \
    --include-task-name "${SMOKE_TASK}" \
    --n-tasks 1 \
    --agent-import-path "harbor_agent:DDXAgent" \
    --model "${PROVIDER_MODEL}" \
    --env "${RUNTIME}" \
    --jobs-dir "${SMOKE_JOBS_DIR}" \
    --job-name "${SMOKE_JOB_NAME}" \
    "${AGENT_ENV_ARGS[@]}" \
    2>&1)
echo "${JOB_OUTPUT}"
JOB_DIR="${SMOKE_JOBS_DIR}/${SMOKE_JOB_NAME}"

# Step 4: Validate results
echo "[4/4] Validating results..."
TRIAL_DIR=$(find "${JOB_DIR}" -mindepth 1 -maxdepth 1 -type d | head -1)
if [[ -z "${TRIAL_DIR}" ]]; then
    echo "ERROR: No trial directory found under ${JOB_DIR}"
    exit 1
fi

REWARD_FILE="${TRIAL_DIR}/verifier/reward.txt"
TRAJECTORY_FILE="${TRIAL_DIR}/agent/trajectory.json"

# Check reward file exists
if [[ ! -f "${REWARD_FILE}" ]]; then
    echo "ERROR: reward.txt not found at ${REWARD_FILE}"
    exit 1
fi
REWARD=$(cat "${REWARD_FILE}")
echo "      reward.txt = ${REWARD}"

# Check trajectory is valid JSON with at least 1 step
if [[ ! -f "${TRAJECTORY_FILE}" ]]; then
    echo "ERROR: trajectory.json not found at ${TRAJECTORY_FILE}"
    exit 1
fi
STEP_COUNT=$(python3 -c "import json; d=json.load(open('${TRAJECTORY_FILE}')); print(len(d.get('steps', [])))" 2>/dev/null || echo "0")
if [[ "${STEP_COUNT}" -lt 1 ]]; then
    echo "ERROR: trajectory.json has ${STEP_COUNT} steps (expected >= 1)"
    exit 1
fi
echo "      trajectory.json: ${STEP_COUNT} steps"

echo ""
echo "=== Smoke run PASSED ==="
echo "  Harness: ddx-agent exited cleanly, trajectory produced, reward captured"
echo "  Task result: reward=${REWARD} (1=pass, 0=fail; both valid for smoke)"
echo "  Trial dir: ${TRIAL_DIR}"
