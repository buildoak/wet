# Persistence Layer for wet Compression State

## Overview

Implement a persistence layer that saves compression replacements to disk so they survive session restarts. When a Claude Code session resumes (`claude --resume`), wet reloads previous compressions and re-applies them to tool_result blocks that reappear in the conversation context.

## Data Model

```
~/.wet/sessions/{session-hash}/replacements.json
```

The file is a JSON object mapping `tool_use_id` to the full tombstone text:

```json
{
  "toolu_abc123": "[compressed: git_status | 3 files modified | turn 4/12 | 847->62 tokens]",
  "toolu_def456": "[compressed: generic | build succeeded with 2 warnings | turn 2/8 | 1200->45 tokens]"
}
```

The session hash comes from `proxy.HashSystemPrompt()` (already computed in `proxy/proxy.go` via `extractSystemHash()`). It is an 8-hex-char SHA256 prefix of the first 500 chars of the system prompt (after stripping the billing header).

---

## File 1: `persist/persist.go` (new package)

Create a new package `persist` at `/Users/otonashi/thinking/building/wet/persist/persist.go`.

### Types and Functions

```go
package persist

import (
    "encoding/json"
    "os"
    "path/filepath"
    "sync"
)

// Store manages persistent compression state for a single session.
type Store struct {
    mu           sync.RWMutex
    sessionHash  string
    replacements map[string]string // tool_use_id -> tombstone text
    dir          string           // ~/.wet/sessions/{hash}/
    dirty        bool             // true if in-memory state differs from disk
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

func sessionDir(hash string) string {
    return filepath.Join(DirFunc(), "sessions", hash)
}

func replacementsPath(hash string) string {
    return filepath.Join(sessionDir(hash), "replacements.json")
}

// Open loads or creates a Store for the given session hash.
// If replacements.json exists, it is loaded. Otherwise an empty store is created.
// Returns nil if hash is empty.
func Open(hash string) (*Store, error)

// Record adds a single replacement to the store and flushes to disk.
// Called after a successful compression in the pipeline.
func (s *Store) Record(toolUseID, tombstone string) error

// RecordBatch adds multiple replacements and flushes once.
func (s *Store) RecordBatch(replacements map[string]string) error

// Lookup returns the stored tombstone for a tool_use_id, or ("", false).
func (s *Store) Lookup(toolUseID string) (string, bool)

// All returns a copy of all stored replacements.
func (s *Store) All() map[string]string

// SessionHash returns the hash this store was opened with.
func (s *Store) SessionHash() string

// flush writes the current state to disk using atomic write (write .tmp, rename).
func (s *Store) flush() error

// load reads replacements.json from disk into memory.
func (s *Store) load() error
```

### Implementation Details

