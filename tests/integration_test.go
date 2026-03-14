package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/buildoak/wet/config"
	"github.com/buildoak/wet/messages"
	"github.com/buildoak/wet/pipeline"
	"github.com/buildoak/wet/proxy"
)

// TestEndToEndProxy verifies the full proxy flow with compression.
func TestEndToEndProxy(t *testing.T) {
	// Mock upstream that echoes back the request body
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write(body)
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = findTestPort(t)
	cfg.Server.Upstream = upstream.URL
	cfg.Server.Mode = "auto"
	cfg.Staleness.Threshold = 2
	cfg.Bypass.ContentPatterns = nil

	srv := proxy.New(cfg)
	go srv.ListenAndServe()
	defer srv.Shutdown()
	waitReady(t, cfg.Server.Port)

	// Build a realistic 5-turn request
	reqBody := buildRealisticRequest()
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/messages", cfg.Server.Port)

	resp, err := http.Post(url, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	echoedBody, _ := io.ReadAll(resp.Body)

	// Parse the echoed body to check compression
	req, err := messages.ParseRequest(echoedBody)
	if err != nil {
		t.Fatalf("parse echoed body: %v", err)
	}

	// Turn 1 git status (turn 1, stale with threshold 2 at turn 5) should be compressed
	turn1Content := extractToolResultContent(t, req.Messages[2], 0)
	if !strings.HasPrefix(strings.TrimSpace(turn1Content), "[compressed:") {
		t.Errorf("expected turn 1 git status to be compressed, got: %.100s...", turn1Content)
	}

	// Turn 5 (current turn) should NOT be compressed
	turn5Content := extractToolResultContent(t, req.Messages[10], 0)
	if strings.HasPrefix(strings.TrimSpace(turn5Content), "[compressed:") {
		t.Error("expected turn 5 (current) to NOT be compressed")
	}

	// Verify non-tool messages are preserved
	if req.Messages[0].Role != "user" {
		t.Error("first message role should be user")
	}
}

// TestProxyFailOpen verifies the proxy forwards malformed requests unchanged.
func TestProxyFailOpen(t *testing.T) {
	var receivedBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = findTestPort(t)
	cfg.Server.Upstream = upstream.URL

	srv := proxy.New(cfg)
	go srv.ListenAndServe()
	defer srv.Shutdown()
	waitReady(t, cfg.Server.Port)

	// Send malformed JSON
	malformed := []byte(`{"not valid json`)
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/messages", cfg.Server.Port)
	resp, err := http.Post(url, "application/json", bytes.NewReader(malformed))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	resp.Body.Close()

	if !bytes.Equal(receivedBody, malformed) {
		t.Error("expected malformed body to be forwarded unchanged")
	}
}

// TestSSEStreamingEndToEnd verifies SSE events pass through without buffering.
func TestSSEStreamingEndToEnd(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(200)

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected Flusher")
			return
		}

		for i := 0; i < 5; i++ {
			fmt.Fprintf(w, "data: {\"event\":%d}\n\n", i)
			flusher.Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = findTestPort(t)
	cfg.Server.Upstream = upstream.URL

	srv := proxy.New(cfg)
	go srv.ListenAndServe()
	defer srv.Shutdown()
	waitReady(t, cfg.Server.Port)

	url := fmt.Sprintf("http://127.0.0.1:%d/v1/messages", cfg.Server.Port)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	// Read events and check timing
	start := time.Now()
	body, _ := io.ReadAll(resp.Body)
	elapsed := time.Since(start)

	events := strings.Count(string(body), "data:")
	if events != 5 {
		t.Fatalf("expected 5 events, got %d", events)
	}

	// Total should be around 200-250ms (5 events * 50ms delay), not 0ms (which would mean buffered)
	if elapsed < 150*time.Millisecond {
		t.Fatalf("events arrived too fast (%s), may be buffered", elapsed)
	}
}

// TestPipelineCompression tests the compression pipeline directly.
func TestPipelineCompression(t *testing.T) {
	req := buildPipelineTestRequest()
	cfg := config.Default()
	cfg.Staleness.Threshold = 1
	cfg.Bypass.ContentPatterns = nil

	result := pipeline.CompressRequest(req, cfg)

	t.Logf("Compression result: total=%d compressed=%d fresh=%d bypass=%d tokens=%d->%d",
		result.TotalToolResults, result.Compressed, result.SkippedFresh, result.SkippedBypass,
		result.TokensBefore, result.TokensAfter)

	if result.TotalToolResults == 0 {
		t.Fatal("expected some tool results")
	}

	if result.Compressed == 0 {
		t.Fatal("expected some compressions")
	}

	if result.SkippedFresh == 0 {
		t.Fatal("expected some fresh skips (current turn)")
	}

	if result.TokensBefore <= result.TokensAfter {
		t.Fatalf("expected tokens to decrease: before=%d after=%d", result.TokensBefore, result.TokensAfter)
	}
}

// --- Helpers ---

func findTestPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func waitReady(t *testing.T, port int) {
	t.Helper()
	client := &http.Client{Timeout: 250 * time.Millisecond}
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("proxy did not become ready")
}

func buildRealisticRequest() []byte {
	gitStatusOutput := makeGitStatusOutput(80)
	readOutput := makeLargeReadOutput(200)
	testOutputOld := makeTestOutput(50, 2)
	editConfirm := "File edited successfully."
	testOutputNew := makeTestOutput(50, 0)

	msgs := []map[string]any{
		{"role": "user", "content": "fix the bug in auth.go"},
		{"role": "assistant", "content": []map[string]any{
			{"type": "text", "text": "Let me check the current state."},
			{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{"command": "git status"}},
		}},
		{"role": "user", "content": []map[string]any{
			{"type": "tool_result", "tool_use_id": "t1", "content": gitStatusOutput},
		}},
		{"role": "assistant", "content": []map[string]any{
			{"type": "tool_use", "id": "t2", "name": "Read", "input": map[string]any{"file_path": "auth.go"}},
		}},
		{"role": "user", "content": []map[string]any{
			{"type": "tool_result", "tool_use_id": "t2", "content": readOutput},
		}},
		{"role": "assistant", "content": []map[string]any{
			{"type": "tool_use", "id": "t3", "name": "Bash", "input": map[string]any{"command": "go test ./..."}},
		}},
		{"role": "user", "content": []map[string]any{
			{"type": "tool_result", "tool_use_id": "t3", "content": testOutputOld},
		}},
		{"role": "assistant", "content": []map[string]any{
			{"type": "tool_use", "id": "t4", "name": "Write", "input": map[string]any{"file_path": "auth.go", "content": "fixed"}},
		}},
		{"role": "user", "content": []map[string]any{
			{"type": "tool_result", "tool_use_id": "t4", "content": editConfirm},
		}},
		{"role": "assistant", "content": []map[string]any{
			{"type": "tool_use", "id": "t5", "name": "Bash", "input": map[string]any{"command": "go test ./..."}},
		}},
		{"role": "user", "content": []map[string]any{
			{"type": "tool_result", "tool_use_id": "t5", "content": testOutputNew},
		}},
	}

	body := map[string]any{
		"model":      "claude-sonnet-4-20250514",
		"max_tokens": 8096,
		"messages":   msgs,
	}

	data, _ := json.Marshal(body)
	return data
}

func buildPipelineTestRequest() *messages.Request {
	body := buildRealisticRequest()
	req, err := messages.ParseRequest(body)
	if err != nil {
		panic(err)
	}
	return req
}

func makeGitStatusOutput(nFiles int) string {
	var b strings.Builder
	b.WriteString("On branch main\n")
	b.WriteString("Changes not staged for commit:\n")
	b.WriteString("  (use \"git add <file>...\" to update what will be committed)\n\n")
	for i := 0; i < nFiles; i++ {
		fmt.Fprintf(&b, "\tmodified:   src/module_%d/handler.go\n", i)
	}
	b.WriteString("\nUntracked files:\n")
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&b, "\tnew_file_%d.go\n", i)
	}
	return b.String()
}

