package pipeline

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/otonashi/wet/config"
	"github.com/otonashi/wet/messages"
)

func TestCompressRequestIntegration(t *testing.T) {
	gitStatus := makeGitStatusOutput()
	npmOutput := makeNpmInstallOutput()
	lsOutput := "-rw-r--r-- 1 user staff 123 Mar 09 file1.go\n-rw-r--r-- 1 user staff 456 Mar 09 file2.go"

	req := &messages.Request{
		Messages: []messages.Message{
			{Role: "user", Content: rawJSON("fix the bug")},
			{
				Role: "assistant",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "text", Text: "Let me check."},
					{Type: "tool_use", ID: "t1", Name: "Bash", Input: rawJSON(map[string]any{"command": "git status"})},
				}),
			},
			{
				Role: "user",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "tool_result", ToolUseID: "t1", Content: rawJSON(gitStatus)},
				}),
			},
			{
				Role: "assistant",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "tool_use", ID: "t2", Name: "Bash", Input: rawJSON(map[string]any{"command": "npm install"})},
				}),
			},
			{
				Role: "user",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "tool_result", ToolUseID: "t2", Content: rawJSON(npmOutput)},
				}),
			},
			{
				Role: "assistant",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "tool_use", ID: "t3", Name: "Bash", Input: rawJSON(map[string]any{"command": "ls -la"})},
				}),
			},
			{
				Role: "user",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "tool_result", ToolUseID: "t3", Content: rawJSON(lsOutput)},
				}),
			},
		},
	}

	cfg := config.Default()
	cfg.Staleness.Threshold = 1
	cfg.Bypass.ContentPatterns = nil

	result := CompressRequest(req, cfg)

	turn1Content := mustToolResultContent(t, req.Messages[2], 0)
	if !IsTombstone(turn1Content) {
		t.Fatalf("expected turn 1 git status to be compressed, got: %q", turn1Content)
	}

	turn2Content := mustToolResultContent(t, req.Messages[4], 0)
	if !IsTombstone(turn2Content) {
		t.Fatalf("expected turn 2 npm output to be compressed, got: %q", turn2Content)
	}

	turn3Content := mustToolResultContent(t, req.Messages[6], 0)
	if turn3Content != lsOutput {
		t.Fatalf("expected turn 3 ls output to remain unchanged\nwant: %q\ngot:  %q", lsOutput, turn3Content)
	}

	if result.Compressed != 2 {
		t.Fatalf("expected 2 compressed results, got %d", result.Compressed)
	}
	if result.SkippedFresh != 1 {
		t.Fatalf("expected 1 fresh skip, got %d", result.SkippedFresh)
	}
}

func mustToolResultContent(t *testing.T, msg messages.Message, blockIdx int) string {
	t.Helper()
	blocks, _, err := messages.ParseContent(msg.Content)
	if err != nil {
		t.Fatalf("ParseContent failed: %v", err)
	}
	if blockIdx < 0 || blockIdx >= len(blocks) {
		t.Fatalf("invalid block index %d for %d blocks", blockIdx, len(blocks))
	}
	var content string
	if err := json.Unmarshal(blocks[blockIdx].Content, &content); err != nil {
		t.Fatalf("unmarshal tool_result content: %v", err)
	}
	return content
}

func rawJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func makeGitStatusOutput() string {
	var b strings.Builder
	b.WriteString("On branch main\n")
	b.WriteString("Changes not staged for commit:\n")
	for i := 0; i < 90; i++ {
		fmt.Fprintf(&b, "\tmodified:   src/file_%d.go\n", i)
	}
	return b.String()
}

func makeNpmInstallOutput() string {
	var b strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "fetching package-%d\n", i)
	}
	b.WriteString("npm WARN deprecated package-x@1.0.0\n")
	b.WriteString("npm ERR! code ERESOLVE\n")
	b.WriteString("added 32 packages in 3s\n")
	return b.String()
}

func TestCompressSelectedTargetsOnlySpecifiedIDs(t *testing.T) {
	gitStatus := makeGitStatusOutput()
	npmOutput := makeNpmInstallOutput()

	req := &messages.Request{
		Messages: []messages.Message{
			{Role: "user", Content: rawJSON("compress selected")},
			{
				Role: "assistant",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "tool_use", ID: "t1", Name: "Bash", Input: rawJSON(map[string]any{"command": "git status"})},
				}),
			},
			{
				Role: "user",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "tool_result", ToolUseID: "t1", Content: rawJSON(gitStatus)},
				}),
			},
			{
				Role: "assistant",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "tool_use", ID: "t2", Name: "Bash", Input: rawJSON(map[string]any{"command": "npm install"})},
				}),
			},
			{
				Role: "user",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "tool_result", ToolUseID: "t2", Content: rawJSON(npmOutput)},
				}),
			},
			{Role: "assistant", Content: rawJSON("later turn")},
		},
	}

	cfg := config.Default()
	cfg.Staleness.Threshold = 1
	cfg.Bypass.ContentPatterns = nil

	result := CompressSelected(req, cfg, []string{"t1"}, nil)

	t1Content := mustToolResultContent(t, req.Messages[2], 0)
	if !IsTombstone(t1Content) {
		t.Fatalf("expected t1 to be compressed, got: %q", t1Content)
	}

	t2Content := mustToolResultContent(t, req.Messages[4], 0)
	if IsTombstone(t2Content) {
		t.Fatalf("expected t2 to remain uncompressed, got tombstone: %q", t2Content)
	}
	if t2Content != npmOutput {
		t.Fatalf("expected t2 to remain unchanged\nwant: %q\ngot:  %q", npmOutput, t2Content)
	}

	if result.Compressed != 1 {
		t.Fatalf("expected 1 compressed result, got %d", result.Compressed)
	}
}

