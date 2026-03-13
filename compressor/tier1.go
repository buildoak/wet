package compressor

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Pre-compiled regexes (package-level vars)
var (
	wordCountRe  = regexp.MustCompile(`(?i)\b\d+\s+(passed|failed|error|warning|test|tests|file|files)\b`)
	testRunnerRe = regexp.MustCompile(`\b(PASS|FAIL|ok|not ok)\b`)
	signalRe     = regexp.MustCompile(`(?i)(error|enoent|fatal|panic|warning|warn|fail|reject|denied|refused|forbidden)`)
	diffAlertRe  = regexp.MustCompile(`(?i)^[+-].*(error|warn)`)
	diffStatRe   = regexp.MustCompile(`(\|\s*\d+)|((files?|insertions?|deletions?)\b)`)
	cargoErrorRe = regexp.MustCompile(`error\[E\d+\]`)
	cargoWarnRe  = regexp.MustCompile(`warning(\[|:)`)
)

func EstimateTokens(s string) int { return len(s) * 10 / 33 }

// Tier1Compress attempts pattern-based compression for known tool families.
// Returns compressed output and true if compression applied, or empty string and false.
func Tier1Compress(family, output string) (string, bool) {
	switch family {
	case "git_status":
		return CompressGitStatus(output)
	case "git_log":
		return CompressGitLog(output)
	case "git_diff":
		return CompressGitDiff(output)
	case "npm":
		return CompressNpm(output)
	case "cargo":
		return CompressCargo(output)
	case "pip":
		return CompressPip(output)
	case "docker":
		return CompressDockerLogs(output)
	case "ls_find":
		return CompressLsFind(output)
	case "make":
		return CompressMake(output)
	case "pytest":
		return CompressPytest(output)
	default:
		return "", false
	}
}

func CompressGitStatus(output string) (string, bool) {
	lines := strings.Split(output, "\n")
	summary := ""
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(line, "On branch") || strings.HasPrefix(line, "HEAD detached") {
			summary = trim
			break
		}
		if summary == "" && trim != "" {
			summary = trim
		}
	}

	if summary == "" {
		return "", false
	}

	changed := make([]string, 0)
	for _, line := range lines {
		if strings.HasPrefix(line, "\t") ||
			strings.Contains(line, "modified:") ||
			strings.Contains(line, "new file:") ||
			strings.Contains(line, "deleted:") ||
			strings.Contains(line, "renamed:") ||
			strings.Contains(line, "both modified:") {
			trim := strings.TrimSpace(line)
			if trim != "" {
				changed = append(changed, trim)
			}
		}
	}

	var b strings.Builder
	b.WriteString(summary)
	b.WriteString("\n")
	fmt.Fprintf(&b, "Changed files: %d", len(changed))
	limit := min(20, len(changed))
	for i := 0; i < limit; i++ {
		b.WriteString("\n")
		b.WriteString(changed[i])
	}
	if len(changed) > 20 {
		fmt.Fprintf(&b, "\n[... %d more files]", len(changed)-20)
	}

	return strings.TrimSpace(b.String()), true
}

func CompressGitLog(output string) (string, bool) {
	lines := strings.Split(output, "\n")
	kept := make([]string, 0)
	commitCount := 0

	for i := 0; i < len(lines) && commitCount < 15; i++ {
		line := lines[i]
		if !strings.HasPrefix(line, "commit ") {
			continue
		}

		kept = append(kept, strings.TrimSpace(line))
		commitCount++

		for j := i + 1; j < len(lines); j++ {
			rawNext := lines[j]
			next := strings.TrimSpace(rawNext)
			if next == "" || strings.HasPrefix(next, "Author:") || strings.HasPrefix(next, "Date:") {
				continue
			}
			if strings.HasPrefix(rawNext, "commit ") {
				break
			}
			kept = append(kept, "    "+next)
			break
		}
	}

	if commitCount == 0 {
		return "", false
	}

	return strings.Join(kept, "\n"), true
}

func CompressGitDiff(output string) (string, bool) {
	lines := strings.Split(output, "\n")
	keepIdx := make(map[int]struct{})
	hunkLineCount := 0

	for i, line := range lines {
		if diffStatRe.MatchString(line) {
			keepIdx[i] = struct{}{}
		}
		if strings.HasPrefix(line, "diff --git") || strings.HasPrefix(line, "@@") {
			keepIdx[i] = struct{}{}
		}
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			if hunkLineCount < 30 {
				keepIdx[i] = struct{}{}
				hunkLineCount++
			}
		}
		if diffAlertRe.MatchString(line) {
			keepIdx[i] = struct{}{}
		}
	}

	if len(keepIdx) == 0 {
		return "", false
	}

	idx := make([]int, 0, len(keepIdx))
	for i := range keepIdx {
		idx = append(idx, i)
	}
	sort.Ints(idx)

	kept := make([]string, 0, len(idx))
	for _, i := range idx {
		kept = append(kept, lines[i])
	}

	return strings.TrimSpace(strings.Join(kept, "\n")), true
}

