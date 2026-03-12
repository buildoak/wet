#!/usr/bin/env bash
# identify-session.sh — find this session's JSONL via salt fingerprint
#
# Usage: identify-session.sh <SALT> [PROJECT_DIR]
#   SALT         The salt string echoed earlier in this session
#   PROJECT_DIR  Directory to search (default: ~/.claude/projects)
#
# Prints the matching JSONL path to stdout.
# Must be called AFTER echoing the salt via a separate Bash tool call,
# so it is already written into the session's JSONL.

set -euo pipefail

SALT="${1:?Usage: identify-session.sh <SALT> [PROJECT_DIR]}"
PROJECT_DIR="${2:-$HOME/.claude/projects}"

# grep all JSONL files for the salt
MATCH=$(grep -rl "$SALT" "$PROJECT_DIR" --include="*.jsonl" 2>/dev/null | head -1)

if [[ -n "$MATCH" ]]; then
    echo "$MATCH"
else
    echo "ERROR: No JSONL found containing salt $SALT" >&2
    exit 1
fi
