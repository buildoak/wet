# Compression Heuristics

Decision rules for classifying tool results. Turn-based staleness, not time-based.

**Key concept:** `turn_age = current_turn - turn` (both fields from the `/_wet/inspect` response).

**Token estimation:** `chars / 3.3` (calibrated for mixed code/JSON). Used only for pre-compression estimates. Post-compression savings use exact API-reported token deltas.

---

## Inspect Response Fields

Each entry from `GET /_wet/inspect` provides these fields for classification:

```json
{
  "tool_use_id": "toolu_01...",
  "tool_name": "Bash|Read|Agent|Task|Grep|Glob|Edit|Write|WebFetch|WebSearch|NotebookEdit",
  "command": "first ~100 chars of command/input",
  "file_path": "/path/to/file (for Read/Edit/Write/Grep/Glob)",
  "turn": 12,
  "current_turn": 45,
  "stale": true,
  "is_error": false,
  "has_images": false,
  "token_count": 847,
  "content_preview": "first 200 chars of content"
}
```

With `?full=1`: adds `"content": "full text"` for LLM rewrite in Phase 4.

---

## NEVER Compress (Hard-Protected)

These are unconditionally protected. No exceptions.

| Class | Rule | Rationale |
|-------|------|-----------|
| Error results | `is_error == true` | Diagnostic info is irreplaceable; errors often explain current state |
| Fresh results | `turn_age <= 3` | LLM is likely still reasoning about these; compressing breaks active thought. This includes fresh images — they are protected by the same floor |
| Active file edits | File was read AND the same file path appears in an edit/write command within the last 5 turns | Read content is reference for ongoing edits |
| Boot reads | Read of SOUL.md, IDENTITY.md, USER.md, or MEMORY.md | SACRED. Identity files are never compressed. Detect via `file_path` ending |
| Pinned results | `content_preview` contains `[WET:PIN]` marker | Coordinator explicitly protected this result |
| Action items | `content_preview` contains `TODO`, `FIXME`, `BUG`, `HACK`, or `XXX` (case-insensitive) | Likely under active discussion or pending resolution |
| Notebook edits | `tool_name` is "NotebookEdit" | Notebook operations are structural changes; compressing loses cell state |
| Already compressed | `content_preview` contains `[compressed` | Tombstone idempotency — never double-compress |

### How to detect "Active file edits"

Scan the full inspect array for entries where:
1. Entry A has `tool_name` = "Read" and a non-empty `file_path`
2. Entry B has `tool_name` = "Edit" or "Write" AND `file_path` matches entry A's `file_path`
3. Entry B's `turn` is within `current_turn - 5`

If both conditions hold, PROTECT entry A regardless of its turn_age.

**Note:** Use `file_path` (not `command`) for path matching. The `file_path` field is populated for Read, Edit, Write, Grep, and Glob tools. For Bash tool results, file paths may still appear in `command`.

---

## ALWAYS Compress (High-Confidence)

These are safe to compress when their turn_age exceeds the threshold.

| Class | Detection | Turn Age Threshold | Rationale |
|-------|-----------|-------------------|-----------|
| git status/diff/log | `command` starts with `git status`, `git diff`, or `git log` | > 5 | Workspace state changes frequently; old snapshots are misleading |
| Superseded test runs | Same test command (e.g., `pytest`, `cargo test`, `npm test`) appears multiple times; this is NOT the newest | Any (keep only newest) | Only the latest test result matters |
| Directory listings | `command` starts with `ls`, `find`; or `tool_name` is "Glob" | > 3 | Directory structure is cheap to re-query |
| Duplicate file reads | Same file path in `file_path`, multiple entries; this is NOT the newest | Any (keep only newest) | The newest read has the most current content |
| Large stale results | `token_count > 2000` | > 8 | High token cost with diminishing relevance |
| Search results | `tool_name` is "Grep" or `command` starts with `grep`, `rg`, `ag` | > 5 | Search results are exploration artifacts; stale ones add noise |
| Stale images | `has_images == true` | > 3 | Image blocks are massive token sinks (thousands of tokens each); screenshots from earlier turns are almost never re-referenced and compress to a tiny tombstone |

### Superseded detection

Group entries by normalized command (strip flags/args that change between runs, match on the base command + test suite name). Keep only the entry with the highest `turn` value. All others are superseded.

### Duplicate file read detection

Group entries where `tool_name` is "Read" by `file_path`. Keep only the entry with the highest `turn` value. All others are duplicates.

---

## CONDITIONAL (Subagent Judgment)

These require contextual reasoning. Classify as COMPRESS or PROTECT based on the specific condition.

