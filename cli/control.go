package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
)

func expandHome(p string) string {
	if len(p) > 0 && p[0] == '~' {
		u, err := user.Current()
		if err != nil {
			return p
		}
		return filepath.Join(u.HomeDir, p[1:])
	}
	return p
}

// resolvePort determines which proxy port to talk to.
// Priority: explicit port > WET_PORT env var.
// Returns 0 if neither is set.
var overridePort int

func SetPort(port int) {
	overridePort = port
}

// GetPort returns the override port, or 0 if not set.
func GetPort() int {
	return overridePort
}

func resolvePort() (int, error) {
	if overridePort > 0 {
		return overridePort, nil
	}
	if env := os.Getenv("WET_PORT"); env != "" {
		p, err := strconv.Atoi(env)
		if err != nil {
			return 0, fmt.Errorf("invalid WET_PORT=%q: %w", env, err)
		}
		return p, nil
	}
	return 0, fmt.Errorf("no proxy port specified: set WET_PORT or use --port flag")
}

func baseURL() (string, error) {
	port, err := resolvePort()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port), nil
}

// httpGet issues a GET to /_wet/{path} and returns the body.
func httpGet(path string) ([]byte, error) {
	base, err := baseURL()
	if err != nil {
		return nil, err
	}
	resp, err := http.Get(base + "/_wet/" + path)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to wet proxy (is it running?): %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return body, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// httpPost issues a POST to /_wet/{path} with a JSON body.
func httpPost(path string, payload any) ([]byte, error) {
	base, err := baseURL()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return nil, err
	}
	resp, err := http.Post(base+"/_wet/"+path, "application/json", &buf)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to wet proxy (is it running?): %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return body, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func prettyPrint(data []byte) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		fmt.Println(string(data))
		return
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(out))
}

func RunStatus() error {
	data, err := httpGet("status")
	if err != nil {
		return err
	}
	prettyPrint(data)
	return nil
}

func RunInspect() error {
	data, err := httpGet("inspect")
	if err != nil {
		return err
	}
	prettyPrint(data)
	return nil
}

func RunInspectResults(format string) error {
	data, err := httpGet("inspect")
	if err != nil {
		return err
	}

	if strings.EqualFold(format, "table") {
		var entries []struct {
			ToolUseID   string `json:"tool_use_id"`
			ToolName    string `json:"tool_name"`
			Command     string `json:"command"`
			Turn        int    `json:"turn"`
			CurrentTurn int    `json:"current_turn"`
			Stale       bool   `json:"stale"`
			IsError     bool   `json:"is_error"`
			TokenCount  int    `json:"token_count"`
		}
		if err := json.Unmarshal(data, &entries); err != nil {
			return fmt.Errorf("decode inspect response: %w", err)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TOOL_USE_ID\tTOOL\tCOMMAND\tTURN\tCUR\tSTALE\tERROR\tTOKENS")
		for _, entry := range entries {
			cmd := strings.TrimSpace(strings.ReplaceAll(entry.Command, "\n", " "))
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%t\t%t\t%d\n",
				entry.ToolUseID, entry.ToolName, cmd, entry.Turn, entry.CurrentTurn, entry.Stale, entry.IsError, entry.TokenCount)
		}
		_ = w.Flush()
		return nil
	}

	prettyPrint(data)
	return nil
}

// ensurePort resolves the port via explicit flag, env var, or fleet discovery.
// All control-plane commands should call this before issuing HTTP requests.
func ensurePort() error {
	port, err := resolvePortOrDiscover()
	if err != nil {
		return err
	}
	SetPort(port)
	return nil
}

func RunRulesList() error {
	if err := ensurePort(); err != nil {
		return err
	}
	data, err := httpGet("rules")
	if err != nil {
		return err
	}
	prettyPrint(data)
	return nil
}

func RunRulesSet(key, value string) error {
	if err := ensurePort(); err != nil {
		return err
	}
	payload := map[string]any{"key": key}
	// Try to parse value as int for stale_after
	if n, err := strconv.Atoi(value); err == nil {
		payload["stale_after"] = n
	} else {
		payload["strategy"] = value
	}
	data, err := httpPost("rules", payload)
	if err != nil {
		return err
	}
	prettyPrint(data)
	return nil
}

func RunPause() error {
	if err := ensurePort(); err != nil {
		return err
	}
	_, err := httpPost("pause", nil)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "[wet] compression paused")
	return nil
}

func RunResume() error {
	if err := ensurePort(); err != nil {
		return err
	}
	_, err := httpPost("resume", nil)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "[wet] compression resumed")
	return nil
}
