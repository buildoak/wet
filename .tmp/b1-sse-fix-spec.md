# B1 Fix: SSE Response Stream Parsing for Token Usage

## Root Cause

wet's proxy currently uses `httputil.ReverseProxy` with `FlushInterval: -1` for pure
SSE pass-through. The `handleMessagesWithCompression` function in `proxy/proxy.go`
only processes the REQUEST body (for compression). The RESPONSE stream flows through
the ReverseProxy completely untouched -- there is no code anywhere that parses SSE
events from the upstream Anthropic response.

Result: `input_tokens` and `output_tokens` are never extracted from the response,
so they are always 0.

## Anthropic SSE Format Reference

The Anthropic Messages API streaming response emits these SSE events in order:

```
event: message_start
data: {"type":"message_start","message":{"id":"msg_...","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-20250514","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1523,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: ping
data: {"type":"ping"}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

... more content_block_delta events ...

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":42}}

event: message_stop
data: {"type":"message_stop"}
```

Key facts:
- `message_start` contains `message.usage.input_tokens` with the initial input token count
- `message_start` may also contain `cache_creation_input_tokens` and `cache_read_input_tokens` (prompt caching)
- `message_delta` contains `usage` with CUMULATIVE token counts (per Anthropic docs: "The token counts shown in the usage field of the message_delta event are cumulative")
- `message_delta` usage can include: `input_tokens`, `output_tokens`, `cache_creation_input_tokens`, `cache_read_input_tokens`, and optionally `server_tool_use`
- `message_delta` is the most reliable source for final token counts -- it supersedes `message_start` values
- `message_stop` has no usage data
- `content_block_*` events have no usage data
- `ping` events are keepalives, no data of interest

**IMPORTANT**: Use `message_delta` usage as the authoritative final count. The `message_start` provides an early read of input_tokens, but `message_delta` at stream end gives cumulative totals that should be preferred when available.

## Design: SSE Tee Reader

The fix must:
1. Intercept the response stream WITHOUT buffering it (pass bytes to client immediately)
2. Parse SSE events on-the-fly to extract usage from `message_start` and `message_delta`
3. Store extracted usage data in session stats
4. Surface in `/_wet/status`, `stats.json`, and statusline

### Architecture

```
Upstream (Anthropic) --> [io.TeeReader] --> Client (Claude Code)
                              |
                              v
                        [SSE Parser goroutine]
                              |
                              v
                        Extract usage from
                        message_start & message_delta
                              |
                              v
                        Store in SessionStats
```

The key insight: we replace the response body with a custom `io.ReadCloser` that:
- Writes every byte read to a pipe (tee)
- A goroutine reads from the pipe and scans for SSE events
- The goroutine only needs to find `message_start` and `message_delta` lines
- The original bytes flow to the client unchanged, with zero added latency

### Non-streaming responses

For non-streaming responses (no `stream: true` in request), the response is a single
JSON body. We should also parse these -- the usage is at `response.usage.input_tokens`.
However, Claude Code always uses streaming, so SSE is the priority.

## Implementation Spec

### File 1: NEW `proxy/sse.go`

Create a new file `proxy/sse.go` with the SSE response interceptor.

