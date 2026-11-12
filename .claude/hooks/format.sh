#!/usr/bin/env bash
# PostToolUse hook (Edit|Write|MultiEdit): format the file that was just
# written. gofmt for Go, ruff format for Python.
#
# Exit 0 = fine (or nothing to do). Exit 2 = formatter failed, which almost
# always means a syntax error; stderr goes back to Claude so it fixes it now
# instead of discovering it at test time.
set -u
. "$(dirname "$0")/lib.sh"

file=$(json_field tool_input.file_path) || exit 0 # no Python: skip rather than block edits
[ -n "$file" ] && [ -f "$file" ] || exit 0

case "$file" in
  *.go)
    if ! out=$(gofmt -w "$file" 2>&1); then
      printf 'gofmt failed on %s (syntax error?):\n%s\n' "$file" "$out" >&2
      exit 2
    fi
    ;;
  *.py)
    proj="${CLAUDE_PROJECT_DIR:-.}/analytics"
    if command -v ruff >/dev/null 2>&1; then
      out=$(ruff format -q "$file" 2>&1)
    elif command -v uv >/dev/null 2>&1 && [ -f "$proj/pyproject.toml" ]; then
      out=$(uv run --quiet --project "$proj" ruff format -q "$file" 2>&1) # the project's pinned ruff
    else
      exit 0 # no formatter available; CI's ruff check still catches it
    fi
    if [ $? -ne 0 ]; then
      printf 'ruff format failed on %s (syntax error?):\n%s\n' "$file" "$out" >&2
      exit 2
    fi
    ;;
esac
exit 0
