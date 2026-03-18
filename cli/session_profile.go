package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/buildoak/wet/config"
)

// charsToTokensFallback converts a character count to an estimated token count.
// Used only when no API usage data is available (pure offline, no assistant messages).
// Uses the standard ~4 chars/token ratio without any multiplier.
func charsToTokensFallback(chars int) int {
	if chars <= 0 {
		return 0
	}
	return int(math.Ceil(float64(chars) / 4.0))
}

// fmtK formats a token count with "k" suffix for thousands.
func fmtK(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%dk", int(math.Round(float64(n)/1000.0)))
	}
	return fmt.Sprintf("%d", n)
}

// fmtPct formats a percentage from part/total.
func fmtPct(part, total int) string {
	if total == 0 {
		return "0.0%"
	}
	return fmt.Sprintf("%.1f%%", float64(part)/float64(total)*100)
}

// healthStatus returns the health label based on context fullness.
func healthStatus(pctFull float64) string {
	switch {
	case pctFull < 40:
		return "healthy"
	case pctFull < 60:
		return "growing"
	case pctFull < 80:
		return "heavy"
	default:
		return "critical"
	}
}

// categoryStats tracks token count and block count for a category.
type categoryStats struct {
	tokens int
	chars  int // raw character count for proportional scaling
	count  int
}

// profileResult holds the parsed session profile.
type profileResult struct {
	totalTokens      int
	contextWindow    int
	apiTotalTokens   int // ground truth from API usage (0 if unavailable)
	model            string
	tokenSource      string // "api" or "estimated"
	userText         categoryStats
	assistantText    categoryStats
	toolUse          categoryStats
	toolResult       categoryStats
	toolResultByName map[string]*categoryStats
}

// canonicalToolOrder defines the display order for tool result sub-categories.
var canonicalToolOrder = []string{
	"Agent", "Read", "Bash", "Grep", "Glob",
	"WebFetch", "WebSearch", "Edit", "Write", "NotebookEdit",
}

// wetInspectSummary holds data from the wet proxy /_wet/inspect endpoint.
type wetInspectSummary struct {
	totalCount          int
	totalTokens         int
	staleCount          int
	staleTokens         int
	freshCount          int
	freshTokens         int
	errorCount          int
	compressCandidates  int
	compressCandidateTk int
}

// jsonlMessage is the top-level structure of each JSONL line.
type jsonlMessage struct {
	Type    string          `json:"type"`
	Message json.RawMessage `json:"message"`
}

// messageEnvelope holds role and content from message field.
type messageEnvelope struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Model   string          `json:"model"`
	Usage   json.RawMessage `json:"usage"`
}

// apiUsage holds the usage block from an assistant message.
type apiUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// contentBlock represents a content block in a message.
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

// inspectEntry represents a single entry from /_wet/inspect.
type inspectEntry struct {
	ToolUseID  string `json:"tool_use_id"`
	ToolName   string `json:"tool_name"`
	Stale      bool   `json:"stale"`
	IsError    bool   `json:"is_error"`
	TokenCount int    `json:"token_count"`
}

// extractTextFromContent extracts raw text from a content field that can be
// a JSON string or an array of content blocks.
func extractTextFromContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// Try as string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	// Try as array of blocks
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			switch b.Type {
			case "text":
				parts = append(parts, b.Text)
			case "image":
				// Count base64 data from image source
				var source struct {
					Data string `json:"data"`
				}
				raw := b.Input // image blocks use "source" not "input"
				if len(raw) == 0 {
					// Try parsing from the block itself
					continue
				}
				json.Unmarshal(raw, &source)
				parts = append(parts, source.Data)
			}
		}
		return strings.Join(parts, "")
	}

	return ""
}

// fetchWetContextWindow queries the wet proxy /_wet/status endpoint and returns
// the context_window value. Returns 0 on any error.
func fetchWetContextWindow(port int) int {
	url := fmt.Sprintf("http://127.0.0.1:%d/_wet/status", port)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0
	}

	var status map[string]any
	if err := json.Unmarshal(body, &status); err != nil {
		return 0
	}

	if v, ok := status["context_window"]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}

