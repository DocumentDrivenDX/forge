#!/usr/bin/env bash
# run.sh — master test runner for benchmark tests
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TESTS_PASSED=0
TESTS_FAILED=0

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

run_test() {
  local test_file="$1"
  local test_name="$(basename "$test_file")"

  echo -e "${YELLOW}Running $test_name...${NC}"

  if bash "$test_file" 2>&1; then
    TESTS_PASSED=$((TESTS_PASSED + 1))
  else
    TESTS_FAILED=$((TESTS_FAILED + 1))
  fi

  echo ""
}

main() {
  echo "Running all benchmark test suites..."
  echo ""

  run_test "${SCRIPT_DIR}/test_runner.sh"
  run_test "${SCRIPT_DIR}/test_harness_adapters.sh"
  run_test "${SCRIPT_DIR}/test_harbor_image.sh"
  run_test "${SCRIPT_DIR}/test_runtime_probe.sh"

  echo "========================================"
  echo "Test Summary:"
  echo "  Suites passed: $TESTS_PASSED"
  echo "  Suites failed: $TESTS_FAILED"
  echo "========================================"

  if [[ $TESTS_FAILED -gt 0 ]]; then
    exit 1
  fi
  exit 0
}

main "$@"