**Open:**
1. If hash is empty, return `nil, nil` (no persistence for sessions without a stable hash).
2. Create a `Store` with the hash and empty map.
3. Call `s.load()` -- if the file exists, parse it. If the file doesn't exist, that's fine (empty map). If the file is corrupt (invalid JSON), log a warning to stderr and start with an empty map (don't fail).
4. Return the store.

**Record / RecordBatch:**
1. Lock the mutex.
2. Add entries to the map (overwrite if key exists -- idempotent).
3. Set dirty = true.
4. Call `s.flush()`.

**flush:**
1. If not dirty, return nil.
2. `os.MkdirAll(s.dir, 0o755)` -- create session directory.
3. Marshal the map to indented JSON.
4. Write to `replacements.json.tmp` in the session directory.
5. `os.Rename` the .tmp file to `replacements.json` (atomic).
6. Set dirty = false.

**load:**
1. Read `replacements.json` from the session directory.
2. If `os.IsNotExist`, return nil (empty store is fine).
3. Unmarshal JSON into the map.
4. If unmarshal fails, log warning and return nil (start fresh).

---

## File 2: `persist/persist_test.go` (new)

Test the following:

1. **TestOpenEmpty** -- Open with a new hash, verify empty store, verify no file on disk until Record is called.
2. **TestRecordAndLookup** -- Record a replacement, verify Lookup returns it.
3. **TestRecordBatch** -- Record multiple replacements in one call.
4. **TestPersistenceAcrossOpen** -- Record entries, close the store, Open again with the same hash, verify entries are loaded.
5. **TestCorruptFile** -- Write invalid JSON to replacements.json, Open should succeed with empty map.
6. **TestEmptyHash** -- Open with "" returns nil, nil.
7. **TestAtomicWrite** -- Verify .tmp file is used (check that partial writes don't corrupt).
8. **TestOverwrite** -- Record same tool_use_id twice, verify latest value wins.

Use `t.TempDir()` and override `DirFunc` for test isolation.

---

## File 3: Integration into `proxy/proxy.go`

### Changes to `Server` struct

Add a field to the Server struct:

```go
type Server struct {
    // ... existing fields ...
    persistStore *persist.Store
}
```

### Changes to `New()` function

After the server is created and the control plane started, but we don't know the session hash yet (it comes from the first request). The store will be opened lazily.

No changes needed in `New()`.

### Changes to `handleMessagesWithCompression()`

This is the key integration point. Two additions:

**A) Lazy store initialization (after session hash is computed):**

After the line `isMain := s.TagOrMatchMainSession(systemHash)`, add:

```go
// Initialize persistence store on first main-session request with a valid hash.
if isMain && systemHash != "" && s.persistStore == nil {
    store, err := persist.Open(systemHash)
    if err != nil {
        s.logf("[wet] persistence store error: %v\n", err)
    } else if store != nil {
        s.persistStore = store
        s.logf("[wet] persistence: loaded %d prior compressions for session %s\n", len(store.All()), systemHash)
    }
}
```

**B) Apply persisted replacements before compression:**

Right after staleness classification but before the compression switch, apply persisted replacements to tool_result blocks that have reverted to their uncompressed form (session resume scenario):

```go
// Apply persisted replacements for resumed sessions.
if isMain && s.persistStore != nil {
    applied := s.applyPersistedReplacements(req, infos)
    if applied > 0 {
        s.logf("[wet] persistence: re-applied %d prior compressions\n", applied)
        // Re-serialize the request since we modified it
        // (the compression pipeline below will see the already-tombstoned results)
    }
}
```

**C) Record new compressions to persistence:**

After the compression result is computed and applied (after `result.Compressed > 0`), record new tombstones:

```go
if result.Compressed > 0 && isMain && s.persistStore != nil {
    s.recordCompressionsToPersist(req, infos)
}
```

### New methods on Server

Add to `proxy/proxy.go`:

```go
// applyPersistedReplacements checks each tool_result block against the persistence
// store. If a stored tombstone exists and the content is NOT already a tombstone,
// apply the replacement. Returns the number of replacements applied.
func (s *Server) applyPersistedReplacements(req *messages.Request, infos []messages.ToolResultInfo) int {
    if s.persistStore == nil {
        return 0
    }
    applied := 0
    for _, info := range infos {
        if pipeline.IsTombstone(info.Content) {
            continue // already compressed
        }
        tombstone, ok := s.persistStore.Lookup(info.ToolUseID)
        if !ok {
            continue
        }
        // Apply the stored tombstone
        if err := pipeline.ReplaceToolResultContent(&req.Messages[info.MsgIdx], info.BlockIdx, tombstone, info.ContentIsStr); err != nil {
            continue
        }
        applied++
    }
    return applied
}

// recordCompressionsToPersist scans tool_result blocks after compression and
// records any new tombstones to the persistence store.
func (s *Server) recordCompressionsToPersist(req *messages.Request, infos []messages.ToolResultInfo) {
    if s.persistStore == nil {
        return
    }
    batch := make(map[string]string)
    for _, info := range infos {
        // Re-read the content from the (now-modified) message to get the tombstone
        blocks, isStr, err := messages.ParseToolResultContent(req.Messages[info.MsgIdx], info.BlockIdx)
        if err != nil {
            continue
        }
        var content string
        if isStr {
            content = string(req.Messages[info.MsgIdx].Content)
            // Unquote if it's a JSON string
            _ = json.Unmarshal(req.Messages[info.MsgIdx].Content, &content)
        } else if len(blocks) > 0 {
            content = blocks[0].Text
        }
        if pipeline.IsTombstone(content) {
            // Only record if not already in the store (avoid unnecessary writes)
            if _, exists := s.persistStore.Lookup(info.ToolUseID); !exists {
                batch[info.ToolUseID] = content
            }
        }
    }
    if len(batch) > 0 {
        if err := s.persistStore.RecordBatch(batch); err != nil {
            s.logf("[wet] persistence write error: %v\n", err)
        }
    }
}
```

