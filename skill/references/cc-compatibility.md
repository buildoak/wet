# Claude Code Compatibility — Diagnostic Reference

Latest known-stable versions (update when validated):

| Component | Version | Validated |
|-----------|---------|-----------|
| Claude Code | 2.1.78 | 2026-03-18 |
| wet | 0.1.5 | 2026-03-18 |

---

## Version Compatibility Matrix

| CC Version | wet ≥0.1.4 | wet ≥0.1.5 | Notes |
|------------|------------|------------|-------|
| ≤2.1.76 | ✅ Full | ✅ Full | Direct HTTP, SSE only |
| 2.1.77 | ⚠️ Conditional | ⚠️ Conditional | IPC routing when Claude.app running |
| 2.1.78 | ❌ Statusline blank | ✅ Full | Non-streaming pre-flight requests added |
| ≥2.1.79 | Unknown | Unknown | Run diagnostic below |

**Key:** ✅ = works, ⚠️ = works with workaround, ❌ = broken without upgrade.

---

## Known Breaking Changes

### CC 2.1.77 — Claude Desktop IPC Routing

**What changed:** When the Claude Desktop app (Claude.app) is running, CC routes all API calls through the desktop app's Unix socket IPC instead of making direct HTTP requests.

**Impact on wet:** `ANTHROPIC_BASE_URL` is completely bypassed. The wet proxy never sees traffic. Statusline shows `wet: ready` forever (zero requests).

**Detection:**
```bash
# Check if Claude.app is running
pgrep -f "Claude.app" >/dev/null && echo "RUNNING" || echo "NOT RUNNING"

# Check if wet is receiving traffic (after at least one prompt)
curl -s http://localhost:${WET_PORT:-10000}/_wet/status | jq '.session_requests'
# If 0 after a prompt was sent → traffic is being routed through IPC
```

**Resolution:** Quit Claude Desktop app before starting `wet claude`. The routing decision is made ONCE at CC process startup — if Claude.app is not running when `wet claude` starts, CC falls back to direct HTTP through the proxy. Opening Claude.app later mid-session is safe; existing transport is locked in.

**Permanent fix:** None yet. CC does not provide a flag to force direct HTTP when IPC is available.

### CC 2.1.78 — Non-Streaming Pre-Flight Requests

**What changed:** CC sends two API requests per user prompt:
1. A non-streaming pre-flight (no `stream` field, returns `application/json`)
2. The actual streaming request (`stream: true`, returns `text/event-stream`)

**Impact on wet <0.1.5:** The SSE interceptor only handles `text/event-stream` responses. The `application/json` pre-flight response carries token usage data but was ignored. Result: `latest_total_input_tokens` stays at 0, statusline shows no fill percentage.

**Impact on wet ≥0.1.5:** Fixed. JSON usage interceptor extracts `usage` from non-streaming responses. Both request types contribute to stats.

**Detection:**
```bash
# Check wet version
wet version

# Check if JSON interceptor is working (after at least one prompt)
curl -s http://localhost:${WET_PORT:-10000}/_wet/status | jq '{
  requests: .session_requests,
  input_tokens: .latest_total_input_tokens,
  context_window: .context_window
}'
# Healthy: requests ≥ 2, input_tokens > 0, context_window > 0
# Broken: requests = 1 or input_tokens = 0
```

**Resolution:** Upgrade wet to ≥0.1.5: `brew upgrade wet` or `go install github.com/buildoak/wet@latest`.

---

## Diagnostic Flowchart

Run this when wet statusline stops working after a CC update. Execute steps in order; stop at the first failure.

### Step 1 — Version Check

```bash
echo "CC: $(claude --version 2>/dev/null || echo 'not found')"
echo "wet: $(wet version 2>/dev/null || echo 'not found')"
```

Compare against the compatibility matrix above. If CC version is newer than the latest validated version, proceed to full diagnostic (Step 2+). If wet version is below the minimum for the CC version, upgrade wet first.

### Step 2 — Proxy Reachability

```bash
curl -s -o /dev/null -w "%{http_code}" http://localhost:${WET_PORT:-10000}/_wet/status
```

