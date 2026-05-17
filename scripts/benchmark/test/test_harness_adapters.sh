#!/usr/bin/env bash
# test_harness_adapters.sh — bash test suite for harness adapters
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ADAPTERS_DIR="${SCRIPT_DIR}/harness-adapters"

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

# test_fiz_command_emits_plan_flag_when_planning_mode_true
test_fiz_command_emits_plan_flag_when_planning_mode_true() {
  local test_name="test_fiz_command_emits_plan_flag_when_planning_mode_true"

  # Test with planning_mode=true
  local profile_true='{"id":"test","provider":{"type":"openai-compat","model":"test","base_url":"http://localhost:8000/v1","api_key_env":"TEST_KEY"},"sampling":{"planning_mode":true},"limits":{}}'
  local cmd_spec_true
  if ! cmd_spec_true=$(echo "$profile_true" | "$ADAPTERS_DIR/fiz" command 2>&1); then
    fail "$test_name (planning_mode=true): failed to generate command spec"
    return
  fi

  if ! echo "$cmd_spec_true" | jq -e '.command | index("--plan") != null' >/dev/null 2>&1; then
    fail "$test_name (planning_mode=true): --plan flag not found in command"
    return
  fi

  # Test with planning_mode=false
  local profile_false='{"id":"test","provider":{"type":"openai-compat","model":"test","base_url":"http://localhost:8000/v1","api_key_env":"TEST_KEY"},"sampling":{"planning_mode":false},"limits":{}}'
  local cmd_spec_false
  if ! cmd_spec_false=$(echo "$profile_false" | "$ADAPTERS_DIR/fiz" command 2>&1); then
    fail "$test_name (planning_mode=false): failed to generate command spec"
    return
  fi

  if echo "$cmd_spec_false" | jq -e '.command | index("--plan") != null' >/dev/null 2>&1; then
    fail "$test_name (planning_mode=false): --plan flag should not be in command"
    return
  fi

  # Test with planning_mode unset (should default to false)
  local profile_unset='{"id":"test","provider":{"type":"openai-compat","model":"test","base_url":"http://localhost:8000/v1","api_key_env":"TEST_KEY"},"sampling":{},"limits":{}}'
  local cmd_spec_unset
  if ! cmd_spec_unset=$(echo "$profile_unset" | "$ADAPTERS_DIR/fiz" command 2>&1); then
    fail "$test_name (planning_mode unset): failed to generate command spec"
    return
  fi

  if echo "$cmd_spec_unset" | jq -e '.command | index("--plan") != null' >/dev/null 2>&1; then
    fail "$test_name (planning_mode unset): --plan flag should not be in command when unset"
    return
  fi

  pass "$test_name"
}

# test_each_adapter_has_summary_header
test_each_adapter_has_summary_header() {
  local test_name="test_each_adapter_has_summary_header"
  local adapters=(fiz claude codex opencode pi cost-probe noop dumb-script)
  local failed_adapters=()

  for adapter in "${adapters[@]}"; do
    local adapter_path="${ADAPTERS_DIR}/${adapter}"
    if [[ ! -f "$adapter_path" ]]; then
      fail "$test_name: adapter file not found: $adapter"
      failed_adapters+=("$adapter")
      continue
    fi

    # Check for SUMMARY header on line 2 (after shebang)
    if ! head -3 "$adapter_path" | grep -q '^# SUMMARY:'; then
      fail "$test_name: adapter $adapter missing SUMMARY header"
      failed_adapters+=("$adapter")
    fi
  done

  if [[ ${#failed_adapters[@]} -eq 0 ]]; then
    pass "$test_name"
  fi
}

# test_fiz_env_includes_planning_mode_var_when_true
test_fiz_env_includes_planning_mode_var_when_true() {
  local test_name="test_fiz_env_includes_planning_mode_var_when_true"

  local profile='{"id":"test","provider":{"type":"openai-compat","model":"test","base_url":"http://localhost:8000/v1","api_key_env":"TEST_KEY"},"sampling":{"planning_mode":true},"limits":{}}'
  local cmd_spec
  if ! cmd_spec=$(echo "$profile" | "$ADAPTERS_DIR/fiz" command 2>&1); then
    fail "$test_name: failed to generate command spec"
    return
  fi

  if ! echo "$cmd_spec" | jq -e '.env.FIZEAU_PLANNING_MODE == "1"' >/dev/null 2>&1; then
    fail "$test_name: FIZEAU_PLANNING_MODE env var not set to 1"
    return
  fi

  pass "$test_name"
}

# test_fiz_env_excludes_planning_mode_var_when_false
test_fiz_env_excludes_planning_mode_var_when_false() {
  local test_name="test_fiz_env_excludes_planning_mode_var_when_false"

  local profile='{"id":"test","provider":{"type":"openai-compat","model":"test","base_url":"http://localhost:8000/v1","api_key_env":"TEST_KEY"},"sampling":{"planning_mode":false},"limits":{}}'
  local cmd_spec
  if ! cmd_spec=$(echo "$profile" | "$ADAPTERS_DIR/fiz" command 2>&1); then
    fail "$test_name: failed to generate command spec"
    return
  fi

  if echo "$cmd_spec" | jq -e '.env.FIZEAU_PLANNING_MODE' >/dev/null 2>&1; then
    fail "$test_name: FIZEAU_PLANNING_MODE env var should not be set when planning_mode is false"
    return
  fi

  pass "$test_name"
}

main() {
  echo "Running harness-adapters tests..."
  echo ""

  test_each_adapter_has_summary_header || true
  test_fiz_command_emits_plan_flag_when_planning_mode_true || true
  test_fiz_env_includes_planning_mode_var_when_true || true
  test_fiz_env_excludes_planning_mode_var_when_false || true

  echo ""
  echo "Tests passed: $TESTS_PASSED"
  echo "Tests failed: $TESTS_FAILED"

  if [[ $TESTS_FAILED -gt 0 ]]; then
    exit 1
  fi
  exit 0
}

main "$@"
