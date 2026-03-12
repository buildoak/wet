---
project: wet
status: spec
date: 2026-03-09
language: go
---

# wet — Wring your context dry

**W**ringing **E**xcess **T**okens

An API proxy that compresses stale tool results in LLM conversation context before forwarding to the Anthropic API, reclaiming 50-90% of wasted context tokens with zero client modifications.

## Problem

Claude Code sessions accumulate tool results that become irrelevant within 2-3 turns. A `git status` from 15 turns ago, a `cat` of a file that was subsequently edited, a `pytest` run before the fix -- these occupy thousands of tokens but contribute nothing to current reasoning.

Real numbers from 13,881 SWE-bench tool outputs:
- Mean raw output: 847 tokens
- Mean compressed (Tier 1): 75 tokens
- Overall compression ratio: **91.2%** token savings
- By tool family: pytest 96.1%, cargo 94.3%, git-log 92.8%, npm 89.1%

In a typical 50-turn Claude Code session, stale tool results consume 40-60% of context. At 200K context windows, that is 80-120K tokens of dead weight -- degrading reasoning quality, increasing cost, and triggering unnecessary context compaction.

No existing solution works:
- GoodMonkey (vaporware, never shipped)
- ClaudeSlim (modifies client, brittle approach)
- ccproxy (routing proxy, no compression)
- session-stripper by vicnaum (offline JSONL editor, destructive, post-hoc)

## Architecture

```
┌────────────────────────────────────────────────────┐
│  Terminal                                          │
│                                                    │
│  $ wet claude "fix the bug"                        │
│    ├── starts proxy on :random_port                │
│    ├── sets ANTHROPIC_BASE_URL                     │
│    └── execs claude with args                      │
│                                                    │
│  ┌──────────┐     ┌──────────────┐                 │
│  │ Claude   │────▶│  wet proxy   │──▶ Anthropic    │
│  │ Code TUI │◀────│  (Go binary) │◀── API          │
│  │          │     │              │                  │
│  │ status:  │     │ unix socket  │◀── wet status    │
│  │ ⚡ wet:  │◀────│ stats.json   │◀── wet inspect   │
│  │ -79%     │     └──────────────┘◀── wet rules     │
│  └──────────┘                                      │
└────────────────────────────────────────────────────┘
```

### Components

| Component | Responsibility |
|-----------|---------------|
| **Proxy server** | HTTP server on `localhost:8100` (or random port in shim mode). Accepts `/v1/messages` POST, all other paths pass through unchanged. Built on `net/http/httputil.ReverseProxy`. |
| **Staleness classifier** | Walks the messages array, marks tool_result blocks as `fresh` or `stale` based on turn distance and configurable per-tool-family rules. |
| **Tier 1 compressor** | Deterministic, pattern-based compression for known CLI tool families. Pure Go — regex and string operations ported from the original Rust implementation. No subprocess dependency. |
| **Tier 2 compressor** | LLM-based compression for unknown/generic outputs. Sends stale content to a fast model with extraction instructions via standard `net/http` client. Gated by token threshold. |
| **Tombstone writer** | Replaces compressed content with a structured tombstone that preserves key signals. |
| **Control plane** | Unix socket server at `~/.wet/wet.sock`. Accepts commands for live stats, rule tuning, pause/resume. |
| **Stats writer** | Writes `~/.wet/stats.json` after each proxied request for statusline integration. |
| **Config loader** | Reads `wet.toml`, applies defaults, validates. |

### Data Flow

1. Client sends `POST /v1/messages` with `messages[]` array
2. Proxy parses the messages array (JSON, not streaming body -- request is always JSON)
3. Staleness classifier walks messages from newest to oldest, tagging each `tool_result` block (respecting per-tool-family `stale_after` overrides)
4. Stale blocks are routed: Tier 1 if tool family is recognized, Tier 2 if enabled and output exceeds token threshold, else left alone
5. Compressed tombstones replace original content in the messages array
6. Modified request forwarded to `https://api.anthropic.com/v1/messages`
7. Response streamed back to client unchanged (SSE pass-through via goroutines for streaming, JSON for non-streaming)

### Language Decision: Go

