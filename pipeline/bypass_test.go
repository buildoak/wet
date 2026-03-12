package pipeline

import (
	"testing"

	"github.com/otonashi/wet/config"
	"github.com/otonashi/wet/messages"
)

func TestBypassFreshResult(t *testing.T) {
	cfg := config.Default()
	info := messages.ToolResultInfo{Stale: false, TokenCount: 1000, Content: "large output"}
	if !ShouldBypass(info, cfg) {
		t.Fatal("expected fresh result to bypass")
	}
}

func TestBypassErrorResult(t *testing.T) {
	cfg := config.Default()
	cfg.Bypass.PreserveErrors = true
	info := messages.ToolResultInfo{Stale: true, IsError: true, TokenCount: 1000, Content: "error output"}
	if !ShouldBypass(info, cfg) {
		t.Fatal("expected error result to bypass when preserve_errors=true")
	}
}

func TestBypassAlreadyCompressed(t *testing.T) {
	cfg := config.Default()
	info := messages.ToolResultInfo{Stale: true, TokenCount: 1000, Content: "[compressed: git_status | ...]"}
	if !ShouldBypass(info, cfg) {
		t.Fatal("expected tombstone content to bypass")
	}
}

func TestBypassSmallOutput(t *testing.T) {
	cfg := config.Default()
	cfg.Compression.MinTokens = 100
	cfg.Bypass.MinTokens = 50
	info := messages.ToolResultInfo{Stale: true, TokenCount: 10, Content: "small"}
	if !ShouldBypass(info, cfg) {
		t.Fatal("expected small output to bypass")
	}
}

func TestBypassErrorPattern(t *testing.T) {
	cfg := config.Default()
	cfg.Bypass.ContentPatterns = []string{"^Error:"}
	info := messages.ToolResultInfo{Stale: true, TokenCount: 1000, Content: "Error: something failed"}
	if !ShouldBypass(info, cfg) {
		t.Fatal("expected output matching bypass regex to bypass")
	}
}

func TestNoBypass(t *testing.T) {
	cfg := config.Default()
	cfg.Bypass.PreserveErrors = true
	cfg.Bypass.ContentPatterns = []string{"^Error:"}
	cfg.Compression.MinTokens = 100
	cfg.Bypass.MinTokens = 100

	info := messages.ToolResultInfo{
		Stale:      true,
		IsError:    false,
		TokenCount: 5000,
		Content:    "all good output with enough tokens and no matching pattern",
	}
	if ShouldBypass(info, cfg) {
		t.Fatal("expected stale non-error large output with no matching patterns to not bypass")
	}
}

func TestBypassImageResult(t *testing.T) {
	cfg := config.Default()
	info := messages.ToolResultInfo{
		Stale:      true,
		HasImages:  true,
		TokenCount: 5000,
		Content:    "screenshot payload",
	}
	if !ShouldBypass(info, cfg) {
		t.Fatal("expected image-containing result to bypass")
	}
}