### IMPORTANT: Re-reading compressed content

The `recordCompressionsToPersist` method needs to read the current content of tool_result blocks AFTER compression has been applied to the request. The `infos` slice was built BEFORE compression, so its `Content` field has the original (pre-compression) text. We need to re-read from `req.Messages` which has been modified in-place by the pipeline.

Add this helper to `messages/parse.go`:

```go
// ParseToolResultContent extracts the text content of a specific tool_result block
// within a message. Returns the content blocks and whether content is a plain string.
func ParseToolResultContent(msg Message, blockIdx int) ([]ContentBlock, bool, error) {
    // If content is a plain string
    trimmed := bytes.TrimSpace(msg.Content)
    if len(trimmed) > 0 && trimmed[0] == '"' {
        return nil, true, nil
    }

    blocks, isStr, err := ParseContent(msg.Content)
    if err != nil {
        return nil, false, err
    }
    if isStr {
        return nil, true, nil
    }
    if blockIdx < 0 || blockIdx >= len(blocks) {
        return nil, false, fmt.Errorf("block index %d out of range", blockIdx)
    }

    // For tool_result blocks, parse the nested content
    block := blocks[blockIdx]
    if block.Type != "tool_result" {
        return []ContentBlock{block}, false, nil
    }

    innerBlocks, innerIsStr, err := ParseContent(block.Content)
    if err != nil {
        return nil, false, err
    }
    if innerIsStr {
        var text string
        if json.Unmarshal(block.Content, &text) == nil {
            return []ContentBlock{{Type: "text", Text: text}}, false, nil
        }
    }
    return innerBlocks, false, nil
}
```

**HOWEVER** -- this approach is fragile. A simpler approach: instead of re-reading from the modified request, capture tombstones at creation time in the pipeline functions themselves.

### REVISED APPROACH: Capture tombstones in the pipeline

Instead of the complex re-reading approach, modify `CompressRequest` and `CompressSelected` to return the list of replacements they made:

**Add to `CompressResult`:**

```go
type CompressResult struct {
    TotalToolResults int
    Compressed       int
    SkippedFresh     int
    SkippedBypass    int
    TokensBefore     int
    TokensAfter      int
    OverheadMs       float64
    // NEW: map of tool_use_id -> tombstone text for all compressions applied this request
    Replacements     map[string]string
}
```

**Modify `CompressRequest` in `pipeline/pipeline.go`:**

After the line `result.TokensAfter += compressedTokens`, add:

```go
if result.Replacements == nil {
    result.Replacements = make(map[string]string)
}
result.Replacements[info.ToolUseID] = tombstone
```

**Modify `CompressSelected` in `pipeline/selective.go`:**

Same pattern -- after each successful compression (both the pre-computed replacement path and the Tier 1 path), record the tombstone:

```go
if result.Replacements == nil {
    result.Replacements = make(map[string]string)
}
result.Replacements[info.ToolUseID] = tombstone
```

**Then in `proxy/proxy.go`, after compression:**

```go
if result.Compressed > 0 && isMain && s.persistStore != nil && len(result.Replacements) > 0 {
    if err := s.persistStore.RecordBatch(result.Replacements); err != nil {
        s.logf("[wet] persistence write error: %v\n", err)
    }
}
```

