package pipeline

import (
	"regexp"
	"strings"

	"github.com/otonashi/wet/config"
	"github.com/otonashi/wet/messages"
)

// ShouldBypass returns true if this tool_result should NOT be compressed.
func ShouldBypass(info messages.ToolResultInfo, cfg *config.Config) bool {
	if cfg == nil {
		cfg = config.Default()
	}

	if !info.Stale {
		return true
	}

	if info.IsError && cfg.Bypass.PreserveErrors {
		return true
	}

	if IsTombstone(info.Content) {
		return true
	}
	if info.HasImages {
		return true
	}

	minTokens := cfg.Compression.MinTokens
	if cfg.Bypass.MinTokens > minTokens {
		minTokens = cfg.Bypass.MinTokens
	}
	if info.TokenCount < minTokens {
		return true
	}

	for _, pattern := range cfg.Bypass.ContentPatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		if re.MatchString(info.Content) {
			return true
		}
	}

	if strings.Contains(info.Content, `"type":"image"`) ||
		strings.Contains(info.Content, `"type": "image"`) {
		return true
	}

	return false
}
