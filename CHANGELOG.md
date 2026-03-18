# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.5] - 2026-03-18

### Fixed

- **Proxy now extracts token usage from non-streaming (JSON) API responses.** Claude Code ≥2.1.78 sends a non-streaming pre-flight request before the streaming request; the proxy previously only parsed SSE streams, so usage was zero for the pre-flight. Both SSE and JSON responses are now intercepted.
- **Statusline no longer goes blank when Claude Desktop app is running.** CC ≥2.1.77 routes API traffic through the desktop app via IPC, bypassing `ANTHROPIC_BASE_URL`. Quit the desktop app before starting `wet claude` sessions so the proxy can intercept traffic. (See README for details.)
- `session profile` context window now uses model-specific lookup (1M for opus/sonnet-4, 200k for haiku) instead of hardcoded 200k; queries `/_wet/status` when `--port` is specified
- `session profile` token counts now use API ground truth (`input_tokens + cache_creation_input_tokens + cache_read_input_tokens`) from the last assistant message instead of the `chars/4 * 2.3` heuristic that overcounted by ~60%; per-category breakdown uses proportional character scaling against the API total

## [0.1.4] - 2026-03-16

### Added

- Phase 3.5 batching for 15+ item compression sessions (sequential subagent batches of 10-12)
- CONTRIBUTING.md — getting started, development workflow, skill development, PR guidelines
- shields.io badges to README (release, Go, license, stars)
- Animated demo (webp) in README, replacing static dashboard image
- Anthropic Terms of Service compliance section in README with subscription safety analysis
- Known Behaviors section documenting ToolSearch / deferred tool loading behavior
- Onboarding reference doc (`references/onboarding.md`) now embedded and installed with skill
- Heuristics customization note in README pointing to `skill/references/heuristics.md`

### Changed

- Compression threshold lowered from 30% to 10% — no hard block above 10%
- Compression framed as context hygiene, not emergency cleanup
- Tier 2 default model upgraded from `claude-haiku-3` to `claude-sonnet-4-6-20250514`
- Bypass threshold lowered from 200 to 100 tokens (README now matches `config.go` default)
- Config example in README corrected: default mode is `passthrough`, not `auto`
- ToolSearch advisory: recommend accepting eager-load overhead rather than using `ENABLE_TOOL_SEARCH` flag

## [0.1.3] - 2026-03-15

### Fixed

- LICENSE year corrected to 2026

### Changed

- Race detection enabled in CI
- Homebrew install promoted to Quick Start, removed from Roadmap
- TOML parser restructured as `internal/toml` package, fixing `go install` compatibility

## [0.1.2] - 2026-03-15

### Added

- Goreleaser config and GitHub release workflow
- Quick Start section in README with agent-first install guidance
- Full README rework: How It Works architecture diagram, Tier 2 meta-aware logic, Roadmap
- Real session data in profiling table (replacing mock data)

### Fixed

- Statusline showing fleet-total instead of main-session item count
- Statusline showing 200k context window on startup (now seeds 1M)
- `TestSSEUsageWithGzipUpstream` flake on Go 1.22 CI

### Changed

- Homebrew tap token wired for cross-repo formula push
- Version injection via ldflags for goreleaser
- Statusline progression examples restored with best-effort disclaimer

## [0.1.1] - 2026-03-14

### Added

- CLI tests and `--port` flag normalization across all commands
- Rule 0: `ALREADY_COMPRESSED` check before all classification in skill
- Onboarding doc moved to `skill/references/`

### Fixed

- Passthrough mode undercounting tool results
- Pause mode skipping accounting and persistence
- Stats file write race in `WriteStatsFile`
- Queue overwrite race in compress control plane

### Removed

- Dead code: unused metrics, helpers, and methods
- AI-generated assets and marketing copy

### Changed

- Agent/Task tool results guarded from auto-mode compression

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

[Unreleased]: https://github.com/buildoak/wet/compare/v0.1.5...HEAD
[0.1.5]: https://github.com/buildoak/wet/compare/v0.1.4...v0.1.5
[0.1.4]: https://github.com/buildoak/wet/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/buildoak/wet/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/buildoak/wet/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/buildoak/wet/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/buildoak/wet/releases/tag/v0.1.0
