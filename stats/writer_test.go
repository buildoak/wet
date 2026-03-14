package stats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/buildoak/wet/pipeline"
)

func TestRecordRequest(t *testing.T) {
	s := NewSessionStats()

	s.RecordRequest(pipeline.CompressResult{
		TotalToolResults: 10,
		Compressed:       5,
		SkippedFresh:     3,
		SkippedBypass:    2,
		TokensBefore:     10000,
		TokensAfter:      2000,
		OverheadMs:       5.0,
	})
	s.RecordRequest(pipeline.CompressResult{
		TotalToolResults: 8,
		Compressed:       4,
		SkippedFresh:     2,
		SkippedBypass:    2,
		TokensBefore:     8000,
		TokensAfter:      1500,
		OverheadMs:       3.0,
	})
	s.RecordRequest(pipeline.CompressResult{
		TotalToolResults: 6,
		Compressed:       3,
		SkippedFresh:     2,
		SkippedBypass:    1,
		TokensBefore:     6000,
		TokensAfter:      1000,
		OverheadMs:       2.5,
	})

	if s.RequestCount != 3 {
		t.Fatalf("expected 3 requests, got %d", s.RequestCount)
	}
	if s.TotalCompressed != 12 {
		t.Fatalf("expected 12 total compressed, got %d", s.TotalCompressed)
	}
	if s.TokensBefore != 24000 {
		t.Fatalf("expected 24000 tokens before, got %d", s.TokensBefore)
	}
	if s.TokensAfter != 4500 {
		t.Fatalf("expected 4500 tokens after, got %d", s.TokensAfter)
	}
}

