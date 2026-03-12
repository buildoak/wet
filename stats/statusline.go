package stats

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RenderStatusline reads the stats file and outputs a one-liner for Claude Code status bar.
//
// Three states:
//
//	State 1 (proxy running, no data):  "wet: ready to compress!"
//	State 2 (proxy running, has data): "wet: 46% (92k/200k) | 18/24 results compressed (50k->20k)"
//	State 3 (no proxy / sleeping):     "wet: sleeping"
func RenderStatusline() (string, error) {
	path, err := findStatsFile()
	if err != nil || path == "" {
		return "wet: sleeping", nil
	}

	// If the file exists, the proxy was running. Show data regardless of age.
	// Only "sleeping" if no stats file at all.
	if _, err := os.Stat(path); err != nil {
		return "wet: sleeping", nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "wet: sleeping", nil
	}

	var s RequestStats
	if err := json.Unmarshal(data, &s); err != nil {
		return "wet: sleeping", nil
	}

	// Compute total context usage: uncached input + cache creation + cache read.
	// LatestTotalInputTokens is set by the writer when all three are available;
	// fall back to summing the individual fields from the per-request snapshot.
	totalInput := s.LatestTotalInputTokens
	if totalInput == 0 {
		totalInput = s.APIInputTokens + s.APICacheCreationInputTokens + s.APICacheReadInputTokens
	}

	// No compression data yet (proxy running but no requests with tool results)
	if s.SessionRequests == 0 && s.SessionTokensSaved == 0 && s.SessionItemsComp == 0 {
		// Still show fill% if we have context window data (e.g. from a passthrough request)
		if s.ContextWindow > 0 && totalInput > 0 {
			pct := int(float64(totalInput) / float64(s.ContextWindow) * 100)
			inputStr := formatTokenCount(int64(totalInput))
			windowStr := formatTokenCount(int64(s.ContextWindow))
			return fmt.Sprintf("wet: %d%% (%s/%s) | ready", pct, inputStr, windowStr), nil
		}
		return "wet: ready", nil
	}

	// Build the statusline
	var parts []string

	// Context fill percentage: total input / context_window
	if s.ContextWindow > 0 && totalInput > 0 {
		pct := int(float64(totalInput) / float64(s.ContextWindow) * 100)
		inputStr := formatTokenCount(int64(totalInput))
		windowStr := formatTokenCount(int64(s.ContextWindow))
		parts = append(parts, fmt.Sprintf("%d%% (%s/%s)", pct, inputStr, windowStr))
	}

	// Compression: items compressed / total + before->after token delta
	if s.SessionItemsTotal > 0 {
		compPart := fmt.Sprintf("%d/%d results compressed", s.SessionItemsComp, s.SessionItemsTotal)
		if s.SessionTokensBefore > 0 && s.SessionTokensAfter > 0 {
			beforeStr := formatTokenCount(s.SessionTokensBefore)
			afterStr := formatTokenCount(s.SessionTokensAfter)
			compPart += fmt.Sprintf(" (%s->%s)", beforeStr, afterStr)
		}
		parts = append(parts, compPart)
	}

	if len(parts) == 0 {
		// Fallback: show tokens saved if we have it
		if s.SessionTokensSaved > 0 {
			saved := formatTokenCount(s.SessionTokensSaved)
			pct := int(s.SessionCompRatio * 100)
			parts = append(parts, fmt.Sprintf("%s saved (-%d%%)", saved, pct))
		} else {
			return "wet: ready to compress!", nil
		}
	}

	return "wet: " + strings.Join(parts, " | "), nil
}

// findStatsFile locates the stats file to read.
// Priority: per-port file from WET_PORT > most recent stats-*.json in ~/.wet/
func findStatsFile() (string, error) {
	dir := wetDir()

	// Try per-port file first
	if port := os.Getenv("WET_PORT"); port != "" {
		path := filepath.Join(dir, fmt.Sprintf("stats-%s.json", port))
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
		// Fall through to auto-discovery
	}

	// Try to find the most recent stats-*.json file
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}

	type statsFile struct {
		path    string
		modTime time.Time
	}
	var candidates []statsFile
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "stats-") && strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".tmp") {
			info, err := e.Info()
			if err != nil {
				continue
			}
			candidates = append(candidates, statsFile{
				path:    filepath.Join(dir, name),
				modTime: info.ModTime(),
			})
		}
	}

	if len(candidates) == 0 {
		// Try legacy stats.json
		path := filepath.Join(dir, "stats.json")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
		return "", nil
	}

	// Sort by modification time, most recent first
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})

	return candidates[0].path, nil
}

// formatTokenCount formats a token count with k/M suffixes.
func formatTokenCount(n int64) string {
	if n < 0 {
		n = 0
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000.0)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000.0)
	}
	return fmt.Sprintf("%d", n)
}

// formatTokens is a backward-compatible alias used by old code.
func formatTokens(n int) string {
	return formatTokenCount(int64(n))
}
