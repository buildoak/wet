---
name: wet-compress
description: Context compression for Claude Code sessions via wet proxy
author: R. Jenkins
version: 0.8.0
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

**This skill IS Tier 2.** The binary handles Tier 1 (mechanical, regex, <5ms). The skill orchestrates LLM rewrites for agent returns, search results, and file reads — the high-value compressions that Tier 1 can't touch.

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
> **Task:**
> 1. Call `GET http://localhost:«WET_PORT»/_wet/status` — this is the API ground truth for context fill. Extract `latest_total_input_tokens` and `context_window`. Compute `fill_pct = latest_total_input_tokens * 100 / context_window`.
> 2. Call `GET http://localhost:«WET_PORT»/_wet/inspect` — this lists compressible tool results only (a subset of total context). System prompt, user/assistant text, tool_use blocks, and protocol overhead are NOT in inspect and NOT compressible.
>
> **IMPORTANT:** `fill_pct` MUST come from `/_wet/status` (API ground truth), NOT from summing inspect token counts. Inspect tells you what's compressible, not how full the context is.
>
> **Classification rules (apply in order):**
> 1. **PROTECTED** — `is_error == true`, `current_turn - turn <= 3`, `has_images == true`, content contains `[compressed`, token_count < 50, OR any tool with token_count < 250 that isn't AGENT_RETURN/SEARCH/FILE_READ. Never compress. (Tier 1 adds ~20-token tombstone wrapper; items under 250 tokens hit the economic gate — compressed + tombstone ≥ original.)
> 2. **BOOT_READ** — Read of SOUL.md, IDENTITY.md, USER.md, or MEMORY.md. **NEVER_COMPRESS.** SACRED.
> 3. **MECHANICAL** — tool_name is `Bash` AND token_count ≥ 250. Tier 1 binary has 10 family-specific compressors (git, npm, cargo, pytest, etc.) plus a generic fallback. Deterministic, safe, ~90% savings. No replacement_text needed. Items under 250 tokens → PROTECTED.
> 4. **AGENT_RETURN** — tool_name is `Agent`, any token_count above 50. Natural-language analysis/summary from subagents. LLM rewrite. ~80% savings.
> 5. **SEARCH** — tool_name is `Grep` or `Glob`, any token_count above 50. Tier 1 generic compressor is unsafe for these (head/tail truncation loses matches from the middle). Always LLM rewrite — preserves which files matched, key findings, match counts. ~75% savings.
> 6. **FILE_READ** — tool_name is `Read` (excluding boot reads). List EACH file with path and token count. OPT_IN — user decides per-file.
> 7. **EDIT** — tool_name is `Edit`. Tiny confirmation messages. Classify as PROTECTED.
>
> **Output this exact structured data only:**
>
> ```
> CONTEXT (from /_wet/status)
> fill_pct: N% (Nk / Nk)
> context_window: N
>
> TOOL RESULTS (from /_wet/inspect)
> total_items: N
> total_tokens: N (N% of context is compressible tool results)
> non_compressible: ~Nk (system prompt, conversation, overhead)
>
> MECHANICAL (Bash ≥250tk): count=N tokens=N ids=id1,id2,...
> AGENT_RETURN: count=N tokens=N ids=id5,id7,...
> SEARCH (Grep/Glob): count=N tokens=N ids=id9,...
> BOOT_READ: count=N tokens=N ids=—
> FILE_READ: count=N tokens=N (opt-in, listed below)
> PROTECTED (errors, recent, small, Edit): count=N tokens=N ids=—
>
> FILE_READS
> id=«id» path=/full/path tokens=N
> (one line per file, empty if none)
>
> COMPRESSION ESTIMATE
>                  items   before    after    saved
> Mechanical:      N       Nk        ~Nk      ~Nk
> Agent rewrite:   N       Nk        ~Nk      ~Nk
> Search rewrite:  N       Nk        ~Nk      ~Nk
> ─────────────────────────────────────────────────
> Total:           N       Nk        ~Nk      ~Nk
> ```
>
> For the estimate table: Mechanical after = before × 0.1, Agent after = before × 0.2, Search after = before × 0.25.
> Under 50 lines. No prose.

---

## Phase 3 — APPROVAL GATE (main session)

Present this ASCII plan (fill `«»` from profiler output):

