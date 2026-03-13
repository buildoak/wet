---
name: wet-compress
description: Context compression for Claude Code sessions via wet proxy
author: R. Jenkins
version: 0.5.0
tools: [Bash, Agent]
triggers:
  - context heavy, compress context, wet compress
  - token pressure, trim context, context management
  - tool result cleanup, context too large, reclaim tokens
  - stale results, compress tool results, agent returns
  - context profile, context health, how full is context
---

# wet-compress

Compress stale tool results through wet's HTTP control plane. Five phases, strict order. Heavy work runs in subagents — the main session stays lean.

---

## How wet Works (Technical Reference)

wet is a Go binary that acts as a transparent reverse proxy between Claude Code and the Anthropic API. It intercepts `POST /v1/messages`, compresses stale tool results in the messages array, and forwards the leaner request. Responses stream back unchanged via SSE pass-through.

### Architecture

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

### Two Compression Paths

| Path | When | How | Latency |
|------|------|-----|---------|
| **Tier 1 (Mechanical)** | Auto mode, or skill Step 1 | Regex-based extraction for 10 tool families (git, pytest, cargo, npm, pip, docker, ls, make, read, generic). No external calls. | <5ms |
| **Tier 2 (LLM Rewrite)** | Skill Step 2 only | Sonnet subagent reads full content via `/_wet/inspect?full=1`, writes dense <150 token summary, POSTs as `replacement_text`. No side-channel API calls — uses session's own subagent budget. | Subagent cost |

**Tier 1 handles:** CLI output, git, test runners, build tools, directory listings.
**Tier 2 handles:** Agent/Task returns (natural language analysis), file reads (opted-in by user).
**This skill IS Tier 2.** The binary provides Tier 1. The skill orchestrates the LLM rewrite path.

### Data Storage

**Runtime stats:** `~/.wet/stats-{port}.json` — updated after every API request.
```json
{
  "session_tokens_saved": 12484,
  "session_items_compressed": 8,
  "session_items_total": 90,
  "session_compression_ratio": 0.579,
  "api_input_tokens": 1,
  "api_output_tokens": 57,
  "api_cache_creation_input_tokens": 247,
  "api_cache_read_input_tokens": 91333,
  "context_window": 200000,
  "latest_total_input_tokens": 91581,
  "session_api_tokens_saved": 6200
}
```

**Session log:** `~/.wet/sessions/{uuid}/session.jsonl` — append-only, one line per API turn.

Every turn records **exact token counts from the Anthropic API response** (via SSE interceptor parsing `message_start` and `message_delta` events). No estimation for ground truth.

Turn record structure:
```json
{
  "type": "turn", "turn": 45,
  "ts": "2026-03-12T12:04:50Z",
  "usage": {
    "input_tokens": 3, "output_tokens": 270,
    "cache_read_input_tokens": 20824,
    "cache_creation_input_tokens": 74882
  },
  "total_context": 95709,
  "chars_saved": 3633,
  "tokens_saved_est": 1100,
  "items": [
    { "id": "toolu_...", "tool": "Read", "cmd": "",
      "orig_chars": 2206, "tomb_chars": 1842, "chars_saved": 364,
      "tombstone": "[compressed: ...]", "preview": "first 200 chars..." }
  ]
}
```

**Token estimation:** `chars / 3.3` (calibrated). Used for pre-request savings estimates only. Post-request savings use API-observed delta (`PrevTotalContext - CurrentTotalContext`).

**Originals are preserved** in Claude Code's conversation JSONL (`~/.claude/projects/*/sessions/*.jsonl`). wet only modifies the API stream in-flight. The CC JSONL is append-only and never touched.

### Statusline

Rendered from `~/.wet/stats-{port}.json`. Format:

- No data: `wet: ready`
- With data: `wet: 46% (92k/200k) | 8/90 compressed (21.5k->9.1k)`
- API-observed savings (when available): `wet: 46% (92k/200k) | saved 6.2k tokens`

Context fill % and total input use **exact API data**. Compression savings prefer API-observed delta, fall back to calibrated estimate.

### Autocompact Interaction

