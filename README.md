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

*Put Claude in the driver's seat for context optimization.*

One Go binary, one Claude Code skill. Toolbox and a Manual.

wet is a **toolbox for agents**. It gives Claude (or any agent sitting on top of Claude Code) surgical access to its own context - the ability to see exactly how much each tool result block consumes, profile the entire session's token distribution, and replace any block with either deterministic compression or a meta-aware subagent rewrite.

The **Go proxy** is the toolbox. It sits between Claude Code and the API, intercepts every `POST /v1/messages`, and exposes a full control plane:

```bash
# Launch & observe
wet claude [args...]                    # start Claude Code through the proxy
wet ps [--all]                          # list all active wet sessions
wet status [--json]                     # context profile: fill%, token counts, compressible items
wet inspect [--json] [--full]           # every tool result block with token count, age, staleness

# Surgical compression
wet compress --ids id1,id2,...          # replace specific blocks — deterministic or with replacement text
wet compress --text-file plan.json     # batch replacement with LLM-rewritten content
wet compress --dry-run --ids ...       # preview what would change without applying

# Runtime control
wet pause                               # bypass all compression (accounting still runs)
wet resume                              # re-enable compression
wet rules list                          # show active compression rules
wet rules set KEY VALUE                 # tune thresholds at runtime

# Session forensics
wet session profile --jsonl <PATH>      # context composition analysis from session trace
wet session salt                        # session self-identification token
wet data status                         # offline storage stats
wet data inspect [--all]                # browse persisted compressed items
wet data diff <turn>                    # what changed at a specific turn
```

Each tool result becomes a first-class object. You can see it, measure it, and replace it. Deterministic compression is calibrated on SWE-bench (91.2% ratio across 13,881 outputs, <5ms overhead) and understands 10 tool families natively: `git`, `pytest`, `cargo`, `npm`, `pip`, `docker`, `make`, `ls/find`, and more.

Per-item token counts are estimated from content length (chars/4 heuristic — no external tokenizer dependency). Session-level fill% and savings come from Anthropic's actual token counts in the API response — ground truth, not estimates.

The **skill** is the manual. It teaches Claude the meta game — how to use the toolbox on itself:

**1. Profile** — run `wet status`, see context fill, token distribution, what's compressible vs sacred.

**2. Propose** — inspect individual blocks, classify each one (mechanical Bash compression vs LLM-guided rewrite for agent returns and file reads), build a compression plan with expected savings.

**3. Process** — execute the plan. Bash outputs get deterministic Tier 1 compression. Agent returns and search results get rewritten by a Sonnet subagent that preserves semantic content while cutting 80-90% of tokens.

Here's what Claude sees when it profiles a real session (this README was written in it):

```
┌──────────────────────────────────────────────────────────────────────┐
│  Tool             Items    Tokens   Stale   Status                  │
├──────────────────────────────────────────────────────────────────────┤
│  Read               13    33.7k    13/13   ██████████████████  80%  │
│  Agent               6     3.5k     6/6    ████░░░░░░░░░░░░░░   8%  │
│  Bash               12     3.1k     9/12   ███░░░░░░░░░░░░░░░   7%  │
│  Grep                2     1.2k     2/2    █░░░░░░░░░░░░░░░░░   3%  │
│  TaskOutput          1     0.7k     1/1    █░░░░░░░░░░░░░░░░░   2%  │
│  Edit                6     0.2k     6/6    ░░░░░░░░░░░░░░░░░░  <1%  │
├──────────────────────────────────────────────────────────────────────┤
│  Total              40    42.4k    37/40   context fill: 11.5%      │
│                                                                      │
│  Sacred:    SOUL, IDENTITY, USER, MEMORY — never compressed          │
│  Fresh:     3 items (current turn) — protected                       │
│  Stale:     37 items — compressible                                  │
└──────────────────────────────────────────────────────────────────────┘
```

Claude sees what's sacred, what's fresh, what's fair game. It proposes a compression plan, you approve, it executes. Or in auto mode - it just handles it.

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
