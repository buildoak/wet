package pipeline

import (
	"strings"
	"time"

	"github.com/buildoak/wet/compressor"
	"github.com/buildoak/wet/config"
	"github.com/buildoak/wet/messages"
)

// CompressSelected runs compression only for tool_result blocks whose tool_use IDs are listed in targetIDs.
// It modifies the request in-place and returns stats.
//
// The optional replacements map provides pre-computed replacement text for specific IDs.
// IDs present in replacements skip Tier 1 compression and use the provided text directly
// as the tombstone summary. IDs not in the map fall through to normal Tier 1 compression.
// Pass nil for backward-compatible behavior (Tier 1 for all targeted IDs).
func CompressSelected(req *messages.Request, cfg *config.Config, targetIDs []string, replacements map[string]string) CompressResult {
	start := time.Now()
	result := CompressResult{}

	if req == nil {
		result.OverheadMs = float64(time.Since(start).Microseconds()) / 1000.0
		return result
	}
	if cfg == nil {
		cfg = config.Default()
	}
	if len(targetIDs) == 0 {
		result.OverheadMs = float64(time.Since(start).Microseconds()) / 1000.0
		return result
	}

	targetSet := make(map[string]struct{}, len(targetIDs))
	for _, id := range targetIDs {
		if id == "" {
			continue
		}
		targetSet[id] = struct{}{}
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

		if _, ok := targetSet[info.ToolUseID]; !ok {
			continue
		}

		// No staleness gate — in selective mode the caller already decided what to compress.
		// Only bypass errors, image-containing results, and already-tombstoned results.
		if shouldBypassSelective(info, cfg) {
			result.SkippedBypass++
			continue
		}

		family := messages.ExtractToolFamily(info.ToolName, info.Command)
		if rule, ok := cfg.Rules[family]; ok && strings.EqualFold(rule.Strategy, "none") {
			result.SkippedBypass++
			continue
		}

		// Check for pre-computed replacement text before running Tier 1.
		if rep, ok := replacements[info.ToolUseID]; ok {
			compressedTokens := messages.EstimateTokens(rep)
			if compressedTokens >= info.TokenCount {
				continue // replacement is not smaller
			}
			tombstone := CreateTombstone(family, rep, info.Turn, currentTurn, info.TokenCount, compressedTokens)
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
			continue
		}

		compressed, ok := compressor.Compress(info.ToolName, info.Command, info.Content)
		if !ok {
			// In selective mode, the agent explicitly chose this target.
			// Force generic compression even if Tier 1 doesn't match.
			compressed = compressor.GenericSignalCompress(info.Content)
			if compressor.EstimateTokens(compressed) >= info.TokenCount {
				continue // compression didn't help
			}
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

func shouldBypassSelective(info messages.ToolResultInfo, cfg *config.Config) bool {
	if cfg == nil {
		cfg = config.Default()
	}
	if info.IsError && cfg.Bypass.PreserveErrors {
		return true
	}
	if info.HasImages {
		return true
	}
	return IsTombstone(info.Content)
}