// RunSessionProfile reads a Claude Code session JSONL file and produces a
// context composition table. Optionally queries /_wet/inspect for tool result
// breakdown and /_wet/status for context window size.
func RunSessionProfile(jsonlPath string, port int) error {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return fmt.Errorf("cannot open JSONL file: %w", err)
	}
	defer f.Close()

	// Two-pass approach:
	// Pass 1: build tool_use_id -> tool_name map + extract model/usage from assistant messages
	// Pass 2: compute character counts per category
	// Then: scale character counts to token counts using API ground truth (or fallback)

	type parsedLine struct {
		msgType string
		role    string
		content json.RawMessage
	}

	var lines []parsedLine
	toolUseMap := make(map[string]string) // tool_use_id -> tool_name

	// Track the last assistant message's model and usage for ground truth
	var lastModel string
	var lastUsage *apiUsage

	scanner := bufio.NewScanner(f)
	// Bump buffer to 4MB for long JSONL lines (tool results can be huge).
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg jsonlMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue // skip malformed lines
		}

		// Only process user/assistant messages
		if msg.Type != "user" && msg.Type != "assistant" {
			continue
		}

		var env messageEnvelope
		if err := json.Unmarshal(msg.Message, &env); err != nil {
			continue
		}

		pl := parsedLine{
			msgType: msg.Type,
			role:    env.Role,
			content: env.Content,
		}
		lines = append(lines, pl)

		// Pass 1: extract tool_use_id -> name mappings and model/usage from assistant messages
		if msg.Type == "assistant" {
			// Extract model name
			if env.Model != "" {
				lastModel = env.Model
			}

			// Extract usage data
			if len(env.Usage) > 0 {
				var usage apiUsage
				if err := json.Unmarshal(env.Usage, &usage); err == nil {
					total := usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
					if total > 0 {
						lastUsage = &usage
					}
				}
			}

			var blocks []contentBlock
			if err := json.Unmarshal(env.Content, &blocks); err == nil {
				for _, b := range blocks {
					if b.Type == "tool_use" && b.ID != "" {
						name := b.Name
						if name == "" {
							name = "unknown"
						}
						toolUseMap[b.ID] = name
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading JSONL: %w", err)
	}

	// Pass 2: compute character counts per category
	result := &profileResult{
		toolResultByName: make(map[string]*categoryStats),
		model:            lastModel,
	}

	for _, pl := range lines {
		switch pl.msgType {
		case "user":
			processUserContent(pl.content, result, toolUseMap)
		case "assistant":
			processAssistantContent(pl.content, result)
		}
	}

	// Compute total characters across all categories
	totalChars := result.userText.chars + result.assistantText.chars +
		result.toolUse.chars + result.toolResult.chars

	// Determine API ground truth token count
	if lastUsage != nil {
		result.apiTotalTokens = lastUsage.InputTokens +
			lastUsage.CacheCreationInputTokens +
			lastUsage.CacheReadInputTokens
	}

	// Scale character counts to token counts
	if result.apiTotalTokens > 0 && totalChars > 0 {
		// Proportional scaling: each category's tokens = (category_chars / total_chars) * api_total
		result.tokenSource = "api"
		result.userText.tokens = proportionalTokens(result.userText.chars, totalChars, result.apiTotalTokens)
		result.assistantText.tokens = proportionalTokens(result.assistantText.chars, totalChars, result.apiTotalTokens)
		result.toolUse.tokens = proportionalTokens(result.toolUse.chars, totalChars, result.apiTotalTokens)
		result.toolResult.tokens = proportionalTokens(result.toolResult.chars, totalChars, result.apiTotalTokens)

		// Scale per-tool-name breakdown
		for _, stats := range result.toolResultByName {
			stats.tokens = proportionalTokens(stats.chars, totalChars, result.apiTotalTokens)
		}

		result.totalTokens = result.apiTotalTokens
	} else {
		// Fallback: standard chars/4 estimation (no multiplier)
		result.tokenSource = "estimated"
		result.userText.tokens = charsToTokensFallback(result.userText.chars)
		result.assistantText.tokens = charsToTokensFallback(result.assistantText.chars)
		result.toolUse.tokens = charsToTokensFallback(result.toolUse.chars)
		result.toolResult.tokens = charsToTokensFallback(result.toolResult.chars)

		for _, stats := range result.toolResultByName {
			stats.tokens = charsToTokensFallback(stats.chars)
		}

		result.totalTokens = result.userText.tokens + result.assistantText.tokens +
			result.toolUse.tokens + result.toolResult.tokens
	}

	// Determine context window size
	result.contextWindow = resolveContextWindow(port, lastModel)

	// Optional: wet inspect for stale/fresh tracking
	var wetSummary *wetInspectSummary
	if port > 0 {
		wetSummary = fetchWetInspect(port)
	}

	// Render output
	renderProfile(result, wetSummary)
	return nil
}

// proportionalTokens computes tokens for a category using proportional scaling.
// Formula: category_tokens = (category_chars / total_chars) * api_total_tokens
func proportionalTokens(categoryChars, totalChars, apiTotal int) int {
	if totalChars == 0 || apiTotal == 0 {
		return 0
	}
	return int(math.Round(float64(categoryChars) / float64(totalChars) * float64(apiTotal)))
}

// resolveContextWindow determines the context window size.
// Priority: 1) wet proxy status endpoint (when port specified), 2) model lookup, 3) 200k fallback.
func resolveContextWindow(port int, model string) int {
	// Try wet proxy first
	if port > 0 {
		if cw := fetchWetContextWindow(port); cw > 0 {
			return cw
		}
	}

	// Offline: look up model in built-in defaults
	if model != "" {
		cfg := config.Default()
		return cfg.ModelContextWindow(model)
	}

	// Ultimate fallback
	return 200_000
}

// processUserContent handles user message content (string or array of blocks).
// It accumulates character counts (not token counts) for later proportional scaling.
func processUserContent(raw json.RawMessage, result *profileResult, toolUseMap map[string]string) {
	if len(raw) == 0 {
		return
	}

	// Try as plain string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		result.userText.chars += len(s)
		result.userText.count++
		return
	}

	// Try as array of blocks
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return
	}

	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			toolName := "unknown"
			if b.ToolUseID != "" {
				if name, ok := toolUseMap[b.ToolUseID]; ok {
					toolName = name
				}
			}
			text := extractTextFromContent(b.Content)
			chars := len(text)
			result.toolResult.chars += chars
			result.toolResult.count++

			if _, ok := result.toolResultByName[toolName]; !ok {
				result.toolResultByName[toolName] = &categoryStats{}
			}
			result.toolResultByName[toolName].chars += chars
			result.toolResultByName[toolName].count++

		case "text":
			result.userText.chars += len(b.Text)
			result.userText.count++

		default:
			// String blocks in user content
			var str string
			raw, _ := json.Marshal(b)
			if json.Unmarshal(raw, &str) == nil {
				result.userText.chars += len(str)
				result.userText.count++
			}
		}
	}
}

