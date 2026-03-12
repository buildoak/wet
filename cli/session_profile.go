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
)

// tokenMultiplier is the calibrated factor: naive char/4 underestimates by
// ~2.3x for mixed code/JSON content.
const tokenMultiplier = 2.3

// contextWindow is Claude's context window size in tokens.
const contextWindow = 200_000

// estimateTokens returns an estimated token count from character length.
func estimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	return int(float64(len(text)) / 4.0 * tokenMultiplier)
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
	count  int
}

// profileResult holds the parsed session profile.
type profileResult struct {
	totalTokens      int
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

// RunSessionProfile reads a Claude Code session JSONL file and produces a
// context composition table. Optionally queries /_wet/inspect for tool result
// breakdown.
func RunSessionProfile(jsonlPath string, port int) error {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return fmt.Errorf("cannot open JSONL file: %w", err)
	}
	defer f.Close()

	// Two-pass approach:
	// Pass 1: build tool_use_id -> tool_name map
	// Pass 2: compute token counts
	//
	// We read into memory since we need two passes and the line-by-line
	// re-read would require re-opening. Store raw lines to keep memory
	// bounded per-line.

	type parsedLine struct {
		msgType string
		role    string
		content json.RawMessage
	}

	var lines []parsedLine
	toolUseMap := make(map[string]string) // tool_use_id -> tool_name

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

		// Pass 1: extract tool_use_id -> name mappings from assistant messages
		if msg.Type == "assistant" {
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

	// Pass 2: compute token counts
	result := &profileResult{
		toolResultByName: make(map[string]*categoryStats),
	}

	for _, pl := range lines {
		switch pl.msgType {
		case "user":
			processUserContent(pl.content, result, toolUseMap)
		case "assistant":
			processAssistantContent(pl.content, result)
		}
	}

	result.totalTokens = result.userText.tokens + result.assistantText.tokens +
		result.toolUse.tokens + result.toolResult.tokens

	// Optional: wet inspect for stale/fresh tracking
	var wetSummary *wetInspectSummary
	if port > 0 {
		wetSummary = fetchWetInspect(port)
	}

	// Render output
	renderProfile(result, wetSummary)
	return nil
}

// processUserContent handles user message content (string or array of blocks).
func processUserContent(raw json.RawMessage, result *profileResult, toolUseMap map[string]string) {
	if len(raw) == 0 {
		return
	}

	// Try as plain string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		tokens := estimateTokens(s)
		result.userText.tokens += tokens
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
			tokens := estimateTokens(text)
			result.toolResult.tokens += tokens
			result.toolResult.count++

			if _, ok := result.toolResultByName[toolName]; !ok {
				result.toolResultByName[toolName] = &categoryStats{}
			}
			result.toolResultByName[toolName].tokens += tokens
			result.toolResultByName[toolName].count++

		case "text":
			tokens := estimateTokens(b.Text)
			result.userText.tokens += tokens
			result.userText.count++

		default:
			// String blocks in user content
			var str string
			raw, _ := json.Marshal(b)
			if json.Unmarshal(raw, &str) == nil {
				tokens := estimateTokens(str)
				result.userText.tokens += tokens
				result.userText.count++
			}
		}
	}
}

// processAssistantContent handles assistant message content.
func processAssistantContent(raw json.RawMessage, result *profileResult) {
	if len(raw) == 0 {
		return
	}

	// Try as plain string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		tokens := estimateTokens(s)
		result.assistantText.tokens += tokens
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
			tokens := estimateTokens(b.Text)
			result.assistantText.tokens += tokens
			result.assistantText.count++

		case "tool_use":
			// Serialize the input to estimate its size
			inputRaw, _ := json.Marshal(b.Input)
			tokens := estimateTokens(string(inputRaw))
			result.toolUse.tokens += tokens
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
	pctFull := float64(total) / float64(contextWindow) * 100
	health := healthStatus(pctFull)

	header := fmt.Sprintf("CONTEXT COMPOSITION — ~%s / %s (~%d%% full) [estimated]",
		fmtK(total), fmtK(contextWindow), int(pctFull))
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
