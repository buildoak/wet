#!/usr/bin/env python3
"""
profile-context.py — Context composition profiler for Claude Code sessions.

Parses a session JSONL file to compute token estimates per category,
optionally augments with wet proxy inspect data. Outputs an ASCII
composition table suitable for embedding in coordinator reports.

Usage:
    python3 profile-context.py [--jsonl PATH] [--salt SALT] [--wet-port PORT] [--project-dir DIR]

Flags:
    --jsonl PATH        Explicit path to session .jsonl file
    --salt SALT         Salt string to grep for in JSONL files (session self-identification)
    --wet-port PORT     If set, also query wet proxy for tool result breakdown
    --project-dir DIR   Claude projects subdirectory to search for .jsonl files
                        (defaults to auto-detect from ~/.claude/projects/)

Exit codes:
    0   Success
    1   No JSONL file found
    2   Parse error

Dependencies: stdlib only (json, os, sys, pathlib, glob, urllib).
"""

import json
import os
import subprocess
import sys
import glob
from pathlib import Path
from urllib.request import urlopen, Request
from urllib.error import URLError

# ---------------------------------------------------------------------------
# Token estimation
# ---------------------------------------------------------------------------
# Calibrated multiplier: naive char/4 underestimates by ~2.3x for mixed
# code/JSON content. So: tokens ≈ len(text) / 4 * 2.3
MULTIPLIER = 2.3


def estimate_tokens(text: str) -> int:
    """Estimate token count from character length."""
    if not text:
        return 0
    return int(len(text) / 4 * MULTIPLIER)


# ---------------------------------------------------------------------------
# JSONL discovery
# ---------------------------------------------------------------------------

def find_jsonl_by_salt(salt: str, project_dir: str | None = None) -> str | None:
    """Find the session JSONL containing the given salt string via grep.

    Shells out to grep -rl for speed — JSONL files can be multi-MB and there
    may be many of them. Returns the first matching file path, or None.
    """
    base = Path.home() / ".claude" / "projects"
    if not base.exists():
        print(f"Error: Claude projects directory not found: {base}", file=sys.stderr)
        return None

    search_path = str(base / project_dir) if project_dir else str(base)

    try:
        result = subprocess.run(
            ["grep", "-rl", salt, "--include=*.jsonl", search_path],
            capture_output=True, text=True, timeout=30,
        )
        matches = result.stdout.strip().splitlines()
        if matches:
            return matches[0]
    except (subprocess.TimeoutExpired, OSError) as e:
        print(f"Warning: salt search failed: {e}", file=sys.stderr)

    return None


def find_jsonl(explicit_path: str | None = None, project_dir: str | None = None) -> str | None:
    """Find the most recently modified .jsonl session file."""
    if explicit_path:
        p = Path(explicit_path)
        if p.exists():
            return str(p)
        print(f"Error: explicit JSONL path does not exist: {explicit_path}", file=sys.stderr)
        return None

    base = Path.home() / ".claude" / "projects"
    if not base.exists():
        print(f"Error: Claude projects directory not found: {base}", file=sys.stderr)
        return None

    if project_dir:
        search_dirs = [base / project_dir]
    else:
        # Search all project subdirs
        search_dirs = [d for d in base.iterdir() if d.is_dir()]

    candidates = []
    for d in search_dirs:
        for f in d.glob("*.jsonl"):
            candidates.append(f)

    if not candidates:
        print("Error: no .jsonl files found in Claude projects", file=sys.stderr)
        return None

    # Most recently modified
    candidates.sort(key=lambda p: p.stat().st_mtime, reverse=True)
    return str(candidates[0])


# ---------------------------------------------------------------------------
# JSONL parsing
# ---------------------------------------------------------------------------

def extract_text_from_content(content) -> str:
    """Extract raw text from a content field (string, list of blocks, etc.)."""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts = []
        for block in content:
            if isinstance(block, str):
                parts.append(block)
            elif isinstance(block, dict):
                # text blocks
                if block.get("type") == "text":
                    parts.append(block.get("text", ""))
                # image blocks — count the base64 data
                elif block.get("type") == "image":
                    source = block.get("source", {})
                    parts.append(source.get("data", ""))
        return "".join(parts)
    return ""