- **200** → Proxy running. Continue to Step 3.
- **Connection refused** → Proxy not running. Start with `wet claude`.
- **Other** → Unexpected. Check `~/.wet/wet.log` for errors.

### Step 3 — Traffic Flow

```bash
curl -s http://localhost:${WET_PORT:-10000}/_wet/status | jq '.session_requests'
```

Run this AFTER sending at least one prompt in the CC session.

- **≥ 2** → Traffic flowing (pre-flight + stream). Continue to Step 4.
- **1** → Only one request type intercepted. Check if JSON interceptor is present (wet ≥0.1.5).
- **0** → No traffic reaching proxy. Check Claude.app IPC routing (Step 3a).

#### Step 3a — IPC Routing Check

```bash
# Is Claude Desktop running?
pgrep -f "Claude.app" >/dev/null && echo "Claude.app: RUNNING (may capture IPC)" || echo "Claude.app: not running"

# Is the CC process using the proxy URL?
WET_PID=$(pgrep -f "wet claude" | head -1)
if [ -n "$WET_PID" ]; then
    # Check child claude process
    CC_PID=$(pgrep -P "$WET_PID" -f "claude" | head -1)
    if [ -n "$CC_PID" ]; then
        lsof -p "$CC_PID" -i TCP 2>/dev/null | grep -c "localhost:${WET_PORT:-10000}" || echo "0 TCP connections to proxy"
    fi
fi
```

If Claude.app is running and TCP connections to proxy = 0: quit Claude.app, restart `wet claude`.

### Step 4 — Usage Data Extraction

```bash
curl -s http://localhost:${WET_PORT:-10000}/_wet/status | jq '{
  input_tokens: .latest_total_input_tokens,
  context_window: .context_window,
  tokens_saved: .session_tokens_saved
}'
```

- **input_tokens > 0, context_window > 0** → Usage extraction working. Statusline should show fill%.
- **input_tokens = 0** → JSON/SSE interceptor not parsing usage. Check wet version (needs ≥0.1.5 for CC ≥2.1.78).
- **context_window = 0** → First request hasn't completed yet, or response format changed.

### Step 5 — Statusline Rendering

```bash
# Test statusline script directly
echo '{"model":{"display_name":"test"},"context_window":{"used_percentage":50,"context_window_size":200000}}' | WET_PORT=${WET_PORT:-10000} ~/.claude/statusline.sh
```

- If wet section appears → statusline script is fine; issue is upstream (Steps 1-4).
- If no wet section → check `~/.wet/stats-${WET_PORT}.json` exists and has data.
- If script errors → run `wet install-statusline` to repair.

### Step 6 — Response Format Verification

If all above pass but data is still wrong, the API response format may have changed:

```bash
# Check last response content types in wet log
grep -i "content-type" ~/.wet/wet.log | tail -5

# Verify stats file structure
jq 'keys' ~/.wet/stats-${WET_PORT:-10000}.json
```

Expected content types: `text/event-stream` (streaming) and `application/json` (pre-flight or non-streaming).

If a new content type appears (e.g., `application/x-ndjson`, `text/plain`), wet needs a new interceptor for that format. File an issue or add a handler.

---

## Post-Update Validation Procedure

Run this after any CC version update to confirm wet still works. Can be executed as a local script on Mac Mini.

