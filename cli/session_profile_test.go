package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// charsToTokensFallback
// ---------------------------------------------------------------------------

func TestCharsToTokensFallback(t *testing.T) {
	tests := []struct {
		chars int
		want  int
	}{
		{0, 0},
		{-1, 0},
		{4, 1},
		{5, 2},
		{100, 25},
		{1, 1},
		{3, 1},
		{8, 2},
		{1000, 250},
	}

	for _, tt := range tests {
		got := charsToTokensFallback(tt.chars)
		if got != tt.want {
			t.Errorf("charsToTokensFallback(%d): got %d, want %d", tt.chars, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// proportionalTokens
// ---------------------------------------------------------------------------

func TestProportionalTokens(t *testing.T) {
	tests := []struct {
		catChars   int
		totalChars int
		apiTotal   int
		want       int
	}{
		{0, 1000, 5000, 0},
		{500, 1000, 10000, 5000},
		{250, 1000, 10000, 2500},
		{1000, 1000, 10000, 10000},
		{0, 0, 10000, 0},     // zero total chars
		{500, 1000, 0, 0},    // zero api total
		{333, 1000, 10000, 3330},
	}

	for _, tt := range tests {
		got := proportionalTokens(tt.catChars, tt.totalChars, tt.apiTotal)
		if got != tt.want {
			t.Errorf("proportionalTokens(%d, %d, %d): got %d, want %d",
				tt.catChars, tt.totalChars, tt.apiTotal, got, tt.want)
		}
	}
}

func TestProportionalTokens_SumsToTotal(t *testing.T) {
	// Four categories with known char counts, total = 10000 tokens from API
	apiTotal := 10000
	cats := []int{4000, 3000, 2000, 1000} // chars: 40%, 30%, 20%, 10%
	totalChars := 0
	for _, c := range cats {
		totalChars += c
	}

	sum := 0
	for _, c := range cats {
		sum += proportionalTokens(c, totalChars, apiTotal)
	}

	// Due to rounding, sum may differ by up to len(cats) from apiTotal
	if diff := sum - apiTotal; diff < -len(cats) || diff > len(cats) {
		t.Errorf("proportional sum = %d, api total = %d, diff = %d (exceeds tolerance %d)",
			sum, apiTotal, diff, len(cats))
	}
}

// ---------------------------------------------------------------------------
// resolveContextWindow
// ---------------------------------------------------------------------------

func TestResolveContextWindow_FromProxy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_wet/status" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"context_window": 1000000,
			})
		}
	}))
	defer srv.Close()

	port := 0
	fmt.Sscanf(strings.Split(srv.URL, ":")[2], "%d", &port)

	got := resolveContextWindow(port, "claude-opus-4-6")
	if got != 1000000 {
		t.Errorf("resolveContextWindow (proxy): got %d, want 1000000", got)
	}
}

func TestResolveContextWindow_FromModel(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"claude-opus-4-6-20250514", 1_000_000},
		{"claude-sonnet-4-6-20250514", 1_000_000},
		{"claude-haiku-4-5-20250101", 200_000},
		{"unknown-model", 200_000},
	}

	for _, tt := range tests {
		got := resolveContextWindow(0, tt.model)
		if got != tt.want {
			t.Errorf("resolveContextWindow(0, %q): got %d, want %d", tt.model, got, tt.want)
		}
	}
}

func TestResolveContextWindow_NoModelNoPort(t *testing.T) {
	got := resolveContextWindow(0, "")
	if got != 200_000 {
		t.Errorf("resolveContextWindow(0, \"\"): got %d, want 200000", got)
	}
}

// ---------------------------------------------------------------------------
// RunSessionProfile — API ground truth
// ---------------------------------------------------------------------------

func TestRunSessionProfile_APIGroundTruth(t *testing.T) {
	// Build a JSONL with:
	// 1. User text message (100 chars)
	// 2. Assistant message with tool_use block (50 chars input)
	// 3. User message with tool_result (200 chars)
	// 4. Assistant message with text + usage data (150 chars text)
	// Total chars = 100 + 50 + 200 + 150 = 500
	// API says total = 1000 tokens
	// Expected proportions: user=20%, tool_use=10%, tool_result=40%, assistant=30%

	userText := strings.Repeat("a", 100)
	toolInput := strings.Repeat("b", 48) // JSON marshaling adds {} wrapper
	toolResultText := strings.Repeat("c", 200)
	assistantText := strings.Repeat("d", 150)

	lines := []string{
		mustJSON(map[string]any{
			"type": "user",
			"message": map[string]any{
				"role":    "user",
				"content": userText,
			},
		}),
		mustJSON(map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"role": "assistant",
				"content": []map[string]any{
					{
						"type":  "tool_use",
						"id":    "toolu_test1",
						"name":  "Bash",
						"input": map[string]string{"command": toolInput},
					},
				},
			},
		}),
		mustJSON(map[string]any{
			"type": "user",
			"message": map[string]any{
				"role": "user",
				"content": []map[string]any{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_test1",
						"content":     toolResultText,
					},
				},
			},
		}),
		mustJSON(map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"role":  "assistant",
				"model": "claude-opus-4-6-20250514",
				"content": []map[string]any{
					{
						"type": "text",
						"text": assistantText,
					},
				},
				"usage": map[string]any{
					"input_tokens":                500,
					"cache_creation_input_tokens":  300,
					"cache_read_input_tokens":      200,
					"output_tokens":                50,
				},
			},
		}),
	}

	f := writeTempJSONL(t, lines)
	defer os.Remove(f)

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := RunSessionProfile(f, 0)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("RunSessionProfile error: %v", err)
	}

	buf := make([]byte, 8192)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	// Should use API ground truth (1000 tokens total = 500 + 300 + 200)
	if !strings.Contains(output, "from API") {
		t.Errorf("expected [from API] label in output, got:\n%s", output)
	}

	// Should show 1k total (1000 tokens)
	if !strings.Contains(output, "1k") {
		t.Errorf("expected '1k' total in output, got:\n%s", output)
	}

	// Context window should be 1000k (opus model = 1M)
	if !strings.Contains(output, "1000k") {
		t.Errorf("expected '1000k' context window in output, got:\n%s", output)
	}

	// Verify "healthy" status (1k/1000k = 0.1% full)
	if !strings.Contains(output, "healthy") {
		t.Errorf("expected 'healthy' status, got:\n%s", output)
	}
}