```go
package proxy

import (
    "bufio"
    "encoding/json"
    "io"
    "strings"
    "sync"
)

// UsageData holds token usage extracted from an SSE response stream.
type UsageData struct {
    InputTokens              int `json:"input_tokens"`
    OutputTokens             int `json:"output_tokens"`
    CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
    CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// sseInterceptor wraps a response body to extract usage data from SSE events
// while passing all bytes through to the original reader unchanged.
type sseInterceptor struct {
    original io.ReadCloser
    pr       *io.PipeReader
    pw       *io.PipeWriter
    reader   io.Reader // TeeReader that reads from original, writes to pw
    usage    UsageData
    done     chan struct{}
    mu       sync.Mutex
}

// newSSEInterceptor wraps the given body. Reads from the returned ReadCloser
// pass through unchanged while a background goroutine extracts usage data.
func newSSEInterceptor(body io.ReadCloser) *sseInterceptor {
    pr, pw := io.Pipe()
    s := &sseInterceptor{
        original: body,
        pr:       pr,
        pw:       pw,
        reader:   io.TeeReader(body, pw),
        done:     make(chan struct{}),
    }
    go s.parse()
    return s
}

func (s *sseInterceptor) Read(p []byte) (int, error) {
    n, err := s.reader.Read(p)
    if err != nil {
        // Close the pipe writer so the parser goroutine finishes
        s.pw.Close()
    }
    return n, err
}

func (s *sseInterceptor) Close() error {
    err := s.original.Close()
    s.pw.Close()
    // Wait for parser to finish (with short timeout to avoid blocking)
    <-s.done
    return err
}

// Usage returns the extracted usage data. Safe to call after Close().
func (s *sseInterceptor) Usage() UsageData {
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.usage
}

// parse reads SSE events from the pipe and extracts usage data.
func (s *sseInterceptor) parse() {
    defer close(s.done)
    scanner := bufio.NewScanner(s.pr)
    // SSE lines are typically short, but data lines can be large.
    // Set a reasonable max to avoid memory issues.
    scanner.Buffer(make([]byte, 0, 64*1024), 512*1024)

    for scanner.Scan() {
        line := scanner.Text()

        // SSE format: "data: {json...}"
        if !strings.HasPrefix(line, "data: ") {
            continue
        }
        data := line[6:] // strip "data: " prefix

        // Quick check before JSON parsing -- only parse lines we care about
        if !strings.Contains(data, "message_start") && !strings.Contains(data, "message_delta") {
            continue
        }

        // Try to extract usage from message_start
        if strings.Contains(data, "message_start") {
            s.parseMessageStart(data)
            continue
        }

        // Try to extract usage from message_delta
        if strings.Contains(data, "message_delta") {
            s.parseMessageDelta(data)
        }
    }
    // Drain any remaining bytes so the TeeReader doesn't block
    io.Copy(io.Discard, s.pr)
}

// messageStartEvent matches the structure of a message_start SSE event.
type messageStartEvent struct {
    Type    string `json:"type"`
    Message struct {
        Usage struct {
            InputTokens              int `json:"input_tokens"`
            OutputTokens             int `json:"output_tokens"`
            CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
            CacheReadInputTokens     int `json:"cache_read_input_tokens"`
        } `json:"usage"`
    } `json:"message"`
}

// messageDeltaEvent matches the structure of a message_delta SSE event.
// Per Anthropic docs: "The token counts shown in the usage field of the
// message_delta event are cumulative." This means message_delta can contain
// input_tokens, output_tokens, cache_creation_input_tokens, and
// cache_read_input_tokens as final cumulative counts.
type messageDeltaEvent struct {
    Type  string `json:"type"`
    Usage struct {
        InputTokens              int `json:"input_tokens"`
        OutputTokens             int `json:"output_tokens"`
        CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
        CacheReadInputTokens     int `json:"cache_read_input_tokens"`
    } `json:"usage"`
}

func (s *sseInterceptor) parseMessageStart(data string) {
    var event messageStartEvent
    if err := json.Unmarshal([]byte(data), &event); err != nil {
        return
    }
    if event.Type != "message_start" {
        return
    }
    s.mu.Lock()
    s.usage.InputTokens = event.Message.Usage.InputTokens
    s.usage.CacheCreationInputTokens = event.Message.Usage.CacheCreationInputTokens
    s.usage.CacheReadInputTokens = event.Message.Usage.CacheReadInputTokens
    s.mu.Unlock()
}

func (s *sseInterceptor) parseMessageDelta(data string) {
    var event messageDeltaEvent
    if err := json.Unmarshal([]byte(data), &event); err != nil {
        return
    }
    if event.Type != "message_delta" {
        return
    }
    s.mu.Lock()
    // message_delta usage is cumulative -- it supersedes message_start values
    s.usage.OutputTokens = event.Usage.OutputTokens
    // Update input tokens and cache tokens if present in the cumulative usage
    if event.Usage.InputTokens > 0 {
        s.usage.InputTokens = event.Usage.InputTokens
    }
    if event.Usage.CacheCreationInputTokens > 0 {
        s.usage.CacheCreationInputTokens = event.Usage.CacheCreationInputTokens
    }
    if event.Usage.CacheReadInputTokens > 0 {
        s.usage.CacheReadInputTokens = event.Usage.CacheReadInputTokens
    }
    s.mu.Unlock()
}
```

### File 2: MODIFY `proxy/proxy.go`

In `handleMessagesWithCompression`, after the request compression logic and before
calling `rp.ServeHTTP(w, r)`, we need to intercept the response.

The cleanest approach: use the ReverseProxy's `ModifyResponse` callback to wrap the
response body with our SSE interceptor. But since `ModifyResponse` is already set
on the shared ReverseProxy and we need per-request state, we should instead:

**Option A (recommended): Wrap the response body in ModifyResponse**

