# Wet Claude

*Wringing Excess Tokens Claude*

**API proxy for Claude Code — teach your Claude to optimize its own context in a meta-transparent way.**

![wet compression dashboard](assets/images/wet-dashboard.jpg)

Your Claude is running dry. Make it wet.

## Why This Exists

Auto compact is brutal. It hits at the worst moments — mid-swarm, mid-experiment — and when it fires, it's all or nothing. Context gets shredded indiscriminately. Important computation goes rogue. Sessions derail. I've had a Mac Mini spiral to 60GB swap from the fallout.

So I audited thousands of tool calls across my Claude Code sessions. The culprit was obvious: **82% of context bloat is stale tool results** - old `git status` outputs, spent `pytest` runs, massive `grep` dumps you already acted on, 30k-token agent returns you'll never look at again. They sit there, rotting, pushing you toward the autocompact cliff.

The problem: there's no hook to intercept tool results before they enter context. I checked Claude Code, Codex - nothing. [Opened a feature request](https://github.com/anthropics/claude-code/issues/32105). I forked Codex and wired in my own compression hooks. I tried JSONL manipulation. Too dirty.

Then the insight: **reverse proxy**. A Go shim that sits between Claude Code and `api.anthropic.com`, intercepts every `POST /v1/messages`, and compresses stale tool results in-place before they reach the API. No client patches. No prompt wrappers. Clean.

But deterministic compression alone wasn't enough - it handles Bash outputs well, but agent returns and file reads need semantic understanding. So I flipped the script: instead of just compressing mechanically, **put Claude in the driver's seat**. Let it profile its own context, decide what's stale, and surgically rewrite its own tool results with a Sonnet subagent. Meta-compression - Claude optimizing Claude's context.

The result: instead of autocompact's sledgehammer, you get a scalpel. Sessions that would hit the wall at turn 120 now run past 200 with headroom to spare. Same work, half the noise.

---

## What It Is

Two components. One Go binary, one Claude Code skill. They work together.

The **proxy** sits between Claude Code and the API. It sees every tool result, tracks staleness, and can deterministically compress Bash outputs in-place at <5ms overhead. Zero quality loss - it understands `git`, `pytest`, `cargo`, `npm`, `docker`, and 10 tool families natively.

The **skill** is where it gets interesting. It teaches Claude to play the meta game:

```
wet status --json          # Claude profiles its own context
wet inspect --json         # Claude sees every tool result with token counts
wet compress --ids ...     # Claude surgically replaces what it chooses
```

The workflow:

**1. Profile** - Claude runs `wet status`, sees the context fill, token distribution, what's compressible.

**2. Propose** - Claude inspects individual tool results, classifies them (mechanical Bash compression vs LLM-guided rewrite for agent returns and file reads), builds a compression plan with expected savings.

**3. Process** - Claude executes the plan. Bash outputs get deterministic compression. Agent returns and search results get rewritten by a Sonnet subagent that preserves semantic content while cutting 80-90% of tokens.

Here's what Claude sees when it profiles a session:

```
┌─────────────────────────────────────────────────────────────────────┐
│  Category          Items   Tokens          Saved    Compression    │
├─────────────────────────────────────────────────────────────────────┤
│  Bash (Tier 1)       10    9.5k  →  1.0k    8.5k   ██████████░░  │
│  Agent Returns       20   27.3k  →  3.2k   24.1k   ██████████░░  │
│  Search (Grep/Glob)   4    2.8k  →  0.5k    2.3k   ██████████░░  │
├─────────────────────────────────────────────────────────────────────┤
│  Total               34   39.6k  →  4.7k   35.0k   88% ratio     │
│                                                                     │
│  Sacred:      5 boot reads (8.4k tk) — never compressed            │
│  Protected:  178 items (13.5k tk) — errors, recent, small, done    │
│  File Reads:  21 items (14.8k tk) — opt-in per file, user decides  │
└─────────────────────────────────────────────────────────────────────┘
```

Claude knows what's sacred (boot files, errors, recent turns). It knows what's fair game. It proposes, you approve, it compresses. Or in auto mode - it just handles it.

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
