package persist

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestEnsureHeaderCreatesFile(t *testing.T) {
	setTestDir(t)

	store, err := Open("session-create")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	header := SessionHeader{
		V:       1,
		Type:    "header",
		Session: "sess-1",
		Created: "2026-03-12T00:00:00Z",
		Model:   "gpt-5",
		Mode:    "auto",
	}
	if err := store.EnsureHeader(header); err != nil {
		t.Fatalf("EnsureHeader() error = %v", err)
	}

	if _, err := os.Stat(sessionJSONLPath(store.SessionKey())); err != nil {
		t.Fatalf("session.jsonl stat error = %v", err)
	}

	gotHeader, gotTurns, err := store.ReadSession()
	if err != nil {
		t.Fatalf("ReadSession() error = %v", err)
	}
	if gotHeader == nil {
		t.Fatal("ReadSession() header is nil")
	}
	if !reflect.DeepEqual(*gotHeader, header) {
		t.Fatalf("header = %#v, want %#v", *gotHeader, header)
	}
	if len(gotTurns) != 0 {
		t.Fatalf("turns len = %d, want 0", len(gotTurns))
	}
}

func TestEnsureHeaderIdempotent(t *testing.T) {
	setTestDir(t)

	store, err := Open("session-idempotent")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	first := SessionHeader{
		V:       1,
		Type:    "header",
		Session: "first",
		Created: "2026-03-12T00:00:00Z",
		Model:   "gpt-5",
		Mode:    "auto",
	}
	second := SessionHeader{
		V:       99,
		Type:    "header",
		Session: "second",
		Created: "2099-01-01T00:00:00Z",
		Model:   "different-model",
		Mode:    "passthrough",
	}

	if err := store.EnsureHeader(first); err != nil {
		t.Fatalf("EnsureHeader(first) error = %v", err)
	}
	if err := store.EnsureHeader(second); err != nil {
		t.Fatalf("EnsureHeader(second) error = %v", err)
	}

	gotHeader, gotTurns, err := store.ReadSession()
	if err != nil {
		t.Fatalf("ReadSession() error = %v", err)
	}
	if gotHeader == nil {
		t.Fatal("ReadSession() header is nil")
	}
	if !reflect.DeepEqual(*gotHeader, first) {
		t.Fatalf("header = %#v, want first %#v", *gotHeader, first)
	}
	if len(gotTurns) != 0 {
		t.Fatalf("turns len = %d, want 0", len(gotTurns))
	}

	data, err := os.ReadFile(sessionJSONLPath(store.SessionKey()))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	lines := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	if lines != 1 {
		t.Fatalf("non-empty line count = %d, want 1", lines)
	}
}

func TestAppendTurn(t *testing.T) {
	setTestDir(t)

	store, err := Open("append-turn")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if err := store.EnsureHeader(SessionHeader{
		Session: "append-turn",
		Created: "2026-03-12T00:00:00Z",
		Model:   "gpt-5",
		Mode:    "auto",
	}); err != nil {
		t.Fatalf("EnsureHeader() error = %v", err)
	}

	wantTurns := []TurnRecord{
		{
			Turn:           1,
			Ts:             "2026-03-12T00:01:00Z",
			Usage:          UsageRecord{InputTokens: 100, OutputTokens: 10},
			TotalContext:   110,
			CharsSaved:     33,
			TokensSavedEst: 10,
			Items: []TurnItem{
				{ID: "t1", Tool: "Bash", Cmd: "git status", OrigChars: 200, TombChars: 100, CharsSaved: 100, Tombstone: "[compressed: 1]", Preview: "p1"},
			},
		},
		{
			Turn:           2,
			Ts:             "2026-03-12T00:02:00Z",
			Usage:          UsageRecord{InputTokens: 120, OutputTokens: 11, CacheReadInputTokens: 3, CacheCreationInputTokens: 4},
			TotalContext:   138,
			CharsSaved:     66,
			TokensSavedEst: 20,
			Items: []TurnItem{
				{ID: "t2", Tool: "Bash", Cmd: "npm install", OrigChars: 300, TombChars: 150, CharsSaved: 150, Tombstone: "[compressed: 2]", Preview: "p2"},
			},
		},
		{
			Turn:           3,
			Ts:             "2026-03-12T00:03:00Z",
			Usage:          UsageRecord{InputTokens: 140, OutputTokens: 12},
			TotalContext:   152,
			CharsSaved:     99,
			TokensSavedEst: 30,
			Items: []TurnItem{
				{ID: "t3", Tool: "Bash", Cmd: "ls -la", OrigChars: 400, TombChars: 200, CharsSaved: 200, Tombstone: "[compressed: 3]", Preview: "p3"},
			},
		},
	}

	for i, rec := range wantTurns {
		if err := store.AppendTurn(rec); err != nil {
			t.Fatalf("AppendTurn(%d) error = %v", i, err)
		}
	}

	gotHeader, gotTurns, err := store.ReadSession()
	if err != nil {
		t.Fatalf("ReadSession() error = %v", err)
	}
	if gotHeader == nil {
		t.Fatal("ReadSession() header is nil")
	}
	if len(gotTurns) != 3 {
		t.Fatalf("turn len = %d, want 3", len(gotTurns))
	}
	for i := range wantTurns {
		wantTurns[i].Type = "turn"
	}
	if !reflect.DeepEqual(gotTurns, wantTurns) {
		t.Fatalf("turns = %#v, want %#v", gotTurns, wantTurns)
	}
}

