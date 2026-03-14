package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buildoak/wet/config"
	"github.com/buildoak/wet/messages"
)

// TombstoneRecord logs a single compression event.
type TombstoneRecord struct {
	ToolUseID        string    `json:"tool_use_id"`
	Family           string    `json:"family"`
	OriginalTokens   int       `json:"original_tokens"`
	CompressedTokens int       `json:"compressed_tokens"`
	Turn             int       `json:"turn"`
	Timestamp        time.Time `json:"timestamp"`
}

// StatusSnapshot holds a point-in-time view of session stats.
type StatusSnapshot struct {
	Requests        int64 `json:"requests"`
	Compressed      int64 `json:"compressed"`
	TokensSaved     int64 `json:"tokens_saved"`
	APIInputTokens  int64 `json:"api_input_tokens"`
	APIOutputTokens int64 `json:"api_output_tokens"`
	APICacheCreate  int64 `json:"api_cache_creation_input_tokens"`
	APICacheRead    int64 `json:"api_cache_read_input_tokens"`
	ContextWindow   int   `json:"context_window,omitempty"`
}

// controlState holds mutable state for the control plane.
// It is embedded in Server.
type controlState struct {
	paused     atomic.Bool
	mu         sync.RWMutex
	tombstones []TombstoneRecord

	// Agent-directed compression state
	lastToolResults []messages.ToolResultInfo // stored after every request
	compressIDs     []string                  // IDs queued for selective compression
	compressReplace map[string]string         // optional pre-computed replacement text per ID

	// Main session tracking: only store tool results for the session that
	// started the proxy (the first request through it). Subagent requests
	// share the same proxy but carry different system prompts and must not
	// overwrite the main session's tool-result state.
	mainSessionHash string // sha256[:8] of first 500 chars of system prompt; empty = not yet set

	// Debug state: populated on every request for the /_wet/debug/sessions endpoint.
	lastRequestHash          string // hash computed from the most recent request
	lastRequestSystemPreview string // first 500 chars of system prompt from most recent request
	lastRequestIsMain        bool   // whether the last request was classified as main
	totalMain                atomic.Int64
	totalSubagent            atomic.Int64

	controlLn net.Listener

	// cumulative counters
	totalCompressed atomic.Int64
	totalSaved      atomic.Int64
}

func (s *Server) StatusSnapshot() StatusSnapshot {
	apiInput, apiOutput, apiCacheCreate, apiCacheRead := s.sessionStats.APIUsageTotals()
	return StatusSnapshot{
		Requests:        s.requestCount.Load(),
		Compressed:      s.ctrl.totalCompressed.Load(),
		TokensSaved:     s.ctrl.totalSaved.Load(),
		APIInputTokens:  apiInput,
		APIOutputTokens: apiOutput,
		APICacheCreate:  apiCacheCreate,
		APICacheRead:    apiCacheRead,
		ContextWindow:   s.sessionStats.GetContextWindow(),
	}
}

func (s *Server) IsPaused() bool {
	return s.ctrl.paused.Load()
}

func (s *Server) RecordTombstone(rec TombstoneRecord) {
	s.ctrl.mu.Lock()
	defer s.ctrl.mu.Unlock()
	s.ctrl.tombstones = append(s.ctrl.tombstones, rec)
}

// HashSystemPrompt computes a short fingerprint for a system-prompt string.
// It hashes the first 500 characters to keep costs constant on long prompts.
func HashSystemPrompt(system string) string {
	runes := []rune(system)
	if len(runes) > 500 {
		runes = runes[:500]
	}
	sum := sha256.Sum256([]byte(string(runes)))
	return hex.EncodeToString(sum[:4]) // 8 hex chars
}

// TagOrMatchMainSession checks whether systemHash belongs to the main session.
//
//   - If systemHash is empty (absent or too-short system prompt), the request
//     is treated as main but the hash is NOT stored — we wait for the first
//     request with a substantial system prompt to lock in the fingerprint.
//   - If no main session is recorded yet, the hash is stored and true returned.
//   - Otherwise true is returned only if the hash matches the stored one.
//
// Thread-safe.
func (s *Server) TagOrMatchMainSession(systemHash string) bool {
	// An empty hash means the system prompt was absent or too short to be a
	// reliable fingerprint.  Let the request through as main without locking
	// in the hash so a later request with a real system prompt can claim it.
	if systemHash == "" {
		s.ctrl.mu.RLock()
		locked := s.ctrl.mainSessionHash != ""
		s.ctrl.mu.RUnlock()
		if locked {
			// A real fingerprint is already set; empty-system requests are
			// ambiguous — treat as main (conservative: avoids dropping
			// legitimate main-session requests with no system prompt).
			return true
		}
		return true
	}

	// Fast path: read-lock to check without writing.
	s.ctrl.mu.RLock()
	current := s.ctrl.mainSessionHash
	s.ctrl.mu.RUnlock()

	if current != "" {
		return current == systemHash
	}

	// Slow path: upgrade to write-lock and set if still empty.
	s.ctrl.mu.Lock()
	defer s.ctrl.mu.Unlock()
	if s.ctrl.mainSessionHash == "" {
		s.ctrl.mainSessionHash = systemHash
		return true
	}
	// Another goroutine raced us; check again.
	return s.ctrl.mainSessionHash == systemHash
}