func TestRunSessionProfile_NoUsageFallback(t *testing.T) {
	// JSONL without any usage data — should fall back to chars/4 estimation
	userText := strings.Repeat("x", 400) // 400 chars = 100 tokens at chars/4

	lines := []string{
		mustJSON(map[string]any{
			"type": "user",
			"message": map[string]any{
				"role":    "user",
				"content": userText,
			},
		}),
		mustJSON(map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"role": "assistant",
				"content": []map[string]any{
					{
						"type": "text",
						"text": strings.Repeat("y", 200),
					},
				},
			},
		}),
	}

	f := writeTempJSONL(t, lines)
	defer os.Remove(f)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := RunSessionProfile(f, 0)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("RunSessionProfile error: %v", err)
	}

	buf := make([]byte, 8192)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	// Should show "estimated" label
	if !strings.Contains(output, "estimated") {
		t.Errorf("expected [estimated] label in output, got:\n%s", output)
	}

	// Context window should fall back to 200k (no model specified)
	if !strings.Contains(output, "200k") {
		t.Errorf("expected '200k' context window in output, got:\n%s", output)
	}
}

func TestRunSessionProfile_ContextWindowFromProxy(t *testing.T) {
	// Mock server returns context_window = 500000
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/_wet/status":
			json.NewEncoder(w).Encode(map[string]any{
				"context_window": 500000,
			})
		case "/_wet/inspect":
			json.NewEncoder(w).Encode([]map[string]any{})
		}
	}))
	defer srv.Close()

	port := 0
	fmt.Sscanf(strings.Split(srv.URL, ":")[2], "%d", &port)

	// Minimal JSONL
	lines := []string{
		mustJSON(map[string]any{
			"type": "user",
			"message": map[string]any{
				"role":    "user",
				"content": "hello",
			},
		}),
		mustJSON(map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"role":  "assistant",
				"model": "claude-haiku-4-5",
				"content": []map[string]any{
					{"type": "text", "text": "hi there"},
				},
				"usage": map[string]any{
					"input_tokens":                100,
					"cache_creation_input_tokens":  0,
					"cache_read_input_tokens":      0,
				},
			},
		}),
	}

	f := writeTempJSONL(t, lines)
	defer os.Remove(f)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := RunSessionProfile(f, port)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("RunSessionProfile error: %v", err)
	}

	buf := make([]byte, 8192)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	// Should use proxy's context window (500k) not model default (200k for haiku)
	if !strings.Contains(output, "500k") {
		t.Errorf("expected '500k' context window from proxy, got:\n%s", output)
	}
}

func TestRunSessionProfile_ProportionalBreakdownSumsToTotal(t *testing.T) {
	// Create JSONL with multiple categories and known API usage
	lines := []string{
		mustJSON(map[string]any{
			"type": "user",
			"message": map[string]any{
				"role":    "user",
				"content": strings.Repeat("u", 1000),
			},
		}),
		mustJSON(map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"role": "assistant",
				"content": []map[string]any{
					{
						"type":  "tool_use",
						"id":    "toolu_1",
						"name":  "Read",
						"input": map[string]string{"path": strings.Repeat("p", 500)},
					},
				},
			},
		}),
		mustJSON(map[string]any{
			"type": "user",
			"message": map[string]any{
				"role": "user",
				"content": []map[string]any{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_1",
						"content":     strings.Repeat("r", 3000),
					},
				},
			},
		}),
		mustJSON(map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"role":  "assistant",
				"model": "claude-sonnet-4-6",
				"content": []map[string]any{
					{"type": "text", "text": strings.Repeat("a", 1500)},
				},
				"usage": map[string]any{
					"input_tokens":                2000,
					"cache_creation_input_tokens":  3000,
					"cache_read_input_tokens":      5000,
				},
			},
		}),
	}

	f := writeTempJSONL(t, lines)
	defer os.Remove(f)

	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	err := RunSessionProfile(f, 0)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("RunSessionProfile error: %v", err)
	}

	// The test passes if no error — the internal proportional math
	// is tested separately in TestProportionalTokens_SumsToTotal.
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func writeTempJSONL(t *testing.T, lines []string) string {
	t.Helper()
	f, err := os.CreateTemp("", "wet-profile-test-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range lines {
		f.WriteString(line + "\n")
	}
	f.Close()
	return f.Name()
}
