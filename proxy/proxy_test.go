package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/buildoak/wet/config"
)

func TestHealthEndpoint(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("upstream should not be called for /health: %s", r.URL.Path)
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Server.Upstream = upstream.URL
	srv := New(cfg)
	proxyTS := httptest.NewServer(srv.httpSrv.Handler)
	defer proxyTS.Close()

	resp, err := http.Get(proxyTS.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode health JSON: %v", err)
	}

	if payload["status"] != "ok" {
		t.Fatalf("expected status=ok, got %#v", payload["status"])
	}
}

func TestPassthrough(t *testing.T) {
	requestBody := `{"model":"claude-sonnet","messages":[{"role":"user","content":"hello"}]}`
	responseBody := `{"id":"msg_123","type":"message"}`

	gotBody := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("expected /v1/messages, got %s", r.URL.Path)
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		gotBody <- string(b)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(responseBody))
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Server.Upstream = upstream.URL
	srv := New(cfg)
	proxyTS := httptest.NewServer(srv.httpSrv.Handler)
	defer proxyTS.Close()

	resp, err := http.Post(proxyTS.URL+"/v1/messages", "application/json", strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("POST proxy failed: %v", err)
	}
	defer resp.Body.Close()

	select {
	case body := <-gotBody:
		if body != requestBody {
			t.Fatalf("forwarded body mismatch\nwant: %s\ngot:  %s", requestBody, body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for upstream request")
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if string(b) != responseBody {
		t.Fatalf("response mismatch\nwant: %s\ngot:  %s", responseBody, string(b))
	}
}

func TestPassthroughWithoutV1Prefix(t *testing.T) {
	requestBody := `{"model":"claude-sonnet","messages":[{"role":"user","content":"hello"}]}`

	upstreamPath := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Server.Upstream = upstream.URL
	srv := New(cfg)
	proxyTS := httptest.NewServer(srv.httpSrv.Handler)
	defer proxyTS.Close()

	resp, err := http.Post(proxyTS.URL+"/messages", "application/json", strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("POST proxy failed: %v", err)
	}
	defer resp.Body.Close()

	select {
	case p := <-upstreamPath:
		// Proxy now forwards paths as-is (no normalization).
		// Claude Code sends /v1/messages; requests without /v1 prefix are forwarded unchanged.
		if p != "/messages" {
			t.Fatalf("expected upstream path /messages, got %s", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for upstream request")
	}
}

func TestHeadersForwarded(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token-123" {
			t.Fatalf("authorization not forwarded: %q", got)
		}
		if got := r.Header.Get("x-api-key"); got != "key-abc" {
			t.Fatalf("x-api-key not forwarded: %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Fatalf("anthropic-version not forwarded: %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Server.Upstream = upstream.URL
	srv := New(cfg)
	proxyTS := httptest.NewServer(srv.httpSrv.Handler)
	defer proxyTS.Close()

	req, err := http.NewRequest(http.MethodPost, proxyTS.URL+"/v1/messages", bytes.NewBufferString(`{"x":1}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer token-123")
	req.Header.Set("x-api-key", "key-abc")
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
}

func TestSetLogOutput(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Server.Upstream = upstream.URL
	srv := New(cfg)
	var logs bytes.Buffer
	srv.SetLogOutput(&logs)
	proxyTS := httptest.NewServer(srv.httpSrv.Handler)
	defer proxyTS.Close()

	resp, err := http.Get(proxyTS.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET proxy failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	if !strings.Contains(logs.String(), "[wet] GET /v1/models -> 204") {
		t.Fatalf("expected request log to be written to custom output, got %q", logs.String())
	}
}

func TestSSEStreaming(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("upstream response writer is not a flusher")
		}

		for i := 1; i <= 3; i++ {
			time.Sleep(100 * time.Millisecond)
			fmt.Fprintf(w, "data: event-%d\n\n", i)
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Server.Upstream = upstream.URL
	srv := New(cfg)
	proxyTS := httptest.NewServer(srv.httpSrv.Handler)
	defer proxyTS.Close()

	start := time.Now()
	resp, err := http.Get(proxyTS.URL + "/v1/messages")
	if err != nil {
		t.Fatalf("GET SSE failed: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	firstEventAt := time.Duration(0)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("failed reading first SSE line: %v", err)
		}
		if strings.HasPrefix(line, "data: ") {
			firstEventAt = time.Since(start)
			break
		}
	}

	if firstEventAt == 0 {
		t.Fatal("did not observe first event")
	}
	if firstEventAt >= 250*time.Millisecond {
		t.Fatalf("first SSE event arrived too late (possible buffering): %s", firstEventAt)
	}

	remaining, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read remaining SSE stream: %v", err)
	}
	if !strings.Contains(string(remaining), "event-3") {
		t.Fatalf("expected full SSE stream, got: %q", string(remaining))
	}
}

// TestSubagentDoesNotOverwriteMainSessionResults verifies that a second
// request with a different system prompt (a subagent) does not overwrite the
// main session's stored tool results.
func TestSubagentDoesNotOverwriteMainSessionResults(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_x","type":"message"}`))
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Server.Upstream = upstream.URL
	cfg.Server.Mode = "passthrough"
	srv := New(cfg)
	proxyTS := httptest.NewServer(srv.httpSrv.Handler)
	defer proxyTS.Close()

	// Helper: build a minimal messages request body with a tool result and system prompt.
	makeBody := func(system, toolUseID, resultContent string) string {
		return fmt.Sprintf(
			`{"system":%q,"messages":[`+
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":%q}]}`+
				`]}`,
			system, toolUseID, resultContent,
		)
	}

	// First request — main session.
	mainBody := makeBody("You are the main session assistant.", "tu_main", "main result content")
	resp, err := http.Post(proxyTS.URL+"/v1/messages", "application/json", strings.NewReader(mainBody))
	if err != nil {
		t.Fatalf("main session request: %v", err)
	}
	resp.Body.Close()

	// Capture tool results after main session.
	mainResults := srv.GetToolResults()
	if len(mainResults) == 0 {
		t.Fatal("expected tool results after main session request, got none")
	}
	if mainResults[0].ToolUseID != "tu_main" {
		t.Fatalf("expected tu_main in results, got %q", mainResults[0].ToolUseID)
	}

	// Second request — subagent with a different system prompt.
	subBody := makeBody("You are a subagent with a completely different task.", "tu_sub", "subagent result content")
	resp, err = http.Post(proxyTS.URL+"/v1/messages", "application/json", strings.NewReader(subBody))
	if err != nil {
		t.Fatalf("subagent request: %v", err)
	}
	resp.Body.Close()

	// Tool results must still reflect the main session, not the subagent.
	afterSubResults := srv.GetToolResults()
	if len(afterSubResults) == 0 {
		t.Fatal("expected tool results to still be present after subagent request")
	}
	if afterSubResults[0].ToolUseID != "tu_main" {
		t.Fatalf("subagent overwrote main session results: got %q, want tu_main",
			afterSubResults[0].ToolUseID)
	}
}

// TestMainSessionHashComputed verifies that HashSystemPrompt is stable and
// that the main session hash is set on the first request.
func TestMainSessionHashComputed(t *testing.T) {
	h1 := HashSystemPrompt("hello world")
	h2 := HashSystemPrompt("hello world")
	if h1 != h2 {
		t.Fatalf("hash not deterministic: %q != %q", h1, h2)
	}

	hDiff := HashSystemPrompt("different prompt")
	if h1 == hDiff {
		t.Fatal("different prompts produced the same hash")
	}

	// Hash should be 8 hex characters (4 bytes).
	if len(h1) != 8 {
		t.Fatalf("expected 8 hex chars, got %d: %q", len(h1), h1)
	}
}

// TestMainSessionSamePromptAccepted verifies that two requests with the same
// system prompt are both treated as main-session requests.
func TestMainSessionSamePromptAccepted(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Server.Upstream = upstream.URL
	cfg.Server.Mode = "passthrough"
	srv := New(cfg)
	proxyTS := httptest.NewServer(srv.httpSrv.Handler)
	defer proxyTS.Close()

	makeBody := func(toolUseID string) string {
		return fmt.Sprintf(
			`{"system":"Same system prompt for every turn.","messages":[`+
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":"content"}]}`+
				`]}`,
			toolUseID,
		)
	}

	// First request.
	resp, err := http.Post(proxyTS.URL+"/v1/messages", "application/json",
		strings.NewReader(makeBody("tu_first")))
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	resp.Body.Close()

	// Second request — same system prompt (normal continuation of the main session).
	resp, err = http.Post(proxyTS.URL+"/v1/messages", "application/json",
		strings.NewReader(makeBody("tu_second")))
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	resp.Body.Close()

	// Results should reflect the SECOND request since both are the main session.
	results := srv.GetToolResults()
	if len(results) == 0 {
		t.Fatal("expected tool results after second request")
	}
	if results[0].ToolUseID != "tu_second" {
		t.Fatalf("expected tu_second after second main-session request, got %q",
			results[0].ToolUseID)
	}
}

func TestFilterPersistableReplacementsSkipsDuplicateIDs(t *testing.T) {
	in := map[string]string{
		"dup":  "[compressed: dup]",
		"uniq": "[compressed: uniq]",
	}
	counts := map[string]int{
		"dup":  2,
		"uniq": 1,
	}

	got := filterPersistableReplacements(in, counts)
	if len(got) != 1 {
		t.Fatalf("expected 1 persistable replacement, got %d", len(got))
	}
	if _, ok := got["dup"]; ok {
		t.Fatal("duplicate ID should be excluded from persisted replacements")
	}
	if got["uniq"] != "[compressed: uniq]" {
		t.Fatalf("unexpected replacement for uniq: %q", got["uniq"])
	}
}
