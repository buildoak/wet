package compressor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Tier2Config holds configuration for LLM-based compression.
type Tier2Config struct {
	Model           string
	APIKey          string
	TimeoutMs       int
	MaxOutputTokens int
	Upstream        string // API endpoint, defaults to https://api.anthropic.com
}

const tier2PromptTemplate = `Extract the key information from this CLI tool output. Preserve:
- Error messages and stack traces (exact text)
- File paths mentioned
- Numeric results, counts, statuses
- Any actionable information

Discard:
- Repetitive log lines
- Progress bars and spinners
- Verbose debug output
- Redundant whitespace and formatting

Return a concise summary under 200 tokens. Start directly with the content, no preamble.

<tool_output>
%s
</tool_output>`

// Tier2Compress sends tool output to an LLM for compression.
// Returns compressed text or empty string on failure/timeout.
// Config-gated: only called when tier2.enabled is true.
func Tier2Compress(ctx context.Context, content string, cfg Tier2Config) (string, error) {
	apiKey := cfg.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		return "", fmt.Errorf("no API key for Tier 2 compression")
	}

	upstream := cfg.Upstream
	if upstream == "" {
		upstream = "https://api.anthropic.com"
	}

	maxTokens := cfg.MaxOutputTokens
	if maxTokens == 0 {
		maxTokens = 300
	}

	model := cfg.Model
	if model == "" {
		model = "claude-haiku-3"
	}

	prompt := fmt.Sprintf(tier2PromptTemplate, content)

	reqBody := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages": []map[string]any{
			{
				"role":    "user",
				"content": prompt,
			},
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	timeoutMs := cfg.TimeoutMs
	if timeoutMs == 0 {
		timeoutMs = 2000
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", upstream+"/v1/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("API call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("API returned %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse Anthropic response
	var apiResp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if len(apiResp.Content) == 0 {
		return "", fmt.Errorf("empty response content")
	}

	return apiResp.Content[0].Text, nil
}
