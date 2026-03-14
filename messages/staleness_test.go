package messages

import (
	"encoding/json"
	"testing"

	"github.com/buildoak/wet/config"
)

func TestClassifyStalenessEmptyMessages(t *testing.T) {
	got := ClassifyStaleness(nil, 2, nil)
	if len(got) != 0 {
		t.Fatalf("expected empty result, got %d", len(got))
	}
}

func TestClassifyStalenessSingleTurnFresh(t *testing.T) {
	msgs := []Message{
		assistantToolUse("t1", "Bash", "git status"),
		userToolResult("t1", "clean"),
	}

	got := ClassifyStaleness(msgs, 2, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0].Stale {
		t.Fatal("expected tool_result to be fresh")
	}
	if got[0].Turn != 1 {
		t.Fatalf("unexpected turn: %d", got[0].Turn)
	}
}

func TestClassifyStalenessTurnOneStaleAtThresholdTwo(t *testing.T) {
	msgs := []Message{
		assistantToolUse("t1", "Bash", "git status"),
		userToolResult("t1", "out"),
		assistantText("later turn 2"),
		assistantText("later turn 3"),
	}

	got := ClassifyStaleness(msgs, 2, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if !got[0].Stale {
		t.Fatal("expected stale result (distance 2)")
	}
}

func TestClassifyStalenessTurnTwoFreshAtThresholdTwo(t *testing.T) {
	msgs := []Message{
		assistantText("turn 1"),
		assistantToolUse("t2", "Bash", "git diff"),
		userToolResult("t2", "diff"),
		assistantText("turn 3"),
	}

	got := ClassifyStaleness(msgs, 2, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0].Stale {
		t.Fatal("expected fresh result (distance 1)")
	}
	if got[0].Turn != 2 {
		t.Fatalf("expected turn 2, got %d", got[0].Turn)
	}
}

func TestClassifyStalenessRuleOverride(t *testing.T) {
	msgs := []Message{
		assistantToolUse("t1", "Bash", "git status"),
		userToolResult("t1", "out"),
		assistantText("turn 2"),
	}
	rules := map[string]config.RuleConfig{
		"git_status": {StaleAfter: 1},
	}

	got := ClassifyStaleness(msgs, 2, rules)
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if !got[0].Stale {
		t.Fatal("expected stale due to stale_after=1 override")
	}
}

func TestClassifyStalenessMultipleToolResultsInOneMessage(t *testing.T) {
	msgs := []Message{
		{
			Role: "assistant",
			Content: rawValue([]ContentBlock{
				{
					Type:  "tool_use",
					ID:    "t1",
					Name:  "Read",
					Input: rawValue(map[string]any{"path": "a.go"}),
				},
				{
					Type:  "tool_use",
					ID:    "t2",
					Name:  "Bash",
					Input: rawValue(map[string]any{"command": "ls -la"}),
				},
			}),
		},
		{
			Role: "user",
			Content: rawValue([]ContentBlock{
				{
					Type:      "tool_result",
					ToolUseID: "t1",
					Content:   rawValue("read output"),
				},
				{
					Type:      "tool_result",
					ToolUseID: "t2",
					Content:   rawValue("ls output"),
				},
			}),
		},
	}

	got := ClassifyStaleness(msgs, 2, nil)
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
	if got[0].BlockIdx != 0 || got[1].BlockIdx != 1 {
		t.Fatalf("unexpected block indexes: %+v", got)
	}
	if got[0].ToolName != "Read" || got[1].ToolName != "Bash" {
		t.Fatalf("unexpected tool names: %+v", got)
	}
	// Read tool should have FilePath extracted from input "path" field
	if got[0].FilePath != "a.go" {
		t.Fatalf("expected Read FilePath='a.go', got %q", got[0].FilePath)
	}
	// Bash tool should have empty FilePath
	if got[1].FilePath != "" {
		t.Fatalf("expected Bash FilePath='', got %q", got[1].FilePath)
	}
}

func TestClassifyStalenessDuplicateToolUseIDUsesNearestPrior(t *testing.T) {
	msgs := []Message{
		assistantToolUse("dup", "Bash", "git status"),
		userToolResult("dup", "first output"),
		assistantToolUse("dup", "Bash", "npm install"),
		userToolResult("dup", "second output"),
		assistantText("turn 3"),
	}

	got := ClassifyStaleness(msgs, 2, nil)
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}

	if got[0].Command != "git status" || got[0].Turn != 1 {
		t.Fatalf("first result mapped to wrong tool_use: %+v", got[0])
	}
	if got[1].Command != "npm install" || got[1].Turn != 2 {
		t.Fatalf("second result mapped to wrong tool_use: %+v", got[1])
	}
}

func TestFilePathExtraction(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		input    map[string]any
		wantPath string
	}{
		{
			name:     "Read with file_path",
			toolName: "Read",
			input:    map[string]any{"file_path": "/src/main.go"},
			wantPath: "/src/main.go",
		},
		{
			name:     "Edit with file_path",
			toolName: "Edit",
			input:    map[string]any{"file_path": "/src/main.go", "old_string": "a", "new_string": "b"},
			wantPath: "/src/main.go",
		},
		{
			name:     "Write with file_path",
			toolName: "Write",
			input:    map[string]any{"file_path": "/tmp/out.txt", "content": "hello"},
			wantPath: "/tmp/out.txt",
		},
		{
			name:     "Grep with path",
			toolName: "Grep",
			input:    map[string]any{"pattern": "TODO", "path": "/src"},
			wantPath: "/src",
		},
		{
			name:     "Glob with path",
			toolName: "Glob",
			input:    map[string]any{"pattern": "**/*.go", "path": "/src"},
			wantPath: "/src",
		},
		{
			name:     "Glob with pattern only (no path)",
			toolName: "Glob",
			input:    map[string]any{"pattern": "**/*.go"},
			wantPath: "**/*.go",
		},
		{
			name:     "Bash has no file_path",
			toolName: "Bash",
			input:    map[string]any{"command": "cat foo.go"},
			wantPath: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := []Message{
				{
					Role: "assistant",
					Content: rawValue([]ContentBlock{
						{
							Type:  "tool_use",
							ID:    "t1",
							Name:  tt.toolName,
							Input: rawValue(tt.input),
						},
					}),
				},
				userToolResult("t1", "output"),
			}

			got := ClassifyStaleness(msgs, 2, nil)
			if len(got) != 1 {
				t.Fatalf("expected 1 result, got %d", len(got))
			}
			if got[0].FilePath != tt.wantPath {
				t.Fatalf("FilePath = %q, want %q", got[0].FilePath, tt.wantPath)
			}
		})
	}
}

