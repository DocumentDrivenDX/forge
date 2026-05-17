#!/usr/bin/env bash
# test_harbor_image.sh — tests for harbor docker image build
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD_SCRIPT="${SCRIPT_DIR}/build-harbor-runner.sh"

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

# test_build_script_exists
test_build_script_exists() {
  local test_name="test_build_script_exists"

  if [[ ! -f "$BUILD_SCRIPT" ]]; then
    fail "$test_name: build script not found at $BUILD_SCRIPT"
    return 1
  fi

  if [[ ! -x "$BUILD_SCRIPT" ]]; then
    fail "$test_name: build script is not executable"
    return 1
  fi

  pass "$test_name"
  return 0
}

# test_build_script_computes_content_sha
test_build_script_computes_content_sha() {
  local test_name="test_build_script_computes_content_sha"

  # The build script should output the computed SHA when sourced as a function
  local output
  if ! output=$(bash "$BUILD_SCRIPT" --dry-run 2>&1 || true); then
    # The script will fail since we're not actually building, but we can check if SHA computation logic exists
    if ! grep -q 'compute_content_sha' "$BUILD_SCRIPT"; then
      fail "$test_name: compute_content_sha function not found in build script"
      return 1
    fi
  fi

  # Check that the script contains image-content-sha label logic
  if ! grep -q 'image-content-sha' "$BUILD_SCRIPT"; then
    fail "$test_name: image-content-sha label not found in build script"
    return 1
  fi

  pass "$test_name"
  return 0
}

# test_build_script_references_docker_label
test_build_script_references_docker_label() {
  local test_name="test_build_script_references_docker_label"

  if ! grep -q '"image-content-sha=' "$BUILD_SCRIPT"; then
    fail "$test_name: docker label not properly set in build script"
    return 1
  fi

  pass "$test_name"
  return 0
}

# test_dockerfile_harbor_runner_exists
test_dockerfile_harbor_runner_exists() {
  local test_name="test_dockerfile_harbor_runner_exists"
  local dockerfile="${SCRIPT_DIR}/Dockerfile.harbor-runner"

  if [[ ! -f "$dockerfile" ]]; then
    fail "$test_name: Dockerfile.harbor-runner not found"
    return 1
  fi

  pass "$test_name"
  return 0
}

# test_harbor_adapters_dir_exists
test_harbor_adapters_dir_exists() {
  local test_name="test_harbor_adapters_dir_exists"
  local adapters_dir="${SCRIPT_DIR}/harbor_adapters"

  if [[ ! -d "$adapters_dir" ]]; then
    fail "$test_name: harbor_adapters directory not found"
    return 1
  fi

  pass "$test_name"
  return 0
}

main() {
  echo "Running harbor-image tests..."
  echo ""

  test_build_script_exists || true
  test_build_script_computes_content_sha || true
  test_build_script_references_docker_label || true
  test_dockerfile_harbor_runner_exists || true
  test_harbor_adapters_dir_exists || true

  echo ""
  echo "Tests passed: $TESTS_PASSED"
  echo "Tests failed: $TESTS_FAILED"

  if [[ $TESTS_FAILED -gt 0 ]]; then
    exit 1
  fi
  exit 0
}

main "$@"
