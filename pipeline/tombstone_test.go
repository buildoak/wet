package pipeline

import "testing"

func TestCreateTombstone(t *testing.T) {
	got := CreateTombstone("git_status", "summary", 1, 3, 1000, 250)
	want := "[compressed: git_status | summary | turn 1/3 | 1000->250 tokens]"
	if got != want {
		t.Fatalf("CreateTombstone mismatch\nwant: %s\ngot:  %s", want, got)
	}
}

func TestIsTombstone(t *testing.T) {
	if !IsTombstone("[compressed: git_status | summary | turn 1/3 | 1000->250 tokens]") {
		t.Fatal("expected tombstone prefix to be detected")
	}
	if IsTombstone("normal text") {
		t.Fatal("did not expect normal text to be a tombstone")
	}
	if !IsTombstone("  [compressed: npm | summary | turn 1/2 | 500->100 tokens]") {
		t.Fatal("expected tombstone prefix to be detected after trimming")
	}
}
