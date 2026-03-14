package pipeline

import (
	"fmt"
	"strings"
	"testing"

	"github.com/buildoak/wet/compressor"
	"github.com/buildoak/wet/config"
	"github.com/buildoak/wet/messages"
)

func TestCompressSelected_MinSavingsThreshold(t *testing.T) {
	t.Run("precomputed replacements", func(t *testing.T) {
		nearOriginal := strings.Repeat("near-original-line ", 260)
		goodOriginal := strings.Repeat("good-original-line ", 260)
		nearReplacement := strings.Repeat("near-summary ", 240)
		goodReplacement := strings.Repeat("tight-summary ", 60)

		nearOriginalTokens := messages.EstimateTokens(nearOriginal)
		goodOriginalTokens := messages.EstimateTokens(goodOriginal)
		nearReplacementTokens := messages.EstimateTokens(nearReplacement)
		goodReplacementTokens := messages.EstimateTokens(goodReplacement)

		nearTombstone := CreateTombstone("bash_generic", nearReplacement, 1, 2, nearOriginalTokens, nearReplacementTokens)
		goodTombstone := CreateTombstone("bash_generic", goodReplacement, 2, 2, goodOriginalTokens, goodReplacementTokens)
		nearTombstoneTokens := messages.EstimateTokens(nearTombstone)
		goodTombstoneTokens := messages.EstimateTokens(goodTombstone)

		if float64(nearTombstoneTokens) <= 0.6*float64(nearOriginalTokens) {
			t.Fatalf("test setup invalid: near replacement ratio must be >60%%, got %.2f", float64(nearTombstoneTokens)/float64(nearOriginalTokens))
		}
		if float64(goodTombstoneTokens) > 0.6*float64(goodOriginalTokens) {
			t.Fatalf("test setup invalid: good replacement ratio must be <=60%%, got %.2f", float64(goodTombstoneTokens)/float64(goodOriginalTokens))
		}

		req := &messages.Request{
			Messages: []messages.Message{
				{Role: "user", Content: rawJSON("compress selected")},
				{
					Role: "assistant",
					Content: rawJSON([]messages.ContentBlock{
						{Type: "tool_use", ID: "near", Name: "Bash", Input: rawJSON(map[string]any{"command": "echo near"})},
					}),
				},
				{
					Role: "user",
					Content: rawJSON([]messages.ContentBlock{
						{Type: "tool_result", ToolUseID: "near", Content: rawJSON(nearOriginal)},
					}),
				},
				{
					Role: "assistant",
					Content: rawJSON([]messages.ContentBlock{
						{Type: "tool_use", ID: "good", Name: "Bash", Input: rawJSON(map[string]any{"command": "echo good"})},
					}),
				},
				{
					Role: "user",
					Content: rawJSON([]messages.ContentBlock{
						{Type: "tool_result", ToolUseID: "good", Content: rawJSON(goodOriginal)},
					}),
				},
			},
		}

		cfg := config.Default()
		cfg.Bypass.ContentPatterns = nil

		result := CompressSelected(req, cfg, []string{"near", "good"}, map[string]string{
			"near": nearReplacement,
			"good": goodReplacement,
		})

		nearContent := mustToolResultContent(t, req.Messages[2], 0)
		if nearContent != nearOriginal {
			t.Fatalf("expected near replacement to be skipped due to min-savings threshold")
		}

		goodContent := mustToolResultContent(t, req.Messages[4], 0)
		if !IsTombstone(goodContent) {
			t.Fatalf("expected good replacement to be compressed, got: %q", goodContent)
		}

		if result.Compressed != 1 {
			t.Fatalf("expected exactly 1 compression, got %d", result.Compressed)
		}
		if _, ok := result.Replacements["near"]; ok {
			t.Fatalf("expected near replacement to be absent from replacements map")
		}
		if _, ok := result.Replacements["good"]; !ok {
			t.Fatalf("expected good replacement to be present in replacements map")
		}
	})

	t.Run("mechanical compression", func(t *testing.T) {
		nearOriginal := makeReadOutputForThresholdTest(120)
		goodOriginal := makeReadOutputForThresholdTest(420)

		nearCompressed, ok := compressor.Compress("Read", "", nearOriginal)
		if !ok {
			t.Fatalf("test setup invalid: expected near output to be compressible")
		}
		goodCompressed, ok := compressor.Compress("Read", "", goodOriginal)
		if !ok {
			t.Fatalf("test setup invalid: expected good output to be compressible")
		}

		nearOriginalTokens := messages.EstimateTokens(nearOriginal)
		goodOriginalTokens := messages.EstimateTokens(goodOriginal)
		nearCompressedTokens := messages.EstimateTokens(nearCompressed)
		goodCompressedTokens := messages.EstimateTokens(goodCompressed)

		nearTombstone := CreateTombstone("read", nearCompressed, 1, 2, nearOriginalTokens, nearCompressedTokens)
		goodTombstone := CreateTombstone("read", goodCompressed, 2, 2, goodOriginalTokens, goodCompressedTokens)
		nearTombstoneTokens := messages.EstimateTokens(nearTombstone)
		goodTombstoneTokens := messages.EstimateTokens(goodTombstone)

		if float64(nearTombstoneTokens) <= 0.6*float64(nearOriginalTokens) {
			t.Fatalf("test setup invalid: near mechanical ratio must be >60%%, got %.2f", float64(nearTombstoneTokens)/float64(nearOriginalTokens))
		}
		if float64(goodTombstoneTokens) > 0.6*float64(goodOriginalTokens) {
			t.Fatalf("test setup invalid: good mechanical ratio must be <=60%%, got %.2f", float64(goodTombstoneTokens)/float64(goodOriginalTokens))
		}

		req := &messages.Request{
			Messages: []messages.Message{
				{Role: "user", Content: rawJSON("compress selected")},
				{
					Role: "assistant",
					Content: rawJSON([]messages.ContentBlock{
						{Type: "tool_use", ID: "near", Name: "Read", Input: rawJSON(map[string]any{"file_path": "/tmp/near.txt"})},
					}),
				},
				{
					Role: "user",
					Content: rawJSON([]messages.ContentBlock{
						{Type: "tool_result", ToolUseID: "near", Content: rawJSON(nearOriginal)},
					}),
				},
				{
					Role: "assistant",
					Content: rawJSON([]messages.ContentBlock{
						{Type: "tool_use", ID: "good", Name: "Read", Input: rawJSON(map[string]any{"file_path": "/tmp/good.txt"})},
					}),
				},
				{
					Role: "user",
					Content: rawJSON([]messages.ContentBlock{
						{Type: "tool_result", ToolUseID: "good", Content: rawJSON(goodOriginal)},
					}),
				},
			},
		}

		cfg := config.Default()
		cfg.Bypass.ContentPatterns = nil

		result := CompressSelected(req, cfg, []string{"near", "good"}, nil)

		nearContent := mustToolResultContent(t, req.Messages[2], 0)
		if nearContent != nearOriginal {
			t.Fatalf("expected near mechanical compression to be skipped due to min-savings threshold")
		}

		goodContent := mustToolResultContent(t, req.Messages[4], 0)
		if !IsTombstone(goodContent) {
			t.Fatalf("expected good mechanical compression to be applied, got: %q", goodContent)
		}

		if result.Compressed != 1 {
			t.Fatalf("expected exactly 1 compression, got %d", result.Compressed)
		}
	})
}

func makeReadOutputForThresholdTest(lines int) string {
	var b strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, "record %04d: 0123456789 abcdefghijklmnopqrstuvwxyz ABCDEFGHIJKLMNOPQRSTUVWXYZ\n", i)
	}
	return b.String()
}