// processAssistantContent handles assistant message content.
// It accumulates character counts (not token counts) for later proportional scaling.
func processAssistantContent(raw json.RawMessage, result *profileResult) {
	if len(raw) == 0 {
		return
	}

	// Try as plain string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		result.assistantText.chars += len(s)
		result.assistantText.count++
		return
	}

	// Try as array of blocks
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return
	}

	for _, b := range blocks {
		switch b.Type {
		case "text":
			result.assistantText.chars += len(b.Text)
			result.assistantText.count++

		case "tool_use":
			// Serialize the input to estimate its size
			inputRaw, _ := json.Marshal(b.Input)
			result.toolUse.chars += len(inputRaw)
			result.toolUse.count++

		case "thinking":
			// Thinking blocks are redacted in JSONL — skip
		}
	}
}

// fetchWetInspect queries the wet proxy inspect endpoint and returns a summary.
func fetchWetInspect(port int) *wetInspectSummary {
	url := fmt.Sprintf("http://127.0.0.1:%d/_wet/inspect", port)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not reach wet proxy on port %d: %v\n", port, err)
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: error reading wet inspect response: %v\n", err)
		return nil
	}

	var entries []inspectEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: error parsing wet inspect response: %v\n", err)
		return nil
	}

	summary := &wetInspectSummary{
		totalCount: len(entries),
	}

	for _, e := range entries {
		summary.totalTokens += e.TokenCount
		if e.IsError {
			summary.errorCount++
		}
		if e.Stale {
			summary.staleCount++
			summary.staleTokens += e.TokenCount
		} else {
			summary.freshCount++
			summary.freshTokens += e.TokenCount
		}
		// Compression candidates: stale and > 500 tokens
		if e.Stale && e.TokenCount > 500 {
			summary.compressCandidates++
			summary.compressCandidateTk += e.TokenCount
		}
	}

	return summary
}

