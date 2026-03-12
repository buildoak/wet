package persist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func setTestDir(t *testing.T) string {
	t.Helper()

	base := t.TempDir()
	orig := DirFunc
	DirFunc = func() string { return base }
	t.Cleanup(func() {
		DirFunc = orig
	})
	return base
}

func TestOpenEmpty(t *testing.T) {
	setTestDir(t)
	hash := "abc12345"

	store, err := Open(hash)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if store == nil {
		t.Fatal("Open() returned nil store")
	}
	if got := store.SessionKey(); got != hash {
		t.Fatalf("SessionKey() = %q, want %q", got, hash)
	}
	if got := store.All(); len(got) != 0 {
		t.Fatalf("All() len = %d, want 0", len(got))
	}

	if _, err := os.Stat(replacementsPath(hash)); !os.IsNotExist(err) {
		t.Fatalf("replacements.json should not exist before first record, err=%v", err)
	}
}

func TestRecordAndLookup(t *testing.T) {
	setTestDir(t)
	hash := "record1"

	store, err := Open(hash)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	want := "[compressed: git_status | demo]"
	if err := store.Record("toolu_1", want); err != nil {
		t.Fatalf("Record() error = %v", err)
	}

	got, ok := store.Lookup("toolu_1")
	if !ok {
		t.Fatal("Lookup() missing key")
	}
	if got != want {
		t.Fatalf("Lookup() = %q, want %q", got, want)
	}
}

func TestRecordBatch(t *testing.T) {
	setTestDir(t)

	store, err := Open("batch1")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	batch := map[string]string{
		"toolu_1": "[compressed: one]",
		"toolu_2": "[compressed: two]",
		"toolu_3": "[compressed: three]",
	}
	if err := store.RecordBatch(batch); err != nil {
		t.Fatalf("RecordBatch() error = %v", err)
	}

	all := store.All()
	if !reflect.DeepEqual(all, batch) {
		t.Fatalf("All() = %#v, want %#v", all, batch)
	}
}

func TestPersistenceAcrossOpen(t *testing.T) {
	setTestDir(t)
	hash := "persist1"

	store, err := Open(hash)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	want := map[string]string{
		"toolu_a": "[compressed: a]",
		"toolu_b": "[compressed: b]",
	}
	if err := store.RecordBatch(want); err != nil {
		t.Fatalf("RecordBatch() error = %v", err)
	}

	reopened, err := Open(hash)
	if err != nil {
		t.Fatalf("Open(reopen) error = %v", err)
	}

	got := reopened.All()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reopened All() = %#v, want %#v", got, want)
	}
}

func TestCorruptFile(t *testing.T) {
	setTestDir(t)
	hash := "corrupt1"

	if err := os.MkdirAll(sessionDir(hash), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(replacementsPath(hash), []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := Open(hash)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if store == nil {
		t.Fatal("Open() returned nil store")
	}
	if len(store.All()) != 0 {
		t.Fatalf("All() len = %d, want 0", len(store.All()))
	}
}

func TestEmptyHash(t *testing.T) {
	setTestDir(t)

	store, err := Open("")
	if err != nil {
		t.Fatalf("Open(\"\") error = %v", err)
	}
	if store != nil {
		t.Fatalf("Open(\"\") returned non-nil store: %#v", store)
	}
}

func TestAtomicWrite(t *testing.T) {
	setTestDir(t)
	hash := "atomic1"

	store, err := Open(hash)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.Record("toolu_a", "[compressed: first]"); err != nil {
		t.Fatalf("Record(first) error = %v", err)
	}

	path := replacementsPath(hash)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(original) error = %v", err)
	}

	tmpPath := filepath.Join(sessionDir(hash), "replacements.json.tmp")
	if err := os.MkdirAll(tmpPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(tmpPath as dir) error = %v", err)
	}

	if err := store.Record("toolu_b", "[compressed: second]"); err == nil {
		t.Fatal("Record(second) expected error due blocked tmp path, got nil")
	}

	afterFailure, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(after failure) error = %v", err)
	}
	if string(afterFailure) != string(original) {
		t.Fatal("replacements.json changed after failed write; expected original content to remain intact")
	}

	var decoded map[string]string
	if err := json.Unmarshal(afterFailure, &decoded); err != nil {
		t.Fatalf("replacements.json became invalid JSON after failed write: %v", err)
	}
	if decoded["toolu_a"] != "[compressed: first]" {
		t.Fatalf("decoded toolu_a = %q, want first value", decoded["toolu_a"])
	}
	if _, ok := decoded["toolu_b"]; ok {
		t.Fatal("toolu_b unexpectedly persisted despite failed write")
	}

	if err := os.Remove(tmpPath); err != nil {
		t.Fatalf("Remove(tmpPath) error = %v", err)
	}
	if err := store.Record("toolu_b", "[compressed: second]"); err != nil {
		t.Fatalf("Record(second retry) error = %v", err)
	}

	reopened, err := Open(hash)
	if err != nil {
		t.Fatalf("Open(reopen) error = %v", err)
	}
	if got, ok := reopened.Lookup("toolu_b"); !ok || got != "[compressed: second]" {
		t.Fatalf("Lookup(toolu_b) = (%q, %v), want ([compressed: second], true)", got, ok)
	}
}