func (s *Server) StoreToolResults(results []messages.ToolResultInfo) {
	s.ctrl.mu.Lock()
	defer s.ctrl.mu.Unlock()
	// Copy the slice so the stored data is fully owned by controlState
	// and cannot be corrupted if the caller mutates the original slice.
	copied := make([]messages.ToolResultInfo, len(results))
	copy(copied, results)
	s.ctrl.lastToolResults = copied
}

func (s *Server) GetToolResults() []messages.ToolResultInfo {
	s.ctrl.mu.RLock()
	defer s.ctrl.mu.RUnlock()
	out := make([]messages.ToolResultInfo, len(s.ctrl.lastToolResults))
	copy(out, s.ctrl.lastToolResults)
	return out
}

func (s *Server) QueueCompressIDs(ids []string) {
	s.ctrl.mu.Lock()
	defer s.ctrl.mu.Unlock()
	s.ctrl.compressIDs = append(s.ctrl.compressIDs, ids...)
}

// QueueCompressWithText queues IDs for selective compression with optional
// pre-computed replacement text. IDs present in replacements skip Tier 1
// compression and use the provided text directly as the tombstone summary.
func (s *Server) QueueCompressWithText(ids []string, replacements map[string]string) {
	s.ctrl.mu.Lock()
	defer s.ctrl.mu.Unlock()
	s.ctrl.compressIDs = append(s.ctrl.compressIDs, ids...)
	if len(replacements) > 0 {
		if s.ctrl.compressReplace == nil {
			s.ctrl.compressReplace = make(map[string]string, len(replacements))
		}
		for k, v := range replacements {
			s.ctrl.compressReplace[k] = v
		}
	}
}

func (s *Server) DrainCompressIDs() []string {
	s.ctrl.mu.Lock()
	defer s.ctrl.mu.Unlock()
	ids := s.ctrl.compressIDs
	s.ctrl.compressIDs = nil
	s.ctrl.compressReplace = nil
	return ids
}

// DrainCompressState returns queued IDs and their optional replacement text,
// clearing both from the control state. IDs without an entry in the
// replacements map fall through to normal Tier 1 compression.
func (s *Server) DrainCompressState() ([]string, map[string]string) {
	s.ctrl.mu.Lock()
	defer s.ctrl.mu.Unlock()
	ids := s.ctrl.compressIDs
	rep := s.ctrl.compressReplace
	s.ctrl.compressIDs = nil
	s.ctrl.compressReplace = nil
	return ids, rep
}

func (s *Server) AddCompressedStats(compressed int, tokensSaved int) {
	s.ctrl.totalCompressed.Add(int64(compressed))
	s.ctrl.totalSaved.Add(int64(tokensSaved))
}

func (s *Server) recordAPIUsage(usage UsageData) {
	s.sessionStats.RecordAPIUsage(
		usage.InputTokens,
		usage.OutputTokens,
		usage.CacheCreationInputTokens,
		usage.CacheReadInputTokens,
	)
	_ = s.sessionStats.WriteStatsFile()
}

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

func (s *Server) StartControlPlane() error {
	sockDir := expandHome("~/.wet")
	if err := os.MkdirAll(sockDir, 0o755); err != nil {
		return fmt.Errorf("create ~/.wet: %w", err)
	}

	sockPath := filepath.Join(sockDir, fmt.Sprintf("wet-%d.sock", s.cfg.Server.Port))
	// Remove stale socket
	_ = os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", sockPath, err)
	}
	s.ctrl.controlLn = ln

	go s.acceptControlConnections(ln)
	return nil
}

func (s *Server) acceptControlConnections(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed
		}
		go s.handleControlConnection(conn)
	}
}

