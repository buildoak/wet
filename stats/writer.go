package stats

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/otonashi/wet/pipeline"
)

// SessionStats tracks cumulative stats for the current proxy session.
type SessionStats struct {
	mu                        sync.Mutex
	Port                      int // proxy port, used for per-instance stats file
	Mode                      string
	StartTime                 time.Time
	RequestCount              int
	TotalToolResults          int
	TotalCompressed           int
	TotalSkippedFresh         int
	TotalSkippedBypass        int
	TokensBefore              int64
	TokensAfter               int64
	Tier1Count                int
	Tier2Count                int
	Tier2Failures             int
	APIInputTokens            int64
	APIOutputTokens           int64
	APICacheCreate            int64
	APICacheRead              int64
	ContextWindow             int    // detected context window size (e.g. 200000)
	LatestAPIInputTokens      int    // most recent single-request uncached input token count
	LatestAPITotalInputTokens int    // most recent single-request total (input + cache_create + cache_read)
	PrevTotalContext          int    // total_context from the previous turn (for delta calculation)
	APITokensSaved            int64  // cumulative API-observed savings (delta when compression fires)
	Model                     string // detected model name from request
	LastRequest               *RequestStats
}

// RequestStats is written to ~/.wet/stats-{port}.json after each request.
// It includes both per-request and cumulative session-level data so that
// the statusline command can render without needing the HTTP endpoint.
type RequestStats struct {
	Timestamp                   string  `json:"timestamp"`
	ToolResults                 int     `json:"tool_results"`
	Compressed                  int     `json:"compressed"`
	SkippedFresh                int     `json:"skipped_fresh"`
	SkippedBypass               int     `json:"skipped_bypass"`
	TokensBefore                int     `json:"tokens_before"`
	TokensAfter                 int     `json:"tokens_after"`
	CompressionRatio            float64 `json:"compression_ratio"`
	OverheadMs                  float64 `json:"overhead_ms"`
	APIInputTokens              int     `json:"api_input_tokens,omitempty"`
	APIOutputTokens             int     `json:"api_output_tokens,omitempty"`
	APICacheCreationInputTokens int     `json:"api_cache_creation_input_tokens,omitempty"`
	APICacheReadInputTokens     int     `json:"api_cache_read_input_tokens,omitempty"`

	// Session-level cumulative fields (for statusline)
	SessionTokensSaved     int64   `json:"session_tokens_saved"`
	SessionRequests        int     `json:"session_requests"`
	SessionItemsTotal      int     `json:"session_items_total"`
	SessionItemsComp       int     `json:"session_items_compressed"`
	SessionCompRatio       float64 `json:"session_compression_ratio"`
	SessionMode            string  `json:"session_mode"`
	ContextWindow          int     `json:"context_window,omitempty"`
	LatestInputTokens      int     `json:"latest_input_tokens,omitempty"`
	LatestTotalInputTokens int     `json:"latest_total_input_tokens,omitempty"`
	SessionTokensBefore    int64   `json:"session_tokens_before,omitempty"`
	SessionTokensAfter     int64   `json:"session_tokens_after,omitempty"`
	SessionAPITokensSaved  int64   `json:"session_api_tokens_saved,omitempty"`
}

// HealthResponse for GET /health.
type HealthResponse struct {
	Status                  string  `json:"status"`
	UptimeSeconds           float64 `json:"uptime_seconds"`
	RequestsProxied         int     `json:"requests_proxied"`
	TotalTokensSaved        int64   `json:"total_tokens_saved"`
	AverageCompressionRatio float64 `json:"average_compression_ratio"`
	Tier1Count              int     `json:"tier1_count"`
	Tier2Count              int     `json:"tier2_count"`
	Tier2Failures           int     `json:"tier2_failures"`
}

func NewSessionStats() *SessionStats {
	return &SessionStats{
		StartTime: time.Now(),
	}
}

