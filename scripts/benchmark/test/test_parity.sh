#!/usr/bin/env bash
# test_parity.sh — acceptance tests for parity fixture capture and diff.sh (A4)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIFF_SCRIPT="${SCRIPT_DIR}/testdata/parity/diff.sh"
PARITY_DIR="${SCRIPT_DIR}/testdata/parity"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

TESTS_PASSED=0
TESTS_FAILED=0

fail() {
  echo -e "${RED}FAIL${NC}: $*" >&2
  TESTS_FAILED=$((TESTS_FAILED + 1))
}

pass() {
  echo -e "${GREEN}PASS${NC}: $*"
  TESTS_PASSED=$((TESTS_PASSED + 1))
}

# Test AC1: Go runner baseline reports exist with PROVENANCE.md
test_go_runner_baseline_exists() {
  local test_name="test_go_runner_baseline_exists"
  local cells=("cancel-async-tasks" "configure-git-webserver" "log-summary-date-ranges")
  local missing=0

  for cell in "${cells[@]}"; do
    if [[ ! -f "${PARITY_DIR}/go-runner/${cell}/report.json" ]]; then
      fail "${test_name}: missing go-runner report for ${cell}"
      missing=1
    fi
  done

  if [[ ! -f "${PARITY_DIR}/go-runner/PROVENANCE.md" ]]; then
    fail "${test_name}: missing go-runner/PROVENANCE.md"
    missing=1
  fi

  if grep -q "Go Runner Baseline" "${PARITY_DIR}/go-runner/PROVENANCE.md" 2>/dev/null; then
    [[ ${missing} -eq 0 ]] && pass "${test_name}"
  else
    fail "${test_name}: PROVENANCE.md missing header"
  fi

  return $((missing))
}

# Test AC2: Bash runner baseline reports exist with PROVENANCE.md
test_bash_runner_baseline_exists() {
  local test_name="test_bash_runner_baseline_exists"
  local cells=("cancel-async-tasks" "configure-git-webserver" "log-summary-date-ranges")
  local missing=0

  for cell in "${cells[@]}"; do
    if [[ ! -f "${PARITY_DIR}/bash-runner/${cell}/report.json" ]]; then
      fail "${test_name}: missing bash-runner report for ${cell}"
      missing=1
    fi
  done

  if [[ ! -f "${PARITY_DIR}/bash-runner/PROVENANCE.md" ]]; then
    fail "${test_name}: missing bash-runner/PROVENANCE.md"
    missing=1
  fi

  if grep -q "Bash Runner Provenance" "${PARITY_DIR}/bash-runner/PROVENANCE.md" 2>/dev/null; then
    [[ ${missing} -eq 0 ]] && pass "${test_name}"
  else
    fail "${test_name}: PROVENANCE.md missing header"
  fi

  return $((missing))
}

# Test AC3: diff.sh runs cleanly against baseline (TestParityDiffIsClean)
test_diff_clean_against_go_baseline() {
  local test_name="test_diff_clean_against_go_baseline"

  if ! bash "${DIFF_SCRIPT}" "${PARITY_DIR}/go-runner" "${PARITY_DIR}/bash-runner" >/dev/null 2>&1; then
    fail "${test_name}: diff.sh exited non-zero; there are unallowlisted divergences"
    bash "${DIFF_SCRIPT}" "${PARITY_DIR}/go-runner" "${PARITY_DIR}/bash-runner" 2>&1 | head -20 >&2
    return 1
  fi

  pass "${test_name}"
  return 0
}

# Test AC4: diff.sh detects unallowlisted drift (test_diff_detects_unallowlisted_drift)
test_diff_detects_unallowlisted_drift() {
  local test_name="test_diff_detects_unallowlisted_drift"
  local tmpdir
  tmpdir=$(mktemp -d)
  trap "rm -rf '${tmpdir}'" RETURN

  # Copy bash-runner to a test directory and modify a non-allowlisted field
  cp -r "${PARITY_DIR}/bash-runner" "${tmpdir}/bash-modified"
  local cell_report="${tmpdir}/bash-modified/cancel-async-tasks/report.json"

  # Modify a non-allowlisted field (task_id)
  jq '.task_id = "MODIFIED"' "${cell_report}" >"${tmpdir}/report.tmp"
  mv "${tmpdir}/report.tmp" "${cell_report}"

  # diff.sh should exit non-zero and report the divergence
  if bash "${DIFF_SCRIPT}" "${PARITY_DIR}/go-runner" "${tmpdir}/bash-modified" >/dev/null 2>&1; then
    fail "${test_name}: diff.sh should have detected task_id divergence but exited 0"
    return 1
  fi

  local diff_output
  diff_output=$(bash "${DIFF_SCRIPT}" "${PARITY_DIR}/go-runner" "${tmpdir}/bash-modified" 2>&1 || true)
  if echo "${diff_output}" | grep -q "task_id"; then
    pass "${test_name}"
    return 0
  else
    fail "${test_name}: diff.sh did not report task_id divergence"
    echo "${diff_output}" >&2
    return 1
  fi
}

