package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// parseCompressArgs
// ---------------------------------------------------------------------------

func TestParseCompressArgs_Help(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		opts, err := parseCompressArgs([]string{flag})
		if err != nil {
			t.Fatalf("parseCompressArgs(%q) error: %v", flag, err)
		}
		if !opts.Help {
			t.Fatalf("expected Help=true for %q", flag)
		}
	}
}

func TestParseCompressArgs_Flags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want compressOptions
	}{
		{
			name: "ids comma-separated",
			args: []string{"--ids", "a,b,c"},
			want: compressOptions{IDs: []string{"a", "b", "c"}},
		},
		{
			name: "ids equals syntax",
			args: []string{"--ids=x,y"},
			want: compressOptions{IDs: []string{"x", "y"}},
		},
		{
			name: "dry-run flag",
			args: []string{"--dry-run"},
			want: compressOptions{DryRun: true},
		},
		{
			name: "json flag",
			args: []string{"--json"},
			want: compressOptions{JSONOutput: true},
		},
		{
			name: "text flag",
			args: []string{"--ids", "id1", "--text", `{"id1":"summary"}`},
			want: compressOptions{IDs: []string{"id1"}, TextJSON: `{"id1":"summary"}`},
		},
		{
			name: "text equals syntax",
			args: []string{"--ids", "id1", "--text={}"},
			want: compressOptions{IDs: []string{"id1"}, TextJSON: "{}"},
		},
		{
			name: "text-file flag",
			args: []string{"--ids", "id1", "--text-file", "/tmp/file.json"},
			want: compressOptions{IDs: []string{"id1"}, TextFile: "/tmp/file.json"},
		},
		{
			name: "text-file equals syntax",
			args: []string{"--ids", "id1", "--text-file=/tmp/f.json"},
			want: compressOptions{IDs: []string{"id1"}, TextFile: "/tmp/f.json"},
		},
		{
			name: "deduplicates ids",
			args: []string{"--ids", "a,b,a,c,b"},
			want: compressOptions{IDs: []string{"a", "b", "c"}},
		},
		{
			name: "strips empty ids",
			args: []string{"--ids", ",a,,b,"},
			want: compressOptions{IDs: []string{"a", "b"}},
		},
		{
			name: "combined flags",
			args: []string{"--ids", "id1,id2", "--dry-run", "--json"},
			want: compressOptions{IDs: []string{"id1", "id2"}, DryRun: true, JSONOutput: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset port for each test
			overridePort = 0
			opts, err := parseCompressArgs(tt.args)
			if err != nil {
				t.Fatalf("parseCompressArgs error: %v", err)
			}
			if len(opts.IDs) != len(tt.want.IDs) {
				t.Errorf("IDs: got %v, want %v", opts.IDs, tt.want.IDs)
			} else {
				for i := range opts.IDs {
					if opts.IDs[i] != tt.want.IDs[i] {
						t.Errorf("IDs[%d]: got %q, want %q", i, opts.IDs[i], tt.want.IDs[i])
					}
				}
			}
			if opts.DryRun != tt.want.DryRun {
				t.Errorf("DryRun: got %v, want %v", opts.DryRun, tt.want.DryRun)
			}
			if opts.JSONOutput != tt.want.JSONOutput {
				t.Errorf("JSONOutput: got %v, want %v", opts.JSONOutput, tt.want.JSONOutput)
			}
			if opts.TextJSON != tt.want.TextJSON {
				t.Errorf("TextJSON: got %q, want %q", opts.TextJSON, tt.want.TextJSON)
			}
			if opts.TextFile != tt.want.TextFile {
				t.Errorf("TextFile: got %q, want %q", opts.TextFile, tt.want.TextFile)
			}
		})
	}
}

