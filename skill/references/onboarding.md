# wet Statusline -- Onboarding Guide

wet adds a live compression dashboard to Claude Code's status bar. This guide covers how it works, how to install it, and how to troubleshoot it.

---

## How It Works

```
Claude Code  ---->  wet proxy  ---->  Anthropic API
                      |
                      v
              ~/.wet/stats-{port}.json   (updated every request)
                      |
                      v
              ~/.claude/statusline.sh    (reads stats, renders one-liner)
                      |
                      v
              Claude Code status bar     (calls script via settings.json)
```

1. **wet proxies** between Claude Code and the Anthropic API. Every API roundtrip, wet writes session stats (context fill, compression counts, token savings) to `~/.wet/stats-{port}.json`.
2. **A shell script** (`~/.claude/statusline.sh`) reads the stats file and renders a one-line summary with ANSI colors.
3. **Claude Code** invokes the script via the `statusLine` config in `~/.claude/settings.json` and displays the output in its status bar.

---

## Installation

### Quick Start

```bash
wet install-statusline
```

This single command handles everything. It detects your current setup and does the right thing:

| Scenario | What happens |
|----------|-------------|
| No `~/.claude/statusline.sh` exists | Creates the script with a base statusline (model + context) plus the wet section |
| Script exists, no wet section | Injects the wet block between `BEGIN_WET_STATUSLINE` / `END_WET_STATUSLINE` markers before the output line. Your custom script is preserved. |
| Script exists, wet section present | Updates the wet block in place (idempotent) |

The command also sets `statusLine` in `~/.claude/settings.json`:

```json
{
  "statusLine": {
    "type": "command",
    "command": "/Users/you/.claude/statusline.sh"
  }
}
```

If you already have a custom `statusLine` pointing to a different script, wet injects into **that** script.

### After Install

Run `wet claude` instead of `claude` to start Claude Code through the proxy:

```bash
wet claude                    # new session
wet claude --resume           # resume last session
wet claude --resume <UUID>    # resume specific session
```

The status bar updates live as you work.

### Brew Installation Note

If distributing via `brew install wet`, the formula should either:
- Run `wet install-statusline` as a post-install step, or
- Print a caveat telling the user to run it manually

A future `wet setup` command will bundle `install-statusline` + any other first-time configuration into one step.

---

## What the Statusline Shows

The wet section appends to the right of Claude Code's base status. Four possible states:

### No data yet (proxy just started)

```
wet: ready
```

Stats file exists but no API roundtrips have completed.

### Context fill only (passthrough, no compressions)

```
wet: 46% (92.0k/200.0k)
```

Shows what percentage of the context window is filled, with absolute token counts.

### Context fill + compression stats

```
wet: 46% (92.0k/200.0k) | 18/24 results compressed (50.0k->20.0k)
```

- `18/24` -- 18 of 24 tool results were compressed
- `(50.0k->20.0k)` -- total tokens before and after compression

When API-observed savings are available, the format changes to:

```
wet: 46% (92.0k/200.0k) | 18/24 results compressed (saved 30.0k)
```

### Proxy not running

```
wet: sleeping
```

No stats file found in `~/.wet/`. The proxy is not active.

### Token formatting

- Under 1k: raw number (`500`)
- 1k--999k: `X.Xk` (`92.0k`)
- 1M+: `X.XM` (`1.2M`)

---

## Uninstallation

```bash
wet uninstall-statusline
```

This removes the wet section (`BEGIN_WET_STATUSLINE` through `END_WET_STATUSLINE`) from `statusline.sh` and cleans up `${WET_SECTION}` references. The base statusline script stays intact.

The `statusLine` setting in `settings.json` is **not** removed -- your base statusline (model + context) keeps working.

---

## Troubleshooting

### Statusline not showing at all

1. Check that `~/.claude/settings.json` has the `statusLine` key:
   ```bash
   jq .statusLine ~/.claude/settings.json
   ```
2. Verify the script exists and is executable:
   ```bash
   ls -la ~/.claude/statusline.sh
   ```
3. Test the script manually (it expects JSON on stdin):
   ```bash
   echo '{"model":{"display_name":"opus"},"context_window":{"used_percentage":50,"context_window_size":200000}}' | ~/.claude/statusline.sh
   ```
4. Re-run `wet install-statusline` to fix both the script and settings.

### Wrong context window size on first request

The context window size comes from the API response. On the very first request of a session, the stats file may show a default value. It self-corrects after the first API roundtrip.

### Compression summary missing after resume

When resuming a session, wet starts with a cold stats file. Compression counts appear after the first tool call that triggers compression. Historical compression data from the previous session is not carried over to the statusline (though it is recorded in the session JSONL).

### Stats file not updating

Check that `wet claude` is running (not bare `claude`). The stats file is only written by the wet proxy:
```bash
ls -la ~/.wet/stats-*.json
```

If running multiple wet instances, each writes to its own `stats-{port}.json`. The statusline script auto-discovers the most recently modified one, or you can set `WET_PORT` to pin to a specific instance.

---

## File Reference

| File | Purpose |
|------|---------|
| `~/.wet/stats-{port}.json` | Per-request stats snapshot, written by the proxy |
| `~/.claude/statusline.sh` | Shell script that renders the statusline |
| `~/.claude/settings.json` | Claude Code config, `statusLine` key points to the script |
| `cli/install_statusline.go` | Install/uninstall logic, base script template, wet block template |
| `stats/statusline.go` | Go-side `RenderStatusline()` for `wet statusline` CLI command |
| `stats/writer.go` | `RequestStats` struct, stats file writer |