func TestCompressSelectedAllowsFreshExplicitTarget(t *testing.T) {
	npmOutput := makeNpmInstallOutput()

	req := &messages.Request{
		Messages: []messages.Message{
			{Role: "user", Content: rawJSON("compress selected")},
			{
				Role: "assistant",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "tool_use", ID: "t1", Name: "Bash", Input: rawJSON(map[string]any{"command": "npm install"})},
				}),
			},
			{
				Role: "user",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "tool_result", ToolUseID: "t1", Content: rawJSON(npmOutput)},
				}),
			},
		},
	}

	cfg := config.Default()
	cfg.Staleness.Threshold = 10 // keep result fresh
	cfg.Bypass.ContentPatterns = nil

	result := CompressSelected(req, cfg, []string{"t1"}, nil)
	if result.Compressed != 1 {
		t.Fatalf("expected fresh explicit target to compress, got %d", result.Compressed)
	}
	content := mustToolResultContent(t, req.Messages[2], 0)
	if !IsTombstone(content) {
		t.Fatalf("expected explicit target to be compressed, got: %q", content)
	}
}

func TestCompressSelectedEmptyIDs(t *testing.T) {
	gitStatus := makeGitStatusOutput()

	req := &messages.Request{
		Messages: []messages.Message{
			{Role: "user", Content: rawJSON("compress selected")},
			{
				Role: "assistant",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "tool_use", ID: "t1", Name: "Bash", Input: rawJSON(map[string]any{"command": "git status"})},
				}),
			},
			{
				Role: "user",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "tool_result", ToolUseID: "t1", Content: rawJSON(gitStatus)},
				}),
			},
			{Role: "assistant", Content: rawJSON("later turn")},
		},
	}

	cfg := config.Default()
	cfg.Staleness.Threshold = 1
	cfg.Bypass.ContentPatterns = nil

	nilIDsResult := CompressSelected(req, cfg, nil, nil)
	if nilIDsResult.Compressed != 0 {
		t.Fatalf("expected nil IDs to compress 0 results, got %d", nilIDsResult.Compressed)
	}

	emptyIDsResult := CompressSelected(req, cfg, []string{}, nil)
	if emptyIDsResult.Compressed != 0 {
		t.Fatalf("expected empty IDs to compress 0 results, got %d", emptyIDsResult.Compressed)
	}

	content := mustToolResultContent(t, req.Messages[2], 0)
	if content != gitStatus {
		t.Fatalf("expected tool result to remain unchanged\nwant: %q\ngot:  %q", gitStatus, content)
	}
}

func TestCompressSelectedRespectsErrors(t *testing.T) {
	npmOutput := makeNpmInstallOutput()

	req := &messages.Request{
		Messages: []messages.Message{
			{Role: "user", Content: rawJSON("compress selected")},
			{
				Role: "assistant",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "tool_use", ID: "t1", Name: "Bash", Input: rawJSON(map[string]any{"command": "npm install"})},
				}),
			},
			{
				Role: "user",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "tool_result", ToolUseID: "t1", IsError: true, Content: rawJSON(npmOutput)},
				}),
			},
			{Role: "assistant", Content: rawJSON("later turn")},
		},
	}

	cfg := config.Default()
	cfg.Staleness.Threshold = 1
	cfg.Bypass.ContentPatterns = nil
	cfg.Bypass.PreserveErrors = true

	result := CompressSelected(req, cfg, []string{"t1"}, nil)

	content := mustToolResultContent(t, req.Messages[2], 0)
	if IsTombstone(content) {
		t.Fatalf("expected error tool_result to remain uncompressed, got tombstone: %q", content)
	}
	if content != npmOutput {
		t.Fatalf("expected error tool_result to remain unchanged\nwant: %q\ngot:  %q", npmOutput, content)
	}
	if result.SkippedBypass < 1 {
		t.Fatalf("expected at least 1 bypass skip, got %d", result.SkippedBypass)
	}
	if result.Compressed != 0 {
		t.Fatalf("expected 0 compressed results, got %d", result.Compressed)
	}
}

func TestCompressSelectedSkipsTombstones(t *testing.T) {
	tombstone := CreateTombstone("git_status", "already compressed", 1, 1, 1200, 200)

	req := &messages.Request{
		Messages: []messages.Message{
			{Role: "user", Content: rawJSON("compress selected")},
			{
				Role: "assistant",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "tool_use", ID: "t1", Name: "Bash", Input: rawJSON(map[string]any{"command": "git status"})},
				}),
			},
			{
				Role: "user",
				Content: rawJSON([]messages.ContentBlock{
					{Type: "tool_result", ToolUseID: "t1", Content: rawJSON(tombstone)},
				}),
			},
		},
	}

	cfg := config.Default()
	cfg.Staleness.Threshold = 0
	cfg.Bypass.ContentPatterns = nil

	result := CompressSelected(req, cfg, []string{"t1"}, nil)

	content := mustToolResultContent(t, req.Messages[2], 0)
	if content != tombstone {
		t.Fatalf("expected tombstone tool_result to remain unchanged\nwant: %q\ngot:  %q", tombstone, content)
	}
	if !IsTombstone(content) {
		t.Fatalf("expected tombstone marker to be preserved, got: %q", content)
	}
	if result.SkippedBypass < 1 {
		t.Fatalf("expected at least 1 bypass skip, got %d", result.SkippedBypass)
	}
	if result.Compressed != 0 {
		t.Fatalf("expected 0 compressed results, got %d", result.Compressed)
	}
}
