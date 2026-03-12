package persist

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store manages persistent compression state for a single session.
type Store struct {
	mu           sync.RWMutex
	sessionKey   string
	replacements map[string]string // tool_use_id -> tombstone text
	dir          string            // ~/.wet/sessions/{key}/ (key = UUID or legacy hash)
	dirty        bool              // true if in-memory state differs from disk
}

// DirFunc allows overriding the base directory for testing.
// Default returns ~/.wet
var DirFunc = defaultDir

func defaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".wet")
	}
	return filepath.Join(home, ".wet")
}

func sessionDir(key string) string {
	return filepath.Join(DirFunc(), "sessions", key)
}

func replacementsPath(key string) string {
	return filepath.Join(sessionDir(key), "replacements.json")
}

func cumulativePath(key string) string {
	return filepath.Join(sessionDir(key), "cumulative.json")
}

// CumulativeStats holds lifetime compression totals for a session.
type CumulativeStats struct {
	TokensBefore    int64  `json:"tokens_before"`
	TokensAfter     int64  `json:"tokens_after"`
	ItemsCompressed int    `json:"items_compressed"`
	Updated         string `json:"updated"`
}

// Open loads or creates a Store for the given session key (UUID or legacy hash).
// If replacements.json exists, it is loaded. Otherwise an empty store is created.
// Returns nil if key is empty.
func Open(key string) (*Store, error) {
	if key == "" {
		return nil, nil
	}

	s := &Store{
		sessionKey:   key,
		replacements: make(map[string]string),
		dir:          sessionDir(key),
	}

	if err := s.load(); err != nil {
		return nil, err
	}

	return s, nil
}

// Record adds a single replacement to the store and flushes to disk.
// Called after a successful compression in the pipeline.
func (s *Store) Record(toolUseID, tombstone string) error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.replacements[toolUseID] = tombstone
	s.dirty = true
	return s.flush()
}

// RecordBatch adds multiple replacements and flushes once.
func (s *Store) RecordBatch(replacements map[string]string) error {
	if s == nil || len(replacements) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for id, tombstone := range replacements {
		s.replacements[id] = tombstone
		s.dirty = true
	}

	return s.flush()
}

// Lookup returns the stored tombstone for a tool_use_id, or ("", false).
func (s *Store) Lookup(toolUseID string) (string, bool) {
	if s == nil {
		return "", false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	tombstone, ok := s.replacements[toolUseID]
	return tombstone, ok
}

// All returns a copy of all stored replacements.
func (s *Store) All() map[string]string {
	if s == nil {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]string, len(s.replacements))
	for k, v := range s.replacements {
		out[k] = v
	}
	return out
}

// SessionKey returns the key this store was opened with (UUID or legacy hash).
func (s *Store) SessionKey() string {
	if s == nil {
		return ""
	}
	return s.sessionKey
}

// SessionHash is a backwards-compatible alias for SessionKey.
// Deprecated: use SessionKey instead.
func (s *Store) SessionHash() string {
	return s.SessionKey()
}

// flush writes the current state to disk using atomic write (write .tmp, rename).
func (s *Store) flush() error {
	if !s.dirty {
		return nil
	}

	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s.replacements, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := filepath.Join(s.dir, "replacements.json.tmp")
	finalPath := filepath.Join(s.dir, "replacements.json")

	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		// Best-effort cleanup; keep dirty=true so a later write can retry.
		_ = os.Remove(tmpPath)
		return err
	}

	s.dirty = false
	return nil
}

// load reads replacements.json from disk into memory.
func (s *Store) load() error {
	path := replacementsPath(s.sessionKey)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var loaded map[string]string
	if err := json.Unmarshal(data, &loaded); err != nil {
		fmt.Fprintf(os.Stderr, "[wet] persistence warning: invalid replacements file %s: %v\n", path, err)
		s.replacements = make(map[string]string)
		return nil
	}

	if loaded == nil {
		loaded = make(map[string]string)
	}
	s.replacements = loaded
	return nil
}

// LoadCumulative reads cumulative.json from disk. Returns zero-value stats if
// the file is missing or corrupt (graceful degradation).
func (s *Store) LoadCumulative() CumulativeStats {
	if s == nil {
		return CumulativeStats{}
	}

	path := cumulativePath(s.sessionKey)
	data, err := os.ReadFile(path)
	if err != nil {
		return CumulativeStats{}
	}

	var cs CumulativeStats
	if err := json.Unmarshal(data, &cs); err != nil {
		fmt.Fprintf(os.Stderr, "[wet] persistence warning: invalid cumulative file %s: %v\n", path, err)
		return CumulativeStats{}
	}
	return cs
}

// UpdateCumulative adds the given deltas to the persisted cumulative stats
// and writes atomically (tmp + rename). Thread-safe.
func (s *Store) UpdateCumulative(tokensBefore, tokensAfter int64, itemsCompressed int) error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Load existing totals.
	cs := s.loadCumulativeUnlocked()
	cs.TokensBefore += tokensBefore
	cs.TokensAfter += tokensAfter
	cs.ItemsCompressed += itemsCompressed
	cs.Updated = time.Now().UTC().Format(time.RFC3339)

	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cs, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := filepath.Join(s.dir, "cumulative.json.tmp")
	finalPath := filepath.Join(s.dir, "cumulative.json")

	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	return nil
}

// loadCumulativeUnlocked reads cumulative.json without locking (caller must hold mu).
func (s *Store) loadCumulativeUnlocked() CumulativeStats {
	path := cumulativePath(s.sessionKey)
	data, err := os.ReadFile(path)
	if err != nil {
		return CumulativeStats{}
	}

	var cs CumulativeStats
	if err := json.Unmarshal(data, &cs); err != nil {
		return CumulativeStats{}
	}
	return cs
}