func TestWriteStatsFile(t *testing.T) {
	// Use temp dir
	tmpDir := t.TempDir()
	origWetDir := wetDir
	// Override wetDir for test
	oldFn := wetDirFn
	wetDirFn = func() string { return tmpDir }
	defer func() { wetDirFn = oldFn }()
	_ = origWetDir

	s := NewSessionStats()
	s.RecordRequest(pipeline.CompressResult{
		TotalToolResults: 5,
		Compressed:       3,
		TokensBefore:     5000,
		TokensAfter:      1000,
	})

	if err := s.WriteStatsFile(); err != nil {
		t.Fatalf("WriteStatsFile failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "stats.json"))
	if err != nil {
		t.Fatalf("read stats.json: %v", err)
	}

	var rs RequestStats
	if err := json.Unmarshal(data, &rs); err != nil {
		t.Fatalf("unmarshal stats: %v", err)
	}

	if rs.Compressed != 3 {
		t.Fatalf("expected 3 compressed, got %d", rs.Compressed)
	}
	if rs.TokensBefore != 5000 {
		t.Fatalf("expected 5000 tokens before, got %d", rs.TokensBefore)
	}
}

func TestRenderStatusline(t *testing.T) {
	tmpDir := t.TempDir()
	oldFn := wetDirFn
	wetDirFn = func() string { return tmpDir }
	defer func() { wetDirFn = oldFn }()

	// Write a stats file with full data: context window + compression.
	// LatestTotalInputTokens = uncached(1) + cache_create(416) + cache_read(91583) = 92000
	rs := RequestStats{
		ToolResults:              10,
		Compressed:               7,
		TokensBefore:             42000,
		TokensAfter:              8900,
		CompressionRatio:         0.79,
		SessionTokensSaved:       142000,
		SessionRequests:          15,
		SessionItemsTotal:        24,
		SessionItemsComp:         18,
		SessionCompRatio:         0.79,
		SessionMode:              "passthrough",
		ContextWindow:            200000,
		LatestInputTokens:        1,
		LatestTotalInputTokens:   92000,
		SessionTokensBefore:      180000,
		SessionTokensAfter:       38000,
	}
	data, _ := json.Marshal(rs)
	if err := os.WriteFile(filepath.Join(tmpDir, "stats.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	line, err := RenderStatusline()
	if err != nil {
		t.Fatal(err)
	}
	if line == "" {
		t.Fatal("expected non-empty statusline")
	}

	// Expected: "wet: 46% (92.0k/200.0k) | 18/24 results compressed (180.0k->38.0k)"
	if !contains(line, "46%") {
		t.Fatalf("expected '46%%' in statusline, got: %s", line)
	}
	if !contains(line, "92.0k/200.0k") {
		t.Fatalf("expected '92.0k/200.0k' in statusline, got: %s", line)
	}
	if !contains(line, "18/24") {
		t.Fatalf("expected '18/24' in statusline, got: %s", line)
	}
	if !contains(line, "180.0k->38.0k") {
		t.Fatalf("expected '180.0k->38.0k' in statusline, got: %s", line)
	}
	if !contains(line, "results compressed") {
		t.Fatalf("expected 'results compressed' in statusline, got: %s", line)
	}
}

func TestRenderStatuslineNoContextWindow(t *testing.T) {
	tmpDir := t.TempDir()
	oldFn := wetDirFn
	wetDirFn = func() string { return tmpDir }
	defer func() { wetDirFn = oldFn }()

	// Stats without context window info (legacy/passthrough-only)
	rs := RequestStats{
		SessionTokensSaved:  50000,
		SessionRequests:     5,
		SessionItemsTotal:   15,
		SessionItemsComp:    10,
		SessionCompRatio:    0.65,
		SessionMode:         "auto",
		SessionTokensBefore: 70000,
		SessionTokensAfter:  20000,
	}
	data, _ := json.Marshal(rs)
	if err := os.WriteFile(filepath.Join(tmpDir, "stats.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	line, err := RenderStatusline()
	if err != nil {
		t.Fatal(err)
	}

	// Without context window, should show compression only
	if !contains(line, "10/15") {
		t.Fatalf("expected '10/15' in statusline, got: %s", line)
	}
	if !contains(line, "70.0k->20.0k") {
		t.Fatalf("expected '70.0k->20.0k' in statusline, got: %s", line)
	}
	// Should NOT contain a percentage fill
	if contains(line, "%(") {
		t.Fatalf("unexpected context fill percentage in statusline, got: %s", line)
	}
}

func TestRenderStatuslineInactive(t *testing.T) {
	tmpDir := t.TempDir()
	oldFn := wetDirFn
	wetDirFn = func() string { return tmpDir }
	defer func() { wetDirFn = oldFn }()

	// No stats file at all
	line, err := RenderStatusline()
	if err != nil {
		t.Fatal(err)
	}
	if line != "wet: sleeping" {
		t.Fatalf("expected 'wet: sleeping', got: %s", line)
	}
}

func TestRenderStatuslineStale(t *testing.T) {
	tmpDir := t.TempDir()
	oldFn := wetDirFn
	wetDirFn = func() string { return tmpDir }
	defer func() { wetDirFn = oldFn }()

	// Write stats file with data but make it old -- should still show data, not idle
	rs := RequestStats{
		SessionTokensSaved:  1000,
		SessionRequests:     1,
		SessionCompRatio:    0.5,
		SessionItemsComp:    1,
		SessionItemsTotal:   2,
		SessionMode:         "auto",
		SessionTokensBefore: 2000,
		SessionTokensAfter:  1000,
	}
	data, _ := json.Marshal(rs)
	path := filepath.Join(tmpDir, "stats.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	line, err := RenderStatusline()
	if err != nil {
		t.Fatal(err)
	}
	// Stale file with data should still show data (no more idle state)
	if !contains(line, "1/2 results compressed") {
		t.Fatalf("expected data in stale statusline, got: %s", line)
	}
}

func TestRenderStatuslineReady(t *testing.T) {
	tmpDir := t.TempDir()
	oldFn := wetDirFn
	wetDirFn = func() string { return tmpDir }
	defer func() { wetDirFn = oldFn }()

	// Write an empty stats file (proxy running, no requests yet)
	rs := RequestStats{
		SessionMode: "passthrough",
	}
	data, _ := json.Marshal(rs)
	if err := os.WriteFile(filepath.Join(tmpDir, "stats.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	line, err := RenderStatusline()
	if err != nil {
		t.Fatal(err)
	}
	if line != "wet: ready" {
		t.Fatalf("expected 'wet: ready', got: %s", line)
	}
}

func TestRenderStatuslinePerPort(t *testing.T) {
	tmpDir := t.TempDir()
	oldFn := wetDirFn
	wetDirFn = func() string { return tmpDir }
	defer func() { wetDirFn = oldFn }()

	// Write per-port stats file.
	// LatestTotalInputTokens = 120000 (sum of all input + cache fields)
	rs := RequestStats{
		SessionTokensSaved:     50000,
		SessionRequests:        5,
		SessionCompRatio:       0.65,
		SessionItemsComp:       10,
		SessionItemsTotal:      15,
		SessionMode:            "auto",
		ContextWindow:          200000,
		LatestInputTokens:      1,
		LatestTotalInputTokens: 120000,
		SessionTokensBefore:    80000,
		SessionTokensAfter:     30000,
	}
	data, _ := json.Marshal(rs)
	if err := os.WriteFile(filepath.Join(tmpDir, "stats-9876.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Set WET_PORT
	t.Setenv("WET_PORT", "9876")

	line, err := RenderStatusline()
	if err != nil {
		t.Fatal(err)
	}
	if !contains(line, "60%") {
		t.Fatalf("expected '60%%' in statusline, got: %s", line)
	}
	if !contains(line, "10/15") {
		t.Fatalf("expected '10/15' in statusline, got: %s", line)
	}
}

func TestFormatTokenCount(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0"},
		{500, "500"},
		{1000, "1.0k"},
		{42000, "42.0k"},
		{142000, "142.0k"},
		{1500000, "1.5M"},
	}
	for _, tc := range tests {
		got := formatTokenCount(tc.input)
		if got != tc.expected {
			t.Errorf("formatTokenCount(%d) = %s, want %s", tc.input, got, tc.expected)
		}
	}
}

func TestWriteStatsFileSessionFields(t *testing.T) {
	tmpDir := t.TempDir()
	oldFn := wetDirFn
	wetDirFn = func() string { return tmpDir }
	defer func() { wetDirFn = oldFn }()

	s := NewSessionStats()
	s.Mode = "passthrough"
	s.RecordRequest(pipeline.CompressResult{
		TotalToolResults: 10,
		Compressed:       7,
		TokensBefore:     10000,
		TokensAfter:      2100,
	})

	if err := s.WriteStatsFile(); err != nil {
		t.Fatalf("WriteStatsFile failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "stats.json"))
	if err != nil {
		t.Fatalf("read stats.json: %v", err)
	}

	var rs RequestStats
	if err := json.Unmarshal(data, &rs); err != nil {
		t.Fatalf("unmarshal stats: %v", err)
	}

	if rs.SessionItemsTotal != 10 {
		t.Fatalf("expected session_items_total=10, got %d", rs.SessionItemsTotal)
	}
	if rs.SessionItemsComp != 7 {
		t.Fatalf("expected session_items_compressed=7, got %d", rs.SessionItemsComp)
	}
	if rs.SessionMode != "passthrough" {
		t.Fatalf("expected session_mode=passthrough, got %s", rs.SessionMode)
	}
	if rs.SessionTokensSaved != 7900 {
		t.Fatalf("expected session_tokens_saved=7900, got %d", rs.SessionTokensSaved)
	}
	if rs.SessionTokensBefore != 10000 {
		t.Fatalf("expected session_tokens_before=10000, got %d", rs.SessionTokensBefore)
	}
	if rs.SessionTokensAfter != 2100 {
		t.Fatalf("expected session_tokens_after=2100, got %d", rs.SessionTokensAfter)
	}
}

func TestRecordModel(t *testing.T) {
	s := NewSessionStats()
	s.RecordModel("claude-opus-4-6-20250310", nil)
	if s.Model != "claude-opus-4-6-20250310" {
		t.Fatalf("expected model 'claude-opus-4-6-20250310', got %s", s.Model)
	}
	if s.ContextWindow != 1000000 {
		t.Fatalf("expected context window 1000000, got %d", s.ContextWindow)
	}
}

func TestModelContextWindow(t *testing.T) {
	tests := []struct {
		model    string
		expected int
	}{
		// Default context windows (nil map = built-in defaults)
		{"claude-opus-4-6-20250310", 1000000},
		{"claude-sonnet-4-6-20250514", 1000000},
		{"claude-sonnet-4-5-20250301", 1000000},
		{"claude-haiku-4-5-20250301", 200000},
		{"claude-3-5-sonnet-20241022", 200000},
		{"claude-3.5-haiku-20241022", 200000},
		{"unknown-model", 200000},
	}
	for _, tc := range tests {
		got := ModelContextWindow(tc.model, nil)
		if got != tc.expected {
			t.Errorf("ModelContextWindow(%q) = %d, want %d", tc.model, got, tc.expected)
		}
	}
}

func TestModelContextWindowCustomConfig(t *testing.T) {
	custom := map[string]int{
		"claude-opus-4-6":    500000,
		"claude-custom-99":   2000000,
	}
	tests := []struct {
		model    string
		expected int
	}{
		{"claude-opus-4-6-20250310", 500000},   // contains-match
		{"claude-opus-4-6", 500000},             // exact match
		{"claude-custom-99-20260101", 2000000},  // contains-match custom
		{"unknown-model", 200000},               // fallback
	}
	for _, tc := range tests {
		got := ModelContextWindow(tc.model, custom)
		if got != tc.expected {
			t.Errorf("ModelContextWindow(%q) with custom config = %d, want %d", tc.model, got, tc.expected)
		}
	}
}

func TestRecordAPIUsageUpdatesLatest(t *testing.T) {
	s := NewSessionStats()
	s.RecordModel("claude-opus-4-6", nil)
	s.RecordRequest(pipeline.CompressResult{
		TotalToolResults: 5,
		Compressed:       3,
		TokensBefore:     5000,
		TokensAfter:      1000,
	})

	s.RecordAPIUsage(92000, 1500, 0, 0)

	if s.LatestAPIInputTokens != 92000 {
		t.Fatalf("expected latest input tokens 92000, got %d", s.LatestAPIInputTokens)
	}
	if s.LastRequest == nil {
		t.Fatal("expected LastRequest to be set")
	}
	if s.LastRequest.LatestInputTokens != 92000 {
		t.Fatalf("expected LastRequest.LatestInputTokens 92000, got %d", s.LastRequest.LatestInputTokens)
	}
	if s.LastRequest.ContextWindow != 1000000 {
		t.Fatalf("expected LastRequest.ContextWindow 1000000, got %d", s.LastRequest.ContextWindow)
	}
}

func TestWriteInitialStatsFile(t *testing.T) {
	tmpDir := t.TempDir()
	oldFn := wetDirFn
	wetDirFn = func() string { return tmpDir }
	defer func() { wetDirFn = oldFn }()

	s := NewSessionStats()
	s.Mode = "passthrough"
	if err := s.WriteInitialStatsFile(); err != nil {
		t.Fatalf("WriteInitialStatsFile failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "stats.json"))
	if err != nil {
		t.Fatalf("read stats.json: %v", err)
	}

	var rs RequestStats
	if err := json.Unmarshal(data, &rs); err != nil {
		t.Fatalf("unmarshal stats: %v", err)
	}

	if rs.SessionMode != "passthrough" {
		t.Fatalf("expected session_mode=passthrough, got %s", rs.SessionMode)
	}
	if rs.SessionRequests != 0 {
		t.Fatalf("expected session_requests=0, got %d", rs.SessionRequests)
	}
}

func TestSeedPersistedCompressions(t *testing.T) {
	tmpDir := t.TempDir()
	oldFn := wetDirFn
	wetDirFn = func() string { return tmpDir }
	defer func() { wetDirFn = oldFn }()

	s := NewSessionStats()
	s.Mode = "passthrough"

	// Seeding with 23 items should write stats file with session_items_compressed=23
	// and exit the "ready" branch in the statusline.
	if err := s.SeedPersistedCompressions(23); err != nil {
		t.Fatalf("SeedPersistedCompressions failed: %v", err)
	}

	if s.TotalCompressed != 23 {
		t.Fatalf("expected TotalCompressed=23, got %d", s.TotalCompressed)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "stats.json"))
	if err != nil {
		t.Fatalf("read stats.json: %v", err)
	}

	var rs RequestStats
	if err := json.Unmarshal(data, &rs); err != nil {
		t.Fatalf("unmarshal stats: %v", err)
	}

	if rs.SessionItemsComp != 23 {
		t.Fatalf("expected session_items_compressed=23, got %d", rs.SessionItemsComp)
	}

	// Seeding zero should be a no-op — no file change.
	s2 := NewSessionStats()
	if err := s2.SeedPersistedCompressions(0); err != nil {
		t.Fatalf("SeedPersistedCompressions(0) unexpected error: %v", err)
	}
	if s2.TotalCompressed != 0 {
		t.Fatalf("expected TotalCompressed=0 after zero seed, got %d", s2.TotalCompressed)
	}

	// After seeding, a subsequent RecordRequest should accumulate on top.
	if err := s.SeedPersistedCompressions(0); err != nil { // no-op
		t.Fatalf("second seed unexpected error: %v", err)
	}
	s.RecordRequest(pipeline.CompressResult{
		TotalToolResults: 5,
		Compressed:       3,
		TokensBefore:     3000,
		TokensAfter:      600,
	})
	if s.TotalCompressed != 26 {
		t.Fatalf("expected TotalCompressed=26 after seed+RecordRequest, got %d", s.TotalCompressed)
	}
	if s.LastRequest.SessionItemsComp != 26 {
		t.Fatalf("expected LastRequest.SessionItemsComp=26, got %d", s.LastRequest.SessionItemsComp)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
