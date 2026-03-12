package cli

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// RunSessionSalt generates a random salt string and prints it to stdout.
// Format: WET_SALT_ + 16 hex characters (8 random bytes).
func RunSessionSalt() error {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("crypto/rand: %w", err)
	}
	fmt.Printf("WET_SALT_%s\n", hex.EncodeToString(b))
	return nil
}

// RunSessionFind searches all .jsonl files under ~/.claude/projects/ for the
// given salt string. On first match it prints JSON with session_id and
// jsonl_path to stdout and exits 0. On no match it prints an error JSON to
// stderr and exits 1.
func RunSessionFind(salt string) error {
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}
	searchRoot := filepath.Join(u.HomeDir, ".claude", "projects")

	var found bool
	err = filepath.Walk(searchRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable dirs
		}
		if found {
			return filepath.SkipAll
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".jsonl") {
			return nil
		}
		ok, scanErr := fileContains(path, salt)
		if scanErr != nil {
			return nil // skip unreadable files
		}
		if ok {
			found = true
			sessionID := strings.TrimSuffix(info.Name(), ".jsonl")
			result := map[string]string{
				"session_id": sessionID,
				"jsonl_path": path,
			}
			out, _ := json.Marshal(result)
			fmt.Println(string(out))
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk %s: %w", searchRoot, err)
	}

	if !found {
		errJSON := map[string]string{
			"error": "no session found",
			"code":  "NOT_FOUND",
		}
		out, _ := json.Marshal(errJSON)
		fmt.Fprintln(os.Stderr, string(out))
		return &exitError{code: 1}
	}
	return nil
}

// fileContains scans a file line by line and returns true on first line
// containing substr.
func fileContains(path, substr string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Default 64KB buffer; bump to 1MB for long JSONL lines.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), substr) {
			return true, nil
		}
	}
	return false, scanner.Err()
}

// exitError lets callers specify a process exit code.
type exitError struct {
	code int
}

func (e *exitError) Error() string   { return fmt.Sprintf("exit %d", e.code) }
func (e *exitError) ExitCode() int   { return e.code }
