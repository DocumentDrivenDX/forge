#!/usr/bin/env bash
# Demo: forge reads, edits, and verifies a file change.
set -euo pipefail

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

cat > "$WORK/config.yaml" <<'YAML'
server:
  host: localhost
  port: 8080
  debug: true

database:
  host: localhost
  port: 5432
  name: myapp_dev
YAML

echo "$ forge -p 'Read config.yaml, change the server port from 8080 to 9090, then verify'"
echo ""
./forge -p "Read config.yaml, use the edit tool to change the server port from 8080 to 9090, then read it again to confirm the change was made." \
  --work-dir "$WORK" 2>&1