def get_tool_name_for_id(tool_use_map: dict, tool_use_id: str) -> str:
    """Look up the tool name for a given tool_use_id."""
    return tool_use_map.get(tool_use_id, "unknown")


def parse_jsonl(path: str) -> dict:
    """
    Parse a session JSONL and return aggregated token stats.

    Returns dict with:
        total_tokens: int
        categories: dict of category -> {tokens: int, count: int}
        tool_results_by_name: dict of tool_name -> {tokens: int, count: int}
    """
    categories = {
        "user_text": {"tokens": 0, "count": 0},
        "assistant_text": {"tokens": 0, "count": 0},
        "tool_use": {"tokens": 0, "count": 0},
        "tool_result": {"tokens": 0, "count": 0},
    }
    tool_results_by_name: dict[str, dict] = {}

    # First pass: build tool_use_id -> tool_name map
    tool_use_map: dict[str, str] = {}

    lines = []
    with open(path, "r") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
                lines.append(obj)
            except json.JSONDecodeError:
                continue

    # Build tool_use map from assistant messages
    for obj in lines:
        if obj.get("type") != "assistant":
            continue
        msg = obj.get("message", {})
        content = msg.get("content", [])
        if not isinstance(content, list):
            continue
        for block in content:
            if isinstance(block, dict) and block.get("type") == "tool_use":
                tid = block.get("id", "")
                tname = block.get("name", "unknown")
                tool_use_map[tid] = tname

    # Second pass: compute token counts
    for obj in lines:
        msg_type = obj.get("type")
        if msg_type not in ("user", "assistant"):
            continue

        msg = obj.get("message", {})
        content = msg.get("content", [])

        if msg_type == "user":
            if isinstance(content, str):
                tokens = estimate_tokens(content)
                categories["user_text"]["tokens"] += tokens
                categories["user_text"]["count"] += 1
            elif isinstance(content, list):
                for block in content:
                    if isinstance(block, str):
                        tokens = estimate_tokens(block)
                        categories["user_text"]["tokens"] += tokens
                        categories["user_text"]["count"] += 1
                    elif isinstance(block, dict):
                        btype = block.get("type", "")
                        if btype == "tool_result":
                            tool_use_id = block.get("tool_use_id", "")
                            tool_name = get_tool_name_for_id(tool_use_map, tool_use_id)
                            raw = extract_text_from_content(block.get("content", ""))
                            tokens = estimate_tokens(raw)
                            categories["tool_result"]["tokens"] += tokens
                            categories["tool_result"]["count"] += 1
                            if tool_name not in tool_results_by_name:
                                tool_results_by_name[tool_name] = {"tokens": 0, "count": 0}
                            tool_results_by_name[tool_name]["tokens"] += tokens
                            tool_results_by_name[tool_name]["count"] += 1
                        elif btype == "text":
                            tokens = estimate_tokens(block.get("text", ""))
                            categories["user_text"]["tokens"] += tokens
                            categories["user_text"]["count"] += 1

        elif msg_type == "assistant":
            if not isinstance(content, list):
                if isinstance(content, str):
                    tokens = estimate_tokens(content)
                    categories["assistant_text"]["tokens"] += tokens
                    categories["assistant_text"]["count"] += 1
                continue

            for block in content:
                if isinstance(block, str):
                    tokens = estimate_tokens(block)
                    categories["assistant_text"]["tokens"] += tokens
                    categories["assistant_text"]["count"] += 1
                elif isinstance(block, dict):
                    btype = block.get("type", "")
                    if btype == "text":
                        tokens = estimate_tokens(block.get("text", ""))
                        categories["assistant_text"]["tokens"] += tokens
                        categories["assistant_text"]["count"] += 1
                    elif btype == "tool_use":
                        # Serialize the input to estimate its size
                        inp = block.get("input", {})
                        raw = json.dumps(inp)
                        tokens = estimate_tokens(raw)
                        categories["tool_use"]["tokens"] += tokens
                        categories["tool_use"]["count"] += 1
                    elif btype == "thinking":
                        # Thinking blocks are redacted in JSONL — skip
                        pass

    total = sum(c["tokens"] for c in categories.values())
    return {
        "total_tokens": total,
        "categories": categories,
        "tool_results_by_name": tool_results_by_name,
    }


