# Contributing to wet

wet is early-stage and welcomes contributors. Whether it's a bug fix, a new compression heuristic, or a documentation tweak — all useful.

## Getting Started

**Requirements:** Go 1.22+

```bash
git clone https://github.com/buildoak/wet.git
cd wet
go build -o wet .
go test ./...
```

Zero external dependencies — `go.mod` has no `require` block. The TOML parser lives in `internal/toml`.

## Development

Run locally:

```bash
# Run directly from source
go run . claude --dangerously-skip-permissions

# Install the skill for testing
go run . install-skill

# Run tests with race detection (mirrors CI)
go test -race ./...
```

Useful commands while developing:

```bash
go run . status          # check proxy state
go run . inspect --json  # see all tracked tool results
go run . ps              # list active sessions
```

## What to Contribute

**Good first issues:**
- Add a new Tier 1 compressor for a tool family (look at existing ones in `compressor/` for the pattern)
- Improve compression ratios on specific output types
- Better error messages for common misconfigurations

**Areas that need help:**
- New compression heuristics — especially for tool families not yet covered
- Tier classification improvements — smarter staleness detection beyond turn-counting
- Performance profiling and optimization
- Cross-platform testing (Linux, Windows/WSL)
- Integration testing with different Claude Code workflows

## Pull Requests

- Branch from `main`
- One feature per PR
- Write tests for new heuristics — see existing tests in `compressor/` for examples
- Describe the **why**, not just the what. What session behavior prompted this change?
- Keep commits atomic with clear messages

## Code Style

- `gofmt` — non-negotiable
- Meaningful variable names, not single letters (except loop indices)
- Comments on non-obvious logic — especially compression heuristics where the "why" matters more than the "what"
- No external dependencies unless absolutely necessary. The zero-dependency constraint is intentional.

## Skill Development

The `skill/` directory contains the Claude Code skill that teaches Claude the meta game:

```
skill/
  SKILL.md                      # Main skill — phases, rules, prompts
  embed.go                      # go:embed directive, lists all embedded files
  references/
    architecture.md             # Technical reference for the proxy
    heuristics.md               # Compression heuristics and thresholds
    onboarding.md               # First-session guidance
```

Files are embedded via `go:embed` in `skill/embed.go` and installed by `wet install-skill`. If you add a new reference file:

1. Create it in `skill/references/`
2. Add the path to the `skillFiles` slice in `cli/install_skill.go`
3. Add it to the `//go:embed` directive in `skill/embed.go`
4. Test: `go run . install-skill` and verify the file appears in `.claude/skills/wet-compress/`

## Issues

Bug reports need:
- **wet version** (`wet --version`)
- **Claude Code version**
- **OS** and architecture
- **Reproduction steps** — ideally a sequence of commands that triggers the bug
- **Expected vs actual behavior**
- **Relevant logs** — `wet status --json` and `wet inspect --json` output if applicable

Feature requests: describe the session behavior you want to improve and why existing heuristics don't handle it.