This eliminates the need for the complex `recordCompressionsToPersist` function and the `ParseToolResultContent` helper. Much cleaner.

---

## Integration Summary

### Files to CREATE:
1. `persist/persist.go` -- Store type, Open/Record/Lookup/flush/load
2. `persist/persist_test.go` -- Unit tests

### Files to MODIFY:

3. `pipeline/pipeline.go`:
   - Add `Replacements map[string]string` to `CompressResult`
   - In `CompressRequest`, populate `result.Replacements` after each successful compression

4. `pipeline/selective.go`:
   - In `CompressSelected`, populate `result.Replacements` after each successful compression (both pre-computed and Tier 1 paths)

5. `proxy/proxy.go`:
   - Add `persistStore *persist.Store` field to `Server`
   - Add `import "github.com/otonashi/wet/persist"`
   - In `handleMessagesWithCompression`: lazy init of persist store after session hash is computed
   - In `handleMessagesWithCompression`: apply persisted replacements before compression
   - In `handleMessagesWithCompression`: record new compressions to persist store after pipeline runs
   - Add `applyPersistedReplacements` method

---

## Exact Edit Locations in `proxy/proxy.go`

### Server struct (add field):
After `ctrl controlState` (line ~46), add:
```go
persistStore *persist.Store
```

### handleMessagesWithCompression (three insertion points):

**Point A: After `isMain := s.TagOrMatchMainSession(systemHash)` (~line 254):**
```go
// Lazy-init persistence store on first main-session request with valid hash.
if isMain && systemHash != "" && s.persistStore == nil {
    store, err := persist.Open(systemHash)
    if err != nil {
        s.logf("[wet] persistence store error: %v\n", err)
    } else if store != nil {
        s.persistStore = store
        count := len(store.All())
        if count > 0 {
            s.logf("[wet] persistence: loaded %d prior compressions for session %s\n", count, systemHash)
        }
    }
}
```

**Point B: After `s.StoreToolResults(infos)` but before the compression switch (~line 264):**
```go
// Re-apply persisted compressions for resumed sessions.
if isMain && s.persistStore != nil {
    applied := s.applyPersistedReplacements(req, infos)
    if applied > 0 {
        s.logf("[wet] persistence: re-applied %d prior compressions\n", applied)
    }
}
```

**Point C: After `s.AddCompressedStats(result.Compressed, result.TokensBefore-result.TokensAfter)` (~line 300):**
```go
// Persist new compressions.
if isMain && s.persistStore != nil && len(result.Replacements) > 0 {
    if err := s.persistStore.RecordBatch(result.Replacements); err != nil {
        s.logf("[wet] persistence write error: %v\n", err)
    }
}
```

---

## Edge Cases

1. **Session hash is empty:** No persistence (Open returns nil). This happens for requests without a substantial system prompt. No-op, safe.

2. **Corrupt replacements.json:** `Open` logs a warning and starts with empty map. Previous compressions are lost but no crash.

3. **Same tool_use_id compressed twice:** `Record` overwrites. Last tombstone wins. This is correct -- if the same content is re-compressed (e.g., turn counts change), the latest tombstone is more accurate.

4. **Content already tombstoned:** `applyPersistedReplacements` checks `IsTombstone()` before applying. Already-compressed blocks are skipped.

5. **Multiple wet instances:** File locking is not implemented. Last-write-wins via atomic rename. Acceptable since multiple instances on the same session is an edge case and the data converges (same compressions).

6. **Session hash changes mid-session:** The persist store is initialized once with the first valid hash. If the hash somehow changes (shouldn't happen), the store remains bound to the original hash. New compressions go to the original store. This is safe -- worst case, some compressions aren't re-applied on a hypothetical second resume.

---

## Verification

After implementation:
```bash
cd /Users/otonashi/thinking/building/wet && go build ./... && go test ./...
```

The build must pass, all existing tests must pass, and the new `persist` package tests must pass.
