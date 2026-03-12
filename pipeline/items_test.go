package pipeline

import (
	"strings"
	"testing"

	"github.com/otonashi/wet/config"
	"github.com/otonashi/wet/messages"
)

func TestCompressedItemPopulated(t *testing.T) {
	content := makeNpmInstallOutput() + strings.Repeat("Z", 350)
	command := "npm install " + strings.Repeat("pkg-very-long-name-", 12)

	req := &messages.Request{
		Messages: []messages.Message{
			{Role: "user", Content: rawJSON("compress")},
			{
				Role: "assistant",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "tool_use", ID: "toolu_stale", Name: "Bash", Input: rawJSON(map[string]any{"command": command})},
				}),
			},
			{
				Role: "user",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "tool_result", ToolUseID: "toolu_stale", Content: rawJSON(content)},
				}),
			},
			{Role: "assistant", Content: rawJSON("later turn")},
		},
	}

	cfg := config.Default()
	cfg.Staleness.Threshold = 1
	cfg.Bypass.ContentPatterns = nil

	result := CompressRequest(req, cfg)
	if result.Compressed != 1 {
		t.Fatalf("Compressed = %d, want 1", result.Compressed)
	}
	if len(result.Items) != 1 {
		t.Fatalf("Items len = %d, want 1", len(result.Items))
	}

	item := result.Items[0]
	if item.ToolUseID != "toolu_stale" {
		t.Fatalf("ToolUseID = %q, want %q", item.ToolUseID, "toolu_stale")
	}
	if item.OriginalChars != len(content) {
		t.Fatalf("OriginalChars = %d, want %d", item.OriginalChars, len(content))
	}
	if item.Preview != truncateStr(content, 200) {
		t.Fatalf("Preview mismatch")
	}
	if item.Command != truncateStr(command, 100) {
		t.Fatalf("Command mismatch")
	}

	gotTombstone := result.Replacements["toolu_stale"]
	if gotTombstone == "" {
		t.Fatal("replacement tombstone missing for toolu_stale")
	}
	if item.Tombstone != gotTombstone {
		t.Fatalf("Tombstone item mismatch")
	}
	if item.TombstoneChars != len(gotTombstone) {
		t.Fatalf("TombstoneChars = %d, want %d", item.TombstoneChars, len(gotTombstone))
	}
}

func TestTruncateStr(t *testing.T) {
	if got := truncateStr("", 5); got != "" {
		t.Fatalf("empty string truncate = %q, want empty", got)
	}

	if got := truncateStr("abc", 5); got != "abc" {
		t.Fatalf("short string truncate = %q, want %q", got, "abc")
	}

	if got := truncateStr("abcde", 5); got != "abcde" {
		t.Fatalf("at limit truncate = %q, want %q", got, "abcde")
	}

	if got := truncateStr("abcdef", 5); got != "abcde" {
		t.Fatalf("longer than limit truncate = %q, want %q", got, "abcde")
	}

	multi := "你好世界"
	if got := truncateStr(multi, 3); got != "你好世" {
		t.Fatalf("multibyte truncate = %q, want %q", got, "你好世")
	}
}
