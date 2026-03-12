package compressor

import (
	"fmt"
	"strings"
	"testing"
)

func TestPassThroughSmallOutput(t *testing.T) {
	got, ok := Compress("Bash", "git status", "small output")
	if ok {
		t.Fatalf("expected no compression, got ok=true with output %q", got)
	}
	if got != "" {
		t.Fatalf("expected empty output, got %q", got)
	}
}

func TestGitStatusCompression(t *testing.T) {
	var b strings.Builder
	b.WriteString("On branch main\nChanges not staged for commit:\n")
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&b, "\tmodified:   src/file_%d.rs\n", i)
	}

	got, ok := Compress("Bash", "git status", b.String())
	if !ok {
		t.Fatalf("expected compression to apply")
	}
	mustContain(t, got, "On branch main")
	mustContain(t, got, "Changed files: 80")
	mustContain(t, got, "file_0.rs")
	mustNotContain(t, got, "file_40.rs")
	mustContain(t, got, "Compressed by hook")
}

func TestNpmInstallCompression(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "fetching package %d\n", i)
	}
	b.WriteString("npm WARN deprecated package-x@1.0.0\n")
	b.WriteString("npm ERR! code ERESOLVE\n")
	b.WriteString("added 32 packages in 3s\n")

	got, ok := Compress("Bash", "npm install", b.String())
	if !ok {
		t.Fatalf("expected compression to apply")
	}
	mustContain(t, got, "npm WARN")
	mustContain(t, got, "npm ERR!")
	mustContain(t, got, "added 32 packages")
	mustNotContain(t, got, "fetching package 12")
}

func TestCargoCompression(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&b, "Compiling crate_%d v0.1.0\n", i)
	}
	b.WriteString("warning: unused import: `foo`\n")
	b.WriteString("error[E0425]: cannot find value `x` in this scope\n")
	b.WriteString("Finished dev [unoptimized + debuginfo] target(s) in 0.11s\n")

	got, ok := Compress("Bash", "cargo build", b.String())
	if !ok {
		t.Fatalf("expected compression to apply")
	}
	mustContain(t, got, "error[E0425]")
	mustContain(t, got, "warning:")
	mustContain(t, got, "Finished dev")
	mustNotContain(t, got, "crate_299")
}

func TestGenericLargeOutput(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 900; i++ {
		fmt.Fprintf(&b, "line %d: normal output payload payload payload\n", i)
	}
	for i := 0; i < 6; i++ {
		fmt.Fprintf(&b, "ERROR connection refused attempt %d\n", i)
	}
	b.WriteString("Traceback (most recent call last):\n")
	b.WriteString("  File \"main.py\", line 10, in <module>\n")
	b.WriteString("status: exited with code 1\n")

	got, ok := Compress("Bash", "custom_command --long", b.String())
	if !ok {
		t.Fatalf("expected compression to apply")
	}
	mustContain(t, got, "ERROR connection refused")
	mustContain(t, got, "Traceback")
	mustContain(t, got, "status: exited with code 1")
	mustContain(t, got, "[... repeated")
}

func TestHardCapFallback(t *testing.T) {
	lines := make([]string, 0, 80)
	for i := 0; i < 80; i++ {
		marker := string(rune('a' + (i % 26)))
		longChunk := strings.Repeat(marker, 820)
		lines = append(lines, fmt.Sprintf("line %02d %s", i, longChunk))
	}
	input := strings.Join(lines, "\n")

	got, ok := Compress("Bash", "unknowncmd", input)
	if !ok {
		t.Fatalf("expected compression to apply")
	}
	mustContain(t, got, "[... hard-capped output ...]")
	mustContain(t, got, "original 80 lines")
	mustContain(t, got, "Compressed by hook")
}

func TestReadToolHandling(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 600; i++ {
		fmt.Fprintf(&b, "line %d long long long long long\n", i)
	}

	got, ok := Compress("Read", "", b.String())
	if !ok {
		t.Fatalf("expected compression to apply")
	}
	mustContain(t, got, "line 0")
	mustContain(t, got, "line 99")
	mustNotContain(t, got, "line 200")
	mustContain(t, got, "truncated read output")
}

func TestGitLogCompression(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 20; i++ {
		hash := fmt.Sprintf("%040x", i+1)
		fmt.Fprintf(&b, "commit %s\n", hash)
		b.WriteString("Author: Dev <dev@example.com>\n")
		fmt.Fprintf(&b, "Date:   Fri Jan %02d 10:00:00 2026 +0000\n\n", (i%28)+1)
		fmt.Fprintf(&b, "    commit message %d\n\n", i)
	}

	got, ok := Compress("Bash", "git log", b.String())
	if !ok {
		t.Fatalf("expected compression to apply")
	}
	commitCount := countLinesWithPrefix(got, "commit ")
	if commitCount > 15 {
		t.Fatalf("expected at most 15 commits, got %d", commitCount)
	}
	mustContain(t, got, "commit message 0")
	mustContain(t, got, "commit 0000000000000000000000000000000000000001")
}