# Test AC5: diff.sh ignores allowlisted paths (test_diff_ignores_allowlisted_paths)
test_diff_ignores_allowlisted_paths() {
  local test_name="test_diff_ignores_allowlisted_paths"
  local tmpdir
  tmpdir=$(mktemp -d)
  trap "rm -rf '${tmpdir}'" RETURN

  # Copy bash-runner and modify allowlisted fields
  cp -r "${PARITY_DIR}/bash-runner" "${tmpdir}/bash-modified"
  local cell_report="${tmpdir}/bash-modified/cancel-async-tasks/report.json"

  # Modify allowlisted fields: cell_id, started_at
  jq '.cell_id = "MODIFIED-CELL-ID" | .started_at = "2099-01-01T00:00:00Z"' "${cell_report}" >"${tmpdir}/report.tmp"
  mv "${tmpdir}/report.tmp" "${cell_report}"

  # diff.sh should exit 0 and ignore these changes
  if bash "${DIFF_SCRIPT}" "${PARITY_DIR}/go-runner" "${tmpdir}/bash-modified" >/dev/null 2>&1; then
    pass "${test_name}"
    return 0
  else
    fail "${test_name}: diff.sh should have ignored cell_id and started_at divergence but exited non-zero"
    bash "${DIFF_SCRIPT}" "${PARITY_DIR}/go-runner" "${tmpdir}/bash-modified" 2>&1 >&2
    return 1
  fi
}

# Test AC6: ALLOWLIST.md exists and documents allowlisted paths
test_allowlist_doc_exists() {
  local test_name="test_allowlist_doc_exists"

  if [[ ! -f "${PARITY_DIR}/ALLOWLIST.md" ]]; then
    fail "${test_name}: ALLOWLIST.md does not exist"
    return 1
  fi

  # Check that ALLOWLIST.md documents key allowlisted paths
  local required_paths=("cell_id" "started_at" "finished_at" "harbor_runner_image_digest")
  local missing_doc=0

  for path in "${required_paths[@]}"; do
    if ! grep -q "${path}" "${PARITY_DIR}/ALLOWLIST.md"; then
      fail "${test_name}: ALLOWLIST.md does not document ${path}"
      missing_doc=1
    fi
  done

  if [[ ${missing_doc} -eq 0 ]]; then
    pass "${test_name}"
    return 0
  fi
  return 1
}

# Test AC6 (bonus): diff.sh internal allowlist matches documented list
test_allowlist_doc_matches_code() {
  local test_name="test_allowlist_doc_matches_code"

  # Extract allowlisted paths from ALLOWLIST.md (backtick code blocks)
  # and verify they are mentioned in the diff.sh script

  # Just check that key documented paths are referenced in diff.sh
  local required_patterns=("cell_id" "started_at" "harbor_runner_image_digest")
  local missing_in_code=0

  for pattern in "${required_patterns[@]}"; do
    if ! grep -q "${pattern}" "${DIFF_SCRIPT}"; then
      fail "${test_name}: diff.sh does not reference ${pattern} in allowlist"
      missing_in_code=1
    fi
  done

  if [[ ${missing_in_code} -eq 0 ]]; then
    pass "${test_name}"
    return 0
  fi
  return 1
}

# Main
main() {
  echo "Running parity tests (A4)..."
  echo ""

  test_go_runner_baseline_exists
  test_bash_runner_baseline_exists
  test_diff_clean_against_go_baseline
  test_diff_detects_unallowlisted_drift
  test_diff_ignores_allowlisted_paths
  test_allowlist_doc_exists
  test_allowlist_doc_matches_code

  echo ""
  echo "========================================"
  echo "Test Summary:"
  echo "  Tests passed: $TESTS_PASSED"
  echo "  Tests failed: $TESTS_FAILED"
  echo "========================================"

  if [[ $TESTS_FAILED -gt 0 ]]; then
    exit 1
  fi
  exit 0
}

main