wet **prevents** Claude Code's autocompact (~83.5% trigger) by reducing the API-visible token count. Since Claude Code reads context fill from `usage.input_tokens` in the API response, and wet compresses before the API call, autocompact threshold is naturally pushed further away. This is a core design property, not a side effect.

### Control Plane Endpoints

| Endpoint | Method | Purpose | Returns |
|----------|--------|---------|---------|
| `/_wet/status` | GET | Health, fill%, tokens saved, compression ratio | JSON (see stats structure above) |
| `/_wet/inspect` | GET | List all tracked tool results with metadata | `[{tool_use_id, tool_name, command, file_path, turn, current_turn, stale, is_error, has_images, token_count, content_preview}]` |
| `/_wet/inspect?full=1` | GET | Same + full content of each tool result | Adds `content` field to each entry |
| `/_wet/compress` | POST | Queue items for compression | `{"ids": [...], "replacement_text": {"id": "text"}}` → `{status: "queued", count, ids}` |
| `/_wet/pause` | POST | Disable compression | `{status: "paused"}` |
| `/_wet/resume` | POST | Re-enable compression | `{status: "resumed"}` |
| `/_wet/rules` | GET | Current per-tool rules | `{tool_family: {stale_after, strategy}}` |
| `/_wet/rules` | POST | Update a rule | `{key, stale_after?, strategy?}` |

**Compression is queue-based.** Items POSTed to `/_wet/compress` are applied on the NEXT API request, not immediately. This avoids mid-conversation mutations.

### CLI Commands (for reference, not used by skill)

| Command | Purpose |
|---------|---------|
| `wet claude [args]` | Start proxy + claude (primary UX) |
| `wet claude --resume UUID --dangerously-skip-permissions` | Resume session through proxy |
| `wet status` | Live proxy stats |
| `wet data status` | Offline session stats from session.jsonl |
| `wet data inspect [--all]` | Offline compressed items review |
| `wet data diff <turn>` | Per-turn compression detail |
| `wet session profile --jsonl PATH` | Context composition analysis |
| `wet pause / resume` | Toggle compression |
| `wet rules list / set KEY VALUE` | Runtime rule tuning |

---

## Port Detection

Run this ONCE at the start. Everything below uses `$WET_PORT`.

```bash
WET_PORT="${WET_PORT:-$(lsof -i -P -n 2>/dev/null | grep wet | grep LISTEN | awk '{print $9}' | cut -d: -f2 | sort -n | tail -1)}"
```

If `WET_PORT` is empty after this: wet is not running. Tell the user and STOP.

---

## Phase 1 — Health Check (main session, 1 curl)

```bash
curl -s "http://localhost:$WET_PORT/_wet/status"
```

Report: mode, fill%, request_count, tokens_saved, api_tokens_saved (if available).

- **fill < 30%** → "Context is healthy. Compression not needed." **STOP.**
- **30-60%** → "Context growing. Compression available."
- **60-80%** → "Context heavy. Compression recommended."
- **> 80%** → "Context critical. Compression strongly recommended."

---

## Phase 2 — Profile (Sonnet 4.6 subagent)

Spawn a subagent with this EXACT prompt (fill in `«WET_PORT»`):

> You are a context profiler for the wet compression proxy.
>
> **Task:** Call `GET http://localhost:«WET_PORT»/_wet/inspect` and classify every tool result.
>
> **Classification rules:**
> - **MECHANICAL** — tool_name matches CLI tools (Bash, git, npm, cargo, pytest, ls, cat, grep, find, make, docker, etc.) OR content is clearly command output. These compress deterministically — no replacement_text needed. wet's Tier 1 handles these with <5ms latency.
> - **AGENT_RETURN** — tool_name contains "Task" or "Agent" or "subagent", OR content is natural-language analysis/summary (not CLI output). These need LLM rewrite — you will write dense summaries in Phase 4.
> - **FILE_READ** — tool_name is "Read" (excluding boot reads). List EACH file individually with its full path and token count. Mark as OPT_IN. Do NOT include in compressible totals.
> - **BOOT_READ** — any Read of SOUL.md, IDENTITY.md, USER.md, or MEMORY.md. Mark as **NEVER_COMPRESS**. These are SACRED. NEVER touch them.
> - **PROTECTED** — error results (`is_error == true`), last 3 turns (`current_turn - turn <= 3`), image blocks (`has_images == true`), already-compressed tombstones (content contains `[compressed`), items under 50 tokens.
>
> **Output this exact structured data and nothing else:**
>
> ```
> TOTALS
> total_tokens: N
> fill_pct: N%
> context_window: N
>
> MECHANICAL: count=N tokens=N ids=id1,id2,...
> AGENT_RETURN: count=N tokens=N ids=id5,id7,...
> BOOT_READ: count=N tokens=N ids=—
> PROTECTED: count=N tokens=N ids=—
>
> FILE_READS
> id=«id» path=/full/path/to/file.ext tokens=N
> id=«id» path=/full/path/to/other.ts tokens=N
> (one line per file read, empty section if none)
>
> ESTIMATES
> mechanical_savings: N (x0.9)
> agent_savings: N (x0.8)
> total_est_savings: N
> ```
>
> Keep your response under 50 lines. No prose. Structured data only.