func TestOverwrite(t *testing.T) {
	setTestDir(t)

	store, err := Open("overwrite1")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.Record("toolu_same", "[compressed: old]"); err != nil {
		t.Fatalf("Record(old) error = %v", err)
	}
	if err := store.Record("toolu_same", "[compressed: new]"); err != nil {
		t.Fatalf("Record(new) error = %v", err)
	}

	got, ok := store.Lookup("toolu_same")
	if !ok {
		t.Fatal("Lookup() missing overwritten key")
	}
	if got != "[compressed: new]" {
		t.Fatalf("Lookup() = %q, want latest value", got)
	}
}

func TestUpdateCumulative(t *testing.T) {
	setTestDir(t)
	hash := "cumul1"

	store, err := Open(hash)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	// First update: 1000 before, 200 after, 5 items.
	if err := store.UpdateCumulative(1000, 200, 5); err != nil {
		t.Fatalf("UpdateCumulative(1) error = %v", err)
	}

	// Verify by loading.
	cs := store.LoadCumulative()
	if cs.TokensBefore != 1000 {
		t.Fatalf("TokensBefore = %d, want 1000", cs.TokensBefore)
	}
	if cs.TokensAfter != 200 {
		t.Fatalf("TokensAfter = %d, want 200", cs.TokensAfter)
	}
	if cs.ItemsCompressed != 5 {
		t.Fatalf("ItemsCompressed = %d, want 5", cs.ItemsCompressed)
	}
	if cs.Updated == "" {
		t.Fatal("Updated should be non-empty")
	}

	// Second update: deltas should ADD.
	if err := store.UpdateCumulative(500, 100, 3); err != nil {
		t.Fatalf("UpdateCumulative(2) error = %v", err)
	}

	cs = store.LoadCumulative()
	if cs.TokensBefore != 1500 {
		t.Fatalf("TokensBefore = %d, want 1500", cs.TokensBefore)
	}
	if cs.TokensAfter != 300 {
		t.Fatalf("TokensAfter = %d, want 300", cs.TokensAfter)
	}
	if cs.ItemsCompressed != 8 {
		t.Fatalf("ItemsCompressed = %d, want 8", cs.ItemsCompressed)
	}
}

func TestUpdateCumulativePersistsAcrossOpen(t *testing.T) {
	setTestDir(t)
	hash := "cumul2"

	store, err := Open(hash)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if err := store.UpdateCumulative(2000, 400, 10); err != nil {
		t.Fatalf("UpdateCumulative() error = %v", err)
	}

	// Reopen the store and verify cumulative stats survive.
	reopened, err := Open(hash)
	if err != nil {
		t.Fatalf("Open(reopen) error = %v", err)
	}

	cs := reopened.LoadCumulative()
	if cs.TokensBefore != 2000 {
		t.Fatalf("reopened TokensBefore = %d, want 2000", cs.TokensBefore)
	}
	if cs.TokensAfter != 400 {
		t.Fatalf("reopened TokensAfter = %d, want 400", cs.TokensAfter)
	}
	if cs.ItemsCompressed != 10 {
		t.Fatalf("reopened ItemsCompressed = %d, want 10", cs.ItemsCompressed)
	}
}

func TestLoadCumulativeMissing(t *testing.T) {
	setTestDir(t)
	hash := "cumul_miss"

	store, err := Open(hash)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	// No cumulative.json exists yet — should return zeroes.
	cs := store.LoadCumulative()
	if cs.TokensBefore != 0 || cs.TokensAfter != 0 || cs.ItemsCompressed != 0 {
		t.Fatalf("expected zero stats for missing file, got %+v", cs)
	}
}

func TestLoadCumulativeCorrupt(t *testing.T) {
	setTestDir(t)
	hash := "cumul_bad"

	if err := os.MkdirAll(sessionDir(hash), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(cumulativePath(hash), []byte("{corrupt!}"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := Open(hash)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	// Corrupt cumulative.json — should return zeroes gracefully.
	cs := store.LoadCumulative()
	if cs.TokensBefore != 0 || cs.TokensAfter != 0 || cs.ItemsCompressed != 0 {
		t.Fatalf("expected zero stats for corrupt file, got %+v", cs)
	}
}

func TestNilStoreUpdateCumulative(t *testing.T) {
	var store *Store
	if err := store.UpdateCumulative(100, 50, 1); err != nil {
		t.Fatalf("nil store UpdateCumulative should return nil, got %v", err)
	}
	cs := store.LoadCumulative()
	if cs.TokensBefore != 0 {
		t.Fatalf("nil store LoadCumulative should return zero stats")
	}
}