Rationale:
- Single static binary, no runtime dependencies — `go build` produces one artifact, trivial to distribute
- Excellent HTTP proxy primitives (`net/http/httputil.ReverseProxy`) — purpose-built for this use case
- Goroutines for SSE streaming — natural fit for concurrent stream proxying without callback hell
- Cross-compilation trivial (`GOOS=darwin GOARCH=arm64`, `GOOS=linux GOARCH=amd64`) — one build step, every platform
- Tier 1 compression patterns ported from Rust to Go — they're regex + string ops, straightforward translation of 773 lines
- No subprocess dependency on the Rust binary — everything lives in one binary
- Tier 2 LLM calls via standard `net/http` client — no third-party HTTP library needed
- Token counting: tiktoken-go or character heuristic (both available as Go packages)
- Performance is excellent by default — no GC pressure from the proxy hot path, goroutine-per-connection scales naturally

## CLI UX — the `wet` shim

### Primary usage — session wrapper

```bash
wet claude "fix the auth bug"
wet claude --model opus-4
wet claude --resume
```

`wet` starts the proxy on a random available port, sets `ANTHROPIC_BASE_URL`, then execs `claude` with all remaining args. When claude exits, proxy shuts down. Zero config needed for basic usage.

### Daemon mode — persistent proxy

```bash
wet daemon start          # start background proxy
wet daemon stop           # stop it
wet daemon status         # show stats
```

For users who want the proxy always running (e.g., in shell profile: `export ANTHROPIC_BASE_URL=http://localhost:8100/v1`).

### Runtime control — Unix socket control plane

```bash
wet status                # live compression stats for current session
wet inspect               # show tombstones (what was compressed, what Claude sees)
wet rules list            # show active compression rules
wet rules set pytest.stale_after 3  # tune on the fly
wet pause                 # temporarily bypass all compression
wet resume                # re-enable compression
wet history               # cross-session compression log
wet tier2 enable          # toggle LLM compression without restart
wet tier2 disable
```

Control commands talk to the running proxy via Unix socket at `~/.wet/wet.sock`.

## Compression Pipeline

### Tier 1: Deterministic (Pattern-Based)

Engine: Pure Go port of `codex-rs/hooks/examples/compress-tool-output/main.rs` (773 lines Rust → Go regex + string ops). Compiled into the single binary.

Recognized tool families and their compression strategies:

| Family | Pattern | Strategy | Eval Compression |
|--------|---------|----------|-----------------|
| `git status` | Extracts modified/added/deleted file lists | Structured summary | 88.4% |
| `git log` | Extracts commit hashes, authors, one-line messages | Table format | 92.8% |
| `git diff` | Extracts file names, hunks summary, +/- line counts | Diff metadata | 90.1% |
| `pytest` | Extracts pass/fail counts, failed test names, error snippets | Test report | 96.1% |
| `npm/yarn` | Extracts installed packages, warnings, errors | Dependency summary | 89.1% |
| `cargo` | Extracts build status, warnings, errors | Build summary | 94.3% |
| `pip` | Extracts installed/upgraded packages | Package list | 87.2% |
| `docker` | Extracts container/image status, logs tail | Status summary | 85.6% |
| `ls/find` | Extracts file listing with counts | Directory summary | 83.9% |
| `make/cmake` | Extracts build targets, errors | Build summary | 88.7% |

**Invocation**: Direct function call within the Go binary. No subprocess, no IPC overhead.

**Performance**: <5ms per invocation (in-process). No network. No model calls.

### Tier 2: LLM-Based

For tool outputs that Tier 1 does not recognize and that exceed the token threshold (`tier2_min_tokens`, default 500).

**Prompt template**:
```
Extract the key information from this CLI tool output. Preserve:
- Error messages and stack traces (exact text)
- File paths mentioned
- Numeric results, counts, statuses
- Any actionable information

Discard:
- Repetitive log lines
- Progress bars and spinners
- Verbose debug output
- Redundant whitespace and formatting

Return a concise summary under 200 tokens. Start directly with the content, no preamble.

<tool_output>
{content}
</tool_output>
```

**Model selection**: Configurable. Default: `claude-haiku-3` (fast, cheap). Alternatives: GPT-5.4-mini, Gemini Flash. Could use agent-mux for model routing but direct API call is simpler for a single-purpose proxy.

**Latency budget**: <2s including model call. If model call times out, skip compression for that block (graceful degradation).

**Cost gate**: Tier 2 only fires when `tier2.enabled = true` AND output exceeds `tier2.min_tokens` tokens. At Haiku pricing (~$0.25/MTok input), compressing a 2000-token output costs ~$0.0005. Acceptable for the token savings gained.

### Pipeline Decision Logic