func CompressNpm(output string) (string, bool) {
	lines := strings.Split(output, "\n")
	keepIdx := make(map[int]struct{})

	for i, line := range lines {
		if strings.Contains(line, "ERR!") || strings.Contains(line, "WARN") {
			keepIdx[i] = struct{}{}
		}
	}

	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			keepIdx[i] = struct{}{}
			break
		}
	}

	if len(keepIdx) == 0 {
		return "", false
	}

	return joinBySortedIdx(lines, keepIdx), true
}

func CompressCargo(output string) (string, bool) {
	lines := strings.Split(output, "\n")
	keepIdx := make(map[int]struct{})
	compilingKept := 0

	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "Compiling") && compilingKept < 5 {
			keepIdx[i] = struct{}{}
			compilingKept++
		}
		if cargoErrorRe.MatchString(line) ||
			cargoWarnRe.MatchString(line) ||
			strings.Contains(line, "test result:") ||
			strings.HasPrefix(trim, "Finished") ||
			strings.HasPrefix(trim, "Running") {
			keepIdx[i] = struct{}{}
		}
	}

	if len(keepIdx) == 0 {
		return "", false
	}

	return joinBySortedIdx(lines, keepIdx), true
}

func CompressPip(output string) (string, bool) {
	lines := strings.Split(output, "\n")
	keepIdx := make(map[int]struct{})

	for i, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(line, "Successfully installed") || strings.Contains(lower, "error") || strings.Contains(lower, "failed") {
			keepIdx[i] = struct{}{}
		}
	}

	if len(keepIdx) == 0 {
		return "", false
	}

	return joinBySortedIdx(lines, keepIdx), true
}

func CompressDockerLogs(output string) (string, bool) {
	lines := strings.Split(output, "\n")
	keepIdx := make(map[int]struct{})

	tailStart := max(0, len(lines)-20)
	for i := tailStart; i < len(lines); i++ {
		keepIdx[i] = struct{}{}
	}

	for i, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "error") || strings.Contains(lower, "warn") || strings.Contains(lower, "fatal") {
			keepIdx[i] = struct{}{}
		}
	}

	return joinBySortedIdx(lines, keepIdx), true
}

func CompressLsFind(output string) (string, bool) {
	lines := strings.Split(output, "\n")
	limit := min(30, len(lines))
	kept := make([]string, 0, limit+2)
	kept = append(kept, lines[:limit]...)

	if len(lines) > 30 {
		kept = append(kept, "")
		kept = append(kept, fmt.Sprintf("[... %d total entries]", len(lines)))
	}

	return strings.Join(kept, "\n"), true
}

func CompressMake(output string) (string, bool) {
	lines := strings.Split(output, "\n")
	keepIdx := make(map[int]struct{})

	for i, line := range lines {
		if strings.Contains(strings.ToLower(line), "error") {
			keepIdx[i] = struct{}{}
		}
	}

	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			keepIdx[i] = struct{}{}
			break
		}
	}

	if len(keepIdx) == 0 {
		return "", false
	}

	return joinBySortedIdx(lines, keepIdx), true
}

func CompressPytest(output string) (string, bool) {
	lines := strings.Split(output, "\n")
	keepIdx := make(map[int]struct{})

	for i, line := range lines {
		lower := strings.ToLower(line)
		if testRunnerRe.MatchString(line) || wordCountRe.MatchString(line) || strings.Contains(lower, "failed") || strings.Contains(lower, "error") || strings.Contains(lower, "warning") {
			keepIdx[i] = struct{}{}
		}
	}

	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			keepIdx[i] = struct{}{}
			break
		}
	}

	if len(keepIdx) == 0 {
		return "", false
	}
	return joinBySortedIdx(lines, keepIdx), true
}

func CompressReadOutput(output string) (string, bool) {
	if EstimateTokens(output) <= 1000 {
		return "", false
	}

	lines := strings.Split(output, "\n")
	limit := min(100, len(lines))
	kept := make([]string, 0, limit+2)
	kept = append(kept, lines[:limit]...)
	kept = append(kept, "")
	kept = append(kept, fmt.Sprintf("[... truncated read output: %d lines / ~%d tok]", len(lines), EstimateTokens(output)))

	return strings.Join(kept, "\n"), true
}

func joinBySortedIdx(lines []string, keepIdx map[int]struct{}) string {
	idx := make([]int, 0, len(keepIdx))
	for i := range keepIdx {
		idx = append(idx, i)
	}
	sort.Ints(idx)

	kept := make([]string, 0, len(idx))
	for _, i := range idx {
		kept = append(kept, lines[i])
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
