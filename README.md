# wet — Wringing Excess Tokens

**A transparent API proxy for Claude Code that compresses stale tool results, extending your context runway.**

Your Claude is running dry. Make it wet.

82% of context bloat is stale tool results — old `git status` outputs, spent `pytest` runs, logs you already acted on. `wet` compresses them transparently so Claude keeps thinking instead of drowning.

```
go install github.com/buildoak/wet@latest
wet install-statusline
wet claude "fix the auth bug"
```

That's it. Everything else is automatic.

---

## The Statusline

Once installed, your Claude Code prompt shows context health in real time:

```
[Opus 4.6 (1M)] (90k/1000k) | wet: 9% (90k/1.0M)
```

As your session grows, wet tracks what's been compressed:

```
[Opus 4.6 (1M)] (200k/1000k) | wet: 20% (200k/1.0M) | 19/105 compressed (21.6k→3.0k)
```

Deep into a session, wet keeps you below the autocompact cliff:

```
[Opus 4.6 (1M)] (350k/1000k) | wet: 35% (350k/1.0M) | 47/230 compressed (89.2k→8.1k)
```

---

## How It Works

wet sits between Claude Code and `api.anthropic.com`. No client patches. No prompt wrappers. Just a proxy.

```
Claude Code ──────► wet proxy (Go) ──────► api.anthropic.com
                        │
                   intercepts POST /v1/messages
                   classifies tool_results: fresh vs stale
                   compresses stale results in-place
                   forwards lean payload
                   streams response unchanged (SSE passthrough)
```

**Tier 1 — Deterministic compression** (<5ms overhead)
10 tool-family-specific compressors that understand structure:

| Tool Family | Compression | What it keeps |
|---|---|---|
| `git status` | 88% | Changed files, branch, counts |
| `git log` | 93% | Hashes, subjects, authors |
| `git diff` | 90% | Filenames, hunk headers, key changes |
| `pytest` | 96% | Pass/fail counts, failed test names |
| `npm/yarn` | 89% | Dependency tree summary |
| `cargo` | 94% | Errors, warnings, build status |
| `pip` | 87% | Installed packages, conflicts |
| `docker` | 86% | Container/image status |
| `ls/find` | 84% | Directory structure, file counts |
| `make/cmake` | 89% | Build targets, errors |

**Tier 2 — LLM-guided rewrite** (optional)
For agent returns and search results that need semantic compression. Uses the `wet-compress` Claude Code skill. Disabled by default.

**Bypass rules** — wet never touches:
- Current-turn results (still in use)
- Error outputs (diagnostic value)
- Images and binary content
- Outputs under 200 tokens (not worth it)
- Already-compressed blocks

---

## Real Session Data

From a production coding session today:

| Metric | Value |
|---|---|
| Total context | 210k tokens |
| Tool results | 54k tokens (26% of context) |
| Items compressed | 34 |
| Tokens saved | 34.9k |
| Compression ratio | 88% |
| Context after | 199k tokens |
| Autocompact triggered | No |

Without wet, this session would have hit autocompact around turn 120. With wet, it ran to turn 190+ with headroom to spare.

---

## CLI Reference

### Session Management

| Command | What it does |
|---|---|
| `wet claude [args...]` | Start Claude Code through the wet proxy |
| `wet ps` | List all active wet sessions |
| `wet install-statusline` | Add wet statusline to Claude Code prompt |
| `wet uninstall-statusline` | Remove wet statusline |

### Live Inspection

| Command | What it does |
|---|---|
| `wet status` | Current session stats (fill%, items, savings) |
| `wet inspect` | Detailed view of all tracked tool results |
| `wet inspect --live` | Auto-refreshing live view |
| `wet inspect --live --format table` | Live table format |

### Compression Control

| Command | What it does |
|---|---|
| `wet compress --ids id1,id2,...` | Manually compress specific items |
| `wet pause` | Pause automatic compression |
| `wet resume` | Resume automatic compression |
| `wet rules list` | Show current compression rules |
| `wet rules set KEY VALUE` | Modify a compression rule at runtime |

### Session Data

