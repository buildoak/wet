package persist

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type SessionHeader struct {
	V       int    `json:"v"`
	Type    string `json:"type"`    // always "header"
	Session string `json:"session"` // session UUID
	Created string `json:"created"` // UTC timestamp RFC3339
	Model   string `json:"model"`   // from first request
	Mode    string `json:"mode"`    // "passthrough" or "auto"
}

type TurnItem struct {
	ID         string `json:"id"`   // tool_use_id
	Tool       string `json:"tool"` // tool name
	Cmd        string `json:"cmd"`  // command (first 100 chars)
	OrigChars  int    `json:"orig_chars"`
	TombChars  int    `json:"tomb_chars"`
	CharsSaved int    `json:"chars_saved"`
	Tombstone  string `json:"tombstone"` // replacement text
	Preview    string `json:"preview"`   // first 200 chars of original
}

type TurnRecord struct {
	Type           string      `json:"type"` // always "turn"
	Turn           int         `json:"turn"`
	Ts             string      `json:"ts"` // UTC timestamp
	Usage          UsageRecord `json:"usage"`
	TotalContext   int         `json:"total_context"`    // input + cache_read + cache_creation
	CharsSaved     int         `json:"chars_saved"`      // sum of items
	TokensSavedEst int         `json:"tokens_saved_est"` // chars_saved / 3.3
	Items          []TurnItem  `json:"items"`
}

type UsageRecord struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

func sessionJSONLPath(key string) string {
	return filepath.Join(sessionDir(key), "session.jsonl")
}

// EnsureHeader writes the session header as line 0 of session.jsonl if the file doesn't exist.
func (s *Store) EnsureHeader(header SessionHeader) error {
	if s == nil {
		return nil
	}

	path := sessionJSONLPath(s.sessionKey)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}

	if header.Type == "" {
		header.Type = "header"
	}
	if header.V == 0 {
		header.V = 1
	}

	line, err := json.Marshal(header)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	if _, err := f.Write(line); err != nil {
		return err
	}
	return f.Sync()
}

// AppendTurn appends a TurnRecord as a JSON line to session.jsonl.
func (s *Store) AppendTurn(rec TurnRecord) error {
	if s == nil {
		return nil
	}

	path := sessionJSONLPath(s.sessionKey)

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}

	if rec.Type == "" {
		rec.Type = "turn"
	}

	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Write(line); err != nil {
		return err
	}
	return f.Sync()
}

// ReadSession reads back the header and all turn records from session.jsonl.
func (s *Store) ReadSession() (*SessionHeader, []TurnRecord, error) {
	if s == nil {
		return nil, nil, nil
	}

	path := sessionJSONLPath(s.sessionKey)

	s.mu.RLock()
	data, err := os.ReadFile(path)
	s.mu.RUnlock()
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	lines := bytes.Split(data, []byte{'\n'})
	nonEmpty := make([][]byte, 0, len(lines))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		nonEmpty = append(nonEmpty, line)
	}

	if len(nonEmpty) == 0 {
		return nil, nil, nil
	}

	var header SessionHeader
	if err := json.Unmarshal(nonEmpty[0], &header); err != nil {
		return nil, nil, err
	}
	if header.Type != "header" {
		return nil, nil, fmt.Errorf("invalid session header type: %q", header.Type)
	}

	turns := make([]TurnRecord, 0, len(nonEmpty)-1)
	for i := 1; i < len(nonEmpty); i++ {
		var rec TurnRecord
		if err := json.Unmarshal(nonEmpty[i], &rec); err != nil {
			return &header, turns, err
		}
		if rec.Type != "turn" {
			return &header, turns, fmt.Errorf("invalid turn record type on line %d: %q", i+1, rec.Type)
		}
		turns = append(turns, rec)
	}

	return &header, turns, nil
}

// LastTurnTotalContext returns the total_context value from the most recent
// turn record in session.jsonl. Returns 0 if there are no turns or on error.
// This is used to hydrate the statusline on resume so it shows context fill
// immediately rather than waiting for the first API round-trip.
func (s *Store) LastTurnTotalContext() int {
	if s == nil {
		return 0
	}
	_, turns, err := s.ReadSession()
	if err != nil || len(turns) == 0 {
		return 0
	}
	return turns[len(turns)-1].TotalContext
}
