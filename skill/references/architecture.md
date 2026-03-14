# wet Architecture Reference

Technical reference for the wet compression proxy. For subagents and skill consumers who need to understand how wet works under the hood.

---

## Overview

wet is a Go binary (~9MB static arm64) that acts as a transparent reverse proxy between Claude Code and the Anthropic API. It intercepts `POST /v1/messages`, compresses stale tool results in the messages array, and forwards the leaner request. Responses stream back unchanged via SSE pass-through.

```
Claude Code TUI → wet proxy (localhost:{port}) → api.anthropic.com
                       ↕
                 Control plane (/_wet/* HTTP endpoints)
                 Skill interacts via curl to these endpoints
```

**Environment variables set by `wet claude`:**
- `ANTHROPIC_BASE_URL=http://127.0.0.1:{port}` — redirects Claude Code to proxy
- `WET_PORT={port}` — for CLI/skill port discovery
- `WET_SESSION_UUID={uuid}` — session identity

---

## Two Compression Paths

| Path | When | How | Latency |
|------|------|-----|---------|
| **Tier 1 (Mechanical)** | Auto mode, or skill Phase 4 Step 1 | Regex-based extraction for 10 tool families. No external calls. | <5ms |
| **Tier 2 (LLM Rewrite)** | Skill Phase 4 Step 2 only | Sonnet subagent reads full content via `/_wet/inspect?full=1`, writes dense <150 token summary, POSTs as `replacement_text`. No side-channel API calls — uses session's own subagent budget. | Subagent cost |

**Tier 1 handles:** CLI output, git, test runners, build tools, directory listings.
**Tier 2 handles:** Agent/Task returns (natural language analysis), file reads (opted-in by user).
**The wet-compress skill IS Tier 2.** The binary provides Tier 1. The skill orchestrates the LLM rewrite path.

### Tier 1 Tool Families

| Family | Avg Compression | Strategy |
|--------|----------------|----------|
| git status | 88% | Branch + changed file list (cap 20) |
| git log | 93% | Commit hash + message (cap 15) |
| git diff | 90% | Diff headers + hunk markers + changed lines (cap 30) |
| pytest | 96% | Pass/fail counts + failed test names + error snippets |
| npm/yarn | 89% | ERR!/WARN lines + summary |
| cargo | 94% | Error/warning/finished lines + 5 compile lines |
| pip | 87% | "Successfully installed" + error lines |
| docker | 86% | Last 20 lines + error/warn/fatal |
| ls/find | 84% | First 30 lines + total count |
| make/cmake | 89% | Error lines + final status |
| read (generic) | ~60-80% | First 100 lines if >1000 tokens |
| bash (generic) | 32-88% | Head 15 + tail 10 + dedup similar lines |

### Real-World Compression Quality (from E2E tests)

| Type | Tier | Avg Compression | Notes |
|------|------|----------------|-------|
| Mechanical (CLI output) | Tier 1 | 47% | Good for progress bars, build logs |
| File reads (source code) | Tier 1 | 60-80% | First 100 lines; imports + structure preserved |
| File reads (source code) | Tier 2 (skill) | ~90% | Dense <150 token summary |
| Agent/Task returns | Tier 1 | 15-38% | Poor — dense text doesn't respond to truncation |
| Agent/Task returns | Tier 2 (skill) | ~80% | LLM rewrite preserving key findings |
| File reads (delete/"unread") | Skill | ~99% | Minimal tombstone |

---

## Control Plane Endpoints

Base URL: `http://localhost:$WET_PORT/_wet/`

| Endpoint | Method | Purpose | Input | Output |
|----------|--------|---------|-------|--------|
| `/status` | GET | Health, fill%, tokens saved, compression ratio | — | StatusJSON |
| `/inspect` | GET | List all tracked tool results | `?full=1` for content | `[InspectEntry]` |
| `/compress` | POST | Queue items for compression | `{ids: [...], replacement_text?: {...}}` | `{status: "queued", count, ids}` |
| `/pause` | POST | Disable compression | — | `{status: "paused"}` |
| `/resume` | POST | Re-enable compression | — | `{status: "resumed"}` |
| `/rules` | GET | Current per-tool rules | — | `{family: RuleConfig}` |
| `/rules` | POST | Update a rule | `{key, stale_after?, strategy?}` | `{status: "ok"}` |

**Compression is queue-based.** Items POSTed to `/compress` are applied on the NEXT API request, not immediately. This avoids mid-conversation mutations.

### Status Response

```json
{
  "uptime_seconds": 1234.5,
  "request_count": 78,
  "tokens_saved": 12484,
  "compression_ratio": 0.579,
  "items_compressed": 8,
  "items_total": 90,
  "api_input_tokens": 1,
  "api_output_tokens": 57,
  "context_window": 200000,
  "latest_input_tokens": 91581,
  "latest_total_input_tokens": 91581,
  "paused": false,
  "mode": "passthrough"
}
```

### Inspect Entry

```json
{
  "tool_use_id": "toolu_01...",
  "tool_name": "Bash",
  "command": "git status (first ~100 chars)",
  "file_path": "/path/to/file (Read/Edit/Write/Grep/Glob only)",
  "turn": 12,
  "current_turn": 45,
  "stale": true,
  "is_error": false,
  "has_images": false,
  "token_count": 847,
  "content_preview": "first 200 chars...",
  "content": "(only with ?full=1)"
}
```

