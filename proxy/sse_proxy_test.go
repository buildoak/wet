package proxy

import (
	"bytes"
	"compress/gzip"
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

func TestSSEUsageInProxy(t *testing.T) {
	ssePayload := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-4-6\",\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":2095,\"output_tokens\":1,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":42}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer is not a flusher")
		}

		lines := strings.Split(ssePayload, "\n")
		for _, line := range lines {
			fmt.Fprintf(w, "%s\n", line)
			flusher.Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Server.Upstream = upstream.URL
	cfg.Server.Mode = "passthrough"

	var logBuf bytes.Buffer
	srv := NewWithLogOutput(cfg, &logBuf)
	proxyTS := httptest.NewServer(srv.httpSrv.Handler)
	defer proxyTS.Close()

	requestBody := `{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hello"}],"stream":true}`
	resp, err := http.Post(proxyTS.URL+"/v1/messages", "application/json", strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	t.Logf("Response body length: %d bytes", len(body))

	snap := srv.StatusSnapshot()
	if snap.APIInputTokens != 2095 {
		t.Errorf("APIInputTokens = %d, want 2095", snap.APIInputTokens)
	}
	if snap.APIOutputTokens != 42 {
		t.Errorf("APIOutputTokens = %d, want 42", snap.APIOutputTokens)
	}
}

// TestSSEUsageWithGzipUpstream verifies that token extraction works even when
// the upstream response would normally be gzip-compressed. The proxy strips
// Accept-Encoding from the outgoing request so the upstream returns plain text.
// This test simulates the worst case: an upstream that conditionally compresses
// based on Accept-Encoding, proving that the Director's header stripping works.
func TestSSEUsageWithGzipUpstream(t *testing.T) {
	ssePayload := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_gz\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-4-6\",\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":5000,\"output_tokens\":1,\"cache_creation_input_tokens\":200,\"cache_read_input_tokens\":300}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":150}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ae := r.Header.Get("Accept-Encoding")
		t.Logf("upstream Accept-Encoding: %q", ae)

		if strings.Contains(ae, "gzip") {
			// Upstream returns gzip-compressed SSE (simulating Anthropic behind Cloudflare)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			gz.Write([]byte(ssePayload))
			gz.Close()
		} else {
			// Upstream returns plain SSE (the proxy stripped Accept-Encoding)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte(ssePayload))
		}
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Server.Upstream = upstream.URL
	cfg.Server.Mode = "passthrough"

	var logBuf bytes.Buffer
	srv := NewWithLogOutput(cfg, &logBuf)
	proxyTS := httptest.NewServer(srv.httpSrv.Handler)
	defer proxyTS.Close()

	requestBody := `{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hello"}],"stream":true}`

	// Send request with Accept-Encoding: gzip (mimicking Claude Code)
	req, err := http.NewRequest("POST", proxyTS.URL+"/v1/messages", strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")

	// Use a fresh client to avoid shared state from http.DefaultClient's
	// transport pool, which can cause the SSE interceptor to miss events
	// on cold-start runs.
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	_, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	// The HTTP response body is fully received by the client before the
	// server-side handler finishes recording API usage stats (the handler
	// writes the response via the reverse proxy, then extracts SSE usage
	// and records it — but the client's io.ReadAll returns as soon as the
	// response body is complete, not when the handler returns). Poll with
	// a short timeout to let the handler finish.
	var snap StatusSnapshot
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap = srv.StatusSnapshot()
		if snap.APIInputTokens > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	snapJSON, _ := json.MarshalIndent(snap, "", "  ")
	t.Logf("StatusSnapshot: %s", snapJSON)

	if snap.APIInputTokens != 5000 {
		t.Errorf("APIInputTokens = %d, want 5000", snap.APIInputTokens)
	}
	if snap.APIOutputTokens != 150 {
		t.Errorf("APIOutputTokens = %d, want 150", snap.APIOutputTokens)
	}
	if snap.APICacheCreate != 200 {
		t.Errorf("APICacheCreate = %d, want 200", snap.APICacheCreate)
	}
	if snap.APICacheRead != 300 {
		t.Errorf("APICacheRead = %d, want 300", snap.APICacheRead)
	}
}
