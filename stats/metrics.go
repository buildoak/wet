package stats

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// MetricsWriter appends JSONL to ~/.wet/metrics.jsonl.
type MetricsWriter struct {
	file *os.File
	mu   sync.Mutex
}

// MetricsEntry is one line of the JSONL metrics file.
type MetricsEntry struct {
	Timestamp         string  `json:"timestamp"`
	RequestID         string  `json:"request_id"`
	TotalToolResults  int     `json:"total_tool_results"`
	Compressed        int     `json:"compressed"`
	SkippedFresh      int     `json:"skipped_fresh"`
	SkippedBypass     int     `json:"skipped_bypass"`
	Tier1Compressions int     `json:"tier1_compressions"`
	Tier2Compressions int     `json:"tier2_compressions"`
	TokensBefore      int     `json:"tokens_before"`
	TokensAfter       int     `json:"tokens_after"`
	CompressionRatio  float64 `json:"compression_ratio"`
	Tier1LatencyMs    float64 `json:"tier1_latency_ms"`
	TotalOverheadMs   float64 `json:"total_proxy_overhead_ms"`
}

// NewMetricsWriter opens ~/.wet/metrics.jsonl in append mode.
func NewMetricsWriter() (*MetricsWriter, error) {
	dir := wetDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(filepath.Join(dir, "metrics.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &MetricsWriter{file: f}, nil
}

// Write appends an entry to the metrics file.
func (w *MetricsWriter) Write(entry MetricsEntry) error {
	if entry.RequestID == "" {
		entry.RequestID = newRequestID()
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.file.Write(data)
	return err
}

func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Close closes the metrics file.
func (w *MetricsWriter) Close() error {
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}
