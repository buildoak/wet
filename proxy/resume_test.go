package proxy

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/otonashi/wet/config"
	"github.com/otonashi/wet/persist"
)

func TestRestoreResumeStatsSeedsImmediatelyAndAvoidsDoubleSeed(t *testing.T) {
	tmpDir := t.TempDir()
	origDir := persist.DirFunc
	persist.DirFunc = func() string { return tmpDir }
	defer func() { persist.DirFunc = origDir }()

	sessionUUID := "resume-session-123"
	sessionDir := filepath.Join(tmpDir, "sessions", sessionUUID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(sessionDir) error = %v", err)
	}

	cumulative := map[string]any{
		"tokens_before":    10000,
		"tokens_after":     4000,
		"items_compressed": 8,
		"updated":          "2026-03-13T00:00:00Z",
	}
	cumulativeData, err := json.MarshalIndent(cumulative, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent(cumulative) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "cumulative.json"), cumulativeData, 0o644); err != nil {
		t.Fatalf("WriteFile(cumulative.json) error = %v", err)
	}

	replacements := map[string]string{
		"toolu_a": "[compressed: a]",
		"toolu_b": "[compressed: b]",
	}
	replacementsData, err := json.MarshalIndent(replacements, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent(replacements) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "replacements.json"), replacementsData, 0o644); err != nil {
		t.Fatalf("WriteFile(replacements.json) error = %v", err)
	}

	cfg := config.Default()
	cfg.Server.Port = 0
	srv := NewWithLogOutput(cfg, io.Discard)
	defer srv.Shutdown()
	srv.SetSessionUUID(sessionUUID)

	srv.RestoreResumeStats()

	snap := srv.StatusSnapshot()
	if snap.Compressed != 8 {
		t.Fatalf("after RestoreResumeStats Compressed = %d, want 8", snap.Compressed)
	}
	if snap.TokensSaved != 6000 {
		t.Fatalf("after RestoreResumeStats TokensSaved = %d, want 6000", snap.TokensSaved)
	}
	if !srv.statsRestored {
		t.Fatalf("statsRestored = false, want true")
	}

	srv.ensurePersistStore("ignored-system-hash")

	snapAfterEnsure := srv.StatusSnapshot()
	if snapAfterEnsure.Compressed != 8 {
		t.Fatalf("after ensurePersistStore Compressed = %d, want 8 (no double-count)", snapAfterEnsure.Compressed)
	}
	if snapAfterEnsure.TokensSaved != 6000 {
		t.Fatalf("after ensurePersistStore TokensSaved = %d, want 6000 (no double-count)", snapAfterEnsure.TokensSaved)
	}
}
