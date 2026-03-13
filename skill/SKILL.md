---
name: wet-compress
description: Context compression for Claude Code sessions via wet proxy
author: R. Jenkins
version: 0.6.0
tools: [Bash, Agent]
triggers:
  - context heavy, compress context, wet compress
  - token pressure, trim context, context management
  - tool result cleanup, context too large, reclaim tokens
  - stale results, compress tool results, agent returns
  - context profile, context health, how full is context
references:
  - references/architecture.md
  - references/heuristics.md
---

# wet-compress

Compress stale tool results through wet's HTTP control plane. Five phases, strict order. Heavy work runs in subagents — the main session stays lean.

**This skill IS Tier 2.** The binary handles Tier 1 (mechanical, regex, <5ms). The skill orchestrates LLM rewrites for agent returns and file reads — the high-value compressions that Tier 1 can't touch.

See `references/architecture.md` for how wet works. See `references/heuristics.md` for classification rules.

---

## Port Detection

Run ONCE at the start. Everything below uses `$WET_PORT`.

```bash
WET_PORT="${WET_PORT:-$(lsof -i -P -n 2>/dev/null | grep wet | grep LISTEN | awk '{print $9}' | cut -d: -f2 | sort -n | tail -1)}"
```

If empty: wet is not running. Tell the user and STOP.

---

## Phase 1 — Health Check (main session, 1 curl)

```bash
curl -s "http://localhost:$WET_PORT/_wet/status"
```

Report: mode, fill%, request_count, tokens_saved.

- **< 30%** → "Context healthy. Compression not needed." **STOP.**
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
> - **MECHANICAL** — tool_name matches CLI tools (Bash, git, npm, cargo, pytest, ls, cat, grep, find, make, docker, etc.) OR content is clearly command output. No replacement_text needed.
> - **AGENT_RETURN** — tool_name contains "Task" or "Agent" or "subagent", OR content is natural-language analysis/summary. Needs LLM rewrite.
> - **FILE_READ** — tool_name is "Read" (excluding boot reads). List EACH file with path and token count. Mark OPT_IN.
> - **BOOT_READ** — Read of SOUL.md, IDENTITY.md, USER.md, or MEMORY.md. **NEVER_COMPRESS.** SACRED.
> - **PROTECTED** — `is_error == true`, `current_turn - turn <= 3`, `has_images == true`, content contains `[compressed`, token_count < 50.
>
> **Output this exact structured data only:**
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
> id=«id» path=/full/path tokens=N
> (one line per file, empty if none)
>
> ESTIMATES
> mechanical_savings: N (x0.9)
> agent_savings: N (x0.8)
> total_est_savings: N
> ```
>
> Under 50 lines. No prose.

---

## Phase 3 — APPROVAL GATE (main session)

Present this ASCII plan (fill `«»` from profiler output):

```
┌─────────────────────────────────────────────────┐
│  WET CONTEXT PROFILE                            │
│  «fill_pct»% full («used_k»k / «window_k»k)    │
│  «total_count» tool results, «total_tokens»k tk │
├─────────────────────────────────────────────────┤
│  «bar_agent»  AGENT RETURNS  «agent_k»k tk      │
│  «bar_boot»   BOOT READS     «boot_k»k tk       │
│  «bar_mech»   BASH/COMMANDS  «mech_k»k tk       │
│  «bar_file»   FILE READS     «file_k»k tk       │
│  «bar_prot»   PROTECTED      «prot_k»k tk       │
├─────────────────────────────────────────────────┤
│  PLAN                                           │
│  Mechanical (Tier 1): «mech_count»  «mech_k»k → ~«mech_save»k │
│  LLM Rewrite:         «agent_count» «agent_k»k → ~«agent_save»k │
│  Est. savings:        ~«total_save»k (~«save_pct»%)             │
│                                                 │
│  File Reads (opt-in):                           │
│   «file_path»  «tokens»k tk  [keep]            │
│  Boot Reads: SACRED — never touched             │
├─────────────────────────────────────────────────┤
│  Approve? [y / n / edit]                        │
│  File reads: [keep / summarize / delete] each   │
└─────────────────────────────────────────────────┘
```

Bars: 20 chars wide, `█`/`░`, scaled to largest category.

### ██████████████████████████████████████████████████████████████
### ██  STOP. WAIT FOR USER. DO NOT AUTO-APPROVE.             ██
### ██████████████████████████████████████████████████████████████

- **no** → Stop.
- **edit** → Adjust lists, re-present.
- **yes / go / do it** → Phase 4 with no file changes.
- **summarize /path** or **delete /path** → Update file entries, re-present.
- **include files** → Move all FILE_READs to summarize list, re-present.

Delete = "unread": minimal tombstone `[deleted: file_read | removed by user]`, no summary.

---

## Phase 4 — Compress (Sonnet 4.6 subagent)

Spawn a subagent (fill in `«WET_PORT»`, `«MECHANICAL_IDS»`, `«REWRITE_IDS»`, `«SUMMARIZE_FILE_IDS»`, `«DELETE_FILE_IDS»`):

> You are a context compressor for the wet proxy.
>
> **Endpoint:** `http://localhost:«WET_PORT»`
>
> **Step 1 — Mechanical:** POST IDs, no replacement_text. Tier 1 handles them.
> ```bash
> curl -s -X POST "http://localhost:«WET_PORT»/_wet/compress" \
>   -H 'Content-Type: application/json' \
>   -d '{"ids": [«MECHANICAL_IDS»]}'
> ```
>
> **Step 2 — LLM rewrite:** IDs: «REWRITE_IDS» «SUMMARIZE_FILE_IDS»
> For EACH: fetch via `GET /_wet/inspect?full=1`, write dense summary (<150 tokens) preserving key findings, decisions, paths, metrics. POST all:
> ```bash
> curl -s -X POST "http://localhost:«WET_PORT»/_wet/compress" \
>   -H 'Content-Type: application/json' \
>   -d '{"ids": ["id5","id7"], "replacement_text": {"id5": "summary...", "id7": "summary..."}}'
> ```
>
> **Step 3 — Delete:** IDs: «DELETE_FILE_IDS»
> ```bash
> curl -s -X POST "http://localhost:«WET_PORT»/_wet/compress" \
>   -H 'Content-Type: application/json' \
>   -d '{"ids": [«DELETE_FILE_IDS»], "replacement_text": {"«id»": "[deleted: file_read | removed by user]"}}'
> ```
>
> **Step 4 — Report (under 80 tokens):**
> "Compressed N mechanical + M rewritten + D deleted. Queued for next request."

Compressions are queued — applied on the NEXT API request.

---

## Phase 5 — Verify (main session, 1 curl)

```bash
curl -s "http://localhost:$WET_PORT/_wet/status"
```

Report: new fill%, tokens_saved, items_compressed. Compare to Phase 1. Prefer `session_api_tokens_saved` (exact) over `session_tokens_saved` (estimate).

---

## Rules — DO NOT violate

1. **NEVER** compress boot reads (SOUL/IDENTITY/USER/MEMORY).
2. **NEVER** compress file reads without explicit user opt-in.
3. **NEVER** compress error results.
4. **NEVER** compress last 3 turns.
5. **NEVER** auto-approve. Phase 3 waits for the user.
6. **NEVER** let subagent returns bloat main session. Structured data only.
7. **NEVER** call compress with empty ID list.
8. **NEVER** retry failed compress calls.
9. **NEVER** modify rules or pause/resume state. Read + compress only.
10. **NEVER** summarize a deleted file read. Delete = tombstone only.