func TestExtractToolFamily(t *testing.T) {
	tests := []struct {
		name    string
		tool    string
		command string
		want    string
	}{
		{name: "git status", tool: "Bash", command: "git status", want: "git_status"},
		{name: "git", tool: "Bash", command: "git", want: "git_status"},
		{name: "git log", tool: "Bash", command: "git log --oneline", want: "git_log"},
		{name: "git diff", tool: "Bash", command: "git diff HEAD~1", want: "git_diff"},
		{name: "npm install", tool: "Bash", command: "npm install", want: "npm"},
		{name: "npm run", tool: "Bash", command: "npm run test", want: "npm"},
		{name: "cargo build", tool: "Bash", command: "cargo build", want: "cargo"},
		{name: "cargo test", tool: "Bash", command: "cargo test --all", want: "cargo"},
		{name: "pip install", tool: "Bash", command: "pip install x", want: "pip"},
		{name: "docker logs", tool: "Bash", command: "docker logs app", want: "docker"},
		{name: "ls", tool: "Bash", command: "ls -la", want: "ls_find"},
		{name: "find", tool: "Bash", command: "find . -name '*.go'", want: "ls_find"},
		{name: "make", tool: "Bash", command: "make test", want: "make"},
		{name: "cmake", tool: "Bash", command: "cmake .", want: "make"},
		{name: "pytest", tool: "Bash", command: "pytest -q", want: "pytest"},
		{name: "python -m pytest", tool: "Bash", command: "python -m pytest tests", want: "pytest"},
		{name: "bash generic", tool: "Bash", command: "echo hi", want: "bash_generic"},
		{name: "read", tool: "Read", command: "", want: "read"},
		{name: "write", tool: "Write", command: "", want: "read"},
		{name: "edit", tool: "Edit", command: "", want: "read"},
		{name: "grep", tool: "Grep", command: "", want: "grep"},
		{name: "glob", tool: "Glob", command: "", want: "glob"},
		{name: "unknown", tool: "Search", command: "", want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractToolFamily(tt.tool, tt.command)
			if got != tt.want {
				t.Fatalf("ExtractToolFamily(%q, %q) = %q, want %q", tt.tool, tt.command, got, tt.want)
			}
		})
	}
}

func TestHasImageBlocksStringContent(t *testing.T) {
	raw := rawValue("just a string")
	if hasImageBlocks(raw) {
		t.Fatal("expected false for string content")
	}
}

func TestHasImageBlocksTextOnly(t *testing.T) {
	raw := rawValue([]ContentBlock{{Type: "text", Text: "hello"}})
	if hasImageBlocks(raw) {
		t.Fatal("expected false for text-only blocks")
	}
}

func TestHasImageBlocksWithImage(t *testing.T) {
	raw := rawValue([]map[string]any{
		{"type": "text", "text": "before"},
		{"type": "image", "source": map[string]any{"type": "base64", "data": "abc"}},
	})
	if !hasImageBlocks(raw) {
		t.Fatal("expected true for content with image block")
	}
}

func TestHasImageBlocksNilContent(t *testing.T) {
	if hasImageBlocks(nil) {
		t.Fatal("expected false for nil content")
	}
}

func TestClassifyStalenessHasImages(t *testing.T) {
	imageContent := rawValue([]map[string]any{
		{"type": "text", "text": "screenshot of the page"},
		{"type": "image", "source": map[string]any{"type": "base64", "data": "AAAA"}},
	})
	msgs := []Message{
		assistantToolUse("t1", "Read", ""),
		{
			Role: "user",
			Content: rawValue([]ContentBlock{
				{
					Type:      "tool_result",
					ToolUseID: "t1",
					Content:   imageContent,
				},
			}),
		},
	}

	got := ClassifyStaleness(msgs, 2, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if !got[0].HasImages {
		t.Fatal("expected HasImages=true for tool_result with image block")
	}
}

func assistantToolUse(id, toolName, command string) Message {
	block := ContentBlock{
		Type:  "tool_use",
		ID:    id,
		Name:  toolName,
		Input: rawValue(map[string]any{}),
	}
	if command != "" {
		block.Input = rawValue(map[string]any{"command": command})
	}
	return Message{
		Role:    "assistant",
		Content: rawValue([]ContentBlock{block}),
	}
}

func assistantText(text string) Message {
	return Message{
		Role: "assistant",
		Content: rawValue([]ContentBlock{
			{Type: "text", Text: text},
		}),
	}
}

func userToolResult(toolUseID, content string) Message {
	return Message{
		Role: "user",
		Content: rawValue([]ContentBlock{
			{
				Type:      "tool_result",
				ToolUseID: toolUseID,
				Content:   rawValue(content),
			},
		}),
	}
}

func rawValue(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