func (s *SessionStats) RecordRequest(result pipeline.CompressResult) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.RequestCount++
	s.TotalToolResults += result.TotalToolResults
	s.TotalCompressed += result.Compressed
	s.TotalSkippedFresh += result.SkippedFresh
	s.TotalSkippedBypass += result.SkippedBypass
	s.TokensBefore += int64(result.TokensBefore)
	s.TokensAfter += int64(result.TokensAfter)
	s.Tier1Count += result.Compressed // All are Tier1 for now

	ratio := 0.0
	if result.TokensBefore > 0 {
		ratio = 1.0 - float64(result.TokensAfter)/float64(result.TokensBefore)
	}

	sessionRatio := 0.0
	if s.TokensBefore > 0 {
		sessionRatio = 1.0 - float64(s.TokensAfter)/float64(s.TokensBefore)
	}
	mode := s.Mode
	if mode == "" {
		mode = "auto"
	}

	s.LastRequest = &RequestStats{
		Timestamp:             time.Now().UTC().Format(time.RFC3339),
		ToolResults:           result.TotalToolResults,
		Compressed:            result.Compressed,
		SkippedFresh:          result.SkippedFresh,
		SkippedBypass:         result.SkippedBypass,
		TokensBefore:          result.TokensBefore,
		TokensAfter:           result.TokensAfter,
		CompressionRatio:      ratio,
		OverheadMs:            result.OverheadMs,
		SessionTokensSaved:    s.TokensBefore - s.TokensAfter,
		SessionRequests:       s.RequestCount,
		SessionItemsTotal:     s.TotalToolResults,
		SessionItemsComp:      s.TotalCompressed,
		SessionCompRatio:      sessionRatio,
		SessionMode:           mode,
		ContextWindow:         s.ContextWindow,
		LatestInputTokens:     s.LatestAPIInputTokens,
		SessionTokensBefore:   s.TokensBefore,
		SessionTokensAfter:    s.TokensAfter,
		SessionAPITokensSaved: s.APITokensSaved,
	}
}

func (s *SessionStats) RecordAPIUsage(input, output, cacheCreate, cacheRead int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.APIInputTokens += int64(input)
	s.APIOutputTokens += int64(output)
	s.APICacheCreate += int64(cacheCreate)
	s.APICacheRead += int64(cacheRead)

	total := input + cacheCreate + cacheRead
	if input > 0 || cacheCreate > 0 || cacheRead > 0 {
		// Track previous total for compression delta calculation
		if s.LatestAPITotalInputTokens > 0 {
			s.PrevTotalContext = s.LatestAPITotalInputTokens
		}
		s.LatestAPIInputTokens = input
		s.LatestAPITotalInputTokens = total
	}

	if s.LastRequest != nil {
		s.LastRequest.APIInputTokens = input
		s.LastRequest.APIOutputTokens = output
		s.LastRequest.APICacheCreationInputTokens = cacheCreate
		s.LastRequest.APICacheReadInputTokens = cacheRead
		s.LastRequest.LatestInputTokens = s.LatestAPIInputTokens
		s.LastRequest.LatestTotalInputTokens = s.LatestAPITotalInputTokens
		s.LastRequest.ContextWindow = s.ContextWindow
	}
}

// RecordCompressionDelta records the API-observed token savings when compression
// was applied. Call AFTER RecordAPIUsage on the same turn.
func (s *SessionStats) RecordCompressionDelta(prevTotal, currentTotal int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prevTotal > currentTotal && currentTotal > 0 {
		delta := int64(prevTotal - currentTotal)
		s.APITokensSaved += delta
		if s.LastRequest != nil {
			s.LastRequest.SessionAPITokensSaved = s.APITokensSaved
		}
	}
}

// GetPrevTotalContext returns the total_context from the previous turn.
func (s *SessionStats) GetPrevTotalContext() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.PrevTotalContext
}

// GetAPITokensSaved returns the cumulative API-observed token savings.
func (s *SessionStats) GetAPITokensSaved() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.APITokensSaved
}

func (s *SessionStats) APIUsageTotals() (input, output, cacheCreate, cacheRead int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.APIInputTokens, s.APIOutputTokens, s.APICacheCreate, s.APICacheRead
}

// TotalTokensBefore returns cumulative pre-compression token count.
func (s *SessionStats) TotalTokensBefore() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.TokensBefore
}

// GetContextWindow returns the detected context window size.
func (s *SessionStats) GetContextWindow() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ContextWindow
}

// GetLatestAPIInputTokens returns the most recent single-request input token count.
func (s *SessionStats) GetLatestAPIInputTokens() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LatestAPIInputTokens
}

// TotalItems returns cumulative tool result count across all requests.
func (s *SessionStats) TotalItems() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(s.TotalToolResults)
}

// SeedPersistedCompressions seeds the session stats with a count of compressions
// that were loaded from persistence (prior session). This ensures the statusline
// immediately shows the active-compression state rather than "ready" after a
// session restart where tombstones are restored from disk.
//
// token counts are unknown at load time so they are left at zero; the statusline
// will show the item count correctly and the token savings will accumulate as
// new compressions are added in the current session.
func (s *SessionStats) SeedPersistedCompressions(count int) error {
	if count <= 0 {
		return nil
	}
	s.mu.Lock()
	s.TotalCompressed += count
	mode := s.Mode
	if mode == "" {
		mode = "auto"
	}
	// Create or update LastRequest so the next WriteStatsFile call reflects the seeded count.
	if s.LastRequest == nil {
		s.LastRequest = &RequestStats{
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
			SessionMode: mode,
		}
	}
	s.LastRequest.SessionItemsComp = s.TotalCompressed
	s.mu.Unlock()
	return s.WriteStatsFile()
}