// renderProfile prints the composition table to stdout.
func renderProfile(result *profileResult, wetSummary *wetInspectSummary) {
	total := result.totalTokens
	ctxWindow := result.contextWindow
	pctFull := float64(total) / float64(ctxWindow) * 100
	health := healthStatus(pctFull)

	sourceLabel := "from API"
	if result.tokenSource == "estimated" {
		sourceLabel = "estimated"
	}

	header := fmt.Sprintf("CONTEXT COMPOSITION — ~%s / %s (~%d%% full) [%s]",
		fmtK(total), fmtK(ctxWindow), int(pctFull), sourceLabel)
	fmt.Println(header)
	fmt.Println(strings.Repeat("═", 51))

	fmt.Printf("%-30s %8s  %6s\n", "Category", "Tokens", "%")
	fmt.Println(strings.Repeat("─", 51))

	// Tool results (total)
	tr := result.toolResult
	fmt.Printf("%-30s %8s  %6s\n", "Tool results (total)",
		fmtK(tr.tokens), fmtPct(tr.tokens, total))

	// Sub-breakdown by tool name: canonical order first, then alphabetical
	seen := make(map[string]bool)
	var orderedNames []string
	for _, name := range canonicalToolOrder {
		if _, ok := result.toolResultByName[name]; ok {
			orderedNames = append(orderedNames, name)
			seen[name] = true
		}
	}
	var remaining []string
	for name := range result.toolResultByName {
		if !seen[name] {
			remaining = append(remaining, name)
		}
	}
	sort.Strings(remaining)
	orderedNames = append(orderedNames, remaining...)

	for _, name := range orderedNames {
		info := result.toolResultByName[name]
		label := fmt.Sprintf("  └─ %s results (%d)", name, info.count)
		fmt.Printf("%-30s %8s  %6s\n", label, fmtK(info.tokens), fmtPct(info.tokens, total))
	}

	// Tool_use blocks
	tu := result.toolUse
	tuLabel := fmt.Sprintf("Tool_use blocks (%d)", tu.count)
	fmt.Printf("%-30s %8s  %6s\n", tuLabel, fmtK(tu.tokens), fmtPct(tu.tokens, total))

	// Assistant text
	at := result.assistantText
	atLabel := fmt.Sprintf("Assistant text (%d)", at.count)
	fmt.Printf("%-30s %8s  %6s\n", atLabel, fmtK(at.tokens), fmtPct(at.tokens, total))

	// User text
	ut := result.userText
	utLabel := fmt.Sprintf("User text (%d)", ut.count)
	fmt.Printf("%-30s %8s  %6s\n", utLabel, fmtK(ut.tokens), fmtPct(ut.tokens, total))

	fmt.Println(strings.Repeat("─", 51))

	// Status line
	fmt.Printf("Status: %s\n", health)

	// Wet inspect addendum
	if wetSummary != nil {
		fmt.Println()
		fmt.Println("WET TRACKING")
		fmt.Println(strings.Repeat("─", 51))
		fmt.Printf("Tracked: %d results, %stk\n",
			wetSummary.totalCount, fmtK(wetSummary.totalTokens))
		fmt.Printf("Stale: %d (%stk)  Fresh: %d (%stk)  Errors: %d\n",
			wetSummary.staleCount, fmtK(wetSummary.staleTokens),
			wetSummary.freshCount, fmtK(wetSummary.freshTokens),
			wetSummary.errorCount)
		fmt.Printf("Compression candidates: %d results, %stk potential savings\n",
			wetSummary.compressCandidates, fmtK(wetSummary.compressCandidateTk))
	}
}
