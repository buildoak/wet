---
project: wet
status: plan
date: 2026-03-09
execution: single-gsd-run
---

# wet — Execution Plan

## Scope

Claude Code only (v1). Single binary Go proxy. Tier 1 deterministic compression in-process.
Tier 2 LLM compression stubbed but not enabled by default.
Codex wrapping (`wet codex ...`) is v2.

## Audit Notes

**From spec review against Rust source and prior plan:**

1. **Rust Tier naming vs SPEC Tier naming.** The Rust binary has 3 tiers: Tier 1 (pattern-matched compressors per tool family), Tier 2 (generic signal extraction — head/tail/signal lines + dedup), Tier 3 (hard cap — head 30 + tail 10). The SPEC has 2 tiers: Tier 1 (pattern, same as Rust Tier 1) and Tier 2 (LLM-based, totally different). We port Rust Tiers 1+2+3 into Go as `wet`'s Tier 1 engine. SPEC's Tier 2 (LLM) is Phase 8, config-gated.

2. **`compress_read_output` in Rust.** The Rust compressor has a separate path for non-Bash tools (Read/Write) — truncates to 100 lines if >1000 tokens. The SPEC doesn't list "read" as a Tier 1 family but has `[rules.read]` with `stale_after: 3`. We must port this to Go. Each phase prompt must be aware that compression targets both Bash tool outputs AND Read/Write tool outputs.

3. **Token threshold mismatch.** Rust binary skips Bash outputs <500 tokens. SPEC sets `min_tokens: 100`. These serve different contexts — Rust fires at ingestion time (every tool call), `wet` fires at API request time (only stale blocks). Lower threshold for `wet` is fine because we only pay the cost on stale content. Keep SPEC's 100.

4. **Rust Tier 2 generic compressor is not LLM-based.** It's a deterministic signal extractor (head 15 lines + tail 10 + signal lines + dedup). This is valuable and must be ported to Go as the fallback when no Tier 1 family matches but output is large (>1500 tokens in Rust). This lives alongside the pattern matchers in the Go code.

5. **SSE streaming is the highest-risk component.** The proxy must forward SSE events chunk-by-chunk without buffering. Go's `httputil.ReverseProxy` does this naturally with `FlushInterval: -1`, but we need to verify it works with Anthropic's chunked transfer encoding. Phase 1 must prove this works before we add any compression.

6. **Staleness classifier needs tool_use_id linkage.** Each `tool_result` has a `tool_use_id` that matches a `tool_use` block in the preceding `assistant` message. The classifier must walk messages, find the `tool_use` block to extract the tool name and command, then tag the `tool_result` with turn number and tool family. This linkage is the most complex parsing in the system.

7. **Request body can be large.** 50-turn sessions produce 500KB-2MB JSON request bodies. Go's `json.Unmarshal` handles this fine but we should avoid unnecessary allocations. Parse into `map[string]any` for the messages array, leave everything else as raw bytes (use `json.RawMessage`).

8. **Config file is TOML in SPEC, was YAML in old plan.** SPEC says `wet.toml`. Use `github.com/BurntSushi/toml`.

## GSD Execution Strategy

**Single GSD coordinator dispatches 8 phases sequentially.** Each phase = one worker. Workers are stateless — they receive a self-contained prompt with all context needed (no memory of previous phases).

**Worker engine**: Codex (GPT-5.4 xhigh) for Phases 1-6 and 8 (deterministic code generation). Claude (Opus 4.6) for Phase 7 (integration testing requires judgment).

**Parallelization**: Phases are strictly sequential — each builds on the previous phase's output. No parallelism within the GSD run. The coordinator verifies each phase's output before dispatching the next.

**Repo path**: `/Users/otonashi/thinking/building/compact-sessions/`
**Go module**: `github.com/otonashi/wet`
**Go version**: 1.22+

**Verification protocol**: After each phase, the GSD coordinator runs `go build ./... && go test ./...` and checks for compilation errors. If tests fail, the coordinator retries the phase with error context appended to the prompt.

**Worker sandbox**: `--sandbox workspace-write --cwd /Users/otonashi/thinking/building/compact-sessions`

## Phase 1: Proxy Skeleton + SSE Streaming

**Worker**: Codex (GPT-5.4 xhigh)
**Goal**: Working pass-through proxy that forwards `/v1/messages` unchanged. SSE streaming works end-to-end.

### Prompt

```
You are building a transparent HTTP reverse proxy for LLM API calls in Go.

Project path: /Users/otonashi/thinking/building/compact-sessions/
Go module: github.com/otonashi/wet

Create this file structure:

compact-sessions/
  go.mod                    # module github.com/otonashi/wet, go 1.22
  go.sum
  main.go                   # entry point, flag parsing, starts server
  proxy/
    proxy.go                # reverse proxy, request handler, SSE forwarding
    proxy_test.go           # tests
  config/
    config.go               # config struct, defaults, TOML loading
    config_test.go

## main.go

package main

import (
    "flag"
    "fmt"
    "log"
    "os"
    "os/signal"
    "syscall"

    "github.com/otonashi/wet/config"
    "github.com/otonashi/wet/proxy"
)

func main() {
    port := flag.Int("port", 0, "proxy port (0 = use config default)")
    configPath := flag.String("config", "", "path to wet.toml")
    flag.Parse()

    cfg := config.Load(*configPath)
    if *port != 0 {
        cfg.Server.Port = *port
    }

    srv := proxy.New(cfg)

    // Graceful shutdown
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
    go func() {
        <-sigCh
        fmt.Fprintln(os.Stderr, "[wet] shutting down...")
        srv.Shutdown()
    }()

    addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
    fmt.Fprintf(os.Stderr, "[wet] listening on %s, upstream: %s\n", addr, cfg.Server.Upstream)
    if err := srv.ListenAndServe(); err != nil {
        log.Fatal(err)
    }
}

## config/config.go

Define these structs and load from TOML:

type Config struct {
    Server      ServerConfig
    Staleness   StalenessConfig
    Compression CompressionConfig
    Rules       map[string]RuleConfig
    Bypass      BypassConfig
}

type ServerConfig struct {
    Host     string `toml:"host"`      // default "127.0.0.1"
    Port     int    `toml:"port"`      // default 8100
    Upstream string `toml:"upstream"`  // default "https://api.anthropic.com"
}

type StalenessConfig struct {
    Threshold   int `toml:"threshold"`    // default 2
    TokenBudget int `toml:"token_budget"` // default 0 (disabled)
}

type CompressionConfig struct {
    MinTokens int          `toml:"min_tokens"` // default 100
    Tier1     Tier1Config  `toml:"tier1"`
    Tier2     Tier2Config  `toml:"tier2"`
}

type Tier1Config struct {
    Enabled bool `toml:"enabled"` // default true
}

type Tier2Config struct {
    Enabled   bool   `toml:"enabled"`    // default false
    Model     string `toml:"model"`      // default "claude-haiku-3"
    MinTokens int    `toml:"min_tokens"` // default 500
    TimeoutMs int    `toml:"timeout_ms"` // default 2000
}

type RuleConfig struct {
    Strategy   string `toml:"strategy"`    // "tier1", "tier2", "none"
    StaleAfter int    `toml:"stale_after"` // override staleness threshold
    Keep       string `toml:"keep"`        // hint for compressor
}

type BypassConfig struct {
    PreserveErrors  bool     `toml:"preserve_errors"`  // default true
    MinTokens       int      `toml:"min_tokens"`       // default 100
    ContentPatterns []string `toml:"content_patterns"`  // default error patterns
}

func Load(path string) *Config: if path is empty, try "./wet.toml", then "~/.wet/wet.toml", then use defaults. Use github.com/BurntSushi/toml.
func Default() *Config: returns config with all defaults filled in.

## proxy/proxy.go

Build the reverse proxy:

type Server struct {
    cfg       *config.Config
    httpSrv   *http.Server
    startTime time.Time
    stats     Stats  // atomic counters for requests, bytes, etc.
}

type Stats struct {
    RequestCount  atomic.Int64
    // ... extend later
}

func New(cfg *config.Config) *Server
func (s *Server) ListenAndServe() error
func (s *Server) Shutdown()

Request handling:
1. GET /health -> return JSON {"status":"ok","uptime_seconds":float}
2. All other paths -> reverse proxy to cfg.Server.Upstream

Use httputil.ReverseProxy with these settings:
- Director: rewrite scheme/host to upstream, copy all headers
- FlushInterval: -1 (flush every chunk immediately — critical for SSE)
- ModifyResponse: log one-line summary to stderr (method, path, status, latency)
- ErrorHandler: return 502 with JSON error body

The proxy must NOT buffer streaming responses. FlushInterval: -1 ensures immediate flush.
The proxy must copy these request headers: Authorization, x-api-key, anthropic-version, content-type, anthropic-beta.
The proxy must forward the response status code and headers unchanged.

## proxy/proxy_test.go

Tests:
1. TestHealthEndpoint: start proxy with httptest.NewServer, GET /health, verify 200 + JSON with "status":"ok"
2. TestPassthrough: mock upstream (httptest.NewServer returns a canned JSON response), send POST /v1/messages through proxy, verify body forwarded unchanged, verify response body matches
3. TestHeadersForwarded: verify Authorization, x-api-key, anthropic-version headers are passed to upstream
4. TestSSEStreaming: mock upstream that sends 3 SSE events with 100ms delays between them, verify proxy forwards each event without waiting for the full response (check that first event arrives before third is sent)

Dependencies: github.com/BurntSushi/toml

After creating all files, run:
  cd /Users/otonashi/thinking/building/compact-sessions && go mod tidy && go build ./... && go test ./... -v -count=1

Return: file paths created, test results, any issues encountered.
```

