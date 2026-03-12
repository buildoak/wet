package compressor

import (
	"fmt"
	"sort"
	"strings"
)

// GenericSignalCompress applies tier-2 signal extraction.
func GenericSignalCompress(output string) string {
	lines := strings.Split(output, "\n")
	keepIdx := make(map[int]struct{})

	for i := 0; i < min(15, len(lines)); i++ {
		keepIdx[i] = struct{}{}
	}

	tailStart := max(0, len(lines)-10)
	for i := tailStart; i < len(lines); i++ {
		keepIdx[i] = struct{}{}
	}

	for i, line := range lines {
		if isSignalLine(line) {
			keepIdx[i] = struct{}{}
		}
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

	kept = dedupSimilar(kept)
	return strings.Join(kept, "\n")
}

func isSignalLine(line string) bool {
	if signalRe.MatchString(line) || testRunnerRe.MatchString(line) || wordCountRe.MatchString(line) {
		return true
	}
	if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, ">") {
		return true
	}
	if strings.Contains(line, "Caused by:") || strings.Contains(line, "Traceback") || strings.Contains(line, `  File "`) {
		return true
	}

	lower := strings.ToLower(line)
	return strings.Contains(lower, "at ") ||
		strings.Contains(lower, "exit code") ||
		strings.Contains(lower, "status:") ||
		strings.Contains(lower, "returned") ||
		strings.Contains(lower, "exited")
}

func normalizeForSimilarity(line string) string {
	trim := strings.TrimSpace(line)
	var b strings.Builder
	b.Grow(len(trim))
	for i := 0; i < len(trim); i++ {
		ch := trim[i]
		if ch >= '0' && ch <= '9' {
			b.WriteByte('#')
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}

func dedupSimilar(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}

	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		norm := normalizeForSimilarity(lines[i])
		j := i + 1
		for j < len(lines) && normalizeForSimilarity(lines[j]) == norm {
			j++
		}

		runLen := j - i
		if runLen >= 3 {
			out = append(out, lines[i])
			out = append(out, fmt.Sprintf("[... repeated %d similar lines ...]", runLen-1))
		} else {
			out = append(out, lines[i:j]...)
		}
		i = j
	}
	return out
}

// HardCapCompress applies tier-3 hard cap compression.
func HardCapCompress(output string, originalLines, originalTokens int) string {
	lines := strings.Split(output, "\n")
	headEnd := min(30, len(lines))
	kept := make([]string, 0, headEnd+12)
	kept = append(kept, lines[:headEnd]...)

	tailStart := len(lines) - 10
	if tailStart > 30 {
		kept = append(kept, "[... hard-capped output ...]")
		kept = append(kept, lines[tailStart:]...)
	}

	result := strings.Join(kept, "\n")
	return result + fmt.Sprintf("\n[... original %d lines / ~%d tok ...]", originalLines, originalTokens)
}