// SeedCumulativeStats seeds the session stats with persisted cumulative totals
// (tokens_before, tokens_after, items_compressed) so the statusline shows real
// lifetime numbers immediately after restart.
func (s *SessionStats) SeedCumulativeStats(tokensBefore, tokensAfter int64, itemsCompressed int) error {
	if tokensBefore == 0 && tokensAfter == 0 && itemsCompressed == 0 {
		return nil
	}
	s.mu.Lock()
	s.TokensBefore += tokensBefore
	s.TokensAfter += tokensAfter
	s.TotalCompressed += itemsCompressed
	mode := s.Mode
	if mode == "" {
		mode = "auto"
	}
	if s.LastRequest == nil {
		s.LastRequest = &RequestStats{
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
			SessionMode: mode,
		}
	}
	s.LastRequest.SessionTokensSaved = s.TokensBefore - s.TokensAfter
	s.LastRequest.SessionTokensBefore = s.TokensBefore
	s.LastRequest.SessionTokensAfter = s.TokensAfter
	s.LastRequest.SessionItemsComp = s.TotalCompressed
	s.mu.Unlock()
	return s.WriteStatsFile()
}

// WriteInitialStatsFile writes a minimal stats file so the statusline can show
// "ready to compress!" before any requests have been processed.
func (s *SessionStats) WriteInitialStatsFile() error {
	s.mu.Lock()
	mode := s.Mode
	if mode == "" {
		mode = "auto"
	}
	s.LastRequest = &RequestStats{
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		SessionMode: mode,
	}
	s.mu.Unlock()
	return s.WriteStatsFile()
}

func (s *SessionStats) WriteStatsFile() error {
	s.mu.Lock()
	last := s.LastRequest
	port := s.Port
	s.mu.Unlock()

	if last == nil {
		return nil
	}

	dir := wetDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(last, "", "  ")
	if err != nil {
		return err
	}

	suffix := "stats.json"
	if port > 0 {
		suffix = fmt.Sprintf("stats-%d.json", port)
	}

	tmpPath := filepath.Join(dir, suffix+".tmp")
	finalPath := filepath.Join(dir, suffix)

	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, finalPath)
}

func (s *SessionStats) HealthResponse() HealthResponse {
	s.mu.Lock()
	defer s.mu.Unlock()

	avgRatio := 0.0
	if s.TokensBefore > 0 {
		avgRatio = 1.0 - float64(s.TokensAfter)/float64(s.TokensBefore)
	}

	return HealthResponse{
		Status:                  "ok",
		UptimeSeconds:           time.Since(s.StartTime).Seconds(),
		RequestsProxied:         s.RequestCount,
		TotalTokensSaved:        s.TokensBefore - s.TokensAfter,
		AverageCompressionRatio: avgRatio,
		Tier1Count:              s.Tier1Count,
		Tier2Count:              s.Tier2Count,
		Tier2Failures:           s.Tier2Failures,
	}
}

// RecordModel stores the detected model name and derives the context window size.
// Called once per request from the proxy when the model field is found.
func (s *SessionStats) RecordModel(model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Model = model
	s.ContextWindow = ModelContextWindow(model)
}

// ModelContextWindow returns the context window size for a given model name.
// Defaults to 200000 for unknown models.
func ModelContextWindow(model string) int {
	m := strings.ToLower(model)

	// Extended thinking models with 1M context
	// (Currently none in GA, but future-proof the mapping)

	// All current Claude 3.5+ and Claude 4+ models: 200k
	switch {
	case strings.Contains(m, "claude-opus-4"),
		strings.Contains(m, "claude-sonnet-4"),
		strings.Contains(m, "claude-haiku-4"),
		strings.Contains(m, "claude-3-5"),
		strings.Contains(m, "claude-3.5"),
		strings.Contains(m, "claude-sonnet-3"),
		strings.Contains(m, "claude-opus-3"),
		strings.Contains(m, "claude-haiku-3"):
		return 200_000
	}

	// Default: 200k (safest assumption for Anthropic models)
	return 200_000
}

// wetDirFn is overridable for testing.
var wetDirFn = defaultWetDir

func wetDir() string {
	return wetDirFn()
}

func defaultWetDir() string {
	u, err := user.Current()
	if err != nil {
		return filepath.Join(os.TempDir(), ".wet")
	}
	return filepath.Join(u.HomeDir, ".wet")
}