func TestParseCompressArgs_Errors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"ids missing value", []string{"--ids"}, "--ids requires a value"},
		{"text missing value", []string{"--text"}, "--text requires a value"},
		{"text-file missing value", []string{"--text-file"}, "--text-file requires a path"},
		{"port missing value", []string{"--port"}, "--port requires a value"},
		{"port invalid value", []string{"--port", "abc"}, "invalid --port value"},
		{"unknown flag", []string{"--bogus"}, "unknown flag"},
		{"unknown argument", []string{"abc"}, "unknown argument"},
		{"text and text-file", []string{"--ids", "x", "--text", "{}", "--text-file", "f"}, "only one of"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overridePort = 0
			_, err := parseCompressArgs(tt.args)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestParseCompressArgs_Port(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantPort int
	}{
		{"--port space", []string{"--port", "9999"}, 9999},
		{"--port= syntax", []string{"--port=8888"}, 8888},
		{"positional port", []string{"7777"}, 7777},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overridePort = 0
			_, err := parseCompressArgs(tt.args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if overridePort != tt.wantPort {
				t.Errorf("port: got %d, want %d", overridePort, tt.wantPort)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseIDList
// ---------------------------------------------------------------------------

func TestParseIDList(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"a,b,c", []string{"a", "b", "c"}},
		{"  a , b ", []string{"a", "b"}},
		{",,,", nil},
		{"single", []string{"single"}},
		{"", nil},
	}

	for _, tt := range tests {
		got := parseIDList(tt.input)
		if len(got) == 0 && len(tt.want) == 0 {
			continue
		}
		if len(got) != len(tt.want) {
			t.Errorf("parseIDList(%q): got %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("parseIDList(%q)[%d]: got %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// isCompressible
// ---------------------------------------------------------------------------

func TestIsCompressible(t *testing.T) {
	tests := []struct {
		name string
		item compressInspectItem
		want bool
	}{
		{"normal item", compressInspectItem{TokenCount: 1000}, true},
		{"zero tokens", compressInspectItem{TokenCount: 0}, false},
		{"error item", compressInspectItem{TokenCount: 500, IsError: true}, false},
		{"image item", compressInspectItem{TokenCount: 500, HasImages: true}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isCompressible(tt.item)
			if got != tt.want {
				t.Errorf("isCompressible: got %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// estimateCompressedTokens
// ---------------------------------------------------------------------------

func TestEstimateCompressedTokens(t *testing.T) {
	tests := []struct {
		name     string
		item     compressInspectItem
		wantMin  int
		wantMax  int
	}{
		{
			name:    "bash tool",
			item:    compressInspectItem{ToolName: "Bash", TokenCount: 1000},
			wantMin: 150,
			wantMax: 200,
		},
		{
			name:    "agent tool",
			item:    compressInspectItem{ToolName: "Agent", TokenCount: 1000},
			wantMin: 300,
			wantMax: 400,
		},
		{
			name:    "read tool",
			item:    compressInspectItem{ToolName: "Read", TokenCount: 1000},
			wantMin: 250,
			wantMax: 300,
		},
		{
			name:    "unknown tool default",
			item:    compressInspectItem{ToolName: "Unknown", TokenCount: 1000},
			wantMin: 280,
			wantMax: 320,
		},
		{
			name:    "minimum floor 1",
			item:    compressInspectItem{ToolName: "Bash", TokenCount: 1},
			wantMin: 1,
			wantMax: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimateCompressedTokens(tt.item)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("estimateCompressedTokens: got %d, want [%d, %d]", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// isAgentTool
// ---------------------------------------------------------------------------

func TestIsAgentTool(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"Agent", true},
		{"agent", true},
		{"AGENT", true},
		{"Task", true},
		{"task", true},
		{"Bash", false},
		{"Read", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAgentTool(tt.name)
			if got != tt.want {
				t.Errorf("isAgentTool(%q): got %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// suggestIDs
// ---------------------------------------------------------------------------

func TestSuggestIDs(t *testing.T) {
	rows := []compressListRow{
		{ID: "a", TokenCount: 3000},
		{ID: "b", TokenCount: 2000},
		{ID: "c", TokenCount: 1000},
		{ID: "d", TokenCount: 500},
	}

	tests := []struct {
		n    int
		want []string
	}{
		{3, []string{"a", "b", "c"}},
		{1, []string{"a"}},
		{0, nil},
		{10, []string{"a", "b", "c", "d"}},
	}

	for _, tt := range tests {
		got := suggestIDs(rows, tt.n)
		if len(got) != len(tt.want) {
			t.Errorf("suggestIDs(n=%d): got %v, want %v", tt.n, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("suggestIDs(n=%d)[%d]: got %q, want %q", tt.n, i, got[i], tt.want[i])
			}
		}
	}
}

func TestSuggestIDs_EmptyRows(t *testing.T) {
	got := suggestIDs(nil, 3)
	if got != nil {
		t.Errorf("suggestIDs(nil): got %v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// chunkIDs
// ---------------------------------------------------------------------------

func TestChunkIDs(t *testing.T) {
	tests := []struct {
		ids  []string
		size int
		want int // number of chunks
	}{
		{[]string{"a", "b", "c", "d", "e"}, 2, 3},
		{[]string{"a", "b", "c"}, 3, 1},
		{[]string{"a", "b", "c"}, 1, 3},
		{[]string{}, 5, 0},
		{[]string{"a"}, 100, 1},
	}

	for _, tt := range tests {
		got := chunkIDs(tt.ids, tt.size)
		if len(got) != tt.want {
			t.Errorf("chunkIDs(%v, %d): got %d chunks, want %d", tt.ids, tt.size, len(got), tt.want)
		}
		// Verify all IDs are present
		var flat []string
		for _, chunk := range got {
			flat = append(flat, chunk...)
		}
		if len(flat) != len(tt.ids) {
			t.Errorf("chunkIDs: flattened %d items, want %d", len(flat), len(tt.ids))
		}
	}
}

// ---------------------------------------------------------------------------
// shortID
// ---------------------------------------------------------------------------

func TestShortID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"toolu_01234567890ABCDEF", "toolu_012345.."}, // 12 runes + ".."
		{"short", "short.."},
		{"", ".."},
		{"exactly12ch", "exactly12ch.."},
	}

	for _, tt := range tests {
		got := shortID(tt.input)
		if got != tt.want {
			t.Errorf("shortID(%q): got %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// previewOneLine
// ---------------------------------------------------------------------------

func TestPreviewOneLine(t *testing.T) {
	tests := []struct {
		input    string
		maxRunes int
		want     string
	}{
		{"hello\nworld", 50, "hello world"},
		{"line1\tline2", 50, "line1 line2"},
		{"  extra   spaces  ", 50, "extra spaces"},
		{"long string here", 5, "long "},
		{"", 10, ""},
	}

	for _, tt := range tests {
		got := previewOneLine(tt.input, tt.maxRunes)
		if got != tt.want {
			t.Errorf("previewOneLine(%q, %d): got %q, want %q", tt.input, tt.maxRunes, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// formatTokenCount
// ---------------------------------------------------------------------------

func TestFormatTokenCount(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0"},
		{500, "500"},
		{1500, "1.5k"},
		{1000, "1.0k"},
		{1_500_000, "1.5M"},
		{-10, "0"},
	}

	for _, tt := range tests {
		got := formatTokenCount(tt.input)
		if got != tt.want {
			t.Errorf("formatTokenCount(%d): got %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// formatDuration
// ---------------------------------------------------------------------------

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		input time.Duration
		want  string
	}{
		{2*time.Hour + 30*time.Minute, "2h30m"},
		{45 * time.Minute, "0h45m"},
		{0, "0h00m"},
		{-5 * time.Minute, "0h00m"},
		{25 * time.Hour, "25h00m"},
	}

	for _, tt := range tests {
		got := formatDuration(tt.input)
		if got != tt.want {
			t.Errorf("formatDuration(%v): got %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// anyInt64, anyFloat, anyBool, anyString
// ---------------------------------------------------------------------------

func TestAnyInt64(t *testing.T) {
	tests := []struct {
		input any
		want  int64
	}{
		{float64(42), 42},
		{int(100), 100},
		{int64(200), 200},
		{"300", 300},
		{nil, 0},
		{true, 0},
	}

	for _, tt := range tests {
		got := anyInt64(tt.input)
		if got != tt.want {
			t.Errorf("anyInt64(%v): got %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestAnyFloat(t *testing.T) {
	tests := []struct {
		input any
		want  float64
	}{
		{float64(3.14), 3.14},
		{int(42), 42.0},
		{int64(100), 100.0},
		{"1.5", 1.5},
		{nil, 0.0},
	}

	for _, tt := range tests {
		got := anyFloat(tt.input)
		if got != tt.want {
			t.Errorf("anyFloat(%v): got %f, want %f", tt.input, got, tt.want)
		}
	}
}

func TestAnyBool(t *testing.T) {
	tests := []struct {
		input any
		want  bool
	}{
		{true, true},
		{false, false},
		{"true", true},
		{"TRUE", true},
		{"false", false},
		{nil, false},
		{42, false},
	}

	for _, tt := range tests {
		got := anyBool(tt.input)
		if got != tt.want {
			t.Errorf("anyBool(%v): got %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestAnyString(t *testing.T) {
	tests := []struct {
		input any
		want  string
	}{
		{"hello", "hello"},
		{42, ""},
		{nil, ""},
	}

	for _, tt := range tests {
		got := anyString(tt.input)
		if got != tt.want {
			t.Errorf("anyString(%v): got %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// extractPortFromFilename
// ---------------------------------------------------------------------------

func TestExtractPortFromFilename(t *testing.T) {
	tests := []struct {
		name    string
		want    int
		wantErr bool
	}{
		{"stats-1234.json", 1234, false},
		{"stats-80.json", 80, false},
		{"stats-.json", 0, true},
		{"bad.json", 0, true},
		{"stats-abc.json", 0, true},
		{"stats-0.json", 0, true},
	}

	for _, tt := range tests {
		got, err := extractPortFromFilename(tt.name)
		if tt.wantErr && err == nil {
			t.Errorf("extractPortFromFilename(%q): expected error", tt.name)
		}
		if !tt.wantErr && err != nil {
			t.Errorf("extractPortFromFilename(%q): unexpected error: %v", tt.name, err)
		}
		if got != tt.want {
			t.Errorf("extractPortFromFilename(%q): got %d, want %d", tt.name, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// parseReplacementText + decodeReplacementMap
// ---------------------------------------------------------------------------

func TestParseReplacementText_NoInput(t *testing.T) {
	m, err := parseReplacementText("", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m != nil {
		t.Fatalf("expected nil map, got %v", m)
	}
}

func TestParseReplacementText_JSONString(t *testing.T) {
	m, err := parseReplacementText(`{"id1":"summary one","id2":"summary two"}`, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["id1"] != "summary one" || m["id2"] != "summary two" {
		t.Fatalf("unexpected map: %v", m)
	}
}

func TestParseReplacementText_InvalidJSON(t *testing.T) {
	_, err := parseReplacementText("not json", "")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseReplacementText_File(t *testing.T) {
	f, err := os.CreateTemp("", "wet-test-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())

	_, _ = f.WriteString(`{"id1":"from file"}`)
	f.Close()

	m, err := parseReplacementText("", f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["id1"] != "from file" {
		t.Fatalf("unexpected map: %v", m)
	}
}

func TestParseReplacementText_FileNotFound(t *testing.T) {
	_, err := parseReplacementText("", "/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// ---------------------------------------------------------------------------
// Help text presence
// ---------------------------------------------------------------------------

func TestCompressUsagePresent(t *testing.T) {
	if compressUsage == "" {
		t.Fatal("compressUsage is empty")
	}
	if !strings.Contains(compressUsage, "--ids") {
		t.Error("compressUsage missing --ids")
	}
	if !strings.Contains(compressUsage, "--port") {
		t.Error("compressUsage missing --port")
	}
	if !strings.Contains(compressUsage, "--dry-run") {
		t.Error("compressUsage missing --dry-run")
	}
	if !strings.Contains(compressUsage, "--json") {
		t.Error("compressUsage missing --json")
	}
	if !strings.Contains(compressUsage, "--text") {
		t.Error("compressUsage missing --text")
	}
}

// ---------------------------------------------------------------------------
// HTTP-based commands with mock server
// ---------------------------------------------------------------------------

// mockWetServer creates a test server that mimics /_wet/* endpoints.
func mockWetServer(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/_wet/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"uptime_seconds":            120.5,
			"request_count":             42,
			"tokens_saved":              15000,
			"compression_ratio":         0.35,
			"items_compressed":          10,
			"items_total":               50,
			"context_window":            200000,
			"latest_input_tokens":       5000,
			"latest_total_input_tokens": 80000,
			"paused":                    false,
			"mode":                      "auto",
		})
	})

	mux.HandleFunc("/_wet/inspect", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		items := []map[string]any{
			{
				"tool_use_id":      "toolu_01ABC",
				"tool_name":        "Bash",
				"command":          "git status",
				"turn":             1,
				"current_turn":     5,
				"stale":            true,
				"is_error":         false,
				"has_images":       false,
				"token_count":      2000,
				"content_preview":  "On branch main...",
				"msg_idx":          0,
				"block_idx":        0,
			},
			{
				"tool_use_id":      "toolu_02DEF",
				"tool_name":        "Read",
				"command":          "",
				"turn":             4,
				"current_turn":     5,
				"stale":            false,
				"is_error":         false,
				"has_images":       false,
				"token_count":      500,
				"content_preview":  "package main...",
				"msg_idx":          1,
				"block_idx":        0,
			},
		}
		_ = json.NewEncoder(w).Encode(items)
	})

	mux.HandleFunc("/_wet/compress", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			IDs             []string          `json:"ids"`
			ReplacementText map[string]string `json:"replacement_text,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "queued",
			"count":  len(body.IDs),
			"ids":    body.IDs,
		})
	})

	mux.HandleFunc("/_wet/pause", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "paused"})
	})

	mux.HandleFunc("/_wet/resume", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "resumed"})
	})

	mux.HandleFunc("/_wet/rules", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"bash_generic": map[string]any{"stale_after": 3}})
	})

	return httptest.NewServer(mux)
}

// setMockPort configures the CLI to talk to the mock server.
func setMockPort(t *testing.T, serverURL string) {
	t.Helper()
	// Parse port from URL like "http://127.0.0.1:PORT"
	parts := strings.Split(serverURL, ":")
	if len(parts) < 3 {
		t.Fatalf("unexpected server URL: %s", serverURL)
	}
	port := 0
	fmt.Sscanf(parts[len(parts)-1], "%d", &port)
	SetPort(port)
}

func TestRunStatus_MockServer(t *testing.T) {
	srv := mockWetServer(t)
	defer srv.Close()
	setMockPort(t, srv.URL)

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := RunStatusEnhanced(false)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("RunStatusEnhanced error: %v", err)
	}

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if !strings.Contains(output, "auto") {
		t.Errorf("status output missing mode, got: %s", output)
	}
	if !strings.Contains(output, "42") {
		t.Errorf("status output missing request count, got: %s", output)
	}
}

func TestRunStatus_JSONOutput(t *testing.T) {
	srv := mockWetServer(t)
	defer srv.Close()
	setMockPort(t, srv.URL)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := RunStatusEnhanced(true)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("RunStatusEnhanced(json) error: %v", err)
	}

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	var parsed map[string]any
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("JSON output not valid: %v\nOutput: %s", err, output)
	}
	if parsed["mode"] != "auto" {
		t.Errorf("JSON mode: got %v, want auto", parsed["mode"])
	}
}

func TestRunInspect_MockServer(t *testing.T) {
	srv := mockWetServer(t)
	defer srv.Close()
	setMockPort(t, srv.URL)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := RunInspectEnhanced(false, false)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("RunInspectEnhanced error: %v", err)
	}

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if !strings.Contains(output, "Bash") {
		t.Errorf("inspect output missing tool name Bash: %s", output)
	}
	if !strings.Contains(output, "2 results") {
		t.Errorf("inspect output missing result count: %s", output)
	}
}

func TestRunInspect_JSONOutput(t *testing.T) {
	srv := mockWetServer(t)
	defer srv.Close()
	setMockPort(t, srv.URL)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := RunInspectEnhanced(true, false)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("RunInspectEnhanced(json) error: %v", err)
	}

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	var parsed []map[string]any
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("JSON output not valid: %v\nOutput: %s", err, output)
	}
	if len(parsed) != 2 {
		t.Errorf("expected 2 items, got %d", len(parsed))
	}
}

func TestRunCompress_ListMode_JSON(t *testing.T) {
	srv := mockWetServer(t)
	defer srv.Close()
	setMockPort(t, srv.URL)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := RunCompress([]string{"--json"})

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("RunCompress(list, json) error: %v", err)
	}

	buf := make([]byte, 8192)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	var parsed map[string]any
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("JSON output not valid: %v\nOutput: %s", err, output)
	}
	if parsed["mode"] != "auto" {
		t.Errorf("mode: got %v, want auto", parsed["mode"])
	}
}

func TestRunCompress_DirectMode(t *testing.T) {
	srv := mockWetServer(t)
	defer srv.Close()
	setMockPort(t, srv.URL)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := RunCompress([]string{"--ids", "toolu_01ABC"})

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("RunCompress(direct) error: %v", err)
	}

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if !strings.Contains(output, "Queued 1 item") {
		t.Errorf("compress output: %s", output)
	}
}

func TestRunCompress_DryRun(t *testing.T) {
	srv := mockWetServer(t)
	defer srv.Close()
	setMockPort(t, srv.URL)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := RunCompress([]string{"--ids", "toolu_01ABC", "--dry-run"})

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("RunCompress(dry-run) error: %v", err)
	}

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if !strings.Contains(output, "dry-run") || !strings.Contains(output, "Would queue") {
		t.Errorf("dry-run output missing expected text: %s", output)
	}
}

func TestRunCompress_UnknownID(t *testing.T) {
	srv := mockWetServer(t)
	defer srv.Close()
	setMockPort(t, srv.URL)

	err := RunCompress([]string{"--ids", "nonexistent_id"})
	if err == nil {
		t.Fatal("expected error for unknown ID")
	}
	if !strings.Contains(err.Error(), "unknown tool_use IDs") {
		t.Errorf("error: %v", err)
	}
}

func TestRunCompress_Help(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := RunCompress([]string{"--help"})

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("RunCompress(help) error: %v", err)
	}

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if !strings.Contains(output, "Usage:") {
		t.Errorf("help output missing Usage: %s", output)
	}
}

// ---------------------------------------------------------------------------
// resolvePort
// ---------------------------------------------------------------------------

func TestResolvePort_Override(t *testing.T) {
	overridePort = 5555
	defer func() { overridePort = 0 }()

	port, err := resolvePort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != 5555 {
		t.Errorf("port: got %d, want 5555", port)
	}
}

func TestResolvePort_EnvVar(t *testing.T) {
	overridePort = 0
	old := os.Getenv("WET_PORT")
	os.Setenv("WET_PORT", "6666")
	defer os.Setenv("WET_PORT", old)

	port, err := resolvePort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != 6666 {
		t.Errorf("port: got %d, want 6666", port)
	}
}

func TestResolvePort_NeitherSet(t *testing.T) {
	overridePort = 0
	old := os.Getenv("WET_PORT")
	os.Unsetenv("WET_PORT")
	defer os.Setenv("WET_PORT", old)

	_, err := resolvePort()
	if err == nil {
		t.Fatal("expected error when no port set")
	}
	if !strings.Contains(err.Error(), "no proxy port specified") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolvePort_InvalidEnvVar(t *testing.T) {
	overridePort = 0
	old := os.Getenv("WET_PORT")
	os.Setenv("WET_PORT", "notanumber")
	defer os.Setenv("WET_PORT", old)

	_, err := resolvePort()
	if err == nil {
		t.Fatal("expected error for invalid WET_PORT")
	}
	if !strings.Contains(err.Error(), "invalid WET_PORT") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// formatCompressHTTPError
// ---------------------------------------------------------------------------

func TestFormatCompressHTTPError(t *testing.T) {
	tests := []struct {
		name    string
		body    []byte
		origErr error
		wantStr string
	}{
		{
			name:    "structured error with code",
			body:    []byte(`{"code":"AGENT_REQUIRES_REPLACEMENT","error":"needs replacement text"}`),
			origErr: fmt.Errorf("HTTP 400"),
			wantStr: "AGENT_REQUIRES_REPLACEMENT: needs replacement text",
		},
		{
			name:    "error without code",
			body:    []byte(`{"error":"something went wrong"}`),
			origErr: fmt.Errorf("HTTP 400"),
			wantStr: "something went wrong",
		},
		{
			name:    "empty body falls back to orig error",
			body:    nil,
			origErr: fmt.Errorf("connection refused"),
			wantStr: "connection refused",
		},
		{
			name:    "invalid json falls back to orig error",
			body:    []byte("not json"),
			origErr: fmt.Errorf("HTTP 500"),
			wantStr: "HTTP 500",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatCompressHTTPError(tt.body, tt.origErr)
			if got.Error() != tt.wantStr {
				t.Errorf("got %q, want %q", got.Error(), tt.wantStr)
			}
		})
	}
}