### Compress Request

```json
{
  "ids": ["toolu_01...", "toolu_02..."],
  "replacement_text": {
    "toolu_01": "dense summary written by subagent",
    "toolu_02": "[deleted: file_read | removed by user]"
  }
}
```

- IDs without `replacement_text` entries → Tier 1 mechanical compression
- IDs with `replacement_text` → content replaced with provided text
- Response: `{status: "queued", count: N, ids: [...]}`

---

## Data Storage

### Runtime Stats: `~/.wet/stats-{port}.json`

Updated after every API request. Read by statusline script.

```json
{
  "timestamp": "2026-03-12T12:12:30Z",
  "session_tokens_saved": 12484,
  "session_items_compressed": 8,
  "session_items_total": 90,
  "session_compression_ratio": 0.579,
  "session_mode": "passthrough",
  "api_input_tokens": 1,
  "api_output_tokens": 57,
  "api_cache_creation_input_tokens": 247,
  "api_cache_read_input_tokens": 91333,
  "context_window": 200000,
  "latest_input_tokens": 1,
  "latest_total_input_tokens": 91581,
  "session_tokens_before": 21547,
  "session_tokens_after": 9063,
  "session_api_tokens_saved": 6200
}
```

### Session Log: `~/.wet/sessions/{uuid}/session.jsonl`

Append-only JSONL. One header line + one line per API turn.

**Header (line 0):**
```json
{"v": 1, "type": "header", "session": "uuid", "created": "RFC3339", "model": "claude-opus-4-6", "mode": "passthrough"}
```

**Turn record (lines 1+):**
```json
{
  "type": "turn",
  "turn": 45,
  "ts": "2026-03-12T12:04:50Z",
  "usage": {
    "input_tokens": 3,
    "output_tokens": 270,
    "cache_read_input_tokens": 20824,
    "cache_creation_input_tokens": 74882
  },
  "total_context": 95709,
  "chars_saved": 3633,
  "tokens_saved_est": 1100,
  "items": [
    {
      "id": "toolu_...",
      "tool": "Read",
      "cmd": "",
      "orig_chars": 2206,
      "tomb_chars": 1842,
      "chars_saved": 364,
      "tombstone": "[compressed: ...]",
      "preview": "first 200 chars of original..."
    }
  ]
}
```

Token counts in `usage` are **exact values from the Anthropic API response** — captured via SSE interceptor parsing `message_start` and `message_delta` events. No estimation for ground truth.

Token estimation (`chars / 3.3`, calibrated) is used only for pre-request savings estimates. Post-request savings use API-observed delta (`PrevTotalContext - CurrentTotalContext`).

### Originals

Original tool result content is **preserved** in Claude Code's conversation JSONL (`~/.claude/projects/*/sessions/*.jsonl`). wet only modifies the API stream in-flight. The CC JSONL is append-only and never touched by wet. Originals are always recoverable.

---

## Statusline

Rendered from `~/.wet/stats-{port}.json` by `~/.claude/statusline.sh`.

| State | Format |
|-------|--------|
| No data | `wet: ready` |
| With compression data | `wet: 46% (92k/200k) \| 8/90 compressed (21.5k->9.1k)` |
| API-observed savings | `wet: 46% (92k/200k) \| saved 6.2k tokens` |
| Proxy not running | `wet: sleeping` |

Context fill % uses **exact API data** (`latest_total_input_tokens / context_window`). Compression savings prefer API-observed delta (`session_api_tokens_saved`), fall back to calibrated estimate (`session_tokens_saved`).

---

## Autocompact Interaction

wet **prevents** Claude Code's autocompact (~83.5% trigger) by reducing the API-visible token count. Claude Code reads context fill from `usage.input_tokens` in the API response. Since wet compresses before the API call, the API reports fewer tokens → Claude Code sees lower fill → autocompact threshold is naturally pushed further away.

This is a core design property, not a side effect. wet's goal is to prevent premature autocompact by keeping API-visible context lean.

---

## CLI Commands

| Command | Purpose |
|---------|---------|
| `wet claude [args]` | Start proxy + claude (primary UX) |
| `wet claude --resume UUID` | Resume session through proxy (stats restored from session.jsonl) |
| `wet status` | Live proxy stats (JSON) |
| `wet data status` | Offline session stats from session.jsonl |
| `wet data inspect [--all]` | Offline compressed items review |
| `wet data diff <turn>` | Per-turn compression detail |
| `wet session salt` | Generate salt for session identification |
| `wet session find <SALT>` | Find CC JSONL by salt |
| `wet session profile --jsonl PATH` | Context composition analysis |
| `wet pause / resume` | Toggle compression at runtime |
| `wet rules list / set KEY VALUE` | Runtime rule tuning |
| `wet install-statusline` | Add wet to Claude Code status bar |

---

## Configuration: `wet.toml`

Located at `./wet.toml` or `~/.wet/wet.toml`.

```toml
[server]
mode = "passthrough"    # "passthrough" (manual via skill) or "auto" (compress on every request)

[staleness]
threshold = 2           # turns before item is stale (default)

[compression.tier1]
enabled = true

[compression.tier2]
enabled = false         # LLM-based compression (disabled — skill handles this)

[bypass]
preserve_errors = true
min_tokens = 100

[rules."git"]
stale_after = 1         # git stales fast

[rules."Read"]
stale_after = 3         # file reads stay relevant longer
```
