package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const statuslineScriptPath = ".claude/statusline.sh"

const wetBeginMarker = "# BEGIN_WET_STATUSLINE"
const wetEndMarker = "# END_WET_STATUSLINE"

// wetStatuslineBlock is the wet section that gets injected into statusline.sh.
// It reads stats files from ~/.wet/ and appends compression info to the statusline.
// The script expects BLUE and RESET color variables to be defined above.
const wetStatuslineBlock = `# BEGIN_WET_STATUSLINE
WET_SECTION=""
WET_DIR="$HOME/.wet"
WET_STATS=""

# Find stats file: per-port first, then most recent stats-*.json, then legacy stats.json
if [ -n "${WET_PORT:-}" ] && [ -f "$WET_DIR/stats-${WET_PORT}.json" ]; then
    WET_STATS="$WET_DIR/stats-${WET_PORT}.json"
elif [ -d "$WET_DIR" ]; then
    WET_STATS=$(ls -t "$WET_DIR"/stats-*.json 2>/dev/null | head -1)
    if [ -z "$WET_STATS" ] && [ -f "$WET_DIR/stats.json" ]; then
        WET_STATS="$WET_DIR/stats.json"
    fi
fi

if [ -n "$WET_STATS" ] && [ -f "$WET_STATS" ]; then
    WET_DATA=$(jq -r '[
        .session_requests // 0,
        .session_tokens_saved // 0,
        .session_items_compressed // 0,
        .session_items_total // 0,
        .context_window // 0,
        .latest_input_tokens // 0,
        .session_tokens_before // 0,
        .session_tokens_after // 0
    ] | @tsv' "$WET_STATS" 2>/dev/null) || WET_DATA=""

    if [ -n "$WET_DATA" ]; then
        IFS=$'\t' read -r W_REQS W_SAVED W_COMP W_TOTAL W_CTX W_INPUT W_BEFORE W_AFTER <<< "$WET_DATA"

        if [ "${W_REQS:-0}" -eq 0 ] && [ "${W_SAVED:-0}" -eq 0 ] && [ "${W_COMP:-0}" -eq 0 ]; then
            WET_SECTION=" | ${BLUE}wet: ready${RESET}"
        else
            WET_PARTS=""

            if [ "${W_CTX:-0}" -gt 0 ] && [ "${W_INPUT:-0}" -gt 0 ]; then
                W_PCT=$((W_INPUT * 100 / W_CTX))
                if [ "$W_INPUT" -ge 1000000 ]; then
                    W_INPUT_FMT=$(printf "%.1fM" "$(echo "$W_INPUT / 1000000" | bc -l)")
                elif [ "$W_INPUT" -ge 1000 ]; then
                    W_INPUT_FMT=$(printf "%.1fk" "$(echo "$W_INPUT / 1000" | bc -l)")
                else
                    W_INPUT_FMT="$W_INPUT"
                fi
                if [ "$W_CTX" -ge 1000000 ]; then
                    W_CTX_FMT=$(printf "%.1fM" "$(echo "$W_CTX / 1000000" | bc -l)")
                elif [ "$W_CTX" -ge 1000 ]; then
                    W_CTX_FMT=$(printf "%.1fk" "$(echo "$W_CTX / 1000" | bc -l)")
                else
                    W_CTX_FMT="$W_CTX"
                fi
                WET_PARTS="${W_PCT}% (${W_INPUT_FMT}/${W_CTX_FMT})"
            fi

            if [ "${W_TOTAL:-0}" -gt 0 ]; then
                COMP_PART="${W_COMP}/${W_TOTAL} compressed"
                if [ "${W_BEFORE:-0}" -gt 0 ] && [ "${W_AFTER:-0}" -gt 0 ]; then
                    if [ "$W_BEFORE" -ge 1000000 ]; then
                        W_BEFORE_FMT=$(printf "%.1fM" "$(echo "$W_BEFORE / 1000000" | bc -l)")
                    elif [ "$W_BEFORE" -ge 1000 ]; then
                        W_BEFORE_FMT=$(printf "%.1fk" "$(echo "$W_BEFORE / 1000" | bc -l)")
                    else
                        W_BEFORE_FMT="$W_BEFORE"
                    fi
                    if [ "$W_AFTER" -ge 1000000 ]; then
                        W_AFTER_FMT=$(printf "%.1fM" "$(echo "$W_AFTER / 1000000" | bc -l)")
                    elif [ "$W_AFTER" -ge 1000 ]; then
                        W_AFTER_FMT=$(printf "%.1fk" "$(echo "$W_AFTER / 1000" | bc -l)")
                    else
                        W_AFTER_FMT="$W_AFTER"
                    fi
                    COMP_PART="${COMP_PART} (${W_BEFORE_FMT}->${W_AFTER_FMT})"
                fi
                if [ -n "$WET_PARTS" ]; then
                    WET_PARTS="${WET_PARTS} | ${COMP_PART}"
                else
                    WET_PARTS="$COMP_PART"
                fi
            fi

            if [ -n "$WET_PARTS" ]; then
                WET_SECTION=" | ${BLUE}wet: ${WET_PARTS}${RESET}"
            fi
        fi
    fi
fi
# END_WET_STATUSLINE`