Modify the approach: instead of using the shared `ModifyResponse`, create a
per-request response interceptor. The way to do this in Go with `httputil.ReverseProxy`
is to use a custom `http.ResponseWriter` that wraps the body.

Actually, the simplest correct approach:

**Use ModifyResponse to wrap resp.Body with the SSE interceptor, and store the
interceptor reference in the request context so we can retrieve usage after the
response is fully streamed.**

But there's a timing problem: ModifyResponse fires when headers arrive, but the
body hasn't been read yet. We need to retrieve usage AFTER the body is fully streamed
to the client. Since `rp.ServeHTTP` is synchronous (it blocks until the response is
fully streamed), we can:

1. In ModifyResponse, check if the response is SSE (`Content-Type: text/event-stream`)
2. If so, wrap `resp.Body` with an `sseInterceptor`
3. Store the interceptor pointer in a per-request location (sync.Map keyed by request pointer, or a closure)
4. After `rp.ServeHTTP` returns, retrieve usage from the interceptor

**Implementation approach:**

Add a field to `Server` for per-request interceptor tracking:

```go
// In Server struct:
activeInterceptors sync.Map // map[*http.Request]*sseInterceptor
```

Modify `ModifyResponse` to wrap SSE response bodies:

```go
ModifyResponse: func(resp *http.Response) error {
    info, _ := resp.Request.Context().Value(requestInfoKey).(requestInfo)
    latency := time.Since(info.Start)
    // ... existing logging ...

    // Wrap SSE responses for usage extraction
    ct := resp.Header.Get("Content-Type")
    if strings.Contains(ct, "text/event-stream") && isMessagesPath(resp.Request.URL.Path) {
        interceptor := newSSEInterceptor(resp.Body)
        resp.Body = interceptor
        s.activeInterceptors.Store(resp.Request, interceptor)
    }

    return nil
},
```

Then in `handleMessagesWithCompression`, after `rp.ServeHTTP(w, r)` returns:

```go
rp.ServeHTTP(w, r)

// Extract usage from SSE stream if intercepted
if val, ok := s.activeInterceptors.LoadAndDelete(r); ok {
    interceptor := val.(*sseInterceptor)
    usage := interceptor.Usage()
    if usage.InputTokens > 0 || usage.OutputTokens > 0 {
        s.recordAPIUsage(usage)
        s.logf("[wet] API usage: input=%d output=%d cache_create=%d cache_read=%d\n",
            usage.InputTokens, usage.OutputTokens,
            usage.CacheCreationInputTokens, usage.CacheReadInputTokens)
    }
}
```

**IMPORTANT DETAIL**: The request pointer used in ModifyResponse's `resp.Request`
is the OUTGOING request (to upstream), not the INCOMING one. We need to use the
same key. Since `rp.Director` modifies the request in-place (same pointer), and
`resp.Request` in `ModifyResponse` is the same pointer, we should use the request
from the context rather than `r` directly.

Actually, looking at the Go source: `resp.Request` in ModifyResponse is the modified
request (the one Director touched). The `r` in `handleMessagesWithCompression` is
the original incoming request. These are the SAME pointer because Director modifies
in-place. So using `r` as the key works.

Wait -- actually `httputil.ReverseProxy` clones the request with `req.Clone()` in
newer Go versions. Let me use a different approach: store the interceptor in the
request context.

**Revised approach: Use a wrapper ResponseWriter**

Actually the cleanest approach that avoids all the pointer identity issues:

```go
func (s *Server) handleMessagesWithCompression(w http.ResponseWriter, r *http.Request, rp *httputil.ReverseProxy) {
    // ... existing compression logic ...

    r.Body = io.NopCloser(bytes.NewReader(forwardBody))
    r.ContentLength = int64(len(forwardBody))

    // Wrap the response to intercept SSE usage data
    iw := &interceptResponseWriter{
        ResponseWriter: w,
        server:         s,
        isMessages:     true,
    }
    rp.ServeHTTP(iw, r)

    // After streaming completes, record usage
    if iw.interceptor != nil {
        usage := iw.interceptor.Usage()
        if usage.InputTokens > 0 || usage.OutputTokens > 0 {
            s.recordAPIUsage(usage)
            s.logf("[wet] API usage: input=%d output=%d cache_create=%d cache_read=%d\n",
                usage.InputTokens, usage.OutputTokens,
                usage.CacheCreationInputTokens, usage.CacheReadInputTokens)
        }
    }
}
```

Hmm, but that doesn't work either because the body interception happens at the
response body level, not at the ResponseWriter level. The ReverseProxy reads
`resp.Body` and writes to `w` (the ResponseWriter). We need to intercept at the
`resp.Body` level.