```
┌──────────────────────────────────────────────────┐
│  WET CONTEXT PROFILE                             │
│  «fill_pct»% full («used_k»k / «window_k»k)     │
│  Tool results: «total_tokens»k («pct_of_ctx»%)   │
│  Fixed overhead: ~«overhead_k»k (not compressible)│
├──────────────────────────────────────────────────┤
│  «bar_mech»  MECHANICAL     «mech_k»k tk         │
│  «bar_agent» AGENT RETURNS  «agent_k»k tk        │
│  «bar_srch»  SEARCH         «srch_k»k tk         │
│  «bar_boot»  BOOT READS     «boot_k»k tk         │
│  «bar_file»  FILE READS     «file_k»k tk         │
│  «bar_prot»  PROTECTED      «prot_k»k tk         │
├──────────────────────────────────────────────────┤
│  COMPRESSION PLAN                                │
│                  items   before   after    saved  │
│  Mechanical:     «N»     «k»k    ~«k»k    «k»k  │
│  Agent rewrite:  «N»     «k»k    ~«k»k    «k»k  │
│  Search rewrite: «N»     «k»k    ~«k»k    «k»k  │
│  ──────────────────────────────────────────────── │
│  Total:          «N»     «k»k    ~«k»k    «k»k  │
│                                                  │
│  File Reads (opt-in):                            │
│   «file_path»  «tokens»k tk  [keep]             │
│  Boot Reads: SACRED — never touched              │
├──────────────────────────────────────────────────┤
│  Approve? [y / n / edit]                         │
│  File reads: [keep / summarize / delete] each    │
└──────────────────────────────────────────────────┘
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

Spawn a subagent (fill in `«WET_PORT»`, `«MECHANICAL_IDS»`, `«REWRITE_IDS»` (agent + search), `«SUMMARIZE_FILE_IDS»`, `«DELETE_FILE_IDS»`):

> You are a context compressor for the wet proxy.
>
> **Endpoint:** `http://localhost:«WET_PORT»`
>
> **Step 1 — Mechanical:** POST Bash IDs only, no replacement_text. Tier 1 handles them.
> ```bash
> curl -s -X POST "http://localhost:«WET_PORT»/_wet/compress" \
>   -H 'Content-Type: application/json' \
>   -d '{"ids": [«MECHANICAL_IDS»]}'
> ```
>
> **Step 2 — LLM rewrite (agent returns + search results + summarized files):**
> IDs: «REWRITE_IDS» «SUMMARIZE_FILE_IDS»
> For EACH: fetch full content via `GET /_wet/inspect?full=1`, find the item by id, then write a dense summary.
>
> **TOKEN BUDGETS (hard limits — not suggestions):**
> - Agent returns: **max 150 tokens**
> - Search results: **max 100 tokens**
> - File summaries: **max 100 tokens**
>
> **WHAT TO PRESERVE (priority order):** decisions made, file paths mentioned, error messages, metrics/numbers, conclusions reached.
> **WHAT TO DROP:** reasoning chains, code snippets, verbose explanations, examples, metadata blocks (`<usage>`, `agentId:`, timestamps), markdown formatting, hedging language.
>
> **ANTI-PASS-THROUGH RULE — READ THIS TWICE:**
> You are a SUMMARIZER, not a wrapper. Your job is to EXTRACT and CONDENSE, not to copy.
> - NEVER submit original content as replacement_text.
> - NEVER wrap original text in a tombstone envelope.
> - NEVER include code blocks in summaries — describe what the code does in one sentence.
> - If your summary is longer than 20% of the original, you FAILED. Cut harder.
> - A 2000-token agent return must become ~150 tokens. A 500-token search result must become ~80 tokens. If you're writing 400+ tokens for any single item, STOP and rewrite shorter.
>
> **VERIFICATION (do this for every item):**
> After writing each summary, estimate tokens (word count × 1.3). If over budget, cut further. Target 80%+ compression on every item. If you cannot hit 80%, you are copying instead of summarizing — rewrite from scratch using only your memory of the key facts.
>
> **Example — this is the density you must hit:**
> BEFORE (2000 tokens): "Root cause found. In proxy/proxy.go line 354, RecordRequest is inside the if result.Compressed > 0 block. When no compression happens, the else branch just forwards the body and never calls RecordRequest. This means session_requests stays at 0 in the stats file despite..."
> AFTER (40 tokens): "Root cause: RecordRequest gated by result.Compressed>0 in proxy.go:354. Passthrough mode skips session_requests increment. Fix: move RecordRequest outside conditional."
>
> POST all rewrites in one call:
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
11. **NEVER** send Grep/Glob results through Tier 1 mechanical compression — head/tail truncation loses matches from the middle. Always LLM rewrite.
