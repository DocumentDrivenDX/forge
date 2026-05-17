#!/usr/bin/env bash
# test_runtime_probe.sh — tests for runtime-probe.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUNTIME_PROBE="${SCRIPT_DIR}/runtime-probe.sh"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m' # No Color

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

# test_runtime_probe_exists
test_runtime_probe_exists() {
  local test_name="test_runtime_probe_exists"

  if [[ ! -f "$RUNTIME_PROBE" ]]; then
    fail "$test_name: runtime-probe.sh not found at $RUNTIME_PROBE"
    return 1
  fi

  if [[ ! -x "$RUNTIME_PROBE" ]]; then
    fail "$test_name: runtime-probe.sh is not executable"
    return 1
  fi

  pass "$test_name"
  return 0
}

# test_runtime_probe_emits_model_server_shape
test_runtime_probe_emits_model_server_shape() {
  local test_name="test_runtime_probe_emits_model_server_shape"

  # Test with lucebox runtime
  local profile='{"id":"test","provider":{"type":"openai-compat","model":"test","base_url":"http://localhost:8000/v1"},"metadata":{"runtime":"lucebox"},"sampling":{},"limits":{}}'
  local output

  # The probe will fail since the endpoint is not reachable, but it should still emit JSON
  output=$(echo "$profile" | "$RUNTIME_PROBE" 2>/dev/null || true)

  if [[ -z "$output" ]]; then
    # Try again with proper exit handling
    output=$(echo "$profile" | "$RUNTIME_PROBE" 2>&1 || true)
  fi

  # Check that the output is valid JSON
  if ! echo "$output" | jq . >/dev/null 2>&1; then
    fail "$test_name: runtime-probe did not emit valid JSON"
    return 1
  fi

  # Check that the JSON has the required keys
  if ! echo "$output" | jq -e '.name' >/dev/null 2>&1; then
    fail "$test_name: output missing 'name' key"
    return 1
  fi

  if ! echo "$output" | jq -e '.version' >/dev/null 2>&1; then
    fail "$test_name: output missing 'version' key"
    return 1
  fi

  if ! echo "$output" | jq -e '.commit' >/dev/null 2>&1; then
    fail "$test_name: output missing 'commit' key"
    return 1
  fi

  if ! echo "$output" | jq -e '.endpoint' >/dev/null 2>&1; then
    fail "$test_name: output missing 'endpoint' key"
    return 1
  fi

  pass "$test_name"
  return 0
}

# test_runtime_probe_handles_empty_profile
test_runtime_probe_handles_empty_profile() {
  local test_name="test_runtime_probe_handles_empty_profile"

  # Empty profile should return exit code 2 (invalid input)
  if echo "" | "$RUNTIME_PROBE" >/dev/null 2>&1; then
    fail "$test_name: runtime-probe should reject empty profile"
    return 1
  fi

  pass "$test_name"
  return 0
}

# test_runtime_probe_handles_missing_runtime
test_runtime_probe_handles_missing_runtime() {
  local test_name="test_runtime_probe_handles_missing_runtime"

  local profile='{"id":"test","provider":{"type":"openai-compat","model":"test","base_url":"http://localhost:8000/v1"},"metadata":{},"sampling":{},"limits":{}}'

  # Missing runtime should return exit code 2 (invalid input)
  if echo "$profile" | "$RUNTIME_PROBE" >/dev/null 2>&1; then
    fail "$test_name: runtime-probe should reject profile with missing runtime"
    return 1
  fi

  pass "$test_name"
  return 0
}

# test_runtime_probe_handles_missing_endpoint
test_runtime_probe_handles_missing_endpoint() {
  local test_name="test_runtime_probe_handles_missing_endpoint"

  local profile='{"id":"test","provider":{"type":"openai-compat","model":"test"},"metadata":{"runtime":"lucebox"},"sampling":{},"limits":{}}'

  # Missing endpoint should return exit code 2 (invalid input)
  if echo "$profile" | "$RUNTIME_PROBE" >/dev/null 2>&1; then
    fail "$test_name: runtime-probe should reject profile with missing endpoint"
    return 1
  fi

  pass "$test_name"
  return 0
}

# test_runtime_probe_contains_summary_header
test_runtime_probe_contains_summary_header() {
  local test_name="test_runtime_probe_contains_summary_header"

  if ! head -3 "$RUNTIME_PROBE" | grep -q '^# SUMMARY:'; then
    fail "$test_name: runtime-probe.sh missing SUMMARY header"
    return 1
  fi

  pass "$test_name"
  return 0
}

main() {
  echo "Running runtime-probe tests..."
  echo ""

  test_runtime_probe_exists || true
  test_runtime_probe_contains_summary_header || true
  test_runtime_probe_emits_model_server_shape || true
  test_runtime_probe_handles_empty_profile || true
  test_runtime_probe_handles_missing_runtime || true
  test_runtime_probe_handles_missing_endpoint || true

  echo ""
  echo "Tests passed: $TESTS_PASSED"
  echo "Tests failed: $TESTS_FAILED"

  if [[ $TESTS_FAILED -gt 0 ]]; then
    exit 1
  fi
  exit 0
}

main "$@"