func (s *Server) handleControlConnection(conn net.Conn) {
	defer conn.Close()

	type inspectResultView struct {
		ToolUseID      string `json:"tool_use_id"`
		ToolName       string `json:"tool_name"`
		Command        string `json:"command,omitempty"`
		Turn           int    `json:"turn"`
		CurrentTurn    int    `json:"current_turn"`
		Stale          bool   `json:"stale"`
		IsError        bool   `json:"is_error"`
		TokenCount     int    `json:"token_count"`
		ContentPreview string `json:"content_preview"`
		MsgIdx         int    `json:"msg_idx"`
		BlockIdx       int    `json:"block_idx"`
	}

	previewContent := func(content string, maxLen int) string {
		if maxLen <= 0 {
			return ""
		}
		runes := []rune(content)
		if len(runes) <= maxLen {
			return content
		}
		return string(runes[:maxLen])
	}

	var req map[string]any
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		writeJSON(conn, map[string]any{
			"error":   "invalid_request",
			"message": "invalid JSON request body",
		})
		return
	}

	command, _ := req["command"].(string)
	if command == "" {
		writeJSON(conn, map[string]any{
			"error":   "missing_command",
			"message": "command is required",
		})
		return
	}

	switch command {
	case "status":
		snap := s.StatusSnapshot()
		writeJSON(conn, map[string]any{
			"uptime_seconds":    time.Since(s.startTime).Seconds(),
			"requests":          snap.Requests,
			"compressed":        snap.Compressed,
			"tokens_saved":      snap.TokensSaved,
			"api_input_tokens":  snap.APIInputTokens,
			"api_output_tokens": snap.APIOutputTokens,
			"paused":            s.ctrl.paused.Load(),
		})

	case "inspect":
		s.ctrl.mu.RLock()
		tombstones := make([]TombstoneRecord, len(s.ctrl.tombstones))
		copy(tombstones, s.ctrl.tombstones)
		s.ctrl.mu.RUnlock()
		writeJSON(conn, tombstones)

	case "inspect_results":
		results := s.GetToolResults()
		currentTurn := 0
		for _, result := range results {
			if result.Turn > currentTurn {
				currentTurn = result.Turn
			}
		}

		view := make([]inspectResultView, 0, len(results))
		for _, result := range results {
			view = append(view, inspectResultView{
				ToolUseID:      result.ToolUseID,
				ToolName:       result.ToolName,
				Command:        result.Command,
				Turn:           result.Turn,
				CurrentTurn:    currentTurn,
				Stale:          result.Stale,
				IsError:        result.IsError,
				TokenCount:     result.TokenCount,
				ContentPreview: previewContent(result.Content, 200),
				MsgIdx:         result.MsgIdx,
				BlockIdx:       result.BlockIdx,
			})
		}
		writeJSON(conn, view)

	case "compress":
		rawIDs, ok := req["ids"].([]any)
		if !ok {
			writeJSON(conn, map[string]any{
				"error":   "invalid_ids",
				"message": "ids must be an array",
			})
			return
		}

		ids := make([]string, 0, len(rawIDs))
		for _, id := range rawIDs {
			if id == nil {
				continue
			}
			if strID, ok := id.(string); ok {
				if strID != "" {
					ids = append(ids, strID)
				}
				continue
			}
			strID := fmt.Sprint(id)
			if strID != "" {
				ids = append(ids, strID)
			}
		}
		if len(ids) == 0 {
			writeJSON(conn, map[string]any{
				"error":   "empty_ids",
				"message": "ids must contain at least one non-empty ID",
			})
			return
		}

		s.QueueCompressIDs(ids)
		writeJSON(conn, map[string]any{
			"status": "queued",
			"count":  len(ids),
			"ids":    ids,
		})

	case "rules_list":
		s.ctrl.mu.RLock()
		rules := s.cfg.Rules
		s.ctrl.mu.RUnlock()
		writeJSON(conn, rules)

	case "rules_set":
		key, _ := req["key"].(string)
		value, _ := req["value"].(string)
		if key == "" {
			writeJSON(conn, map[string]any{
				"error":   "missing_key",
				"message": "key is required",
			})
			return
		}
		s.ctrl.mu.Lock()
		if s.cfg.Rules == nil {
			s.cfg.Rules = make(map[string]config.RuleConfig)
		}
		rule := s.cfg.Rules[key]
		// Try to set stale_after if value is numeric
		var n int
		if _, err := fmt.Sscanf(value, "%d", &n); err == nil {
			rule.StaleAfter = n
		} else {
			rule.Strategy = value
		}
		s.cfg.Rules[key] = rule
		s.ctrl.mu.Unlock()
		writeJSON(conn, map[string]any{"status": "ok"})

	case "pause":
		s.ctrl.paused.Store(true)
		writeJSON(conn, map[string]any{"status": "paused"})

	case "resume":
		s.ctrl.paused.Store(false)
		writeJSON(conn, map[string]any{"status": "resumed"})

	default:
		writeJSON(conn, map[string]any{
			"error":   "unknown_command",
			"message": "unknown command",
		})
	}
}

func (s *Server) CleanupControlPlane() {
	if s.ctrl.controlLn != nil {
		s.ctrl.controlLn.Close()
		sockPath := expandHome(fmt.Sprintf("~/.wet/wet-%d.sock", s.cfg.Server.Port))
		_ = os.Remove(sockPath)
	}
}

func writeJSON(conn net.Conn, v any) {
	_ = json.NewEncoder(conn).Encode(v)
}
