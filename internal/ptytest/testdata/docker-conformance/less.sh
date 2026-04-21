#!/bin/sh
set -eu

export TERM="${TERM:-xterm-256color}"
export LANG="${LANG:-C.UTF-8}"
export LC_ALL="${LC_ALL:-C.UTF-8}"

file=/tmp/agent-pty-less.txt
: >"$file"
i=1
while [ "$i" -le 80 ]; do
  printf 'less-line-%03d: pager conformance fixture text\n' "$i" >>"$file"
  i=$((i + 1))
done

exec less -SR "$file"