// baseStatuslineScript is installed when no statusline.sh exists yet.
// It provides the base Claude Code status (model + context) plus the wet section.
const baseStatuslineScript = `#!/bin/bash
# Claude Code composable statusline
# Base: model + context usage from Claude Code stdin JSON
# Optional: wet compression stats (appended when active)

set -euo pipefail

# --- Read Claude Code JSON from stdin ---
input=$(cat)

MODEL=$(echo "$input" | jq -r '.model.display_name // "unknown"')
USED_PCT=$(echo "$input" | jq -r '.context_window.used_percentage // 0' | cut -d. -f1)
CONTEXT_SIZE=$(echo "$input" | jq -r '.context_window.context_window_size // 200000')

# Calculate tokens used from percentage
USED_TOKENS=$(( ${USED_PCT:-0} * ${CONTEXT_SIZE:-200000} / 100 ))
USED_K=$((USED_TOKENS / 1000))
TOTAL_K=$((CONTEXT_SIZE / 1000))

# --- Colors ---
GREEN='\033[32m'
YELLOW='\033[33m'
RED='\033[31m'
BLUE='\033[38;5;39m'
DIM='\033[38;5;245m'
RESET='\033[0m'

if [ "${USED_PCT:-0}" -ge 80 ]; then
    CTX_COLOR="$RED"
elif [ "${USED_PCT:-0}" -ge 50 ]; then
    CTX_COLOR="$YELLOW"
else
    CTX_COLOR="$GREEN"
fi

# --- Base statusline ---
BASE=$(printf "${DIM}[%s]${RESET} ${CTX_COLOR}(%dk/%dk)${RESET}" "$MODEL" "$USED_K" "$TOTAL_K")

` + wetStatuslineBlock + `

# --- Output ---
printf '%b' "${BASE}${WET_SECTION}\n"
`

// RunInstallStatusline injects the wet section into ~/.claude/statusline.sh
// and ensures settings.json points to the shell script.
func RunInstallStatusline() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}

	scriptPath := filepath.Join(home, statuslineScriptPath)
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	// Ensure ~/.claude/ directory exists
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	// Read existing script or create new one
	scriptContent, err := os.ReadFile(scriptPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read %s: %w", scriptPath, err)
		}
		// No script exists -- write the full base + wet script
		if err := os.WriteFile(scriptPath, []byte(baseStatuslineScript), 0o755); err != nil {
			return fmt.Errorf("write %s: %w", scriptPath, err)
		}
		fmt.Fprintf(os.Stderr, "[wet] created %s with base statusline + wet section\n", scriptPath)
	} else {
		// Script exists -- check if wet section is already present
		content := string(scriptContent)
		if strings.Contains(content, wetBeginMarker) {
			// Replace existing wet section
			content, err = replaceSection(content, wetBeginMarker, wetEndMarker, wetStatuslineBlock)
			if err != nil {
				return fmt.Errorf("update wet section: %w", err)
			}
			if err := os.WriteFile(scriptPath, []byte(content), 0o755); err != nil {
				return fmt.Errorf("write %s: %w", scriptPath, err)
			}
			fmt.Fprintf(os.Stderr, "[wet] updated wet section in %s\n", scriptPath)
		} else {
			// Inject wet section before the last line (output line)
			// Find a good insertion point: before the final printf/echo output
			content = injectWetSection(content, wetStatuslineBlock)
			if err := os.WriteFile(scriptPath, []byte(content), 0o755); err != nil {
				return fmt.Errorf("write %s: %w", scriptPath, err)
			}
			fmt.Fprintf(os.Stderr, "[wet] injected wet section into %s\n", scriptPath)
		}
	}

	// Ensure settings.json points to the shell script
	if err := ensureSettingsPointToScript(settingsPath, scriptPath); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "      Usage: run `wet claude` instead of `claude` to start with the proxy.\n")
	fmt.Fprintf(os.Stderr, "      The status bar will show live compression savings.\n")
	return nil
}

