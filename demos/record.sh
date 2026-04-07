#!/usr/bin/env bash
# Record all demo reels. Requires forge binary and asciinema.
# Usage: ./demos/record.sh [--lmstudio URL]
#
# By default uses FORGE_BASE_URL from env or http://localhost:1234/v1.
# Session logs are saved to demos/sessions/ for CI replay.
set -euo pipefail

cd "$(dirname "$0")/.."

# Ensure forge is built
make build

LMSTUDIO_URL="${FORGE_BASE_URL:-http://localhost:1234/v1}"
MODEL="${FORGE_MODEL:-qwen/qwen3-coder-next}"

export FORGE_BASE_URL="$LMSTUDIO_URL"
export FORGE_MODEL="$MODEL"

echo "Recording demos against $LMSTUDIO_URL with model $MODEL"

for script in demos/scripts/demo-*.sh; do
  name=$(basename "$script" .sh | sed 's/demo-//')
  cast="website/static/demos/${name}.cast"
  echo "Recording: $name -> $cast"
  asciinema rec "$cast" \
    --cols 100 --rows 30 \
    --title "Forge: $name" \
    --command "bash $script" \
    --overwrite
done

# Copy session logs for CI replay
echo "Copying session logs..."
latest_sessions=$(ls -t .forge/sessions/*.jsonl 2>/dev/null | head -3)
i=0
for session in $latest_sessions; do
  cp "$session" "demos/sessions/"
  echo "  $session -> demos/sessions/"
done

echo "Done. Cast files in website/static/demos/, sessions in demos/sessions/"
