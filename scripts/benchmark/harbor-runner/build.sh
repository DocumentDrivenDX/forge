#!/usr/bin/env bash
# Build the fizeau-harbor-runner Docker image with a deterministic
# image-content-sha label computed from harbor_adapters/, harbor_agent.py,
# and Dockerfile. Re-running with no source changes reuses
# the cached image (label sha unchanged, layers from Docker build cache).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

IMAGE_TAG="${IMAGE_TAG:-fizeau-harbor-runner:latest}"
DOCKERFILE="${SCRIPT_DIR}/Dockerfile"
ADAPTERS_DIR="${SCRIPT_DIR}/../harbor_adapters"
HARBOR_AGENT_PATH="${SCRIPT_DIR}/../harbor_agent.py"

compute_content_sha() {
  {
    LC_ALL=C find "${ADAPTERS_DIR}" -type f \
      | LC_ALL=C sort \
      | while IFS= read -r f; do
          printf '%s  %s\n' "$(sha256sum "$f" | awk '{print $1}')" "${f#${SCRIPT_DIR}/../}"
        done
    printf '%s  %s\n' "$(sha256sum "${HARBOR_AGENT_PATH}" | awk '{print $1}')" "harbor_agent.py"
    printf '%s  %s\n' "$(sha256sum "${DOCKERFILE}" | awk '{print $1}')" "Dockerfile"
  } | sha256sum | awk '{print $1}'
}

CONTENT_SHA="$(compute_content_sha)"

docker build \
  --file "${DOCKERFILE}" \
  --label "image-content-sha=${CONTENT_SHA}" \
  --tag "${IMAGE_TAG}" \
  "${REPO_ROOT}"

printf 'image-content-sha=%s\n' "${CONTENT_SHA}"
printf 'tag=%s\n' "${IMAGE_TAG}"