// RunUninstallStatusline removes the wet section from ~/.claude/statusline.sh.
// Does NOT remove the statusLine setting from settings.json (leaves the base statusline intact).
func RunUninstallStatusline() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}

	scriptPath := filepath.Join(home, statuslineScriptPath)

	scriptContent, err := os.ReadFile(scriptPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "[wet] no statusline.sh found, nothing to uninstall")
			return nil
		}
		return fmt.Errorf("read %s: %w", scriptPath, err)
	}

	content := string(scriptContent)
	if !strings.Contains(content, wetBeginMarker) {
		fmt.Fprintln(os.Stderr, "[wet] no wet section found in statusline.sh, nothing to uninstall")
		return nil
	}

	// Remove the wet section (including markers)
	content, err = replaceSection(content, wetBeginMarker, wetEndMarker, "")
	if err != nil {
		return fmt.Errorf("remove wet section: %w", err)
	}

	// Clean up: replace WET_SECTION references in the output line
	// Replace ${WET_SECTION} with empty in the output
	content = strings.ReplaceAll(content, "${WET_SECTION}", "")

	if err := os.WriteFile(scriptPath, []byte(content), 0o755); err != nil {
		return fmt.Errorf("write %s: %w", scriptPath, err)
	}

	fmt.Fprintf(os.Stderr, "[wet] removed wet section from %s\n", scriptPath)
	fmt.Fprintln(os.Stderr, "      base statusline preserved")
	return nil
}

// ensureSettingsPointToScript makes sure settings.json statusLine points to the shell script.
func ensureSettingsPointToScript(settingsPath, scriptPath string) error {
	settings, err := readJSONFile(settingsPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", settingsPath, err)
	}
	if settings == nil {
		settings = make(map[string]any)
	}

	// Check current statusLine config
	needsUpdate := true
	if existing, ok := settings["statusLine"]; ok {
		existingMap, isMap := existing.(map[string]any)
		if isMap {
			if cmd, ok := existingMap["command"].(string); ok && cmd == scriptPath {
				needsUpdate = false
			}
		}
	}

	if needsUpdate {
		settings["statusLine"] = map[string]any{
			"type":    "command",
			"command": scriptPath,
		}
		if err := writeJSONFile(settingsPath, settings); err != nil {
			return fmt.Errorf("write %s: %w", settingsPath, err)
		}
		fmt.Fprintf(os.Stderr, "[wet] settings.json statusLine -> %s\n", scriptPath)
	}

	return nil
}

// replaceSection replaces content between beginMarker and endMarker (inclusive) with replacement.
// If replacement is empty, the section (and surrounding blank lines) are removed.
func replaceSection(content, beginMarker, endMarker, replacement string) (string, error) {
	beginIdx := strings.Index(content, beginMarker)
	if beginIdx == -1 {
		return content, fmt.Errorf("begin marker not found: %s", beginMarker)
	}

	endIdx := strings.Index(content, endMarker)
	if endIdx == -1 {
		return content, fmt.Errorf("end marker not found: %s", endMarker)
	}

	// Include the end marker line
	endIdx = endIdx + len(endMarker)
	// Also consume the newline after end marker if present
	if endIdx < len(content) && content[endIdx] == '\n' {
		endIdx++
	}

	if replacement == "" {
		// Remove: also clean up extra blank lines
		result := content[:beginIdx] + content[endIdx:]
		// Clean up double blank lines
		for strings.Contains(result, "\n\n\n") {
			result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
		}
		return result, nil
	}

	return content[:beginIdx] + replacement + "\n" + content[endIdx:], nil
}

// injectWetSection inserts the wet block into an existing statusline script.
// It looks for the output line (printf or echo at end) and inserts before it.
func injectWetSection(content, wetBlock string) string {
	lines := strings.Split(content, "\n")

	// Find the last printf/echo line (the output line)
	outputIdx := -1
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "printf") || strings.HasPrefix(trimmed, "echo") {
			outputIdx = i
			break
		}
	}

	if outputIdx == -1 {
		// No output line found -- append before end
		return content + "\n" + wetBlock + "\n"
	}

	// Insert the wet block before the output line
	var result []string
	result = append(result, lines[:outputIdx]...)
	result = append(result, wetBlock)
	result = append(result, lines[outputIdx:]...)
	return strings.Join(result, "\n")
}

// claudeSettingsPath returns the path to Claude Code's settings.json.
func claudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}

	return filepath.Join(dir, "settings.json"), nil
}

// findWetBinary returns the absolute path to the wet binary.
func findWetBinary() (string, error) {
	// Try the currently running binary
	exe, err := os.Executable()
	if err == nil {
		resolved, err := filepath.EvalSymlinks(exe)
		if err == nil {
			return resolved, nil
		}
		return exe, nil
	}

	// Fall back to PATH lookup
	path, err := exec.LookPath("wet")
	if err == nil {
		return path, nil
	}

	return "wet", nil // bare name, hope it's in PATH
}

// readJSONFile reads a JSON file into a map.
func readJSONFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	return result, nil
}

// writeJSONFile writes a map to a JSON file with indentation.
func writeJSONFile(path string, data map[string]any) error {
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return os.WriteFile(path, out, 0o644)
}
