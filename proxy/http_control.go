package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/otonashi/wet/config"
)

// inspectResultView is the JSON shape returned by /_wet/inspect.
type inspectResultView struct {
	ToolUseID      string `json:"tool_use_id"`
	ToolName       string `json:"tool_name"`
	Command        string `json:"command,omitempty"`
	FilePath       string `json:"file_path,omitempty"`
	Turn           int    `json:"turn"`
	CurrentTurn    int    `json:"current_turn"`
	Stale          bool   `json:"stale"`
	IsError        bool   `json:"is_error"`
	HasImages      bool   `json:"has_images"`
	TokenCount     int    `json:"token_count"`
	ContentPreview string `json:"content_preview"`
	Content        string `json:"content,omitempty"`
	MsgIdx         int    `json:"msg_idx"`
	BlockIdx       int    `json:"block_idx"`
}

// RegisterHTTPControl adds /_wet/* routes to the given mux.
// Called during server construction so the control plane shares the proxy port.
func (s *Server) RegisterHTTPControl(mux *http.ServeMux) {
	mux.HandleFunc("/_wet/status", s.httpStatus)
	mux.HandleFunc("/_wet/inspect", s.httpInspect)
	mux.HandleFunc("/_wet/compress", s.httpCompress)
	mux.HandleFunc("/_wet/pause", s.httpPause)
	mux.HandleFunc("/_wet/resume", s.httpResume)
	mux.HandleFunc("/_wet/rules", s.httpRules)
	mux.HandleFunc("/_wet/debug/sessions", s.httpDebugSessions)
}

func (s *Server) httpStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use GET")
		return
	}
	snap := s.StatusSnapshot()

	// Compute derived fields
	compressionRatio := 0.0
	tokensBefore := s.sessionStats.TotalTokensBefore()
	if tokensBefore > 0 {
		compressionRatio = 1.0 - float64(tokensBefore-snap.TokensSaved)/float64(tokensBefore)
	}

	writeHTTPJSON(w, http.StatusOK, map[string]any{
		"uptime_seconds":            time.Since(s.startTime).Seconds(),
		"request_count":             snap.Requests,
		"tokens_saved":              snap.TokensSaved,
		"compression_ratio":         compressionRatio,
		"items_compressed":          snap.Compressed,
		"items_total":               s.sessionStats.TotalItems(),
		"api_input_tokens":          snap.APIInputTokens,
		"api_output_tokens":         snap.APIOutputTokens,
		"context_window":            snap.ContextWindow,
		"latest_input_tokens":       s.sessionStats.GetLatestAPIInputTokens(),
		"latest_total_input_tokens": s.sessionStats.GetLatestAPITotalInputTokens(),
		"paused":                    s.ctrl.paused.Load(),
		"mode":                      s.Mode(),
	})
}

func (s *Server) httpInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use GET")
		return
	}

	full := r.URL.Query().Get("full") == "1"

	results := s.GetToolResults()
	currentTurn := 0
	for _, result := range results {
		if result.Turn > currentTurn {
			currentTurn = result.Turn
		}
	}

	view := make([]inspectResultView, 0, len(results))
	for _, result := range results {
		rv := inspectResultView{
			ToolUseID:      result.ToolUseID,
			ToolName:       result.ToolName,
			Command:        result.Command,
			FilePath:       result.FilePath,
			Turn:           result.Turn,
			CurrentTurn:    currentTurn,
			Stale:          result.Stale,
			IsError:        result.IsError,
			HasImages:      result.HasImages,
			TokenCount:     result.TokenCount,
			ContentPreview: previewContent(result.Content, 200),
			MsgIdx:         result.MsgIdx,
			BlockIdx:       result.BlockIdx,
		}
		if full {
			rv.Content = result.Content
		}
		view = append(view, rv)
	}
	writeHTTPJSON(w, http.StatusOK, view)
}