```
for each tool_result block in messages:
    if block is in current turn:
        SKIP (never compress)
    if block is already compressed (has tombstone marker):
        SKIP (idempotent)
    if block matches bypass rules:
        SKIP
    if block.token_count < min_compress_tokens:
        SKIP (too small to bother)

    rule = lookup_rule(block.tool_family)  // per-tool-family override or default
    staleness = current_turn - block_turn
    if staleness < rule.stale_after:
        SKIP (too recent per tool-family rule)

    if rule.strategy == "tier1" and Tier 1 recognizes the tool family:
        compressed = tier1_compress(block, rule.keep)
    elif rule.strategy == "tier2" or (tier2_enabled and block.token_count > tier2_min_tokens):
        compressed = tier2_compress(block)
    elif rule.strategy == "none":
        SKIP
    else:
        SKIP

    replace block content with tombstone(compressed)
```

## Compression Rules

Per-tool-family rules allow fine-grained control over what gets compressed, when, and how.

```toml
# wet.toml

[server]
host = "127.0.0.1"
port = 8100
upstream = "https://api.anthropic.com"

[staleness]
threshold = 2          # turns before compression
token_budget = 0       # 0 = disabled, use turn-based

[compression]
min_tokens = 100       # skip tiny outputs

[compression.tier1]
enabled = true

[compression.tier2]
enabled = false        # opt-in
model = "claude-haiku-3"
min_tokens = 500
timeout_ms = 2000

# Per-tool-family rules override defaults
[rules.git]
strategy = "tier1"
stale_after = 1        # git output stales fast
keep = "changes"       # preserve file change list

[rules.pytest]
strategy = "tier1"
stale_after = 1
keep = "failures"      # preserve failure details, drop passing test noise

[rules.read]
strategy = "tier1"
stale_after = 3        # file contents stay relevant longer

[rules.cargo]
strategy = "tier1"
stale_after = 2

[rules.npm]
strategy = "tier1"
stale_after = 2

[rules.docker]
strategy = "tier1"
stale_after = 2

[rules.custom]
# Catch-all for unrecognized tools
strategy = "none"      # options: none, tier1, tier2
stale_after = 3

[bypass]
preserve_errors = true
min_tokens = 100
content_patterns = [
    "^Error:",
    "^FATAL",
    "^panic",
    "Traceback \\(most recent"
]
```

Rules cascade: tool-family-specific `[rules.<family>]` overrides the global `[staleness]` defaults. The `keep` field tells the Tier 1 compressor which signals to preserve (e.g., `"failures"` for pytest means keep failed test names and error snippets, drop passing test output). The `strategy` field can be `none` (never compress), `tier1` (deterministic only), or `tier2` (allow LLM compression).

Runtime tuning via Unix socket:
```bash
wet rules set pytest.stale_after 3  # make pytest results last longer
wet rules set custom.strategy tier1  # start compressing unknown tools
```

## Configuration

File: `wet.toml` in working directory, `~/.wet/wet.toml`, or path via `--config` flag.

