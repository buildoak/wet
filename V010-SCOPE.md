# wet v0.1.0 — Scope & Blockers

Status: pre-release
Last updated: 2026-03-11

## What works (done)

- Transparent HTTP proxy, zero client modification (`wet claude [args...]`)
- Tier 1 deterministic compression (Go port, <5ms, pattern-based per tool family)
- Passthrough mode (default) — agent-directed compression only
- HTTP control plane: `/_wet/status`, `/_wet/inspect`, `/_wet/compress`, `/_wet/pause`, `/_wet/resume`, `/_wet/rules`
- Per-session isolation (port-as-identity, stats files, env vars)
- `CompressSelected` pipeline — explicit tool_use_id compression works end-to-end
- wet-compress skill (6-phase protocol)
- Session helpers: `wet session salt`, `wet session find`, `wet session profile`
- Tombstone idempotency (already-compressed blocks skipped)

## v0.1.0 blockers

### B1: Token savings transparency

The value proposition of wet is "save tokens, extend context." The user must see exactly how much they're saving and trust it.

Current problems:
- Token estimation uses `len(text)/4 * 2.3` — overestimates ~10%
- SSE `message_start` event parsing broken — `usage.input_tokens` always 0
- Gap between "queued for compression" and "actually saved" is confusing (25k queued → 3.8k saved in last test)
- Need clear before/after reporting per compression action

Goal: when wet compresses something, the user knows exactly how many tokens were saved, verified against API ground truth.

### B2: Status bar token savings

`wet statusline` reads `~/.wet/stats-{port}.json`. Shows cumulative `tokens_saved` from proxy's own diff math, not reconciled with what the API actually billed.

Goal: status bar shows trustworthy savings number. Consider: per-request delta, cumulative total, percentage of context reclaimed.

### B3: Autocompact avoidance strategy

Claude Code's native autocompact fires at ~83.5% context fill and compresses the entire conversation. wet fires before each request, surgically compressing selected tool results.

Risk: if autocompact fires, it may render wet's compression redundant (or worse, the user sees double-compression artifacts). Tombstones are idempotent (won't re-compress), but the interaction needs formal analysis.

Goal: documented strategy for how wet and autocompact coexist. Options:
- wet compresses proactively to PREVENT autocompact from ever firing (keep context under threshold)
- wet defers to autocompact for bulk compression, only handles surgical agent-directed compression
- wet monitors context fill % and escalates compression aggressiveness as threshold approaches

### B4: Agent return compression (LLM-based)

Agent returns are 29-31% of total context — the single largest category. Tier 1 mechanical compression gives only ~15% savings on them because agent summaries are unstructured natural language, not parseable output.

This is the make-or-break feature for wet's value. Two architectural options:

**Option A: Built-in Tier 2 (wet-internal)**
- wet's existing Tier 2 pipeline sends content to a fast LLM (Haiku-class) for compression
- Pros: self-contained, no external dependency, works in passthrough mode via `/_wet/compress`
- Cons: wet needs API key management, model selection, latency budget (~500ms per compression)

**Option B: Externalized via agent-mux**
- The coordinating agent (R. Jenkins) dispatches a compression worker via agent-mux
- Worker reads the stale content, produces a compressed summary, returns it
- wet receives the compressed text via `/_wet/compress` with `replacement_text` field
- Pros: leverages existing infra, model-agnostic, coordinator controls quality
- Cons: requires agent-mux integration, slower feedback loop, compression happens outside wet

**Decision needed before v0.1.0.**

## Testing plan

- [ ] End-to-end compression test with real session (not synthetic)
- [ ] Verify token savings against API `usage.input_tokens` (requires B1 fix)
- [ ] Stress test: 50+ tool results, measure compression latency
- [ ] Autocompact interaction test: fill context to ~80%, verify wet prevents autocompact trigger
- [ ] Agent return compression: compare Tier 1 vs Tier 2 savings on real agent output
- [ ] Status bar accuracy test: compare displayed savings vs actual API billing delta

## Out of scope for v0.1.0

- Codex wrapping (`wet codex ...`) — v2
- Token budget mode (compress oldest-first until under budget) — spec'd, later
- Daemon mode (`wet daemon start`) — later
- Homebrew/distribution — later
- README / Show HN positioning — after v0.1.0 is solid
