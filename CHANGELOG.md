# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-03-13

Initial public release.

### Added

- **Transparent API proxy** — sits between Claude Code and `api.anthropic.com` via `ANTHROPIC_BASE_URL`, compresses stale tool results on-the-fly
- **Session wrapper** — `wet claude "fix the bug"` starts proxy, runs Claude Code, cleans up on exit
- **Tier 1 compression** — deterministic compressors for 10 tool families (git, pytest, npm, cargo, pip, docker, ls/find, make/cmake, read), ported from Rust
- **Generic compressor** — signal extraction + hard cap fallback for unrecognized tool output
- **Turn-distance staleness model** — per-tool-family `stale_after` thresholds with configurable defaults
- **Bypass rules** — never compresses errors, current-turn results, images, small outputs, or already-compressed items
- **Tombstone format** — `[compressed: family | summary | turn N/M | X->Y tokens]`
- **Unix socket control plane** — `wet status`, `wet inspect`, `wet pause`, `wet resume`, `wet rules`
- **Statusline integration** — `wet statusline` for Claude Code status bar, `wet install-statusline` / `wet uninstall-statusline`
- **Skill management** — `wet install-skill` / `wet uninstall-skill` for Claude Code wet-compress skill (go:embed)
- **Data layer v3** — `session.jsonl` with per-turn records capturing exact API token counts from SSE responses
- **Session commands** — `wet session salt`, `wet session find`, `wet session profile` for offline analysis
- **Data commands** — `wet data status`, `wet data inspect`, `wet data diff` for session inspection
- **Selective compression** — `wet compress --ids` for agent-directed compression via HTTP control plane
- **Configuration** — optional `~/.wet/wet.toml` with per-tool staleness thresholds, bypass rules, server settings

### Fixed

- Record every proxied turn in session.jsonl, not just compression turns (`8f1d61a`)
- Restore compression stats from session history on resume (`cfb9440`)
- Use calibrated token estimation and API-observed savings in statusline (`f6155ba`)

### Performance

- 91.2% average compression on 13,881 SWE-bench tool outputs
- 73.7% compression in E2E integration test (42,678 chars saved)
- 57.7% compression in E2E test #2 (12,484 tokens saved)
- <5ms overhead per request (Tier 1 only)
- 9.3MB arm64 binary, zero runtime dependencies
- 125 tests passing across 8 packages