### Verification
- `go build ./...` succeeds
- `go test ./...` all pass
- SSE streaming test proves no buffering (first event arrives before last is sent)
- `GET /health` returns 200

---

## Phase 2: Message Parsing + Staleness Classifier

**Worker**: Codex (GPT-5.4 xhigh)
**Goal**: Parse the messages array from API requests, identify `tool_result` blocks, link them to `tool_use` blocks, assign turn numbers, classify stale/fresh.

### Prompt

```
You are adding message parsing and staleness classification to the wet proxy at /Users/otonashi/thinking/building/compact-sessions/.

Go module: github.com/otonashi/wet

The proxy skeleton is already working (main.go, proxy/, config/). You are adding message parsing.

Create these files:

## messages/parse.go

package messages

Purpose: Parse the Anthropic API messages array, extract tool_result blocks, link them to tool_use blocks, assign turn numbers.

The Anthropic API request body has this structure:
{
  "model": "claude-sonnet-4-20250514",
  "max_tokens": 8096,
  "system": "...",
  "messages": [
    {"role": "user", "content": "fix the bug"},
    {"role": "assistant", "content": [
      {"type": "text", "text": "I'll look at the code."},
      {"type": "tool_use", "id": "toolu_abc", "name": "Bash", "input": {"command": "git status"}}
    ]},
    {"role": "user", "content": [
      {"type": "tool_result", "tool_use_id": "toolu_abc", "content": "On branch main\n..."}
    ]},
    {"role": "assistant", "content": [
      {"type": "text", "text": "I see the changes."}
    ]},
    {"role": "user", "content": "looks good, ship it"}
  ]
}

Key data types:

// Request is a partial parse of the API request — only what we need.
// Everything else is preserved as json.RawMessage.
type Request struct {
    Messages []Message       `json:"messages"`
    Rest     map[string]json.RawMessage  // all other fields preserved verbatim
}

type Message struct {
    Role    string          `json:"role"`
    Content json.RawMessage `json:"content"` // string or []ContentBlock
}

type ContentBlock struct {
    Type      string          `json:"type"`
    Text      string          `json:"text,omitempty"`
    ID        string          `json:"id,omitempty"`         // tool_use
    Name      string          `json:"name,omitempty"`       // tool_use
    Input     json.RawMessage `json:"input,omitempty"`      // tool_use
    ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result
    Content   json.RawMessage `json:"content,omitempty"`    // tool_result (can be string or []ContentBlock)
    IsError   bool            `json:"is_error,omitempty"`   // tool_result
}

func ParseRequest(body []byte) (*Request, error)
  - Unmarshal into a map[string]json.RawMessage first
  - Extract and parse "messages" key into []Message
  - Store everything else in Rest
  - Return Request

func (r *Request) Marshal() ([]byte, error)
  - Reconstruct the full JSON with modified messages + all Rest fields

func ParseContent(raw json.RawMessage) ([]ContentBlock, bool, error)
  - If raw is a JSON string: return a single text ContentBlock, isString=true
  - If raw is a JSON array: unmarshal into []ContentBlock, isString=false
  - Handle both formats (Anthropic API accepts both)

## messages/staleness.go

package messages

// ToolResultInfo holds parsed metadata about a tool_result block
type ToolResultInfo struct {
    ToolUseID    string
    ToolName     string // from matching tool_use block (e.g. "Bash", "Read", "Write")
    Command      string // extracted from tool_use input.command (Bash only)
    Turn         int    // which assistant turn produced this tool_use
    Stale        bool   // true if turn distance >= threshold
    IsError      bool   // from tool_result.is_error
    Content      string // the text content of the tool_result
    TokenCount   int    // estimated tokens (len/4)
    MsgIdx       int    // index in messages array
    BlockIdx     int    // index in content blocks array
    ContentIsStr bool   // true if the message content was a plain string (not array)
}

func ClassifyStaleness(msgs []Message, threshold int, rules map[string]config.RuleConfig) []ToolResultInfo
  Algorithm:
  1. Build a map of tool_use_id -> {tool_name, command, turn} by walking messages:
     - Maintain a turn counter, starting at 0
     - For each message with role "assistant": increment turn counter. Parse content blocks.
       For each block with type "tool_use": store {id, name, input.command (if Bash), turn}
  2. Determine current_turn = the final turn counter value
  3. Walk messages again. For each message with role "user":
     - Parse content blocks
     - For each block with type "tool_result":
       - Look up tool_use_id in the map to get tool_name, command, turn
       - Determine effective stale_after: check rules[tool_family].StaleAfter, fall back to threshold
       - Set stale = (current_turn - turn) >= effective_stale_after
       - Extract text content from the tool_result content field (may be string or array of content blocks)
       - Compute token count = len(content) / 4
       - Build ToolResultInfo and add to result list
  4. Return the list

func ExtractToolFamily(toolName, command string) string
  - If toolName == "Bash":
    - "git status" or "git" -> "git_status"
    - "git log" -> "git_log"
    - "git diff" -> "git_diff"
    - "npm install" or "npm run" -> "npm"
    - "cargo build" or "cargo test" -> "cargo"
    - "pip install" -> "pip"
    - "docker logs" -> "docker"
    - "ls" or "find" -> "ls_find"
    - "make" or "cmake" -> "make"
    - "pytest" or "python -m pytest" -> "pytest"
    - anything else -> "bash_generic"
  - If toolName == "Read" or "Write" -> "read"
  - Else -> "unknown"

func EstimateTokens(s string) int { return len(s) / 4 }

## messages/parse_test.go

Test ParseRequest with:
1. Simple request with string content: {"messages":[{"role":"user","content":"hello"}]}
2. Request with tool_use and tool_result blocks (multi-turn conversation)
3. Verify Rest fields are preserved (model, max_tokens, system)
4. Verify Marshal() round-trips correctly (parse then marshal produces equivalent JSON)

## messages/staleness_test.go

Test ClassifyStaleness with:
1. Empty messages -> empty result
2. Single turn (1 assistant + 1 tool_result) -> tool_result is fresh (current_turn - turn = 0)
3. Three turns, tool_result from turn 1 with threshold=2 -> stale
4. Three turns, tool_result from turn 2 with threshold=2 -> fresh (distance = 1)
5. Per-rule override: git_status with stale_after=1, threshold=2 -> stale after 1 turn
6. Multiple tool_results in one user message -> each classified independently
7. Test ExtractToolFamily for all known families

After creating all files, run:
  cd /Users/otonashi/thinking/building/compact-sessions && go mod tidy && go build ./... && go test ./... -v -count=1

Return: file paths created, test results.
```

