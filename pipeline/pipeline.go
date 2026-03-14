package pipeline

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/otonashi/wet/compressor"
	"github.com/otonashi/wet/config"
	"github.com/otonashi/wet/messages"
)

// CompressResult holds per-request compression stats
type CompressResult struct {
	TotalToolResults int
	Compressed       int
	SkippedFresh     int
	SkippedBypass    int
	TokensBefore     int
	TokensAfter      int
	OverheadMs       float64
	Replacements     map[string]string
	Items            []CompressedItem
}

// CompressedItem records per-item character-level details for a compression action.
type CompressedItem struct {
	ToolUseID      string
	ToolName       string
	Command        string // first 100 chars
	OriginalChars  int
	TombstoneChars int
	Tombstone      string
	Preview        string // first 200 chars of original content
}

// CompressRequest runs the full compression pipeline on a parsed request.
// It modifies the messages in-place and returns stats.
func CompressRequest(req *messages.Request, cfg *config.Config) CompressResult {
	start := time.Now()
	result := CompressResult{}

	if req == nil {
		result.OverheadMs = float64(time.Since(start).Microseconds()) / 1000.0
		return result
	}
	if cfg == nil {
		cfg = config.Default()
	}

	infos := messages.ClassifyStaleness(req.Messages, cfg.Staleness.Threshold, cfg.Rules)
	currentTurn := 0
	for _, info := range infos {
		if info.Turn > currentTurn {
			currentTurn = info.Turn
		}
	}
	if currentTurn == 0 {
		for _, msg := range req.Messages {
			if msg.Role == "assistant" {
				currentTurn++
			}
		}
	}

	for _, info := range infos {
		result.TotalToolResults++

		if !info.Stale {
			result.SkippedFresh++
			continue
		}

		if ShouldBypass(info, cfg) {
			result.SkippedBypass++
			continue
		}

		family := messages.ExtractToolFamily(info.ToolName, info.Command)
		if rule, ok := cfg.Rules[family]; ok && strings.EqualFold(rule.Strategy, "none") {
			result.SkippedBypass++
			continue
		}

		compressed, ok := compressor.Compress(info.ToolName, info.Command, info.Content)
		if !ok {
			continue
		}

		compressedTokens := messages.EstimateTokens(compressed)
		if compressedTokens >= info.TokenCount {
			continue
		}

		tombstone := CreateTombstone(family, compressed, info.Turn, currentTurn, info.TokenCount, compressedTokens)
		tombstoneTokens := messages.EstimateTokens(tombstone)
		if tombstoneTokens >= info.TokenCount {
			continue // tombstone overhead makes this a net loss
		}
		// Minimum savings threshold: reject if compressed result is > 60% of original
		if float64(tombstoneTokens) > 0.6*float64(info.TokenCount) {
			continue // near-pass-through, not worth the replacement
		}
		if err := ReplaceToolResultContent(&req.Messages[info.MsgIdx], info.BlockIdx, tombstone, info.ContentIsStr); err != nil {
			continue
		}

		result.Compressed++
		result.TokensBefore += info.TokenCount
		result.TokensAfter += tombstoneTokens
		if result.Replacements == nil {
			result.Replacements = make(map[string]string)
		}
		result.Replacements[info.ToolUseID] = tombstone
		result.Items = append(result.Items, CompressedItem{
			ToolUseID:      info.ToolUseID,
			ToolName:       info.ToolName,
			Command:        truncateStr(info.Command, 100),
			OriginalChars:  len(info.Content),
			TombstoneChars: len(tombstone),
			Tombstone:      tombstone,
			Preview:        truncateStr(info.Content, 200),
		})
	}

	result.OverheadMs = float64(time.Since(start).Microseconds()) / 1000.0
	return result
}

func truncateStr(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return s
}

func ReplaceToolResultContent(msg *messages.Message, blockIdx int, newContent string, contentIsStr bool) error {
	if msg == nil {
		return fmt.Errorf("nil message")
	}

	if contentIsStr {
		raw, err := json.Marshal(newContent)
		if err != nil {
			return err
		}
		msg.Content = raw
		return nil
	}

	blocks, _, err := messages.ParseContent(msg.Content)
	if err != nil {
		return err
	}
	if blockIdx < 0 || blockIdx >= len(blocks) {
		return fmt.Errorf("invalid block index %d", blockIdx)
	}
	if blocks[blockIdx].Type != "tool_result" {
		return fmt.Errorf("block %d is %q, want tool_result", blockIdx, blocks[blockIdx].Type)
	}

	raw, err := json.Marshal(newContent)
	if err != nil {
		return err
	}
	blocks[blockIdx].Content = raw

	encoded, err := json.Marshal(blocks)
	if err != nil {
		return err
	}
	msg.Content = encoded
	return nil
}