| Command | What it does |
|---|---|
| `wet data status` | Storage stats for session data |
| `wet data inspect [--all]` | Browse persisted session data |
| `wet data diff <turn>` | Show what changed at a specific turn |
| `wet session salt` | Show current session's salt token |
| `wet session find <SALT>` | Find a session by its salt |
| `wet session profile --jsonl <PATH>` | Analyze a session's JSONL trace |

### Skill Helpers

| Command | What it does |
|---|---|
| `wet install-skill [--dir PATH]` | Install wet-compress skill into Claude Code |
| `wet uninstall-skill [--dir PATH]` | Remove wet-compress skill |

---

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                     Claude Code                         │
│                  (unmodified client)                     │
└────────────────────────┬────────────────────────────────┘
                         │ ANTHROPIC_BASE_URL=localhost:PORT
                         ▼
┌─────────────────────────────────────────────────────────┐
│                      wet proxy                          │
│                                                         │
│  ┌──────────┐  ┌──────────────┐  ┌───────────────────┐ │
│  │ Intercept │──│  Classifier  │──│   Compressor      │ │
│  │ messages  │  │ fresh/stale  │  │ 10 tool families  │ │
│  └──────────┘  └──────────────┘  └───────────────────┘ │
│                                                         │
│  ┌──────────────────────────────────────────────────┐   │
│  │          Control Plane (Unix socket)              │   │
│  │  status · inspect · compress · pause · resume     │   │
│  └──────────────────────────────────────────────────┘   │
│                                                         │
│  ┌──────────────────────────────────────────────────┐   │
│  │          Persistence Layer                        │   │
│  │  session data · compression log · stats           │   │
│  └──────────────────────────────────────────────────┘   │
└────────────────────────┬────────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────────┐
│                 api.anthropic.com                        │
│              (SSE streaming passthrough)                 │
└─────────────────────────────────────────────────────────┘
```

---

## Quick Start

**Install:**

```bash
go install github.com/buildoak/wet@latest
```

Or build from source:

```bash
git clone https://github.com/buildoak/wet
cd wet && go build -o wet .
```

**Run Claude through wet:**

```bash
wet claude fix the bug
```

**Add the statusline:**

```bash
wet install-statusline
```

**Monitor a running session:**

```bash
wet status          # quick stats
wet inspect --live  # live dashboard
wet ps              # all sessions
```

---

## Configuration

wet works with zero config. For tuning:

```toml
# ~/.wet/wet.toml

[server]
mode = "auto"              # "passthrough" (default) or "auto"

[staleness]
default_turns = 3          # turns before a result is considered stale
git_status_turns = 2       # git status goes stale faster
pytest_turns = 1           # test output is stale immediately after acting on it

[tier2]
enabled = false            # LLM-guided compression (requires wet-compress skill)
```

- **Go version:** 1.22
- **Dependencies:** 1 (`BurntSushi/toml`, vendored)
- **Binary size:** 9.3 MB (arm64)
- **Module:** `github.com/buildoak/wet`

---

## Key Numbers

| Metric | Value |
|---|---|
| Tier 1 overhead | <5ms per request |
| SWE-bench average compression | 91.2% (13,881 outputs) |
| E2E test #1 | 73.7% compression (42,678 chars saved) |
| E2E test #2 | 57.7% compression (12,484 tokens saved) |
| Tests passing | 125 across 8 packages |
| Runtime dependencies | 0 |

---

## Status

Alpha. Dogfooded daily.

**Shipping now:**
- Tier 1 deterministic compression (10 tool families)
- Full CLI with live inspection
- Statusline integration
- Session persistence and profiling
- 125 tests, 2 E2E integration tests

**Coming in v0.2.0+:**
- Tier 2 LLM compression for agent returns
- Codex CLI support (`wet codex ...`)
- Homebrew formula (`brew install wet`)
- Semantic staleness model

---

## License

MIT

---

> *"Before wet, by turn 150 I'm swimming through stale grep outputs and old build logs I'll never look at again. After compression, it's like someone cleared my desk — same work, half the noise. I can actually find what I'm looking for."*
>
> — Claude, after being made wet