# ---------------------------------------------------------------------------
# Wet inspect (optional)
# ---------------------------------------------------------------------------

def fetch_wet_inspect(port: int) -> dict | None:
    """Fetch wet proxy inspect data. Returns parsed JSON or None."""
    url = f"http://127.0.0.1:{port}/_wet/inspect"
    try:
        req = Request(url, method="GET")
        with urlopen(req, timeout=5) as resp:
            return json.loads(resp.read().decode())
    except (URLError, OSError, json.JSONDecodeError) as e:
        print(f"Warning: could not reach wet proxy on port {port}: {e}", file=sys.stderr)
        return None


def summarize_wet_inspect(data) -> dict:
    """Summarize wet inspect response into per-tool-name token totals."""
    if not isinstance(data, list):
        return {}
    by_name: dict[str, dict] = {}
    total = 0
    for entry in data:
        name = entry.get("tool_name", "unknown")
        tokens = entry.get("token_count", 0)
        if name not in by_name:
            by_name[name] = {"tokens": 0, "count": 0}
        by_name[name]["tokens"] += tokens
        by_name[name]["count"] += 1
        total += tokens
    return {"by_name": by_name, "total_tokens": total, "total_count": len(data)}


# ---------------------------------------------------------------------------
# Output formatting
# ---------------------------------------------------------------------------

CONTEXT_WINDOW = 200_000  # Claude's context window

# Canonical display order for tool result sub-categories
TOOL_DISPLAY_ORDER = ["Agent", "Read", "Bash", "Grep", "Glob", "WebFetch", "WebSearch", "Edit", "Write", "NotebookEdit"]


def fmt_k(n: int) -> str:
    """Format token count as Xk or X."""
    if n >= 1000:
        return f"{n / 1000:.0f}k"
    return str(n)


def fmt_pct(part: int, total: int) -> str:
    """Format percentage."""
    if total == 0:
        return "0.0%"
    return f"{part / total * 100:.1f}%"


def health_assessment(pct: float) -> str:
    """Return health label based on context fullness percentage."""
    if pct < 40:
        return "healthy"
    elif pct < 60:
        return "growing"
    elif pct < 80:
        return "heavy"
    else:
        return "critical"


