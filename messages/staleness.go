package messages

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/buildoak/wet/config"
)

// ToolResultInfo holds parsed metadata about a tool_result block.
type ToolResultInfo struct {
	ToolUseID    string
	ToolName     string
	Command      string
	FilePath     string // file/dir path for Read, Edit, Write, Grep, Glob tools
	Turn         int
	Stale        bool
	IsError      bool
	Content      string
	TokenCount   int
	MsgIdx       int
	BlockIdx     int
	ContentIsStr bool
	HasImages    bool
}

type toolUseMeta struct {
	ToolName string
	Command  string
	FilePath string
	Turn     int
}

// ClassifyStaleness extracts tool_result blocks and marks stale ones by turn distance.
func ClassifyStaleness(msgs []Message, threshold int, rules map[string]config.RuleConfig) []ToolResultInfo {
	currentTurn := 0
	for _, msg := range msgs {
		if msg.Role == "assistant" {
			currentTurn++
		}
	}

	toolUses := map[string]toolUseMeta{}
	turn := 0
	out := make([]ToolResultInfo, 0)

	for msgIdx, msg := range msgs {
		switch msg.Role {
		case "assistant":
			turn++
			blocks, _, err := ParseContent(msg.Content)
			if err != nil {
				continue
			}
			for _, block := range blocks {
				if block.Type != "tool_use" || block.ID == "" {
					continue
				}
				command := ""
				filePath := ""
				if strings.EqualFold(block.Name, "Bash") {
					command = extractCommand(block.Input)
				} else {
					filePath = extractFilePath(block.Input)
				}
				// Keep the latest metadata seen so that if the same tool_use_id is
				// reused, each tool_result maps to the nearest preceding tool_use.
				toolUses[block.ID] = toolUseMeta{
					ToolName: block.Name,
					Command:  command,
					FilePath: filePath,
					Turn:     turn,
				}
			}

		case "user":
			blocks, isString, err := ParseContent(msg.Content)
			if err != nil {
				continue
			}

			for blockIdx, block := range blocks {
				if block.Type != "tool_result" {
					continue
				}

				meta := toolUses[block.ToolUseID]
				toolFamily := ExtractToolFamily(meta.ToolName, meta.Command)

				staleAfter := threshold
				if rule, ok := rules[toolFamily]; ok && rule.StaleAfter > 0 {
					staleAfter = rule.StaleAfter
				}

				content := extractText(block.Content)
				info := ToolResultInfo{
					ToolUseID:    block.ToolUseID,
					ToolName:     meta.ToolName,
					Command:      meta.Command,
					FilePath:     meta.FilePath,
					Turn:         meta.Turn,
					Stale:        (currentTurn - meta.Turn) >= staleAfter,
					IsError:      block.IsError,
					Content:      content,
					TokenCount:   EstimateTokens(content),
					MsgIdx:       msgIdx,
					BlockIdx:     blockIdx,
					ContentIsStr: isString,
					HasImages:    hasImageBlocks(block.Content),
				}
				out = append(out, info)
			}
		}
	}

	return out
}

func extractCommand(input json.RawMessage) string {
	if len(bytes.TrimSpace(input)) == 0 {
		return ""
	}

	var payload struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return ""
	}
	return payload.Command
}

// extractFilePath extracts the file/directory path from non-Bash tool inputs.
// Different tools use different field names: Read/Edit/Write use "file_path",
// Grep/Glob use "path", and Glob also has "pattern" as a fallback.
func extractFilePath(input json.RawMessage) string {
	if len(bytes.TrimSpace(input)) == 0 {
		return ""
	}

	var payload struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		Pattern  string `json:"pattern"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return ""
	}
	if payload.FilePath != "" {
		return payload.FilePath
	}
	if payload.Path != "" {
		return payload.Path
	}
	return payload.Pattern
}

func extractText(raw json.RawMessage) string {
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 || bytes.Equal(trim, []byte("null")) {
		return ""
	}

	if trim[0] == '"' {
		var s string
		if err := json.Unmarshal(trim, &s); err == nil {
			return s
		}
		return ""
	}

	if trim[0] != '[' {
		return ""
	}

	var blocks []ContentBlock
	if err := json.Unmarshal(trim, &blocks); err != nil {
		return ""
	}

	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Text != "" {
			parts = append(parts, block.Text)
			continue
		}
		if nested := extractText(block.Content); nested != "" {
			parts = append(parts, nested)
		}
	}

	return strings.Join(parts, "\n")
}

// hasImageBlocks returns true if the raw tool_result content contains any
// image-type content blocks.  When content is a plain string (or empty/null),
// it returns false.
func hasImageBlocks(raw json.RawMessage) bool {
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 || trim[0] != '[' {
		return false
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(trim, &blocks); err != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type == "image" {
			return true
		}
	}
	return false
}

func ExtractToolFamily(toolName, command string) string {
	switch {
	case strings.EqualFold(toolName, "Bash"):
		cmd := strings.TrimSpace(strings.ToLower(command))
		switch {
		case cmd == "git" || strings.HasPrefix(cmd, "git status"):
			return "git_status"
		case strings.HasPrefix(cmd, "git log"):
			return "git_log"
		case strings.HasPrefix(cmd, "git diff"):
			return "git_diff"
		case strings.HasPrefix(cmd, "npm install") || strings.HasPrefix(cmd, "npm run"):
			return "npm"
		case strings.HasPrefix(cmd, "cargo build") || strings.HasPrefix(cmd, "cargo test"):
			return "cargo"
		case strings.HasPrefix(cmd, "pip install"):
			return "pip"
		case strings.HasPrefix(cmd, "docker logs"):
			return "docker"
		case cmd == "ls" || strings.HasPrefix(cmd, "ls ") || cmd == "find" || strings.HasPrefix(cmd, "find "):
			return "ls_find"
		case cmd == "make" || strings.HasPrefix(cmd, "make ") || cmd == "cmake" || strings.HasPrefix(cmd, "cmake "):
			return "make"
		case cmd == "pytest" || strings.HasPrefix(cmd, "pytest ") || strings.HasPrefix(cmd, "python -m pytest"):
			return "pytest"
		default:
			return "bash_generic"
		}
	case strings.EqualFold(toolName, "Read"), strings.EqualFold(toolName, "Write"),
		strings.EqualFold(toolName, "Edit"):
		return "read"
	case strings.EqualFold(toolName, "Grep"):
		return "grep"
	case strings.EqualFold(toolName, "Glob"):
		return "glob"
	default:
		return "unknown"
	}
}

func EstimateTokens(s string) int {
	return len(s) * 10 / 33
}