| Class | Detection | Compress When | Protect When |
|-------|-----------|---------------|--------------|
| File reads | `tool_name` is "Read", `turn_age > 8` | File was NOT subsequently edited (no Edit/Write with same `file_path` in later turns) | File WAS edited in a later turn (content may be reference) |
| Build/compile output | `command` contains `make`, `cmake`, `cargo build`, `go build`, `npm run build` | A newer build exists for the same target | This is the most recent build output |
| Web fetch results | `tool_name` is "WebFetch" | `turn_age > 5` AND content not referenced in recent tool results | Referenced in recent results or turn_age <= 5 |
| Web search results | `tool_name` is "WebSearch" | `turn_age > 5` | turn_age <= 5 (search results are ephemeral but recent ones may guide decisions) |
| API responses | `command` suggests HTTP/API call (curl, fetch, etc.) | `turn_age > 5` AND response was successful (no error indicators) | Response contains error, unexpected status, or is the only API call |

---

## Compression Quality by Type (from E2E testing)

Real-world compression ratios from dogfood sessions:

| Type | Tier | Avg Compression | Best Case | Worst Case | Notes |
|------|------|----------------|-----------|------------|-------|
| git status/log/diff | Tier 1 | 88-93% | 96% (pytest) | 83% (ls/find) | Deterministic regex, proven on 13,881 SWE-bench outputs |
| Bash CLI output | Tier 1 (generic) | 32-88% | 88% (long JSON) | 32% (short output) | Head/tail + dedup similar lines |
| File reads (source code) | Tier 1 (CompressReadOutput) | 60-80% | 80% (large files) | 16% (small files) | First 100 lines kept. Works well for iterative workflows where imports + structure matter |
| File reads (source code) | Tier 2 (skill rewrite) | ~90% | — | — | Dense <150 token summary. User must opt-in at approval gate |
| Agent/Task returns | Tier 1 (generic) | 15-38% | 38% | 15% | Poor — dense analytical text doesn't respond to truncation |
| Agent/Task returns | Tier 2 (skill rewrite) | ~80% | — | — | Skill writes dense summary preserving key findings. THIS IS WHY THE SKILL EXISTS |
| File reads (delete/"unread") | Skill | ~99% | — | — | Minimal tombstone: `[deleted: file_read | removed by user]` |

**Key insight:** The skill's primary value is Tier 2 compression of agent returns and opted-in file reads. Without the skill, agent returns get 15-38% compression via truncation. With the skill, they get ~80% via LLM rewrite. The skill IS Tier 2.

---

## Thresholds Summary

| Parameter | Value | Notes |
|-----------|-------|-------|
| Minimum savings to proceed | 5000 tokens | Below this, overhead of compression outweighs benefit |
| Tombstone overhead | ~20 tokens each | Factor into net savings: `net = gross - (20 * count)` |
| Too few results | < 5 total | Skip analysis entirely; not worth the overhead |
| Too few tokens | < 20000 total | Context is not under pressure; skip |
| Fresh protection | turn_age <= 3 | Hard floor, no exceptions |
| Active edit window | 5 turns | Lookback for detecting ongoing file edits |

---

## Classification Priority

When multiple rules apply to the same entry, use this priority order:

1. **PROTECT rules always win.** If any PROTECT rule matches, the entry is protected regardless of COMPRESS matches.
2. **Within COMPRESS rules:** If an entry matches multiple COMPRESS rules, list all matching reasons in the report (e.g., "superseded test run + large stale result").
3. **CONDITIONAL falls to COMPRESS only when the compress condition is met.** Otherwise it becomes PROTECT.

---

## Profile-Informed Urgency

Phase 1 (Health Check) produces a context health assessment before heuristics are applied. The health status adjusts how aggressively these heuristics should be applied:

| Health Status | Context % | Heuristic Adjustment |
|---------------|-----------|---------------------|
| light | < 10% | Compression optional. Proceed only on explicit user request |
| accruing | 10-30% | Standard thresholds. Proactive compression recommended — stale results degrade signal quality regardless of fill level |
| growing | 30-60% | Lower the "Large stale results" token threshold from 2000 to 1000. Compress CONDITIONAL items more aggressively (prefer compress over protect when in doubt) |
| heavy | > 60% | Lower all turn_age thresholds by 2 (e.g., fresh protection becomes turn_age <= 1, git commands compress at > 3). Compress CONDITIONAL items unless there is a strong protect signal. Maximum reclamation |

These adjustments are guidelines, not overrides of hard-protect rules. Error results, boot reads, and pinned results remain unconditionally protected regardless of context pressure.
