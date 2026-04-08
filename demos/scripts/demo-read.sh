#!/usr/bin/env bash
# Demo: ddx-agent reads a file and describes its contents.
# This script creates a workspace, runs ddx-agent, and cleans up.
set -euo pipefail

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

cat > "$WORK/main.go" <<'GOFILE'
package main

import (
	"fmt"
	"net/http"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Hello from DDX Agent!")
	})
	fmt.Println("Listening on :8080")
	http.ListenAndServe(":8080", nil)
}
GOFILE

echo "$ ddx-agent -p 'Read main.go and explain what this program does'"
echo ""
./ddx-agent -p "Read main.go and explain what this program does. Be concise — 2-3 sentences max." \
  --work-dir "$WORK" 2>&1
