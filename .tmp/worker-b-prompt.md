## TASK: Add inspect_results + compress control commands, CLI subcommands

You are implementing part of an agent-directed compression mode for the wet Go proxy.

### Files to read first:
- proxy/control.go (your main server-side target — note it already has StoreToolResults, GetToolResults, QueueCompressIDs, DrainCompressIDs methods added)
- cli/control.go (your main CLI target)
- main.go (you'll add new CLI commands)
- messages/staleness.go (understand ToolResultInfo struct)

### Changes to make:

#### 1. proxy/control.go — Update handleControlConnection

The current handleControlConnection uses `map[string]string` for request decoding. You MUST change it to `map[string]any` to support the compress command which sends IDs as an array.

Add two new command cases: `inspect_results` and `compress`.

Change the request type from `map[string]string` to `map[string]any`, and update all existing string field access to use type assertions like `command, _ := req["command"].(string)`.

For `inspect_results`:
- Call `s.GetToolResults()`
- Compute currentTurn from the results
- For each result, create a JSON-friendly struct with: tool_use_id, tool_name, command (omitempty), turn, current_turn, stale, is_error, token_count, content_preview (first 200 chars of Content), msg_idx, block_idx
- Send the array via writeJSON

For `compress`:
- Extract `ids` from req as `[]any`, convert each element to string
- Validate non-empty
- Call `s.QueueCompressIDs(ids)`
- Return `{"status": "queued", "count": N, "ids": [...]}`
- On errors return structured JSON with "error" and "message" keys

Also for `rules_set`, update to use `req["key"].(string)` and `req["value"].(string)` instead of direct map access.

Update error responses to include both "error" (code) and "message" (description) for machine-readability.

#### 2. cli/control.go — Add SendCommandAny, RunInspectResults, RunCompress

Add `"strings"` to imports.

Add `SendCommandAny(payload any) ([]byte, error)` — same as SendCommand but takes arbitrary JSON payload to encode.

Add `RunInspectResults(format string) error`:
- Sends `inspect_results` command via SendCommand
- If format is "table", parse the JSON entries and print a human-readable table with columns: TOOL_USE_ID, TOOL, COMMAND, TURN, CUR, STALE, ERROR, TOKENS
- Otherwise pretty-print JSON

Add `RunCompress(ids []string) error`:
- Sends compress command via SendCommandAny with payload `{"command": "compress", "ids": ids}`
- Check for error in response, pretty-print result

#### 3. main.go — Wire up new CLI commands

Add `"strings"` to imports.

Update usageText to include:
```
  wet inspect --live       # show current tool results (agent API)
  wet inspect --live --format table  # human-readable table
  wet compress --ids id1,id2,id3     # queue selective compression
```

Update the "inspect" case to parse --live and --format flags (remove the `len(args) != 1` check):
```go
case "inspect":
    hasLive := false
    format := "json"
    for i := 1; i < len(args); i++ {
        switch args[i] {
        case "--live":
            hasLive = true
        case "--format":
            if i+1 < len(args) {
                format = args[i+1]
                i++
            }
        }
    }
    if hasLive {
        err = cli.RunInspectResults(format)
    } else {
        err = cli.RunInspect()
    }
```

Add a new "compress" case BEFORE the default case:
```go
case "compress":
    var ids []string
    for i := 1; i < len(args); i++ {
        if args[i] == "--ids" && i+1 < len(args) {
            ids = strings.Split(args[i+1], ",")
            i++
        }
    }
    if len(ids) == 0 {
        fmt.Fprintln(os.Stderr, "[wet] error: --ids required. Usage: wet compress --ids id1,id2,id3")
        os.Exit(1)
    }
    err = cli.RunCompress(ids)
```

### IMPORTANT:
- Do NOT touch config/config.go or pipeline/ files
- Do NOT touch proxy/proxy.go
- You CAN and SHOULD modify proxy/control.go, cli/control.go, and main.go
- After making changes, run: go build ./...
- If build fails, fix it. The code MUST compile.
- Return a 3-5 sentence summary of what you changed.
