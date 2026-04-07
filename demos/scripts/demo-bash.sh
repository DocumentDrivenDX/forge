#!/usr/bin/env bash
# Demo: forge uses bash tool to explore a project.
set -euo pipefail

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/cmd/server" "$WORK/internal/api" "$WORK/internal/db"
echo 'package main' > "$WORK/cmd/server/main.go"
echo 'package api' > "$WORK/internal/api/handler.go"
echo 'package api' > "$WORK/internal/api/middleware.go"
echo 'package db' > "$WORK/internal/db/postgres.go"
echo 'module example.com/myapp' > "$WORK/go.mod"

echo "$ forge -p 'List all Go files in this project and summarize the package structure'"
echo ""
./forge -p "Use bash to find all .go files in this project, then summarize the package structure. Be concise." \
  --work-dir "$WORK" 2>&1