func (s *Server) httpCompress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use POST")
		return
	}

	var body struct {
		IDs             []string          `json:"ids"`
		ReplacementText map[string]string `json:"replacement_text,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeHTTPError(w, http.StatusBadRequest, "INVALID_JSON", "invalid JSON body")
		return
	}

	// Filter empty strings
	ids := make([]string, 0, len(body.IDs))
	for _, id := range body.IDs {
		if id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		writeHTTPError(w, http.StatusBadRequest, "EMPTY_IDS", "ids must contain at least one non-empty ID")
		return
	}

	// Validate: Agent/Task tool results require replacement_text (LLM rewrite).
	// Bash/Read/Grep/etc without replacement_text are fine (Tier 1 handles them).
	if len(body.ReplacementText) == 0 || len(body.ReplacementText) < len(ids) {
		results := s.GetToolResults()
		toolNameByID := make(map[string]string, len(results))
		for _, result := range results {
			toolNameByID[result.ToolUseID] = result.ToolName
		}

		for _, id := range ids {
			toolName := toolNameByID[id]
			if strings.EqualFold(toolName, "Agent") || strings.EqualFold(toolName, "Task") {
				if _, hasReplacement := body.ReplacementText[id]; !hasReplacement {
					writeHTTPError(w, http.StatusBadRequest, "AGENT_REQUIRES_REPLACEMENT",
						fmt.Sprintf("tool result %s is an %s result which requires replacement_text (LLM rewrite). "+
							"Tier 1 mechanical compression cannot adequately summarize agent/task output. "+
							"Provide replacement_text for this ID.", id, toolName))
					return
				}
			}
		}
	}

	if len(body.ReplacementText) > 0 {
		s.QueueCompressWithText(ids, body.ReplacementText)
	} else {
		s.QueueCompressIDs(ids)
	}
	writeHTTPJSON(w, http.StatusOK, map[string]any{
		"status": "queued",
		"count":  len(ids),
		"ids":    ids,
	})
}

func (s *Server) httpPause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use POST")
		return
	}
	s.ctrl.paused.Store(true)
	writeHTTPJSON(w, http.StatusOK, map[string]any{"status": "paused"})
}

func (s *Server) httpResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use POST")
		return
	}
	s.ctrl.paused.Store(false)
	writeHTTPJSON(w, http.StatusOK, map[string]any{"status": "resumed"})
}

func (s *Server) httpRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.ctrl.mu.RLock()
		rules := s.cfg.Rules
		s.ctrl.mu.RUnlock()
		writeHTTPJSON(w, http.StatusOK, rules)

	case http.MethodPost:
		var body struct {
			Key        string `json:"key"`
			StaleAfter *int   `json:"stale_after,omitempty"`
			Strategy   string `json:"strategy,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeHTTPError(w, http.StatusBadRequest, "INVALID_JSON", "invalid JSON body")
			return
		}
		if body.Key == "" {
			writeHTTPError(w, http.StatusBadRequest, "MISSING_KEY", "key is required")
			return
		}

		s.ctrl.mu.Lock()
		if s.cfg.Rules == nil {
			s.cfg.Rules = make(map[string]config.RuleConfig)
		}
		rule := s.cfg.Rules[body.Key]
		if body.StaleAfter != nil {
			rule.StaleAfter = *body.StaleAfter
		}
		if body.Strategy != "" {
			rule.Strategy = body.Strategy
		}
		s.cfg.Rules[body.Key] = rule
		s.ctrl.mu.Unlock()
		writeHTTPJSON(w, http.StatusOK, map[string]any{"status": "ok"})

	default:
		writeHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use GET or POST")
	}
}

func (s *Server) httpDebugSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use GET")
		return
	}

	s.ctrl.mu.RLock()
	mainHash := s.ctrl.mainSessionHash
	lastHash := s.ctrl.lastRequestHash
	preview := s.ctrl.lastRequestSystemPreview
	lastIsMain := s.ctrl.lastRequestIsMain
	s.ctrl.mu.RUnlock()

	totalRequests := s.requestCount.Load()
	totalMain := s.ctrl.totalMain.Load()
	totalSubagent := s.ctrl.totalSubagent.Load()

	writeHTTPJSON(w, http.StatusOK, map[string]any{
		"main_session_hash":            mainHash,
		"last_request_hash":            lastHash,
		"hashes_match":                 mainHash != "" && lastHash != "" && mainHash == lastHash,
		"last_request_system_preview":  preview,
		"last_request_classified_as":   classifyLabel(lastIsMain),
		"total_requests":               totalRequests,
		"total_classified_as_main":     totalMain,
		"total_classified_as_subagent": totalSubagent,
	})
}

func classifyLabel(isMain bool) string {
	if isMain {
		return "main"
	}
	return "subagent"
}

// previewContent truncates content to maxLen runes.
func previewContent(content string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(content)
	if len(runes) <= maxLen {
		return content
	}
	return string(runes[:maxLen])
}

func writeHTTPJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeHTTPError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": message,
		"code":  code,
	})
}

// Port returns the configured port. Useful for CLI commands to discover the control plane.
func (s *Server) Port() int {
	return s.cfg.Server.Port
}

// Mode returns the configured operating mode.
func (s *Server) Mode() string {
	mode := s.cfg.Server.Mode
	if mode == "" {
		return "auto"
	}
	return mode
}

// previewContent in control.go's handleControlConnection used a local closure.
// The HTTP handlers above use the package-level previewContent function.
// The old socket handler still works with its local closure for backward compat.

// Uptime returns the duration since server start.
func (s *Server) Uptime() time.Duration {
	return time.Since(s.startTime)
}

// SessionStatsJSON returns a StatusSnapshot enriched with extra fields for the HTTP status endpoint.
func (s *Server) SessionStatsJSON() map[string]any {
	snap := s.StatusSnapshot()

	compressionRatio := 0.0
	tokensBefore := s.sessionStats.TotalTokensBefore()
	if tokensBefore > 0 {
		compressionRatio = 1.0 - float64(tokensBefore-snap.TokensSaved)/float64(tokensBefore)
	}

	return map[string]any{
		"uptime_seconds":            s.Uptime().Seconds(),
		"request_count":             snap.Requests,
		"compressed":                snap.Compressed,
		"tokens_saved":              snap.TokensSaved,
		"compression_ratio":         compressionRatio,
		"items_compressed":          snap.Compressed,
		"items_total":               s.sessionStats.TotalItems(),
		"api_input_tokens":          snap.APIInputTokens,
		"api_output_tokens":         snap.APIOutputTokens,
		"context_window":            snap.ContextWindow,
		"latest_input_tokens":       s.sessionStats.GetLatestAPIInputTokens(),
		"latest_total_input_tokens": s.sessionStats.GetLatestAPITotalInputTokens(),
		"paused":                    s.ctrl.paused.Load(),
		"mode":                      s.Mode(),
	}
}

// formatHTTPError builds a structured error string for agent consumption.
func formatHTTPError(code, message string) string {
	return fmt.Sprintf("%s: %s", code, message)
}