See [Compression Rules](#compression-rules) above for the full configuration format.

**Client setup** (Claude Code):
```bash
# Option 1: Session wrapper (recommended, zero config)
wet claude "fix the bug"

# Option 2: Daemon mode
wet daemon start
export ANTHROPIC_BASE_URL=http://localhost:8100/v1
# Then use Claude Code normally. No other changes needed.
```

## Staleness Model

Two modes, selectable in config:

### Mode 1: Turn-Distance (default)

A tool_result is stale when `current_turn - result_turn >= rule.stale_after` (where `rule.stale_after` comes from the per-tool-family rule, falling back to `staleness.threshold`).

Turn counting:
- Walk the messages array. Each `assistant` message increments the turn counter.
- Tool results inherit the turn number of their preceding `assistant` message (the one that issued the tool_use).
- Current turn = the turn being responded to (the last user message + any tool results from the most recent assistant tool_use).

Default threshold: **2 turns**. Per-tool-family overrides: git and pytest stale after 1, read after 3.

### Mode 2: Token-Budget

Set `staleness.token_budget` to a positive number (e.g., 50000). The proxy compresses the oldest tool results first until the total token count of all tool_result blocks is under the budget.

This mode is more aggressive and context-aware but harder to reason about. Recommended for power users who want to maximize context utilization.

### Hybrid (future)

Combine both: use turn-distance as the primary signal, but also enforce a token budget as a ceiling. Not in v1.

## Tombstone Format

When a tool_result is compressed, its content block is replaced with a tombstone:

```json
{
  "type": "tool_result",
  "tool_use_id": "toolu_abc123",
  "content": [
    {
      "type": "text",
      "text": "[compressed: git_status | 3 files modified, 1 added | turn 4/12 | 847->62 tokens]"
    }
  ]
}
```

### Tombstone format string

```
[compressed: {tool_family} | {summary} | turn {original_turn}/{current_turn} | {original_tokens}->{compressed_tokens} tokens]
```

Fields:
- `tool_family`: Detected tool family (e.g., `git_status`, `pytest`, `npm_install`) or `generic` for Tier 2
- `summary`: The compressed content. For Tier 1, this is the structured extract. For Tier 2, the LLM summary.
- `original_turn/current_turn`: Provenance for debugging
- `original_tokens->compressed_tokens`: Token counts for observability

### Idempotency

Tombstones are detected by the `[compressed:` prefix. Already-compressed blocks are never re-compressed. This makes the proxy safe to chain (multiple proxies) or reprocess.

### Why This Format

The model can still reference compressed results. "The git status from earlier showed 3 files modified" -- the tombstone preserves enough signal for this. The bracketed format signals to the model that details were elided, preventing hallucination of specific content that was removed.

## Bypass Rules

These tool results are **never** compressed, regardless of staleness:

| Rule | Rationale |
|------|-----------|
| Current-turn results | Claude needs full context for active reasoning |
| `is_error: true` results | Error details are critical for debugging chains |
| Content matching error patterns (`Error:`, `FATAL`, `panic`, `Traceback`) | Same as above, catches unmarked errors |
| Results under `min_tokens` (default 100) | Compression overhead exceeds savings |
| Already-compressed (tombstone) results | Idempotency -- don't double-compress |
| Image content blocks (`type: "image"`) | Binary content, not compressible by text methods |

## Observability

### Claude Code Status Line Integration

`wet` writes compression stats to `~/.wet/stats.json` after each proxied request. User's Claude Code `statusLine` config points to a bundled script (`wet statusline`). The script reads `~/.wet/stats.json` and renders a one-liner in the status bar:

```
⚡ wet: 42.3k→8.9k (-79%) | 18/24 compressed | session: 142k saved
```

- Setup: `wet setup-statusline` auto-configures Claude Code's `settings.json`
- The statusline shows: current request compression, session cumulative savings

### Stderr Logging

Default: one-line summary per request to stderr.
```
[wet] 24 results, 18 compressed (15 T1 + 3 T2), 42380→8920 tokens (79% saved), +12ms
```

### Metrics File (optional)

JSON lines to `~/.wet/metrics.jsonl` if configured. Per-request detail for analysis:

```json
{
  "timestamp": "2026-03-09T14:32:01Z",
  "request_id": "req_abc123",
  "total_tool_results": 24,
  "compressed": 18,
  "skipped_fresh": 4,
  "skipped_bypass": 2,
  "tier1_compressions": 15,
  "tier2_compressions": 3,
  "tokens_before": 42380,
  "tokens_after": 8920,
  "compression_ratio": 0.789,
  "tier1_latency_ms": 3,
  "tier2_latency_ms": 1240,
  "total_proxy_overhead_ms": 1250,
  "model": "claude-sonnet-4-20250514"
}
```

### Health Endpoint

`GET /health` on the proxy port returns proxy status, uptime, aggregate stats summary:

```json
{
  "status": "ok",
  "uptime_seconds": 3421,
  "requests_proxied": 87,
  "total_tokens_saved": 142380,
  "average_compression_ratio": 0.81,
  "tier1_count": 312,
  "tier2_count": 28,
  "tier2_failures": 1
}
```

## Testing Strategy

### Unit Tests

- **Staleness classifier**: Given a messages array, verify correct turn assignment and stale/fresh tagging with per-tool-family rules
- **Tombstone writer**: Verify format, idempotency detection, field extraction
- **Bypass rules**: Verify all bypass conditions (errors, small outputs, current turn, already compressed)
- **Config loader**: Verify TOML parsing, defaults, overrides, validation errors
- **Token counter**: Verify token estimation accuracy (tiktoken-go or character heuristic)
- **Tier 1 compressor**: Verify each tool family's regex patterns and extraction logic (ported from Rust, test parity)
- **Rule resolver**: Verify per-tool-family rule lookup, fallback to defaults, runtime override via control plane

### Integration Tests

- **Proxy round-trip**: Start proxy, send a real-shaped request, verify response passes through unchanged
- **Tier 1 in-process**: Verify Go compression functions match Rust binary output for a corpus of known inputs
- **Tier 2 mock**: Mock LLM API, verify prompt construction, response parsing, timeout fallback
- **Streaming pass-through**: Verify SSE events are forwarded without buffering (goroutine-based streaming)
- **Shim mode**: Verify `wet claude ...` starts proxy, sets env, execs child, cleans up on exit
- **Control plane**: Verify Unix socket commands (`wet status`, `wet rules set`, `wet pause`) work against running proxy
- **Statusline**: Verify `stats.json` is written after each request and `wet statusline` renders correctly

### Replay Tests (from real sessions)

Source: JSONL session files from `data/claude-code-sessions/`.

1. Extract messages arrays from real session transcripts
2. Run through compression pipeline
3. Verify:
   - Current-turn results are untouched
   - Error results are untouched
   - Compressed outputs contain key signals (spot-check)
   - Token counts match expected compression ratios

### Quality Eval (from SWE-bench corpus)

Reuse the existing eval methodology from the Rust compressor:
- 13,881 tool outputs with ground-truth Q&A pairs
- Run each through the compression pipeline
- Measure: compression ratio, answerable accuracy (can the compressed version answer questions about the original?)
- Target: maintain >65% accuracy at >85% compression (current Tier 1: 68.5% at 91.2%)

### Stress Tests

- Large messages array (200+ tool results)
- Very large individual tool outputs (>50K tokens)
- Concurrent requests (proxy under load — goroutine pool behavior)
- Tier 2 model timeout (verify graceful degradation)
- Shim mode: child process crash, signal forwarding (SIGINT, SIGTERM)

## Prior Art

| Tool | What It Does | Why Not Sufficient |
|------|-------------|-------------------|
| **Codex AfterToolUse hook** | Rust compressor at ingestion time (same Tier 1 engine) | Codex-only. Compresses at write time, not at API call time. Cannot retroactively compress stale results. |
| **GoodMonkey** | Announced context compression for Claude | Vaporware. Never shipped. |
| **ClaudeSlim** | Client-side context editor | Modifies Claude Code internals. Brittle, breaks on updates. |
| **ccproxy** | API routing proxy | Routes between providers, no compression. |
| **session-stripper** (vicnaum) | Offline JSONL session editor | Post-hoc, destructive (deletes content), manual process. |
| **Claude's native compaction** | Built-in context window management | Triggered only at context limit. Compresses everything, not selectively. No tool-result-specific intelligence. |

wet is differentiated by:
1. Transparent proxy -- zero client modifications
2. Selective compression -- only stale tool results, not conversation
3. Two-tier quality -- deterministic for known patterns, LLM for unknown
4. Proven compression engine -- 91.2% savings validated on 13,881 real outputs
5. Single static binary -- no runtime, no dependencies, cross-platform
6. Per-tool-family rules -- different tools get different compression policies
7. Session wrapper UX -- `wet claude ...` just works, zero config

## Open Questions

1. **Token counting method**: Use tiktoken-go (accurate but adds dependency) or character-based heuristic (fast, ~4 chars/token)? tiktoken-go is preferred for accuracy but must not add latency. Pre-compute on first pass.

2. **System prompt compression**: Should the proxy also compress the system prompt? Some Claude Code sessions have >10K token system prompts with stale context. Out of scope for v1, but worth considering.

3. **Cache layer**: Should the proxy cache compressed versions of tool results? If the same messages array is sent twice (retry), avoid recompression. Keyed on `tool_use_id`. Low priority -- requests rarely repeat identically.

4. **Multi-model support**: The proxy currently targets Anthropic API format. Supporting OpenAI-format requests (for Codex, GPT) would widen applicability. Different message schema, same compression logic. v2 scope.

5. **Compression quality feedback loop**: Can we detect when Claude asks "what was the output of X?" and correlate with a compressed tombstone? This would measure real-world accuracy. Requires response parsing. Research track.

6. **Turn-distance vs semantic staleness**: A `git status` from 10 turns ago might still be relevant if no file operations happened since. Semantic staleness (tracking what tools invalidate which results) is more accurate but much harder to implement. v2 scope.

7. **Codex wrapping**: Should `wet` also support wrapping Codex? (`wet codex ...`) — same proxy mechanics, different env var and child process.

8. **Shell completion generation**: `wet completion bash/zsh/fish` — standard Go CLI pattern (cobra/urfave generate these). Ship in v1 for discoverability of subcommands.

9. **Homebrew formula for distribution**: `brew install wet` — single binary makes this trivial. Tap or core formula?