**FINAL APPROACH: Use ModifyResponse closure with shared variable**

The simplest correct approach is to capture a pointer in a closure:

```go
func (s *Server) handleMessagesWithCompression(w http.ResponseWriter, r *http.Request, rp *httputil.ReverseProxy) {
    // ... existing compression logic (lines 168-252 stay the same) ...

    // We need to intercept the response. Create a one-shot ReverseProxy
    // that wraps the shared one's behavior but adds response body interception.
    // Actually -- we can't easily clone the ReverseProxy.

    // Instead: store per-request interceptor via sync.Map keyed on a unique ID
    // that we put in the request context.
    reqID := newRequestID() // use the same ID generator from stats/metrics.go
    ctx := context.WithValue(r.Context(), sseReqIDKey, reqID)
    r = r.WithContext(ctx)

    r.Body = io.NopCloser(bytes.NewReader(forwardBody))
    r.ContentLength = int64(len(forwardBody))
    rp.ServeHTTP(w, r)

    // Retrieve interceptor stored by ModifyResponse
    if val, ok := s.sseInterceptors.LoadAndDelete(reqID); ok {
        interceptor := val.(*sseInterceptor)
        usage := interceptor.Usage()
        if usage.InputTokens > 0 || usage.OutputTokens > 0 {
            s.recordAPIUsage(usage)
            s.logf("[wet] API usage: input=%d output=%d cache_create=%d cache_read=%d\n",
                usage.InputTokens, usage.OutputTokens,
                usage.CacheCreationInputTokens, usage.CacheReadInputTokens)
        }
    }
}
```

And in ModifyResponse:
```go
ModifyResponse: func(resp *http.Response) error {
    // ... existing logging ...

    // Check if this is an SSE messages response that needs usage extraction
    ct := resp.Header.Get("Content-Type")
    if strings.Contains(ct, "text/event-stream") {
        if reqID, ok := resp.Request.Context().Value(sseReqIDKey).(string); ok {
            interceptor := newSSEInterceptor(resp.Body)
            resp.Body = interceptor
            s.sseInterceptors.Store(reqID, interceptor)
        }
    }

    return nil
},
```

This approach:
- Uses request context to correlate the ModifyResponse call with the handleMessagesWithCompression call
- The reqID is unique per request, no pointer identity issues
- ModifyResponse wraps the body, the interceptor tees bytes through
- After ServeHTTP returns (response fully streamed), we grab usage from the interceptor

### File 3: MODIFY `stats/writer.go`

Add API usage fields to `SessionStats` and `RequestStats`:

```go
// Add to SessionStats:
APIInputTokens  int64
APIOutputTokens int64
APICacheCreate  int64
APICacheRead    int64

// Add to RequestStats:
APIInputTokens              int `json:"api_input_tokens,omitempty"`
APIOutputTokens             int `json:"api_output_tokens,omitempty"`
APICacheCreationInputTokens int `json:"api_cache_creation_input_tokens,omitempty"`
APICacheReadInputTokens     int `json:"api_cache_read_input_tokens,omitempty"`
```

### File 4: MODIFY `proxy/control.go`

Add `recordAPIUsage` method and expose usage in `StatusSnapshot`:

```go
func (s *Server) recordAPIUsage(usage UsageData) {
    s.sessionStats.mu.Lock()
    s.sessionStats.APIInputTokens += int64(usage.InputTokens)
    s.sessionStats.APIOutputTokens += int64(usage.OutputTokens)
    s.sessionStats.APICacheCreate += int64(usage.CacheCreationInputTokens)
    s.sessionStats.APICacheRead += int64(usage.CacheReadInputTokens)
    if s.sessionStats.LastRequest != nil {
        s.sessionStats.LastRequest.APIInputTokens = usage.InputTokens
        s.sessionStats.LastRequest.APIOutputTokens = usage.OutputTokens
        s.sessionStats.LastRequest.APICacheCreationInputTokens = usage.CacheCreationInputTokens
        s.sessionStats.LastRequest.APICacheReadInputTokens = usage.CacheReadInputTokens
    }
    s.sessionStats.mu.Unlock()
    _ = s.sessionStats.WriteStatsFile()
}
```

### File 5: NEW `proxy/sse_test.go`

Test the SSE interceptor with realistic Anthropic SSE payloads:

