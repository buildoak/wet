package compressor

import (
	"fmt"
	"strings"
)

// Compress mirrors the Rust compressor pipeline.
func Compress(toolName, command, output string) (string, bool) {
	originalLines := len(strings.Split(output, "\n"))
	originalTokens := EstimateTokens(output)

	if !strings.EqualFold(toolName, "Bash") {
		if compressed, ok := CompressReadOutput(output); ok {
			return AppendFooter(compressed, originalLines, originalTokens), true
		}
	}

	if originalTokens < 500 {
		return "", false
	}

	family := extractFamily(command)
	if compressed, ok := Tier1Compress(family, output); ok {
		if originalTokens > 5000 && EstimateTokens(compressed) > 1500 {
			compressed = HardCapCompress(output, originalLines, originalTokens)
		}
		return AppendFooter(compressed, originalLines, originalTokens), true
	}

	if originalTokens > 1500 {
		compressed := GenericSignalCompress(output)
		if originalTokens > 5000 && EstimateTokens(compressed) > 1500 {
			compressed = HardCapCompress(output, originalLines, originalTokens)
		}
		return AppendFooter(compressed, originalLines, originalTokens), true
	}

	return "", false
}

func extractFamily(command string) string {
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
}

func AppendFooter(compressed string, originalLines, originalTokens int) string {
	compressed = strings.TrimSpace(compressed)
	compressedTokens := EstimateTokens(compressed)
	return fmt.Sprintf("%s\n\n[Compressed by hook: %d lines / ~%d tok -> ~%d tok]", compressed, originalLines, originalTokens, compressedTokens)
}