```bash
#!/bin/bash
# wet + CC compatibility check
# Run after CC updates to validate proxy integration

set -euo pipefail

RED='\033[31m'
GREEN='\033[32m'
YELLOW='\033[33m'
RESET='\033[0m'

pass() { printf "${GREEN}✓ %s${RESET}\n" "$1"; }
warn() { printf "${YELLOW}⚠ %s${RESET}\n" "$1"; }
fail() { printf "${RED}✗ %s${RESET}\n" "$1"; }

echo "=== wet + Claude Code Compatibility Check ==="
echo ""

# 1. Version check
CC_VER=$(claude --version 2>/dev/null | head -1 || echo "not found")
WET_VER=$(wet version 2>/dev/null | head -1 || echo "not found")
echo "Claude Code: $CC_VER"
echo "wet:         $WET_VER"

KNOWN_CC="2.1.78"
if [[ "$CC_VER" == *"$KNOWN_CC"* ]]; then
    pass "CC version matches latest validated ($KNOWN_CC)"
else
    warn "CC version differs from latest validated ($KNOWN_CC) — run full diagnostic"
fi
echo ""

# 2. Claude.app check
if pgrep -f "Claude.app" >/dev/null 2>&1; then
    warn "Claude Desktop app is running — will capture IPC from new sessions"
else
    pass "Claude Desktop app not running"
fi

# 3. Proxy reachability
PORT=${WET_PORT:-10000}
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:${PORT}/_wet/status" 2>/dev/null || echo "000")
if [ "$HTTP_CODE" = "200" ]; then
    pass "wet proxy reachable on port $PORT"

    # 4. Traffic check
    REQS=$(curl -s "http://localhost:${PORT}/_wet/status" | jq -r '.session_requests // 0')
    INPUT=$(curl -s "http://localhost:${PORT}/_wet/status" | jq -r '.latest_total_input_tokens // 0')
    CTX=$(curl -s "http://localhost:${PORT}/_wet/status" | jq -r '.context_window // 0')

    if [ "$REQS" -gt 0 ]; then
        pass "Proxy receiving traffic (${REQS} requests)"
    else
        warn "No requests recorded — send a prompt first, then re-run"
    fi

    if [ "$INPUT" -gt 0 ]; then
        pass "Usage extraction working (input_tokens: ${INPUT})"
    else
        if [ "$REQS" -gt 0 ]; then
            fail "Usage extraction broken — input_tokens = 0 after ${REQS} requests"
        else
            warn "Usage not yet available — no requests recorded"
        fi
    fi

    if [ "$CTX" -gt 0 ]; then
        pass "Context window detected (${CTX})"
    else
        warn "Context window not yet reported"
    fi
else
    warn "wet proxy not reachable on port $PORT (HTTP $HTTP_CODE) — start with 'wet claude'"
fi
echo ""

# 5. Statusline check
if [ -f "$HOME/.claude/statusline.sh" ]; then
    if grep -q "WET_STATUSLINE" "$HOME/.claude/statusline.sh"; then
        pass "Statusline script has wet section"
    else
        warn "Statusline script exists but missing wet section — run 'wet install-statusline'"
    fi
else
    warn "No statusline script — run 'wet install-statusline'"
fi

# 6. Stats file check
STATS_FILE="$HOME/.wet/stats-${PORT}.json"
if [ -f "$STATS_FILE" ]; then
    AGE=$(($(date +%s) - $(stat -f %m "$STATS_FILE" 2>/dev/null || stat -c %Y "$STATS_FILE" 2>/dev/null || echo 0)))
    if [ "$AGE" -lt 300 ]; then
        pass "Stats file fresh (${AGE}s old)"
    else
        warn "Stats file stale (${AGE}s old)"
    fi
else
    warn "Stats file not found at $STATS_FILE"
fi

echo ""
echo "=== Done ==="
```

---

## When a New CC Version Appears

If CC updates beyond the latest validated version:

1. **Don't panic.** Most CC updates don't touch the API transport layer.
2. **Run the validation procedure** above after updating.
3. **If validation passes:** Update the "Latest known-stable versions" table at the top of this document. Bump the validated date.
4. **If validation fails:** Walk through the diagnostic flowchart. Common failure patterns:
   - **New response format** → wet needs a new interceptor (check Content-Type headers in `~/.wet/wet.log`)
   - **Changed IPC behavior** → check if Claude.app routing logic changed (process tree + TCP analysis)
   - **New request patterns** → check if request structure changed (stream field, new endpoints)
5. **Document the fix** in the "Known Breaking Changes" section above, update the compatibility matrix, bump versions.

### Updating This Document

When you validate a new CC version or fix a compatibility issue:

1. Update the version table at the top
2. Add any new breaking change to the "Known Breaking Changes" section
3. Update the compatibility matrix
4. Commit with message: `skill: update CC compatibility to vX.Y.Z`

This document is embedded in the wet binary and served as a skill reference. Changes require a wet release to propagate.
