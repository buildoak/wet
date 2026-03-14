package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/otonashi/wet/config"
	"github.com/otonashi/wet/messages"
)

func newTestServerWithControl(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	t.Cleanup(upstream.Close)

	cfg := config.Default()
	cfg.Server.Upstream = upstream.URL
	cfg.Server.Mode = "passthrough"
	srv := NewWithLogOutput(cfg, nil)
	ts := httptest.NewServer(srv.httpSrv.Handler)
	t.Cleanup(ts.Close)
	return srv, ts
}

func TestHTTPStatus(t *testing.T) {
	_, ts := newTestServerWithControl(t)

	resp, err := http.Get(ts.URL + "/_wet/status")
	if err != nil {
		t.Fatalf("GET /_wet/status: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %s", ct)
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if _, ok := payload["uptime_seconds"]; !ok {
		t.Error("missing uptime_seconds")
	}
	if _, ok := payload["request_count"]; !ok {
		t.Error("missing request_count")
	}
	if _, ok := payload["api_input_tokens"]; !ok {
		t.Error("missing api_input_tokens")
	}
	if _, ok := payload["api_output_tokens"]; !ok {
		t.Error("missing api_output_tokens")
	}
	if _, ok := payload["paused"]; !ok {
		t.Error("missing paused")
	}
	if payload["paused"] != false {
		t.Errorf("expected paused=false, got %v", payload["paused"])
	}
}

func TestHTTPStatusMethodNotAllowed(t *testing.T) {
	_, ts := newTestServerWithControl(t)

	resp, err := http.Post(ts.URL+"/_wet/status", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /_wet/status: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

func TestHTTPInspectEmpty(t *testing.T) {
	_, ts := newTestServerWithControl(t)

	resp, err := http.Get(ts.URL + "/_wet/inspect")
	if err != nil {
		t.Fatalf("GET /_wet/inspect: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var results []inspectResultView
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty results, got %d", len(results))
	}
}

func TestHTTPInspectWithResults(t *testing.T) {
	srv, ts := newTestServerWithControl(t)

	// Store some tool results
	srv.StoreToolResults([]messages.ToolResultInfo{
		{
			ToolUseID:  "tu_abc",
			ToolName:   "Bash",
			Command:    "git status",
			Turn:       1,
			Stale:      true,
			IsError:    false,
			Content:    "On branch main\nChanges not staged...",
			TokenCount: 250,
			MsgIdx:     2,
			BlockIdx:   0,
		},
		{
			ToolUseID:  "tu_def",
			ToolName:   "Read",
			Turn:       3,
			Stale:      false,
			IsError:    false,
			Content:    "package main\n\nfunc main() {}",
			TokenCount: 50,
			MsgIdx:     6,
			BlockIdx:   0,
		},
	})

	resp, err := http.Get(ts.URL + "/_wet/inspect")
	if err != nil {
		t.Fatalf("GET /_wet/inspect: %v", err)
	}
	defer resp.Body.Close()

	var results []inspectResultView
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	r0 := results[0]
	if r0.ToolUseID != "tu_abc" {
		t.Errorf("expected tool_use_id tu_abc, got %s", r0.ToolUseID)
	}
	if r0.ToolName != "Bash" {
		t.Errorf("expected tool_name Bash, got %s", r0.ToolName)
	}
	if r0.Command != "git status" {
		t.Errorf("expected command 'git status', got %s", r0.Command)
	}
	if r0.Turn != 1 {
		t.Errorf("expected turn 1, got %d", r0.Turn)
	}
	if r0.CurrentTurn != 3 {
		t.Errorf("expected current_turn 3, got %d", r0.CurrentTurn)
	}
	if !r0.Stale {
		t.Error("expected stale=true")
	}
	if r0.TokenCount != 250 {
		t.Errorf("expected token_count 250, got %d", r0.TokenCount)
	}
	if r0.ContentPreview == "" {
		t.Error("expected non-empty content_preview")
	}

	r1 := results[1]
	if r1.ToolUseID != "tu_def" {
		t.Errorf("expected tool_use_id tu_def, got %s", r1.ToolUseID)
	}
	if r1.Stale {
		t.Error("expected stale=false for second result")
	}
	// Neither result has images
	if r0.HasImages {
		t.Error("expected has_images=false for Bash result")
	}
	if r1.HasImages {
		t.Error("expected has_images=false for Read result")
	}
}

func TestHTTPInspectHasImages(t *testing.T) {
	srv, ts := newTestServerWithControl(t)

	srv.StoreToolResults([]messages.ToolResultInfo{
		{
			ToolUseID:  "tu_img",
			ToolName:   "Read",
			Turn:       1,
			HasImages:  true,
			Content:    "[image content]",
			TokenCount: 5000,
			MsgIdx:     2,
			BlockIdx:   0,
		},
		{
			ToolUseID:  "tu_txt",
			ToolName:   "Bash",
			Command:    "echo hi",
			Turn:       2,
			HasImages:  false,
			Content:    "hi",
			TokenCount: 10,
			MsgIdx:     4,
			BlockIdx:   0,
		},
	})

	resp, err := http.Get(ts.URL + "/_wet/inspect")
	if err != nil {
		t.Fatalf("GET /_wet/inspect: %v", err)
	}
	defer resp.Body.Close()

	var results []inspectResultView
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if !results[0].HasImages {
		t.Error("expected has_images=true for image result")
	}
	if results[1].HasImages {
		t.Error("expected has_images=false for text result")
	}
}

func TestHTTPCompress(t *testing.T) {
	srv, ts := newTestServerWithControl(t)

	body, _ := json.Marshal(map[string]any{"ids": []string{"tu_abc", "tu_def"}})
	resp, err := http.Post(ts.URL+"/_wet/compress", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /_wet/compress: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result["status"] != "queued" {
		t.Errorf("expected status=queued, got %v", result["status"])
	}
	if result["count"] != float64(2) {
		t.Errorf("expected count=2, got %v", result["count"])
	}

	// Verify IDs were queued
	ids := srv.DrainCompressIDs()
	if len(ids) != 2 {
		t.Fatalf("expected 2 queued IDs, got %d", len(ids))
	}
	if ids[0] != "tu_abc" || ids[1] != "tu_def" {
		t.Errorf("unexpected IDs: %v", ids)
	}
}

func TestHTTPCompressEmptyIDs(t *testing.T) {
	_, ts := newTestServerWithControl(t)

	body, _ := json.Marshal(map[string]any{"ids": []string{}})
	resp, err := http.Post(ts.URL+"/_wet/compress", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /_wet/compress: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHttpCompress_AgentRequiresReplacement(t *testing.T) {
	cfg := config.Default()
	cfg.Server.Upstream = "http://example.com"
	cfg.Server.Mode = "passthrough"
	srv := NewWithLogOutput(cfg, nil)
	t.Cleanup(srv.Shutdown)

	srv.StoreToolResults([]messages.ToolResultInfo{
		{
			ToolUseID: "tu_agent",
			ToolName:  "Agent",
		},
	})

	body, _ := json.Marshal(map[string]any{"ids": []string{"tu_agent"}})
	req := httptest.NewRequest(http.MethodPost, "/_wet/compress", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result["code"] != "AGENT_REQUIRES_REPLACEMENT" {
		t.Fatalf("expected code AGENT_REQUIRES_REPLACEMENT, got %q", result["code"])
	}
}

func TestHTTPCompressInvalidJSON(t *testing.T) {
	_, ts := newTestServerWithControl(t)

	resp, err := http.Post(ts.URL+"/_wet/compress", "application/json", bytes.NewReader([]byte(`{broken`)))
	if err != nil {
		t.Fatalf("POST /_wet/compress: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHTTPPauseResume(t *testing.T) {
	srv, ts := newTestServerWithControl(t)

	// Initially not paused
	if srv.IsPaused() {
		t.Fatal("should not be paused initially")
	}

	// Pause
	resp, err := http.Post(ts.URL+"/_wet/pause", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /_wet/pause: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !srv.IsPaused() {
		t.Error("should be paused after POST /pause")
	}

	// Verify status reflects pause
	statusResp, err := http.Get(ts.URL + "/_wet/status")
	if err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	json.NewDecoder(statusResp.Body).Decode(&status)
	statusResp.Body.Close()
	if status["paused"] != true {
		t.Errorf("expected paused=true in status, got %v", status["paused"])
	}

	// Resume
	resp, err = http.Post(ts.URL+"/_wet/resume", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /_wet/resume: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if srv.IsPaused() {
		t.Error("should not be paused after POST /resume")
	}
}

func TestHTTPRulesGetEmpty(t *testing.T) {
	_, ts := newTestServerWithControl(t)

	resp, err := http.Get(ts.URL + "/_wet/rules")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var rules map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rules); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Default config has empty rules
	if len(rules) != 0 {
		t.Fatalf("expected empty rules, got %v", rules)
	}
}

func TestHTTPRulesSet(t *testing.T) {
	_, ts := newTestServerWithControl(t)

	// Set a rule with stale_after
	body, _ := json.Marshal(map[string]any{"key": "Bash", "stale_after": 5})
	resp, err := http.Post(ts.URL+"/_wet/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Set a rule with strategy
	body, _ = json.Marshal(map[string]any{"key": "Read", "strategy": "preserve"})
	resp, err = http.Post(ts.URL+"/_wet/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Verify rules via GET
	resp, err = http.Get(ts.URL + "/_wet/rules")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var rules map[string]any
	json.NewDecoder(resp.Body).Decode(&rules)

	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d: %v", len(rules), rules)
	}
}

func TestHTTPRulesSetMissingKey(t *testing.T) {
	_, ts := newTestServerWithControl(t)

	body, _ := json.Marshal(map[string]any{"strategy": "preserve"})
	resp, err := http.Post(ts.URL+"/_wet/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHTTPParallelInstances(t *testing.T) {
	// Create two independent servers to verify they don't interfere
	srv1, ts1 := newTestServerWithControl(t)
	srv2, ts2 := newTestServerWithControl(t)

	// Store different tool results in each
	srv1.StoreToolResults([]messages.ToolResultInfo{
		{ToolUseID: "srv1_id", ToolName: "Bash", Command: "ls", Turn: 1, Content: "file1.go"},
	})
	srv2.StoreToolResults([]messages.ToolResultInfo{
		{ToolUseID: "srv2_id", ToolName: "Read", Turn: 2, Content: "package main"},
	})

	// Pause srv1
	resp, _ := http.Post(ts1.URL+"/_wet/pause", "application/json", nil)
	resp.Body.Close()

	// Verify srv1 is paused, srv2 is not
	if !srv1.IsPaused() {
		t.Error("srv1 should be paused")
	}
	if srv2.IsPaused() {
		t.Error("srv2 should NOT be paused")
	}

	// Verify inspect returns correct data per instance
	resp1, _ := http.Get(ts1.URL + "/_wet/inspect")
	var results1 []inspectResultView
	json.NewDecoder(resp1.Body).Decode(&results1)
	resp1.Body.Close()

	resp2, _ := http.Get(ts2.URL + "/_wet/inspect")
	var results2 []inspectResultView
	json.NewDecoder(resp2.Body).Decode(&results2)
	resp2.Body.Close()

	if len(results1) != 1 || results1[0].ToolUseID != "srv1_id" {
		t.Errorf("srv1 inspect returned wrong data: %+v", results1)
	}
	if len(results2) != 1 || results2[0].ToolUseID != "srv2_id" {
		t.Errorf("srv2 inspect returned wrong data: %+v", results2)
	}

	// Queue compress on srv2, verify srv1 is not affected
	compBody, _ := json.Marshal(map[string]any{"ids": []string{"srv2_id"}})
	resp, _ = http.Post(ts2.URL+"/_wet/compress", "application/json", bytes.NewReader(compBody))
	resp.Body.Close()

	ids1 := srv1.DrainCompressIDs()
	ids2 := srv2.DrainCompressIDs()
	if len(ids1) != 0 {
		t.Errorf("srv1 should have no compress IDs, got %v", ids1)
	}
	if len(ids2) != 1 || ids2[0] != "srv2_id" {
		t.Errorf("srv2 should have [srv2_id], got %v", ids2)
	}
}

func TestHTTPContentPreviewTruncation(t *testing.T) {
	srv, ts := newTestServerWithControl(t)

	// Store a result with very long content
	longContent := make([]byte, 1000)
	for i := range longContent {
		longContent[i] = 'a'
	}
	srv.StoreToolResults([]messages.ToolResultInfo{
		{ToolUseID: "tu_long", ToolName: "Bash", Content: string(longContent), TokenCount: 250},
	})

	resp, err := http.Get(ts.URL + "/_wet/inspect")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var results []inspectResultView
	json.NewDecoder(resp.Body).Decode(&results)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	preview := results[0].ContentPreview
	if len([]rune(preview)) != 200 {
		t.Errorf("expected preview truncated to 200 runes, got %d", len([]rune(preview)))
	}
}
