#!/usr/bin/env bash
# wet-ctl.sh — thin wrapper for wet HTTP control plane
# Usage: wet-ctl.sh status | inspect | compress id1,id2,id3
#
# All intelligence lives in the subagent prompt (SKILL.md).
# This script only validates inputs and calls HTTP endpoints.
# Error output is always JSON to stderr.

set -euo pipefail

PORT="${WET_PORT:-}"
if [[ -z "$PORT" ]]; then
  echo '{"error": "WET_PORT not set. Export WET_PORT or set it to 8100 (default).", "code": "NO_PORT"}' >&2
  exit 10
fi

BASE="http://127.0.0.1:${PORT}/_wet"

# Helper: run curl, preserve proxy error body on failure.
# On connect failure, emit a generic UNREACHABLE error.
_call() {
  local RESP HTTP_CODE
  RESP=$(curl -s -w '\n%{http_code}' "$@") || {
    echo '{"error": "wet not reachable on port '"$PORT"'", "code": "UNREACHABLE"}' >&2
    return 1
  }
  HTTP_CODE=$(echo "$RESP" | tail -1)
  RESP=$(echo "$RESP" | sed '$d')
  if [[ "$HTTP_CODE" -ge 400 ]]; then
    # Proxy returned a structured error -- pass it through
    echo "$RESP" >&2
    return 1
  fi
  echo "$RESP"
}

case "${1:-}" in
  status)
    _call "$BASE/status" || exit 11
    ;;

  inspect)
    _call "$BASE/inspect" || exit 11
    ;;

  compress)
    [[ -n "${2:-}" ]] || {
      echo '{"error": "no IDs provided. Usage: wet-ctl.sh compress id1,id2,id3", "code": "NO_IDS"}' >&2
      exit 12
    }
    IFS=',' read -ra IDS <<< "$2"
    JSON_IDS=$(printf '"%s",' "${IDS[@]}")
    JSON_IDS="[${JSON_IDS%,}]"
    _call -X POST "$BASE/compress" \
      -H 'Content-Type: application/json' \
      -d "{\"ids\": $JSON_IDS}" || exit 13
    ;;

  *)
    echo "Usage: wet-ctl.sh {status|inspect|compress <id1,id2,...>}" >&2
    exit 1
    ;;
esac
