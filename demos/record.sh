#!/usr/bin/env bash
# Record all demo reels. Requires ddx-agent binary and asciinema.
# Usage: ./demos/record.sh [--lmstudio URL]
#
# By default uses AGENT_BASE_URL from env or http://localhost:1234/v1.
# Session logs are saved to demos/sessions/ for CI replay.
set -euo pipefail

cd "$(dirname "$0")/.."

# Ensure ddx-agent is built
make build

LMSTUDIO_URL="${AGENT_BASE_URL:-http://localhost:1234/v1}"
MODEL="${AGENT_MODEL:-qwen/qwen3-coder-next}"

export AGENT_BASE_URL="$LMSTUDIO_URL"
export AGENT_MODEL="$MODEL"

echo "Recording demos against $LMSTUDIO_URL with model $MODEL"

for script in demos/scripts/demo-*.sh; do
  name=$(basename "$script" .sh | sed 's/demo-//')
  cast="website/static/demos/${name}.cast"
  echo "Recording: $name -> $cast"
  asciinema rec "$cast" \
    --cols 100 --rows 30 \
    --title "DDX Agent: $name" \
    --command "bash $script" \
    --overwrite
done

# Copy session logs for CI replay
echo "Copying session logs..."
latest_sessions=$(ls -t .agent/sessions/*.jsonl 2>/dev/null | head -3)
i=0
for session in $latest_sessions; do
  cp "$session" "demos/sessions/"
  echo "  $session -> demos/sessions/"
done

echo "Done. Cast files in website/static/demos/, sessions in demos/sessions/"
