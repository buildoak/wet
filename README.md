# wet (Wringing Excess Tokens)
Context compression proxy for Claude Code sessions - I keep old tool output from eating your context window alive.

## The Problem (the itch that kept punching me)
My Claude Code sessions kept dying right when the work got interesting.

Not because the model got dumb - because the context filled with dead stuff. `git status` from 15 turns ago. A `pytest` run from before I fixed the bug. Logs I already acted on. Same story, every day.

When context hits the wall, autocompact fires and message structure gets flattened. I lose the thread, restart, and pretend that is fine. It is not fine.

From first principles, this is mostly a tool-output problem, not a reasoning per se problem. The meta pattern in my traces was boringly consistent: tool results were the heavy fella - roughly 82% of the bloat.

## What wet does (without touching your client)
I built `wet` as a transparent proxy between Claude Code and `api.anthropic.com`.

You run Claude through `wet`, it sets `ANTHROPIC_BASE_URL`, intercepts `POST /v1/messages`, compresses stale `tool_result` blocks, and forwards a leaner payload. Response streaming is passed through unchanged (SSE stays SSE). No Claude Code patching. No weird wrappers in your prompts.

```text
$ wet claude fix the bug
  starts proxy on :random_port
  sets ANTHROPIC_BASE_URL
  execs claude with args

  Claude Code -----> wet proxy (Go) -----> api.anthropic.com
       ^                  |
       |                  v
  status: wet: -79%  Unix socket control plane
                      wet status
                      wet inspect
                      wet rules
                      wet pause/resume
```

## Key Numbers (proper receipts)
- 73.7% compression in E2E integration test #1 (`42,678` chars saved, `7` items compressed)
- 57.7% compression in E2E integration test #2 (`12,484` tokens saved, `8` items compressed)
- 91.2% average compression on `13,881` SWE-bench tool outputs
- 125 tests passing across 8 packages
- 9.3MB arm64 binary, zero runtime dependencies
- Tier 1 overhead under 5ms/request
- Plays nice with autocompact: avoids triggering it instead of fighting it

Tier 1 tool-family compression (deterministic):
- `git status` (88.4%), `git log` (92.8%), `git diff` (90.1%)
- `pytest` (96.1%), `npm/yarn` (89.1%), `cargo` (94.3%), `pip` (87.2%)
- `docker` (85.6%), `ls/find` (83.9%), `make/cmake` (88.7%)
- non-Bash file reads (~90%)

## Quick Start (yes, this is really it)
Install:

```bash
go install github.com/buildoak/wet@latest
```

or build locally:

```bash
git clone https://github.com/buildoak/wet
cd wet
go build -o wet .
```

Run Claude through wet:

```bash
wet claude fix the bug
```

While that session is running:

```bash
wet status
wet inspect
wet pause
wet resume
wet statusline
```

Statusline integration for Claude Code:

```bash
wet install-statusline
```

## How it works (short pipeline, no magic)
1. Session wrapper starts proxy on a random port, exports `ANTHROPIC_BASE_URL`, then execs `claude`.
2. Proxy intercepts `POST /v1/messages` and parses the `messages` array.
3. Each `tool_result` is classified fresh vs stale using turn-distance and per-tool-family thresholds.
4. Stale results get Tier 1 compression (deterministic, fast).
5. Wet writes a tombstone, for example:

```text
[compressed: git_status | 3 files modified | turn 4/12 | 847->62 tokens]
```

6. Bypass rules skip anything risky or pointless: errors, current-turn results, images, tiny outputs, already-compressed blocks.
7. Control plane stays live over Unix socket for runtime inspection/tuning.

## CLI Reference (the commands I actually use)
Session wrapper:

```bash
wet claude [args...]
```

Control-plane commands (`WET_PORT` env var or `--port`):

```bash
wet status
wet inspect
wet inspect --live
wet inspect --live --format table
wet compress --ids id1,id2,id3
wet rules list
wet rules set KEY VALUE
wet pause
wet resume
wet statusline
wet install-statusline
wet uninstall-statusline
```

Session data and diagnostics:

```bash
wet data status
wet data inspect [--all]
wet data diff <turn>
wet session salt
wet session find <SALT>
wet session profile --jsonl <PATH> [--port PORT]
```

Skill helpers:

```bash
wet install-skill [--dir PATH]
wet uninstall-skill [--dir PATH]
```

Help:

```bash
wet --help
wet help
```

## Configuration (optional, sane defaults)
- Default config: `~/.wet/wet.toml`
- `WET_PORT` tells control commands which running proxy to hit
- Per-tool-family staleness thresholds are configurable
- Tier 2 LLM compression exists behind config gates and is disabled by default
- Go version: `1.22`
- Module path: `github.com/buildoak/wet`
- Dependency policy: one dependency (`BurntSushi/toml`), vendored in `third_party/`

## Status (alpha, but not hand-wavy)
Alpha. I dogfood this daily.

Proven today:
- 125 tests passing
- 2 E2E integration tests passing
- Tier 1 deterministic compression in production use

Coming in `v0.2.0+`:
- Tier 2 LLM compression for agent returns (stubbed now, disabled)
- Codex CLI support (`wet codex ...`)
- Homebrew formula (`brew install wet`)
- `--dry-run` mode
- Semantic staleness model

## License
MIT.

P.S. If this saves you from one autocompact wipeout, my itch was worth shipping.