---

## Phase 3 — APPROVAL GATE (main session)

Using the profiler's structured data, construct and present this ASCII plan to the user. Fill in all `«»` placeholders from the profiler output.

**ASCII Template:**

```
┌─────────────────────────────────────────────────┐
│  WET CONTEXT PROFILE                            │
│  Session: «fill_pct»% full («used_k»k / «window_k»k)  │
│  Tool results: «total_count» total, «total_tokens»k tokens  │
├─────────────────────────────────────────────────┤
│                                                 │
│  «bar_agent»  AGENT RETURNS  «agent_k»k tk      │
│  «bar_boot»   BOOT READS     «boot_k»k tk       │
│  «bar_mech»   BASH/COMMANDS  «mech_k»k tk       │
│  «bar_file»   FILE READS     «file_k»k tk       │
│  «bar_prot»   PROTECTED      «prot_k»k tk       │
│                                                 │
├─────────────────────────────────────────────────┤
│  COMPRESSION PLAN                               │
│                                                 │
│  Mechanical (Tier 1):  «mech_count» items  «mech_k»k → ~«mech_save»k  │
│  LLM Rewrite:         «agent_count» items  «agent_k»k → ~«agent_save»k │
│  ─────────────────────────────────              │
│  Est. savings:         ~«total_save»k tokens (~«save_pct»%)  │
│                                                 │
│  File Reads (opt-in):                           │
│   «file_path_1»  «file_tokens_1»k tk  [keep]   │
│   «file_path_2»  «file_tokens_2»k tk  [keep]   │
│   (repeat per file)                             │
│                                                 │
│  Boot Reads: SACRED — never touched             │
├─────────────────────────────────────────────────┤
│  Approve? [y / n / edit]                        │
│  File reads: [keep / summarize / delete] each   │
└─────────────────────────────────────────────────┘
```

**Bar chart generation:** Each bar is 20 chars wide. Scale each category relative to the largest category token count. Use `█` for filled, `░` for empty. Example: largest=13.3k at 20 chars, 5.8k = round(5.8/13.3 * 20) = 9 chars → `█████████░░░░░░░░░░░`.

**File reads block:** List every FILE_READ from profiler output as a separate line. Default action shown is `[keep]`. User can change each to `[summarize]` or `[delete]`.

### ██████████████████████████████████████████████████████████████
### ██  STOP HERE. WAIT FOR THE USER TO RESPOND.              ██
### ██  DO NOT PROCEED. DO NOT AUTO-APPROVE.                  ██
### ██  DO NOT ASSUME APPROVAL. THE USER MUST SAY YES.        ██
### ██  THE COORDINATOR NEVER APPROVES ON THE USER'S BEHALF.  ██
### ██████████████████████████████████████████████████████████████

- User says **no** → Done. Stop.
- User says **edit** → Adjust the ID lists per their instructions. Re-present.
- User says **yes** / **go** / **approved** / **do it** / **cut it** → Proceed to Phase 4 with no file read changes.
- User specifies per-file actions (`summarize /path/to/file`, `delete /path/to/file`) → Update those file entries, re-present for final approval.
- User says **include files** → Move all FILE_READ IDs to the summarize list. Re-present for approval.