def render_table(stats: dict, wet_summary: dict | None = None) -> str:
    """Render the ASCII composition table."""
    total = stats["total_tokens"]
    pct_full = total / CONTEXT_WINDOW * 100 if CONTEXT_WINDOW > 0 else 0
    health = health_assessment(pct_full)

    lines = []
    lines.append(f"CONTEXT COMPOSITION — {fmt_k(total)} / {fmt_k(CONTEXT_WINDOW)} ({pct_full:.0f}% full)")
    lines.append("═" * 51)

    # Header
    lines.append(f"{'Category':<30} {'Tokens':>8}  {'%':>6}")
    lines.append("─" * 51)

    cats = stats["categories"]
    tr_by_name = stats["tool_results_by_name"]

    # Tool results (total)
    tr = cats["tool_result"]
    lines.append(f"{'Tool results (total)':<30} {fmt_k(tr['tokens']):>8}  {fmt_pct(tr['tokens'], total):>6}")

    # Sub-breakdown by tool name
    # Sort: canonical order first, then alphabetical for the rest
    seen = set()
    ordered_names = []
    for name in TOOL_DISPLAY_ORDER:
        if name in tr_by_name:
            ordered_names.append(name)
            seen.add(name)
    for name in sorted(tr_by_name.keys()):
        if name not in seen:
            ordered_names.append(name)

    for name in ordered_names:
        info = tr_by_name[name]
        label = f"  └─ {name} results ({info['count']})"
        lines.append(f"{label:<30} {fmt_k(info['tokens']):>8}  {fmt_pct(info['tokens'], total):>6}")

    # Tool_use blocks
    tu = cats["tool_use"]
    lines.append(f"{'Tool_use blocks (' + str(tu['count']) + ')':<30} {fmt_k(tu['tokens']):>8}  {fmt_pct(tu['tokens'], total):>6}")

    # Assistant text
    at = cats["assistant_text"]
    lines.append(f"{'Assistant text (' + str(at['count']) + ')':<30} {fmt_k(at['tokens']):>8}  {fmt_pct(at['tokens'], total):>6}")

    # User text
    ut = cats["user_text"]
    lines.append(f"{'User text (' + str(ut['count']) + ')':<30} {fmt_k(ut['tokens']):>8}  {fmt_pct(ut['tokens'], total):>6}")

    lines.append("─" * 51)

    # Health assessment
    recommend = ""
    if health == "heavy":
        recommend = " — recommend compression"
    elif health == "critical":
        recommend = " — compress aggressively"
    lines.append(f"Context at {pct_full:.0f}% — {health}{recommend}")

    # Wet inspect addendum (if available)
    if wet_summary and wet_summary.get("by_name"):
        lines.append("")
        lines.append(f"WET TRACKED: {wet_summary['total_count']} tool results, {fmt_k(wet_summary['total_tokens'])} tracked tokens")
        for name in TOOL_DISPLAY_ORDER:
            if name in wet_summary["by_name"]:
                info = wet_summary["by_name"][name]
                lines.append(f"  └─ {name}: {info['count']} results, {fmt_k(info['tokens'])}")
        for name in sorted(wet_summary["by_name"].keys()):
            if name not in TOOL_DISPLAY_ORDER:
                info = wet_summary["by_name"][name]
                lines.append(f"  └─ {name}: {info['count']} results, {fmt_k(info['tokens'])}")

    return "\n".join(lines)


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def main():
    import argparse

    parser = argparse.ArgumentParser(description="Profile Claude Code session context composition")
    parser.add_argument("--jsonl", default=None, help="Explicit path to session .jsonl file")
    parser.add_argument("--salt", default=None, help="Salt string to grep for in JSONL files (session self-identification)")
    parser.add_argument("--wet-port", type=int, default=None, help="Wet proxy port for inspect data")
    parser.add_argument("--project-dir", default=None, help="Claude projects subdirectory name")
    args = parser.parse_args()

    # Find JSONL — priority: explicit path > salt search > auto-detect
    jsonl_path = None
    if args.jsonl:
        jsonl_path = find_jsonl(args.jsonl, args.project_dir)
    elif args.salt:
        jsonl_path = find_jsonl_by_salt(args.salt, args.project_dir)
        if not jsonl_path:
            print(f"Warning: no JSONL found containing salt '{args.salt}', falling back to auto-detect", file=sys.stderr)
            jsonl_path = find_jsonl(None, args.project_dir)
    else:
        jsonl_path = find_jsonl(None, args.project_dir)

    if not jsonl_path:
        print("Error: could not find session JSONL file", file=sys.stderr)
        sys.exit(1)

    print(f"Session: {Path(jsonl_path).name}", file=sys.stderr)

    # Parse
    try:
        stats = parse_jsonl(jsonl_path)
    except Exception as e:
        print(f"Error parsing JSONL: {e}", file=sys.stderr)
        sys.exit(2)

    # Optional: wet inspect
    wet_summary = None
    if args.wet_port:
        wet_data = fetch_wet_inspect(args.wet_port)
        if wet_data is not None:
            wet_summary = summarize_wet_inspect(wet_data)

    # Render
    table = render_table(stats, wet_summary)
    print(table)


if __name__ == "__main__":
    main()