### Verification
- All tests pass
- ParseRequest correctly handles both string and array content formats
- ClassifyStaleness correctly assigns turn numbers and stale/fresh tags
- ExtractToolFamily maps all 10 tool families correctly

---

## Phase 3: Tier 1 Compressor (Go port)

**Worker**: Codex (GPT-5.4 xhigh)
**Goal**: Port the Rust compression patterns to Go. All 10 tool families + generic signal extraction (Rust Tier 2) + hard cap (Rust Tier 3) + read output truncation.

### Prompt

```
You are porting a Rust CLI output compressor to Go for the wet proxy at /Users/otonashi/thinking/building/compact-sessions/.

Go module: github.com/otonashi/wet

Create these files:

## compressor/tier1.go

package compressor

import (
    "regexp"
    "strings"
    "fmt"
    "sort"
)

// Pre-compiled regexes (package-level vars)
var (
    wordCountRe  = regexp.MustCompile(`(?i)\b\d+\s+(passed|failed|error|warning|test|tests|file|files)\b`)
    testRunnerRe = regexp.MustCompile(`\b(PASS|FAIL|ok|not ok)\b`)
    signalRe     = regexp.MustCompile(`(?i)(error|enoent|fatal|panic|warning|warn|fail|reject|denied|refused|forbidden)`)
    diffAlertRe  = regexp.MustCompile(`(?i)^[+-].*(error|warn)`)
    diffStatRe   = regexp.MustCompile(`(\|\s*\d+)|((files?|insertions?|deletions?)\b)`)
    cargoErrorRe = regexp.MustCompile(`error\[E\d+\]`)
    cargoWarnRe  = regexp.MustCompile(`warning(\[|:)`)
)

func EstimateTokens(s string) int { return len(s) / 4 }

// Tier1Compress attempts pattern-based compression for known tool families.
// Returns compressed output or empty string if no compression applied.
// The family parameter comes from messages.ExtractToolFamily.
func Tier1Compress(family, output string) (string, bool)

Port these functions from the Rust source. Translate line-for-line:

### CompressGitStatus(output string) (string, bool)
- Split output into lines
- Find first line starting with "On branch" or "HEAD detached" (or first non-empty line)
- Collect changed files: lines starting with tab, or containing "modified:", "new file:", "deleted:", "renamed:", "both modified:"
- Build output: summary line + "Changed files: N" + first 20 changed file lines
- If >20 changed files, append "[... N more files]"
- Return trimmed result

### CompressGitLog(output string) (string, bool)
- Walk lines, find "commit " prefixed lines
- For each commit: keep the commit line + first non-empty non-Author/non-Date line (the message)
- Keep at most 15 commits
- Return joined output

### CompressGitDiff(output string) (string, bool)
- Use a sorted set of line indices to keep
- Keep lines matching diffStatRe
- Keep lines starting with "diff --git", "@@", "+", "-" (up to 30 hunk lines)
- Keep lines matching diffAlertRe
- Return kept lines in original order

### CompressNpm(output string) (string, bool)
- Keep lines containing "ERR!" or "WARN"
- If last non-empty line is not already in kept lines, add it (summary line)
- Return joined output

### CompressCargo(output string) (string, bool)
- Keep up to 5 "Compiling" lines
- Keep lines matching cargoErrorRe, cargoWarnRe, containing "test result:", starting with "Finished", starting with "Running"
- Return joined output

### CompressPip(output string) (string, bool)
- Keep lines containing "Successfully installed" or (case-insensitive) "error" or "failed"
- Return joined output

### CompressDockerLogs(output string) (string, bool)
- Keep last 20 lines always
- Also keep any line containing (case-insensitive) "error", "warn", "fatal"
- Use sorted set to maintain order
- Return joined output

### CompressLsFind(output string) (string, bool)
- Keep first 30 lines
- If total > 30, append "[... N total entries]"
- Return joined output

### CompressMake(output string) (string, bool)
- Keep lines containing (case-insensitive) "error"
- If last non-empty line not already kept, add it
- Return joined output

### CompressReadOutput(output string) (string, bool)
- Only compress if EstimateTokens(output) > 1000
- Keep first 100 lines
- Append "[... truncated read output: N lines / ~T tok]"
- Return result

## compressor/generic.go

package compressor

Port the Rust Tier 2 (generic signal extraction) and Tier 3 (hard cap):

### GenericSignalCompress(output string) string
  Rust's tier2_compress:
  - Keep first 15 lines
  - Keep last 10 lines
  - Keep any line where isSignalLine(line) is true
  - Use sorted set to maintain original order
  - Dedup similar consecutive lines (normalize digits to '#', if 3+ consecutive lines have same normalized form, keep first + "[... repeated N similar lines ...]")
  - Return joined output

### isSignalLine(line string) bool
  Return true if any of:
  - signalRe matches
  - testRunnerRe matches
  - wordCountRe matches
  - line starts with '+', '-', '>'
  - line contains "Caused by:", "Traceback", `  File "`
  - lowercase contains "at ", "exit code", "status:", "returned", "exited"

### normalizeForSimilarity(line string) string
  - Trim whitespace, replace all ASCII digits with '#'

### dedupSimilar(lines []string) []string
  - Walk lines, find runs of 3+ consecutive lines with same normalized form
  - Replace run with first line + "[... repeated N similar lines ...]"
  - Keep runs of 1-2 unchanged

### HardCapCompress(output string, originalLines, originalTokens int) string
  Rust's tier3_compress:
  - Keep first 30 lines
  - If tail start (len-10) > 30: add "[... hard-capped output ...]" + last 10 lines
  - Append "[... original N lines / ~T tok ...]"

## compressor/compress.go

package compressor

Main entry point that mirrors the Rust compress() function:

### Compress(toolName, command, output string) (string, bool)
  Algorithm (mirrors Rust logic):
  1. If toolName != "Bash": try CompressReadOutput. If successful, return with footer.
  2. originalTokens := EstimateTokens(output)
  3. If originalTokens < 500: return "", false (too small, skip)
     NOTE: This is the Rust-compatible threshold for the compressor itself.
     The proxy-level min_tokens (100) is checked separately before calling Compress.
  4. Get family from command. Try Tier1Compress(family, output).
     If successful:
       - If originalTokens > 5000 AND EstimateTokens(compressed) > 1500: fall back to HardCapCompress
       - Return with footer
  5. If originalTokens > 1500: apply GenericSignalCompress
     - If originalTokens > 5000 AND EstimateTokens(result) > 1500: fall back to HardCapCompress
     - Return with footer
  6. Return "", false

### AppendFooter(compressed string, originalLines, originalTokens int) string
  Format: "{compressed}\n\n[Compressed by hook: {originalLines} lines / ~{originalTokens} tok → ~{compressedTokens} tok]"

## compressor/tier1_test.go

Test cases ported from the Rust tests:

### TestPassThroughSmallOutput
  - Input: "small output" with command "git status"
  - Expected: no compression (output too small, <500 tokens)

### TestGitStatusCompression
  - Input: "On branch main\nChanges not staged for commit:\n" + 80 lines of "\tmodified:   src/file_{i}.rs"
  - Expected: contains "On branch main", "Changed files: 80", "file_0.rs", NOT "file_40.rs", contains "Compressed by hook"

### TestNpmInstallCompression
  - Input: 400 lines of "fetching package {i}" + "npm WARN deprecated package-x@1.0.0" + "npm ERR! code ERESOLVE" + "added 32 packages..."
  - Expected: contains "npm WARN", "npm ERR!", "added 32 packages", NOT "fetching package 12"

### TestCargoCompression
  - Input: 300 lines of "Compiling crate_{i} v0.1.0" + "warning: unused import..." + "error[E0425]..." + "Finished dev..."
  - Expected: contains "error[E0425]", "warning:", "Finished dev", NOT "crate_299"

### TestGenericLargeOutput
  - Input: 900 lines of "line {i}: normal output" + 6 lines of "ERROR connection refused attempt {i}" + "Traceback..." + "  File..." + "status: exited with code 1"
  - Expected: contains "ERROR connection refused", "Traceback", "status: exited with code 1", "[... repeated"

### TestHardCapFallback
  - Input: 80 lines of very long content (800+ chars each) with unknown command
  - Expected: contains "[... hard-capped output ...]", "original 80 lines", "Compressed by hook"

### TestReadToolHandling
  - Input: 600 lines of "line {i} long long long" with toolName="Read"
  - Expected: contains "line 0", "line 99", NOT "line 200", contains "truncated read output"

### TestGitLogCompression
  - Input: 20 commits in standard git log format
  - Expected: keeps at most 15 commits, includes commit hashes and messages

### TestGitDiffCompression
  - Input: multi-file diff with hunks
  - Expected: keeps diff headers, hunk markers, changed lines (up to 30)

### TestPipCompression
  - Input: pip install output with "Successfully installed" line
  - Expected: keeps "Successfully installed" line

### TestMakeCompression
  - Input: make output with error lines and a final status
  - Expected: keeps error lines and final status

### TestDockerLogsCompression
  - Input: 100 log lines with some error lines scattered
  - Expected: keeps last 20 lines + error lines

### TestLsFindCompression
  - Input: 50 file listing lines
  - Expected: keeps first 30 + "[... 50 total entries]"

After creating all files, run:
  cd /Users/otonashi/thinking/building/compact-sessions && go build ./... && go test ./... -v -count=1

Return: file paths created, test results, compression ratios achieved.
```

### Verification
- All tests pass
- Compression output matches Rust behavior for identical inputs
- All 10 tool families + generic + hard cap + read output work correctly

---

## Phase 4: Tombstone Writer + Bypass Rules + Config

**Worker**: Codex (GPT-5.4 xhigh)
**Goal**: Tombstone format, idempotency detection, bypass rules, config integration, and wiring everything into the proxy request handler.

### Prompt

```
You are wiring compression into the wet proxy at /Users/otonashi/thinking/building/compact-sessions/.

Go module: github.com/otonashi/wet

Existing code: main.go, proxy/ (reverse proxy), config/ (TOML config), messages/ (parse + staleness), compressor/ (Tier 1 + generic + hard cap).

Create/modify these files:

## pipeline/tombstone.go

package pipeline

import "fmt"

const TombstonePrefix = "[compressed: "

// CreateTombstone builds the tombstone string that replaces a compressed tool_result.
// Format: [compressed: {family} | {summary} | turn {originalTurn}/{currentTurn} | {originalTokens}->{compressedTokens} tokens]
func CreateTombstone(family, summary string, originalTurn, currentTurn, originalTokens, compressedTokens int) string {
    return fmt.Sprintf("[compressed: %s | %s | turn %d/%d | %d->%d tokens]",
        family, summary, originalTurn, currentTurn, originalTokens, compressedTokens)
}

// IsTombstone checks if content has already been compressed (idempotency).
func IsTombstone(content string) bool {
    return strings.HasPrefix(strings.TrimSpace(content), TombstonePrefix)
}

## pipeline/bypass.go

package pipeline

import (
    "regexp"
    "strings"
    "github.com/otonashi/wet/config"
    "github.com/otonashi/wet/messages"
)

// ShouldBypass returns true if this tool_result should NOT be compressed.
func ShouldBypass(info messages.ToolResultInfo, cfg *config.Config) bool

Rules (all must be checked):
1. info.Turn == currentTurn (fresh — never compress current turn)
   NOTE: this is already handled by info.Stale == false, so just check !info.Stale... wait, no.
   Actually: if Stale is false, bypass. This covers current-turn AND recently-fresh results.
2. info.IsError == true AND cfg.Bypass.PreserveErrors == true
3. IsTombstone(info.Content) — already compressed, idempotent
4. info.TokenCount < cfg.Bypass.MinTokens (or cfg.Compression.MinTokens) — too small
5. Content matches any bypass pattern from cfg.Bypass.ContentPatterns (regex match against content)
6. Content block is an image (type: "image") — not compressible
   NOTE: for this phase, just check if the raw content contains `"type":"image"` or `"type": "image"`

Return true if ANY bypass rule matches.

## pipeline/pipeline.go

package pipeline

import (
    "github.com/otonashi/wet/compressor"
    "github.com/otonashi/wet/config"
    "github.com/otonashi/wet/messages"
)

// CompressResult holds per-request compression stats
type CompressResult struct {
    TotalToolResults int
    Compressed       int
    SkippedFresh     int
    SkippedBypass    int
    TokensBefore     int
    TokensAfter      int
    OverheadMs       float64
}

// CompressRequest runs the full compression pipeline on a parsed request.
// It modifies the messages in-place and returns stats.
func CompressRequest(req *messages.Request, cfg *config.Config) CompressResult

Algorithm:
1. Classify staleness: call messages.ClassifyStaleness(req.Messages, cfg.Staleness.Threshold, cfg.Rules)
2. For each ToolResultInfo where Stale == true:
   a. Check ShouldBypass — if true, increment SkippedBypass, continue
   b. Determine compression strategy:
      - family := messages.ExtractToolFamily(info.ToolName, info.Command)
      - If family is in cfg.Rules and strategy is "none": skip
      - Try compressor.Compress(info.ToolName, info.Command, info.Content)
      - If compression succeeded:
        - Compute compressed token count
        - If compressed tokens >= original tokens: skip (footer overhead regression)
        - Build tombstone with CreateTombstone(family, compressed, info.Turn, currentTurn, originalTokens, compressedTokens)
        - Replace the content in req.Messages[info.MsgIdx] at info.BlockIdx with the tombstone text
        - Increment Compressed, update TokensBefore/TokensAfter
3. For fresh results: increment SkippedFresh
4. Return CompressResult

To replace content in the messages array, you need a helper:

func ReplaceToolResultContent(msg *messages.Message, blockIdx int, newContent string, contentIsStr bool) error
  - Parse msg.Content into blocks
  - Replace the content of blocks[blockIdx] with newContent
  - Marshal back to msg.Content as json.RawMessage

## Modify proxy/proxy.go

Add compression to the request handler:

In the handler for POST /v1/messages:
1. Read the request body
2. Parse with messages.ParseRequest
3. Run pipeline.CompressRequest
4. If any compression happened: marshal the modified request, use that as the new body
5. If no compression: use original body unchanged (avoid unnecessary marshal/unmarshal)
6. Forward to upstream
7. Log compression stats to stderr: "[wet] N results, M compressed (skipped: F fresh, B bypass), X->Y tokens (Z% saved), +Wms"

Important: for non-/v1/messages paths, skip all parsing and forward unchanged.
Important: if request parsing fails, forward original body unchanged (fail-open).

## pipeline/tombstone_test.go

- TestCreateTombstone: verify format string
- TestIsTombstone: true for "[compressed: ...", false for "normal text", false for "  [compressed: ..." (with leading whitespace — should still be true after trim)

## pipeline/bypass_test.go

- TestBypassFreshResult: Stale=false -> bypass
- TestBypassErrorResult: IsError=true -> bypass
- TestBypassAlreadyCompressed: content starts with "[compressed:" -> bypass
- TestBypassSmallOutput: TokenCount < MinTokens -> bypass
- TestBypassErrorPattern: content starts with "Error:" -> bypass
- TestNoBypass: stale, not error, large enough, no patterns -> no bypass

## pipeline/pipeline_test.go

Integration test:
- Build a messages array with 3 turns:
  Turn 1: assistant uses Bash(git status) -> user has tool_result with large git status output
  Turn 2: assistant uses Bash(npm install) -> user has tool_result with large npm output
  Turn 3: assistant uses Bash(ls) -> user has tool_result with ls output (current turn)
- Set threshold=1
- Run CompressRequest
- Assert: turn 1 git status is compressed (tombstone format)
- Assert: turn 2 npm is compressed
- Assert: turn 3 ls is NOT compressed (current turn)
- Assert: CompressResult.Compressed == 2, SkippedFresh == 1

After creating all files, run:
  cd /Users/otonashi/thinking/building/compact-sessions && go build ./... && go test ./... -v -count=1

Return: file paths created/modified, test results.
```

### Verification
- All tests pass
- Proxy compresses stale tool results on `/v1/messages` POST
- Current-turn and error results are never compressed
- Tombstones are idempotent (already-compressed blocks are skipped)
- Fail-open on parse errors (original request forwarded)

---

## Phase 5: CLI Shim + Control Plane

**Worker**: Codex (GPT-5.4 xhigh)
**Goal**: `wet claude ...` session wrapper (start proxy, set env, exec child), Unix socket control plane, `wet status/inspect/rules/pause/resume` subcommands.

### Prompt

```
You are building the CLI shim and control plane for wet at /Users/otonashi/thinking/building/compact-sessions/.

Go module: github.com/otonashi/wet

Existing code: main.go, proxy/, config/, messages/, compressor/, pipeline/.

## Restructure main.go for subcommands

Replace the current main.go with a subcommand-based CLI (no external framework — use os.Args manual dispatch):

Usage:
  wet claude [args...]     # session wrapper (primary)
  wet daemon start         # persistent proxy
  wet daemon stop
  wet daemon status
  wet status               # live stats from running proxy
  wet inspect              # show tombstones
  wet rules list           # show active rules
  wet rules set KEY VALUE  # tune rule at runtime
  wet pause                # bypass all compression
  wet resume               # re-enable compression
  wet statusline           # output one-liner for Claude Code status bar
  wet setup-statusline     # auto-configure Claude Code settings.json
  wet --help / wet help

## cli/shim.go

package cli

The core UX: `wet claude "fix the bug"` does:
1. Find a free port (net.Listen on :0, get port, close listener)
2. Start the proxy in-process on that port (goroutine)
3. Wait for proxy to be ready (poll GET /health, max 2s)
4. Set environment: ANTHROPIC_BASE_URL=http://127.0.0.1:{port}
5. Exec `claude` with all remaining args (os.Args after "claude")
   Use syscall.Exec (unix exec, replaces process) — NO, we need cleanup.
   Use exec.Command instead, forward stdin/stdout/stderr, wait for exit.
6. When child exits: shut down proxy, print session stats to stderr, exit with child's exit code

func RunShim(args []string, cfg *config.Config) error

Signal handling: forward SIGINT/SIGTERM to child process. If child dies, shut down proxy.

## cli/control.go

package cli

Control plane client. Talks to running proxy via Unix socket at ~/.wet/wet.sock.

Commands send JSON over the socket:
  {"command": "status"}     -> returns stats JSON
  {"command": "inspect"}    -> returns list of tombstones from current session
  {"command": "rules_list"} -> returns current rules
  {"command": "rules_set", "key": "pytest.stale_after", "value": "3"} -> updates rule
  {"command": "pause"}      -> disables compression
  {"command": "resume"}     -> enables compression

func SendCommand(command string, args map[string]string) ([]byte, error)
  - Dial unix socket at ~/.wet/wet.sock
  - Send JSON command
  - Read JSON response
  - Return response bytes

func RunStatus() error   // sends "status", pretty-prints stats
func RunInspect() error  // sends "inspect", pretty-prints tombstones
func RunRulesList() error
func RunRulesSet(key, value string) error
func RunPause() error
func RunResume() error

## proxy/control.go

Server-side control plane. Add to the proxy server.

func (s *Server) StartControlPlane() error
  - Create ~/.wet/ directory
  - Listen on unix socket at ~/.wet/wet.sock
  - Accept connections, read JSON command, dispatch:
    "status"     -> return s.GetStats()
    "inspect"    -> return s.GetTombstones()
    "rules_list" -> return s.cfg.Rules as JSON
    "rules_set"  -> update s.cfg.Rules[key] (thread-safe with mutex)
    "pause"      -> set s.paused = true (atomic bool)
    "resume"     -> set s.paused = false
  - Respond with JSON

Add to Server struct:
  paused    atomic.Bool
  mu        sync.RWMutex  // protects cfg.Rules
  tombstones []TombstoneRecord  // append-only log of compressions this session
  controlLn  net.Listener

type TombstoneRecord struct {
    ToolUseID      string    `json:"tool_use_id"`
    Family         string    `json:"family"`
    OriginalTokens int       `json:"original_tokens"`
    CompressedTokens int     `json:"compressed_tokens"`
    Turn           int       `json:"turn"`
    Timestamp      time.Time `json:"timestamp"`
}

Check s.paused in the request handler — if true, skip all compression (forward unchanged).

## cli/shim_test.go

- TestFindFreePort: verify we can find a free port
- TestShimHelp: run `wet claude --help` in a subprocess (mock claude binary that just prints help)

## cli/control_test.go

- TestControlPlaneRoundtrip: start proxy, send "status" via control plane, verify response has expected fields

## Modify proxy/proxy.go

- Call s.StartControlPlane() during server startup
- Check s.paused before compression
- Record tombstones in s.tombstones during compression
- Clean up socket on shutdown (os.Remove)

After creating all files, run:
  cd /Users/otonashi/thinking/building/compact-sessions && go build ./... && go test ./... -v -count=1

Verify: `go build -o wet . && ./wet --help` prints usage.

Return: file paths created/modified, test results.
```

### Verification
- `go build -o wet .` produces a binary
- `./wet --help` prints usage with all subcommands
- Control plane socket is created and responds to commands
- `wet pause` / `wet resume` toggles compression

---

## Phase 6: Observability (Statusline + Metrics)

**Worker**: Codex (GPT-5.4 xhigh)
**Goal**: Stats writer (`~/.wet/stats.json`), `wet statusline` output script, `wet setup-statusline`, stderr logging, metrics JSONL file.

### Prompt

```
You are adding observability to the wet proxy at /Users/otonashi/thinking/building/compact-sessions/.

Go module: github.com/otonashi/wet

Existing code: main.go, proxy/, config/, messages/, compressor/, pipeline/, cli/.

## stats/writer.go

package stats

import (
    "encoding/json"
    "os"
    "path/filepath"
    "sync"
    "time"
)

// SessionStats tracks cumulative stats for the current proxy session
type SessionStats struct {
    mu                sync.Mutex
    StartTime         time.Time
    RequestCount      int
    TotalToolResults  int
    TotalCompressed   int
    TotalSkippedFresh int
    TotalSkippedBypass int
    TokensBefore      int64
    TokensAfter       int64
    Tier1Count        int
    Tier2Count        int
    Tier2Failures     int
    LastRequest       *RequestStats
}

// RequestStats is written to ~/.wet/stats.json after each request
type RequestStats struct {
    Timestamp         string  `json:"timestamp"`
    ToolResults       int     `json:"tool_results"`
    Compressed        int     `json:"compressed"`
    SkippedFresh      int     `json:"skipped_fresh"`
    SkippedBypass     int     `json:"skipped_bypass"`
    TokensBefore      int     `json:"tokens_before"`
    TokensAfter       int     `json:"tokens_after"`
    CompressionRatio  float64 `json:"compression_ratio"`
    OverheadMs        float64 `json:"overhead_ms"`
    SessionSaved      int64   `json:"session_tokens_saved"`
    SessionRequests   int     `json:"session_requests"`
}

func NewSessionStats() *SessionStats
func (s *SessionStats) RecordRequest(result pipeline.CompressResult)
func (s *SessionStats) WriteStatsFile() error
  - Write to ~/.wet/stats.json (create dir if needed)
  - Atomic write: write to temp file, rename

// HealthResponse for GET /health
type HealthResponse struct {
    Status                string  `json:"status"`
    UptimeSeconds         float64 `json:"uptime_seconds"`
    RequestsProxied       int     `json:"requests_proxied"`
    TotalTokensSaved      int64   `json:"total_tokens_saved"`
    AverageCompressionRatio float64 `json:"average_compression_ratio"`
    Tier1Count            int     `json:"tier1_count"`
    Tier2Count            int     `json:"tier2_count"`
    Tier2Failures         int     `json:"tier2_failures"`
}

func (s *SessionStats) HealthResponse() HealthResponse

## stats/metrics.go

package stats

// MetricsWriter appends JSONL to ~/.wet/metrics.jsonl
type MetricsWriter struct {
    file *os.File
    mu   sync.Mutex
}

func NewMetricsWriter() (*MetricsWriter, error) // opens ~/.wet/metrics.jsonl in append mode
func (w *MetricsWriter) Write(entry MetricsEntry) error
func (w *MetricsWriter) Close() error

type MetricsEntry struct {
    Timestamp          string  `json:"timestamp"`
    RequestID          string  `json:"request_id"`    // generated UUID
    TotalToolResults   int     `json:"total_tool_results"`
    Compressed         int     `json:"compressed"`
    SkippedFresh       int     `json:"skipped_fresh"`
    SkippedBypass      int     `json:"skipped_bypass"`
    Tier1Compressions  int     `json:"tier1_compressions"`
    Tier2Compressions  int     `json:"tier2_compressions"`
    TokensBefore       int     `json:"tokens_before"`
    TokensAfter        int     `json:"tokens_after"`
    CompressionRatio   float64 `json:"compression_ratio"`
    Tier1LatencyMs     float64 `json:"tier1_latency_ms"`
    TotalOverheadMs    float64 `json:"total_proxy_overhead_ms"`
}

## stats/statusline.go

package stats

// RenderStatusline reads ~/.wet/stats.json and outputs a one-liner for Claude Code status bar.
// Format: ⚡ wet: 42.3k→8.9k (-79%) | 18/24 compressed | session: 142k saved
func RenderStatusline() (string, error)
  - Read ~/.wet/stats.json
  - If file doesn't exist or is stale (>5 min old): return "" (no output = hide status line)
  - Format token counts as "Xk" (divide by 1000, 1 decimal)
  - Calculate percentage saved
  - Return the one-liner

## cli/statusline.go

package cli

func RunStatusline() error {
    line, err := stats.RenderStatusline()
    if err != nil { return nil } // silent failure
    if line != "" { fmt.Println(line) }
    return nil
}

func RunSetupStatusline() error
  - Find Claude Code settings: ~/.claude/settings.json (or ~/.config/claude/settings.json)
  - Read existing settings (or create new)
  - Set statusLine.command to the absolute path of the wet binary + " statusline"
  - Write back
  - Print confirmation message

## Modify proxy/proxy.go

- Add *stats.SessionStats to Server struct
- After compression, call stats.RecordRequest(result) and stats.WriteStatsFile()
- If metrics enabled in config, write MetricsEntry
- Update GET /health to return stats.HealthResponse()
- Stderr logging (already exists, enhance):
  "[wet] 24 results, 18 compressed (15 T1 + 3 T2), 42380→8920 tokens (79% saved), +12ms"

## stats/writer_test.go

- TestRecordRequest: record 3 requests, verify cumulative stats
- TestWriteStatsFile: write stats, read back, verify JSON
- TestRenderStatusline: write a stats file, render statusline, verify format

## Modify main.go / CLI routing

- Add "statusline" and "setup-statusline" subcommands
- Route to cli.RunStatusline() and cli.RunSetupStatusline()

After creating all files, run:
  cd /Users/otonashi/thinking/building/compact-sessions && go build ./... && go test ./... -v -count=1

Return: file paths created/modified, test results.
```

### Verification
- `~/.wet/stats.json` written after each proxied request
- `wet statusline` renders correctly
- Metrics JSONL appended correctly
- GET /health returns full stats
- Stderr logging shows compression summary

---

## Phase 7: Integration Test + Dogfood

**Worker**: Claude (Opus 4.6)
**Goal**: Run wet against a real Claude Code JSONL session replay. Measure compression ratio. Test the full `wet claude` flow end-to-end. Find and fix bugs.

### Prompt

```
You are testing the wet proxy end-to-end at /Users/otonashi/thinking/building/compact-sessions/.

Go module: github.com/otonashi/wet

The proxy is fully built: Tier 1 compression, staleness classification, bypass rules, tombstones, CLI shim, control plane, observability. Your job is to verify it works against real data and fix any bugs.

## Task 1: Build a replay test

Create tests/replay_test.go:

1. Write a function loadSessionMessages(jsonlPath string) ([]messages.Message, error):
   - Read a Claude Code session JSONL file line by line
   - Each line is JSON: {"type":"...", "uuid":"...", "parentUuid":"...", "sessionId":"...", "message":{...}}
   - Build a chain from the last entry back to the root via parentUuid
   - Extract messages in order (role + content from each entry's message field)
   - Return as []messages.Message in API request format

2. Write TestReplayCompression:
   - Load 5 session files from /Users/otonashi/thinking/pratchett-os/data/claude-code-sessions/2026/03/08/
     (pick the 5 largest files by size)
   - For each session:
     a. Parse into messages
     b. Build a Request with those messages
     c. Run pipeline.CompressRequest with default config
     d. Print stats: session file, total tool_results, compressed, tokens before/after, ratio
   - Assert for each session:
     - No panics
     - No current-turn tool_results compressed
     - If there are stale tool_results: compression ratio > 30%

3. Write TestEndToEndProxy:
   - Start the proxy server in-process on a random port
   - Build a realistic request body with 5 turns:
     Turn 1: user asks to fix a bug
     Turn 1: assistant uses Bash(git status) -> tool_result with 80-file git status
     Turn 2: assistant uses Bash(cat main.go) -> tool_result with 200-line file content (Read tool)
     Turn 3: assistant uses Bash(go test ./...) -> tool_result with test output (50 lines of passing tests + 2 failures)
     Turn 4: assistant edits a file -> tool_result with edit confirmation
     Turn 5: assistant uses Bash(go test ./...) -> tool_result with all tests passing (current turn)
   - Send POST /v1/messages to proxy (with mock upstream that echoes back the request body)
   - Parse the echoed-back body
   - Assert: Turn 1 git status is compressed (tombstone format)
   - Assert: Turn 2 Read output is compressed (if >1000 tokens)
   - Assert: Turn 3 test output may be compressed (stale after threshold)
   - Assert: Turn 5 test output is NOT compressed (current turn)
   - Assert: tombstone format is correct
   - Verify: no data loss in non-tool-result messages (user text, assistant text preserved exactly)

4. Write TestProxyFailOpen:
   - Send a malformed JSON body through the proxy
   - Verify the proxy forwards it unchanged (doesn't crash, doesn't modify)
   - Send a request where messages is not an array
   - Verify fail-open behavior

5. Write TestSSEStreamingEndToEnd:
   - Start proxy with mock upstream that sends 5 SSE events with 50ms delays
   - Verify all events arrive at the client
   - Verify first event arrives before last event is sent (no buffering)

## Task 2: Fix bugs

After running all tests, fix any bugs you find. Common issues to watch for:
- JSON parsing edge cases (content as string vs array)
- Tool results with no text content (image-only blocks)
- Empty messages arrays
- Messages with no tool_use/tool_result blocks
- Unicode in tool output
- Very large tool outputs (>100KB)

## Task 3: Build quality report

Create tests/quality_report_test.go with TestQualityReport:
- Load 10+ session files
- Run compression on each
- Print a summary table:
  | Session | Tool Results | Compressed | Tokens Before | Tokens After | Ratio | Time |
- Print aggregate stats: total compressed, average ratio, p50/p95 overhead

Run: cd /Users/otonashi/thinking/building/compact-sessions && go test ./... -v -count=1 -run TestReplay -timeout 60s
And: go test ./... -v -count=1 -run TestEndToEnd
And: go test ./... -v -count=1 -run TestQualityReport

Return: test results, quality report table, bugs found and fixed, overall assessment.
```

### Verification
- All replay tests pass on real session data
- End-to-end proxy test passes
- Compression ratio >50% on sessions with stale tool results
- No panics on any real session data
- SSE streaming works without buffering
- Fail-open behavior confirmed

---

## Phase 8: Tier 2 LLM Stub (Optional)

**Worker**: Codex (GPT-5.4 xhigh)
**Goal**: Tier 2 LLM compression plumbing. Config-gated, disabled by default. Calls a fast LLM to compress unrecognized tool outputs.

### Prompt

```
You are adding LLM-based Tier 2 compression to wet at /Users/otonashi/thinking/building/compact-sessions/.

Go module: github.com/otonashi/wet

Existing code is fully working with Tier 1 compression. You are adding an optional Tier 2 path for tool outputs that Tier 1 doesn't recognize and that exceed a token threshold.

## compressor/tier2.go

package compressor

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "os"
    "time"
)

// Tier2Compress sends tool output to an LLM for compression.
// Returns compressed text or empty string on failure/timeout.
// Config-gated: only called when tier2.enabled is true.
func Tier2Compress(ctx context.Context, content string, cfg Tier2Config) (string, error)

type Tier2Config struct {
    Model          string
    APIKey         string  // from ANTHROPIC_API_KEY env var
    TimeoutMs      int
    MaxOutputTokens int
}

Implementation:
1. Build the Anthropic API request:
   POST https://api.anthropic.com/v1/messages
   Headers:
     x-api-key: {api_key}
     anthropic-version: 2023-06-01
     content-type: application/json
   Body:
   {
     "model": "{model}",
     "max_tokens": {max_output_tokens},
     "messages": [{
       "role": "user",
       "content": "Extract the key information from this CLI tool output. Preserve:\n- Error messages and stack traces (exact text)\n- File paths mentioned\n- Numeric results, counts, statuses\n- Any actionable information\n\nDiscard:\n- Repetitive log lines\n- Progress bars and spinners\n- Verbose debug output\n- Redundant whitespace and formatting\n\nReturn a concise summary under 200 tokens. Start directly with the content, no preamble.\n\n<tool_output>\n{content}\n</tool_output>"
     }]
   }

2. Send with context deadline (timeout from config)
3. Parse response: extract content[0].text
4. On error or timeout: return "", err (caller handles graceful degradation)

## Modify pipeline/pipeline.go

After Tier 1 compression attempt fails (no family match or compression returned empty):
- Check if cfg.Compression.Tier2.Enabled
- Check if info.TokenCount >= cfg.Compression.Tier2.MinTokens
- If both true: call compressor.Tier2Compress with a context deadline
- If successful: use the LLM summary as the tombstone content, family = "generic_llm"
- If failed/timeout: leave original content unchanged (fail-open)

NOTE: For v1, Tier 2 calls are sequential (one at a time). Parallel dispatch is a v2 optimization.

## compressor/tier2_test.go

1. TestTier2PromptConstruction:
   - Mock HTTP server that captures the request body
   - Call Tier2Compress with sample content
   - Verify the prompt template is correct (contains "Extract the key information", wraps content in <tool_output> tags)
   - Verify model and max_tokens are set correctly

2. TestTier2ResponseParsing:
   - Mock HTTP server returns a valid Anthropic API response
   - Verify the extracted text is correct

3. TestTier2Timeout:
   - Mock HTTP server that sleeps 5s
   - Call with 100ms timeout
   - Verify error is returned (context deadline exceeded)
   - Verify caller handles gracefully (no panic)

4. TestTier2Disabled:
   - Integration test: run CompressRequest with tier2.enabled=false
   - Verify no HTTP calls are made (mock server records zero requests)

After creating all files, run:
  cd /Users/otonashi/thinking/building/compact-sessions && go build ./... && go test ./... -v -count=1

Return: file paths created/modified, test results.
```

### Verification
- With `tier2.enabled=false` (default): no LLM calls, pure Tier 1 behavior unchanged
- With `tier2.enabled=true`: unrecognized large tool outputs get LLM-compressed
- Timeout handling works (fail-open)
- Prompt template matches SPEC

---

## Worker Dependency Graph

```
Phase 1: Proxy Skeleton + SSE
    │
    ▼
Phase 2: Message Parsing + Staleness
    │
    ▼
Phase 3: Tier 1 Compressor (Go port)
    │
    ▼
Phase 4: Tombstone + Bypass + Wiring  ◄── wires Phases 2+3 into Phase 1
    │
    ├──────────────────┐
    ▼                  ▼
Phase 5: CLI Shim    Phase 6: Observability
    │                  │
    └────────┬─────────┘
             ▼
Phase 7: Integration Test + Dogfood  ◄── tests everything
             │
             ▼
Phase 8: Tier 2 LLM Stub (optional)
```

Phases 5 and 6 could theoretically run in parallel (independent features on top of Phase 4), but within a single GSD run with stateless workers, they must be sequential because each worker sees only the current file state.

---

## Risk Register

### 1. SSE Streaming Breaks (Severity: HIGH, Likelihood: MEDIUM)
**Risk**: `httputil.ReverseProxy` with `FlushInterval: -1` doesn't flush SSE events immediately with Anthropic's chunked transfer encoding, causing Claude Code to hang.
**Mitigation**: Phase 1 includes a streaming test with timed assertions. If `FlushInterval: -1` doesn't work, implement manual response copying with `http.Flusher` interface check + per-chunk flush.
**Rescue**: Fall back to raw `io.Copy` between connections (bypass `ReverseProxy` for streaming responses).

### 2. Request Body Parsing Latency (Severity: MEDIUM, Likelihood: LOW)
**Risk**: Parsing 1-2MB JSON request bodies adds >50ms overhead per request, degrading user experience.
**Mitigation**: Use `json.RawMessage` for non-messages fields to avoid parsing the full body. Only parse the `messages` array. Benchmark in Phase 4 wiring. Go's `encoding/json` handles 2MB in <5ms typically.
**Rescue**: If parsing is slow, use streaming JSON parser (`json.Decoder`) to walk messages without full unmarshal.

### 3. Tool Result Content Format Edge Cases (Severity: MEDIUM, Likelihood: HIGH)
**Risk**: `tool_result.content` can be a string, an array of content blocks, or have image blocks mixed with text. Edge cases in parsing cause panics or incorrect compression.
**Mitigation**: Phase 2 tests both string and array content formats. Phase 7 replays real sessions to find edge cases. Fail-open on parse errors.
**Rescue**: If content parsing is too complex, simplify to only compress `tool_result` blocks where content is a plain string.

### 4. Compression Makes Output Worse (Severity: LOW, Likelihood: LOW)
**Risk**: Compressed tombstone is larger than original (footer overhead on small outputs), wasting tokens instead of saving them.
**Mitigation**: Phase 4 bypass rules skip outputs under `min_tokens` (100). Tombstone creation checks if compressed >= original and skips. The Rust compressor already handles this case.

### 5. Unix Socket Control Plane Conflicts (Severity: LOW, Likelihood: MEDIUM)
**Risk**: Stale socket file from crashed proxy prevents new proxy from starting. Multiple proxy instances fight over the socket.
**Mitigation**: On startup, check if socket exists and test if a proxy is already listening. If stale (no response), remove and recreate. Use session-specific socket path if needed (`~/.wet/wet-{pid}.sock`).

---

## Success Criteria

- `wet claude "hello"` starts proxy, launches claude, compresses stale tool results, shuts down cleanly
- >50% token savings on real session replays (stale tool results)
- <50ms overhead per request (Tier 1 only)
- Status line shows compression stats in Claude Code
- `wet status` reports live stats via control plane
- `wet pause` / `wet resume` toggles compression without restart
- All tests pass: unit, integration, replay, streaming
- Single binary: `go build -o wet .` produces one file, no runtime dependencies

## Future (v2)

- `wet codex ...` — Codex wrapping (different env var, different child process)
- Parallel Tier 2 LLM dispatch (concurrent goroutines with shared timeout budget)
- Token-budget staleness mode (compress oldest first until under budget)
- Semantic staleness (track which tools invalidate which results)
- Homebrew formula (`brew install wet`)
- Shell completions (`wet completion bash/zsh/fish`)
- `--dry-run` mode (log what would be compressed without modifying)
- Cache layer (avoid recompression on retries, keyed on tool_use_id)