```go
package proxy

import (
    "io"
    "strings"
    "testing"
)

func TestSSEInterceptorExtractsUsage(t *testing.T) {
    sseStream := `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-20250514","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1523,"output_tokens":0,"cache_creation_input_tokens":100,"cache_read_input_tokens":50}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":42}}

event: message_stop
data: {"type":"message_stop"}

`
    body := io.NopCloser(strings.NewReader(sseStream))
    interceptor := newSSEInterceptor(body)

    // Read all bytes -- they should pass through unchanged
    got, err := io.ReadAll(interceptor)
    if err != nil {
        t.Fatalf("Read failed: %v", err)
    }
    interceptor.Close()

    if string(got) != sseStream {
        t.Error("SSE stream was modified -- bytes must pass through unchanged")
    }

    usage := interceptor.Usage()
    if usage.InputTokens != 1523 {
        t.Errorf("InputTokens = %d, want 1523", usage.InputTokens)
    }
    if usage.OutputTokens != 42 {
        t.Errorf("OutputTokens = %d, want 42", usage.OutputTokens)
    }
    if usage.CacheCreationInputTokens != 100 {
        t.Errorf("CacheCreationInputTokens = %d, want 100", usage.CacheCreationInputTokens)
    }
    if usage.CacheReadInputTokens != 50 {
        t.Errorf("CacheReadInputTokens = %d, want 50", usage.CacheReadInputTokens)
    }
}

func TestSSEInterceptorCumulativeDelta(t *testing.T) {
    // message_delta contains cumulative usage that supersedes message_start
    sseStream := `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"model":"claude-opus-4-6","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":2679,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":3}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":10682,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":510}}

event: message_stop
data: {"type":"message_stop"}

`
    body := io.NopCloser(strings.NewReader(sseStream))
    interceptor := newSSEInterceptor(body)
    _, _ = io.ReadAll(interceptor)
    interceptor.Close()

    usage := interceptor.Usage()
    // message_delta cumulative values should supersede message_start
    if usage.InputTokens != 10682 {
        t.Errorf("InputTokens = %d, want 10682 (cumulative from message_delta)", usage.InputTokens)
    }
    if usage.OutputTokens != 510 {
        t.Errorf("OutputTokens = %d, want 510", usage.OutputTokens)
    }
}

func TestSSEInterceptorNoUsage(t *testing.T) {
    // Non-SSE response
    body := io.NopCloser(strings.NewReader(`{"type":"message","usage":{"input_tokens":500}}`))
    interceptor := newSSEInterceptor(body)
    _, _ = io.ReadAll(interceptor)
    interceptor.Close()

    usage := interceptor.Usage()
    if usage.InputTokens != 0 {
        t.Errorf("Expected 0 input_tokens for non-SSE body, got %d", usage.InputTokens)
    }
}
```

## Summary of Changes

| File | Action | What |
|------|--------|------|
| `proxy/sse.go` | NEW | SSE interceptor: TeeReader + parser goroutine |
| `proxy/sse_test.go` | NEW | Unit tests for SSE interceptor |
| `proxy/proxy.go` | MODIFY | Add sseInterceptors sync.Map, reqID context key, wrap response in ModifyResponse, extract usage after ServeHTTP |
| `stats/writer.go` | MODIFY | Add API usage fields to SessionStats and RequestStats |
| `proxy/control.go` | MODIFY | Add recordAPIUsage method |
| `proxy/http_control.go` | MODIFY | Surface api_input_tokens in /_wet/status |

## Key Constraints

1. **Zero added latency**: The TeeReader approach means bytes flow to the client at exactly the same rate as before. The parser goroutine runs concurrently.
2. **No buffering**: TeeReader writes to the pipe as bytes are read by the client. The scanner in the parser goroutine processes them line by line.
3. **Graceful degradation**: If SSE parsing fails (malformed events, unexpected format), usage stays at 0 -- same as current behavior. No crashes.
4. **Memory bounded**: The scanner buffer is capped at 512KB. Individual SSE data lines exceeding this are skipped.
5. **Thread safe**: UsageData is protected by a mutex. The interceptor is safe for concurrent read after Close().

## Non-streaming responses

For completeness, we should also handle non-streaming JSON responses. These have
`Content-Type: application/json` and the usage is at the top level:
```json
{"id":"msg_...","type":"message","usage":{"input_tokens":1523,"output_tokens":42}}
```

This is lower priority since Claude Code always streams, but the implementation
should check Content-Type and handle both cases. For non-streaming, read the
response body, parse usage, then serve the original bytes. Since non-streaming
responses are typically small, buffering is acceptable.

For the initial fix, we can skip non-streaming and focus on SSE only.
