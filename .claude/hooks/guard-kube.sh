#!/usr/bin/env bash
# PreToolUse hook (Bash|PowerShell): allow kubectl/helm only against k3d-*
# clusters, so an agent can never touch a real cluster from this repo.
#
# Rules, in order:
#   1. Commands that don't run kubectl/helm pass.
#   2. Every context named in the command (--context, --kube-context,
#      config use-context) must start with k3d-.
#   3. With no context named, the current kubeconfig context must be k3d-*.
#   4. Anything that can't be determined is blocked (fail closed).
#
# A seatbelt, not a security boundary: e.g. KUBECONFIG=other.yaml or a
# script that calls kubectl internally isn't seen. Exit 2 blocks the tool
# call and shows stderr to Claude.
set -u
. "$(dirname "$0")/lib.sh"

block() {
  printf 'Blocked by .claude/hooks/guard-kube.sh: %s\nOnly k3d-* contexts are allowed. Use --context k3d-meshtwin (created by `make cluster`).\n' "$1" >&2
  exit 2
}

input=$(cat)
cmd=$(printf '%s' "$input" | json_field tool_input.command) || block "could not parse the hook input"

# kubectl/helm as a command word: at the start, or after ; & | ( ` $( , optionally after VAR=value prefixes.
pattern='(^|[;&|(`]|\$\()[[:space:]]*([A-Za-z_][A-Za-z0-9_]*=[^[:space:]]*[[:space:]]+)*(sudo[[:space:]]+)?(kubectl|helm)(\.exe)?([[:space:]]|$)'
printf '%s\n' "$cmd" | grep -Eq "$pattern" || exit 0

named=$(printf '%s\n' "$cmd" |
  grep -Eo -- '(--kube-context|--context)(=|[[:space:]]+)[^[:space:];&|)]+|use-context[[:space:]]+[^[:space:];&|)]+' |
  sed -E 's/^(--kube-context|--context|use-context)(=|[[:space:]]+)//')
if [ -n "$named" ]; then
  for ctx in $named; do
    case "$ctx" in k3d-*) ;; *) block "context '$ctx' is not a k3d cluster" ;; esac
  done
  exit 0
fi

current=$(kubectl config current-context 2>/dev/null) || block "no current kube context is set"
case "$current" in
  k3d-*) exit 0 ;;
  *) block "current context '$current' is not a k3d cluster" ;;
esac