func TestGitDiffCompression(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&b, "diff --git a/file_%d.txt b/file_%d.txt\n", i, i)
		b.WriteString("index 1111111..2222222 100644\n")
		fmt.Fprintf(&b, "--- a/file_%d.txt\n", i)
		fmt.Fprintf(&b, "+++ b/file_%d.txt\n", i)
		b.WriteString("@@ -1,5 +1,5 @@\n")
		for j := 0; j < 5; j++ {
			fmt.Fprintf(&b, "-old line %d-%d\n", i, j)
			fmt.Fprintf(&b, "+new line %d-%d\n", i, j)
		}
	}
	b.WriteString(" 8 files changed, 40 insertions(+), 40 deletions(-)\n")

	got, ok := Compress("Bash", "git diff", b.String())
	if !ok {
		t.Fatalf("expected compression to apply")
	}
	mustContain(t, got, "diff --git")
	mustContain(t, got, "@@")
	mustContain(t, got, "+new line")
	mustContain(t, got, "-old line")

	changedCount := 0
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			changedCount++
		}
	}
	if changedCount > 30 {
		t.Fatalf("expected at most 30 changed lines, got %d", changedCount)
	}
}

func TestPipCompression(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, "Collecting package_%d from index with long metadata line\n", i)
	}
	b.WriteString("Successfully installed foo bar\n")

	got, ok := Compress("Bash", "pip install foo", b.String())
	if !ok {
		t.Fatalf("expected compression to apply")
	}
	mustContain(t, got, "Successfully installed foo bar")
}

func TestMakeCompression(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 350; i++ {
		fmt.Fprintf(&b, "[CC] compiling source_%d.c with flags and long text\n", i)
	}
	b.WriteString("src/main.c:42:10: error: unknown type name 'widget'\n")
	b.WriteString("src/lib.c:18:5: error: implicit declaration of function 'boom'\n")
	b.WriteString("make: *** [all] Error 2\n")

	got, ok := Compress("Bash", "make", b.String())
	if !ok {
		t.Fatalf("expected compression to apply")
	}
	mustContain(t, got, "unknown type name")
	mustContain(t, got, "implicit declaration")
	mustContain(t, got, "make: *** [all] Error 2")
}

func TestDockerLogsCompression(t *testing.T) {
	lines := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		switch i {
		case 5:
			lines = append(lines, "2026-03-09T10:00:05Z ERROR upstream refused connection")
		case 40:
			lines = append(lines, "2026-03-09T10:00:40Z WARN retrying request")
		case 70:
			lines = append(lines, "2026-03-09T10:01:10Z FATAL worker crashed")
		default:
			lines = append(lines, fmt.Sprintf("2026-03-09T10:%02d:%02dZ info regular log line payload payload", i/60, i%60))
		}
	}
	input := strings.Join(lines, "\n")

	got, ok := Compress("Bash", "docker logs myapp", input)
	if !ok {
		t.Fatalf("expected compression to apply")
	}
	mustContain(t, got, "ERROR upstream refused connection")
	mustContain(t, got, "WARN retrying request")
	mustContain(t, got, "FATAL worker crashed")
	mustContain(t, got, "10:01:39Z")
	mustNotContain(t, got, "10:00:50Z")
}

func TestLsFindCompression(t *testing.T) {
	lines := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		lines = append(lines, fmt.Sprintf("-rw-r--r-- 1 user staff 12345 Mar 09 10:00 /very/long/path/component_%02d/entry_%02d_detail_name.txt", i, i))
	}
	input := strings.Join(lines, "\n")

	got, ok := Compress("Bash", "ls -la", input)
	if !ok {
		t.Fatalf("expected compression to apply")
	}
	mustContain(t, got, "entry_00")
	mustNotContain(t, got, "entry_40")
	mustContain(t, got, "[... 50 total entries]")
}

func mustContain(t *testing.T, s, want string) {
	t.Helper()
	if !strings.Contains(s, want) {
		t.Fatalf("expected output to contain %q\noutput:\n%s", want, s)
	}
}

func mustNotContain(t *testing.T, s, want string) {
	t.Helper()
	if strings.Contains(s, want) {
		t.Fatalf("expected output not to contain %q\noutput:\n%s", want, s)
	}
}

func countLinesWithPrefix(s, prefix string) int {
	count := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}