**Delete action:** If user marks a file read for deletion, it does NOT get summarized — it gets a minimal tombstone: `[deleted: file_read | removed by user]`. This is the "unread" operation. Pass its ID to the compressor with `action=delete`. See Phase 4.

---

## Phase 4 — Compress (Sonnet 4.6 subagent)

Spawn a subagent with this EXACT prompt (fill in `«WET_PORT»`, `«MECHANICAL_IDS»`, `«REWRITE_IDS»`, `«SUMMARIZE_FILE_IDS»`, `«DELETE_FILE_IDS»`):

> You are a context compressor for the wet proxy.
>
> **WET endpoint:** `http://localhost:«WET_PORT»`
>
> **Step 1 — Mechanical compression:**
> POST these IDs with no replacement_text. Wet's Tier 1 handles them deterministically (<5ms, regex-based, 91% avg compression on 10 tool families).
> ```bash
> curl -s -X POST "http://localhost:«WET_PORT»/_wet/compress" \
>   -H 'Content-Type: application/json' \
>   -d '{"ids": [«MECHANICAL_IDS»]}'
> ```
>
> **Step 2 — LLM rewrite (agent returns + approved-for-summarize file reads):**
> IDs to rewrite: «REWRITE_IDS» «SUMMARIZE_FILE_IDS»
>
> For EACH id: fetch full content via `GET http://localhost:«WET_PORT»/_wet/inspect?full=1`, find the matching entry, then write a dense summary (under 150 tokens) preserving: key findings, decisions, file paths, metrics, open threads. This is the skill's core value — you are Tier 2.
>
> POST all at once:
> ```bash
> curl -s -X POST "http://localhost:«WET_PORT»/_wet/compress" \
>   -H 'Content-Type: application/json' \
>   -d '{"ids": ["id5","id7"], "replacement_text": {"id5": "your summary...", "id7": "your summary..."}}'
> ```
>
> **Step 3 — Delete file reads (user-marked for deletion):**
> IDs to delete: «DELETE_FILE_IDS»
>
> These were file reads the user wants fully removed from context ("unread"). Replace with a minimal tombstone — no content, no summary.
>
> POST each with a tombstone replacement_text:
> ```bash
> curl -s -X POST "http://localhost:«WET_PORT»/_wet/compress" \
>   -H 'Content-Type: application/json' \
>   -d '{"ids": [«DELETE_FILE_IDS»], "replacement_text": {"«id»": "[deleted: file_read | removed by user]"}}'
> ```
>
> **Step 4 — Report back (under 80 tokens):**
> "Compressed N mechanical + M rewritten + D deleted. Queued for next request."
> Do NOT return full summaries. They went to wet via HTTP. Keep your return brief.

**Note:** Compressions are queued and applied on the NEXT API request. The savings won't appear in `/_wet/status` until after the next message exchange.

---

## Phase 5 — Verify (main session, 1 curl)

```bash
curl -s "http://localhost:$WET_PORT/_wet/status"
```

Report: new fill%, tokens_saved, items_compressed, api_tokens_saved. Compare to Phase 1 numbers.

If `session_api_tokens_saved` is available, report it as the authoritative savings number (derived from API response delta). Otherwise report `session_tokens_saved` (calibrated estimate).

---

## Rules — DO NOT violate

1. **NEVER compress boot reads** — SOUL.md, IDENTITY.md, USER.md, MEMORY.md. Not "protected". **NEVER.**
2. **NEVER compress file reads without explicit user opt-in** at the approval gate.
3. **NEVER compress error results** — they are diagnostic gold.
4. **NEVER compress results from the last 3 turns.**
5. **NEVER auto-approve.** The coordinator does not approve on the user's behalf. Phase 3 waits.
6. **NEVER let subagent returns bloat the main session.** Profiler: structured data only. Compressor: one-line brief only.
7. **NEVER call compress with an empty ID list.**
8. **NEVER retry failed compress calls.** Report the error and stop.
9. **NEVER modify wet rules or pause/resume state.** This skill is read + compress only.
10. **NEVER summarize a deleted file read.** Delete = tombstone only. Summarize = LLM rewrite. These are different.