func makeLargeReadOutput(nLines int) string {
	var b strings.Builder
	for i := 0; i < nLines; i++ {
		fmt.Fprintf(&b, "     %d\tpackage auth\n", i+1)
		fmt.Fprintf(&b, "     %d\t\n", i+1)
		fmt.Fprintf(&b, "     %d\tfunc Authenticate(user, pass string) (bool, error) {\n", i+1)
	}
	return b.String()
}

func makeTestOutput(nTests, nFails int) string {
	var b strings.Builder
	for i := 0; i < nTests; i++ {
		if i < nFails {
			fmt.Fprintf(&b, "--- FAIL: TestAuth%d (0.01s)\n", i)
			fmt.Fprintf(&b, "    auth_test.go:42: expected true, got false\n")
		} else {
			fmt.Fprintf(&b, "--- PASS: TestAuth%d (0.01s)\n", i)
		}
	}
	if nFails > 0 {
		fmt.Fprintf(&b, "FAIL\n")
		fmt.Fprintf(&b, "FAIL\tgithub.com/example/auth\t0.%ds\n", nTests)
	} else {
		fmt.Fprintf(&b, "PASS\n")
		fmt.Fprintf(&b, "ok  \tgithub.com/example/auth\t0.%ds\n", nTests)
	}
	return b.String()
}

func extractToolResultContent(t *testing.T, msg messages.Message, blockIdx int) string {
	t.Helper()
	blocks, _, err := messages.ParseContent(msg.Content)
	if err != nil {
		t.Fatalf("ParseContent: %v", err)
	}
	if blockIdx >= len(blocks) {
		t.Fatalf("block index %d out of range (have %d blocks)", blockIdx, len(blocks))
	}
	block := blocks[blockIdx]
	if block.Type != "tool_result" {
		t.Fatalf("expected tool_result, got %s", block.Type)
	}

	// Content can be a string or array
	var s string
	if err := json.Unmarshal(block.Content, &s); err == nil {
		return s
	}

	// Try as array of content blocks
	var innerBlocks []messages.ContentBlock
	if err := json.Unmarshal(block.Content, &innerBlocks); err == nil {
		for _, b := range innerBlocks {
			if b.Type == "text" {
				return b.Text
			}
		}
	}

	return string(block.Content)
}