func TestReadSessionMissing(t *testing.T) {
	setTestDir(t)

	store, err := Open("missing-session")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	header, turns, err := store.ReadSession()
	if err != nil {
		t.Fatalf("ReadSession() error = %v", err)
	}
	if header != nil {
		t.Fatalf("header = %#v, want nil", header)
	}
	if turns != nil {
		t.Fatalf("turns = %#v, want nil", turns)
	}
}

func TestReadSessionCorruptLine(t *testing.T) {
	setTestDir(t)

	store, err := Open("corrupt-session")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	header := SessionHeader{
		Session: "corrupt-session",
		Created: "2026-03-12T00:00:00Z",
		Model:   "gpt-5",
		Mode:    "auto",
	}
	if err := store.EnsureHeader(header); err != nil {
		t.Fatalf("EnsureHeader() error = %v", err)
	}

	f, err := os.OpenFile(sessionJSONLPath(store.SessionKey()), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}
	if _, err := f.WriteString("{this is not valid json}\n"); err != nil {
		_ = f.Close()
		t.Fatalf("WriteString() error = %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	gotHeader, gotTurns, err := store.ReadSession()
	if err == nil {
		t.Fatal("ReadSession() error = nil, want error")
	}
	if gotHeader == nil {
		t.Fatal("ReadSession() header is nil")
	}
	if gotHeader.Session != header.Session {
		t.Fatalf("header.Session = %q, want %q", gotHeader.Session, header.Session)
	}
	if len(gotTurns) != 0 {
		t.Fatalf("turns len = %d, want 0", len(gotTurns))
	}
}

func TestNilStoreSession(t *testing.T) {
	var store *Store

	if err := store.EnsureHeader(SessionHeader{Session: "x"}); err != nil {
		t.Fatalf("EnsureHeader(nil store) error = %v", err)
	}
	if err := store.AppendTurn(TurnRecord{Turn: 1}); err != nil {
		t.Fatalf("AppendTurn(nil store) error = %v", err)
	}

	header, turns, err := store.ReadSession()
	if err != nil {
		t.Fatalf("ReadSession(nil store) error = %v", err)
	}
	if header != nil {
		t.Fatalf("header = %#v, want nil", header)
	}
	if turns != nil {
		t.Fatalf("turns = %#v, want nil", turns)
	}
}

func TestTurnRecordFields(t *testing.T) {
	want := TurnRecord{
		Type:           "turn",
		Turn:           7,
		Ts:             "2026-03-12T02:03:04Z",
		Usage:          UsageRecord{InputTokens: 111, OutputTokens: 22, CacheReadInputTokens: 33, CacheCreationInputTokens: 44},
		TotalContext:   210,
		CharsSaved:     99,
		TokensSavedEst: 30,
		Items: []TurnItem{
			{ID: "toolu_1", Tool: "Bash", Cmd: "git status", OrigChars: 1000, TombChars: 120, CharsSaved: 880, Tombstone: "[compressed: git_status]", Preview: "On branch main"},
			{ID: "toolu_2", Tool: "Read", Cmd: "", OrigChars: 2000, TombChars: 100, CharsSaved: 1900, Tombstone: "[compressed: read]", Preview: "file content"},
		},
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var got TurnRecord
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip = %#v, want %#v", got, want)
	}
}

func TestAppendTurnCharsSaved(t *testing.T) {
	setTestDir(t)

	store, err := Open("chars-saved")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.EnsureHeader(SessionHeader{
		Session: "chars-saved",
		Created: "2026-03-12T00:00:00Z",
		Model:   "gpt-5",
		Mode:    "auto",
	}); err != nil {
		t.Fatalf("EnsureHeader() error = %v", err)
	}

	items := []TurnItem{
		{ID: "a", Tool: "Bash", Cmd: "cmd-a", OrigChars: 500, TombChars: 350, CharsSaved: 150, Tombstone: "[compressed: a]", Preview: "aaa"},
		{ID: "b", Tool: "Bash", Cmd: "cmd-b", OrigChars: 250, TombChars: 120, CharsSaved: 130, Tombstone: "[compressed: b]", Preview: "bbb"},
	}
	charsSaved := items[0].CharsSaved + items[1].CharsSaved
	tokensSavedEst := int(float64(charsSaved) / 3.3)

	rec := TurnRecord{
		Turn:           1,
		Ts:             "2026-03-12T00:01:00Z",
		Usage:          UsageRecord{InputTokens: 400, OutputTokens: 20},
		TotalContext:   420,
		CharsSaved:     charsSaved,
		TokensSavedEst: tokensSavedEst,
		Items:          items,
	}
	if err := store.AppendTurn(rec); err != nil {
		t.Fatalf("AppendTurn() error = %v", err)
	}

	_, turns, err := store.ReadSession()
	if err != nil {
		t.Fatalf("ReadSession() error = %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("turns len = %d, want 1", len(turns))
	}
	got := turns[0]
	if got.CharsSaved != charsSaved {
		t.Fatalf("CharsSaved = %d, want %d", got.CharsSaved, charsSaved)
	}
	if got.TokensSavedEst != tokensSavedEst {
		t.Fatalf("TokensSavedEst = %d, want %d", got.TokensSavedEst, tokensSavedEst)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items len = %d, want 2", len(got.Items))
	}
}
