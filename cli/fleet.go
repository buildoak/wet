package cli

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/otonashi/wet/stats"
)

type FleetEntry struct {
	Port      int
	StatsPath string
	ModTime   time.Time
	Alive     bool
	Stats     *stats.RequestStats
}

func extractPortFromFilename(name string) (int, error) {
	if !strings.HasPrefix(name, "stats-") || !strings.HasSuffix(name, ".json") {
		return 0, fmt.Errorf("malformed stats filename: %q", name)
	}
	portText := strings.TrimSuffix(strings.TrimPrefix(name, "stats-"), ".json")
	if portText == "" {
		return 0, fmt.Errorf("malformed stats filename: %q", name)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 {
		return 0, fmt.Errorf("malformed stats filename: %q", name)
	}
	return port, nil
}

func DiscoverFleet() ([]FleetEntry, error) {
	u, err := user.Current()
	wetPath := filepath.Join(os.TempDir(), ".wet")
	if err == nil {
		wetPath = filepath.Join(u.HomeDir, ".wet")
	}

	entries, err := os.ReadDir(wetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	fleet := make([]FleetEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "stats-") || !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".tmp") {
			continue
		}

		port, err := extractPortFromFilename(name)
		if err != nil {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		path := filepath.Join(wetPath, name)
		var parsed *stats.RequestStats
		if data, readErr := os.ReadFile(path); readErr == nil {
			var s stats.RequestStats
			if unmarshalErr := json.Unmarshal(data, &s); unmarshalErr == nil {
				parsed = &s
			}
		}

		fleet = append(fleet, FleetEntry{
			Port:      port,
			StatsPath: path,
			ModTime:   info.ModTime(),
			Alive:     probeHealth(port),
			Stats:     parsed,
		})
	}

	sort.Slice(fleet, func(i, j int) bool {
		return fleet[i].ModTime.After(fleet[j].ModTime)
	})
	return fleet, nil
}

func FindLiveProxy() (*FleetEntry, error) {
	fleet, err := DiscoverFleet()
	if err != nil {
		return nil, err
	}
	for i := range fleet {
		if fleet[i].Alive {
			return &fleet[i], nil
		}
	}
	return nil, fmt.Errorf("no live wet proxy found")
}

func resolvePortOrDiscover() (int, error) {
	port, err := resolvePort()
	if err == nil {
		return port, nil
	}
	// Fallback only when no explicit/env port was provided.
	if !strings.Contains(err.Error(), "no proxy port specified") {
		return 0, err
	}
	live, findErr := FindLiveProxy()
	if findErr != nil {
		return 0, findErr
	}
	return live.Port, nil
}

func probeHealth(port int) bool {
	client := &http.Client{Timeout: 200 * time.Millisecond}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	resp, err := client.Get("http://" + addr + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func RunPS(showAll bool) error {
	fleet, err := DiscoverFleet()
	if err != nil {
		return err
	}
	if len(fleet) == 0 {
		fmt.Fprintln(os.Stderr, "No wet proxies found.")
		return nil
	}

	filtered := make([]FleetEntry, 0, len(fleet))
	for _, entry := range fleet {
		if showAll || entry.Alive {
			filtered = append(filtered, entry)
		}
	}
	if len(filtered) == 0 {
		fmt.Fprintln(os.Stderr, "No live wet proxies. Use --all to see stale entries.")
		return nil
	}

	now := time.Now()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PORT\tSTATUS\tUPTIME\tFILL%\tCOMPRESSED\tSAVED\tMODE")
	for _, entry := range filtered {
		status := "(stale)"
		uptime := "—"
		fill := "—"
		compressed := "0/0"
		saved := int64(0)
		mode := "auto"

		if entry.Alive {
			status = "live"
			if entry.Stats != nil && entry.Stats.Timestamp != "" {
				if ts, err := time.Parse(time.RFC3339, entry.Stats.Timestamp); err == nil {
					uptime = formatDuration(now.Sub(ts))
				}
			}
		}

		if entry.Stats != nil {
			if entry.Stats.ContextWindow > 0 && entry.Stats.LatestTotalInputTokens > 0 {
				pct := int(float64(entry.Stats.LatestTotalInputTokens) / float64(entry.Stats.ContextWindow) * 100.0)
				fill = fmt.Sprintf("%d%%", pct)
			}
			compressed = fmt.Sprintf("%d/%d", entry.Stats.SessionItemsComp, entry.Stats.SessionItemsTotal)
			if entry.Stats.SessionAPITokensSaved > 0 {
				saved = entry.Stats.SessionAPITokensSaved
			} else {
				saved = entry.Stats.SessionTokensSaved
			}
			if strings.TrimSpace(entry.Stats.SessionMode) != "" {
				mode = entry.Stats.SessionMode
			}
		}

		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s tk\t%s\n",
			entry.Port, status, uptime, fill, compressed, formatTokenCount(saved), mode)
	}
	_ = w.Flush()
	return nil
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	hours := int(d / time.Hour)
	minutes := int((d % time.Hour) / time.Minute)
	return fmt.Sprintf("%dh%02dm", hours, minutes)
}

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

func RunStatusEnhanced(jsonOutput bool) error {
	port, err := resolvePortOrDiscover()
	if err != nil {
		return err
	}
	SetPort(port)

	data, err := httpGet("status")
	if err != nil {
		return err
	}
	if jsonOutput {
		prettyPrint(data)
		return nil
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("decode status response: %w", err)
	}

	mode := anyString(payload["mode"])
	if mode == "" {
		mode = "auto"
	}
	uptime := time.Duration(anyFloat(payload["uptime_seconds"]) * float64(time.Second))
	requestCount := anyInt64(payload["request_count"])
	tokensSaved := anyInt64(payload["tokens_saved"])
	compressionRatio := anyFloat(payload["compression_ratio"])
	itemsCompressed := anyInt64(payload["items_compressed"])
	itemsTotal := anyInt64(payload["items_total"])
	contextWindow := anyInt64(payload["context_window"])
	latestTotalInput := anyInt64(payload["latest_total_input_tokens"])
	paused := anyBool(payload["paused"])
	apiInputTokens := anyInt64(payload["api_input_tokens"])

	contextText := "—"
	if contextWindow > 0 && latestTotalInput > 0 {
		fillPct := int(float64(latestTotalInput) / float64(contextWindow) * 100.0)
		contextText = fmt.Sprintf("%d%% (%s / %s)", fillPct, formatTokenCount(latestTotalInput), formatTokenCount(contextWindow))
	}

	itemsPct := 0
	if itemsTotal > 0 {
		itemsPct = int(float64(itemsCompressed) / float64(itemsTotal) * 100.0)
	}

	savedSource := "estimated"
	if apiInputTokens > 0 {
		savedSource = "API-observed"
	}

	pausedText := "no"
	if paused {
		pausedText = "yes"
	}

	fmt.Printf("wet proxy on :%d  [%s]  up %s\n\n", port, mode, formatDuration(uptime))
	fmt.Printf("Context:    %s\n", contextText)
	fmt.Printf("Requests:   %d\n", requestCount)
	fmt.Printf("Compressed: %d / %d items (%d%%)\n", itemsCompressed, itemsTotal, itemsPct)
	fmt.Printf("Saved:      %s tokens (%s)\n", formatTokenCount(tokensSaved), savedSource)
	fmt.Printf("Ratio:      %.1f%%\n", compressionRatio*100.0)
	fmt.Printf("Paused:     %s\n", pausedText)
	return nil
}

func RunInspectEnhanced(jsonOutput, fullOutput bool) error {
	port, err := resolvePortOrDiscover()
	if err != nil {
		return err
	}
	SetPort(port)

	path := "inspect"
	if fullOutput {
		path = "inspect?full=1"
	}
	data, err := httpGet(path)
	if err != nil {
		return err
	}
	if jsonOutput {
		prettyPrint(data)
		return nil
	}

	type inspectResultEntry struct {
		ToolUseID      string `json:"tool_use_id"`
		ToolName       string `json:"tool_name"`
		Turn           int    `json:"turn"`
		CurrentTurn    int    `json:"current_turn"`
		Stale          bool   `json:"stale"`
		TokenCount     int64  `json:"token_count"`
		ContentPreview string `json:"content_preview"`
	}

	var entries []inspectResultEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("decode inspect response: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTOOL\tTURN\tCUR\tTOKENS\tSTALE\tPREVIEW")

	staleCount := 0
	staleTokens := int64(0)
	freshTokens := int64(0)
	for _, entry := range entries {
		staleText := "no"
		if entry.Stale {
			staleText = "yes"
			staleCount++
			staleTokens += entry.TokenCount
		} else {
			freshTokens += entry.TokenCount
		}

		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%s\t%s\n",
			shortID(entry.ToolUseID),
			entry.ToolName,
			entry.Turn,
			entry.CurrentTurn,
			entry.TokenCount,
			staleText,
			previewOneLine(entry.ContentPreview, 50),
		)
	}
	_ = w.Flush()

	freshCount := len(entries) - staleCount
	fmt.Printf("%d results | %d stale (%s tk) | %d fresh (%s tk)\n",
		len(entries), staleCount, formatTokenCount(staleTokens), freshCount, formatTokenCount(freshTokens))
	return nil
}

func shortID(id string) string {
	runes := []rune(id)
	if len(runes) > 12 {
		runes = runes[:12]
	}
	return string(runes) + ".."
}

func previewOneLine(s string, maxRunes int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return s
}

func anyInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int:
		return int64(t)
	case int64:
		return t
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	default:
		return 0
	}
}

func anyFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		n, _ := t.Float64()
		return n
	case string:
		n, _ := strconv.ParseFloat(t, 64)
		return n
	default:
		return 0
	}
}

func anyBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true")
	default:
		return false
	}
}

func anyString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return ""
	}
}
