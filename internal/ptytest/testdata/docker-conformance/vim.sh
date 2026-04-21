#!/bin/sh
set -eu

export TERM="${TERM:-xterm-256color}"
export LANG="${LANG:-C.UTF-8}"
export LC_ALL="${LC_ALL:-C.UTF-8}"

file=/tmp/agent-pty-vim.txt
cat >"$file" <<'EOF'
alpha vim conformance line
beta vim conformance line
gamma vim conformance line
EOF

exec vim.tiny -Nu NONE -n -i NONE -N "$file"
