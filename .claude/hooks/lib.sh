#!/usr/bin/env bash
# Shared helpers for MeshTwin hooks. Sourced, not run.

# py: run a working Python. Tries python3 then python, and checks each one
# actually runs: on Windows "python3" is often a Microsoft Store stub that
# only prints an install hint.
py() {
  for c in python3 python; do
    if command -v "$c" >/dev/null 2>&1 && "$c" -c "" >/dev/null 2>&1; then
      "$c" "$@"
      return
    fi
  done
  return 127
}

# json_field PATH: read the hook's JSON from stdin and print one field,
# e.g. json_field tool_input.file_path. Prints nothing if it's missing.
json_field() {
  py -c '
import json, sys
d = json.load(sys.stdin)
for k in sys.argv[1].split("."):
    d = d.get(k, {}) if isinstance(d, dict) else {}
print(d if isinstance(d, str) else "")
' "$1"
}
