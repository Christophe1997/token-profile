package machineid

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPersist_LoserReturnsWinnerID deterministically simulates the exact
// interleaving behind the TOCTOU race (KTD6): two processes both see the
// path missing and both generate an id, but only one of them can win the
// first-write-wins race in persist. Calling persist twice in sequence with
// two different ids for the same path reproduces that interleaving without
// goroutines: the first call is the winner (creates the file), the second
// is the loser and must return the winner's id — the one actually on disk —
// rather than silently keeping its own, which would otherwise leave two
// different machine ids for the same machine.
func TestPersist_LoserReturnsWinnerID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "machine-id")

	winnerID, err := generate()
	if err != nil {
		t.Fatalf("generate() error = %v", err)
	}
	got, err := persist(path, winnerID)
	if err != nil {
		t.Fatalf("persist() (winner) error = %v, want nil", err)
	}
	if got != winnerID {
		t.Fatalf("persist() (winner) = %q, want %q", got, winnerID)
	}

	loserID, err := generate()
	if err != nil {
		t.Fatalf("generate() error = %v", err)
	}
	if loserID == winnerID {
		t.Fatal("test invariant violated: generate() produced two equal ids")
	}

	got, err = persist(path, loserID)
	if err != nil {
		t.Fatalf("persist() (loser) error = %v, want nil", err)
	}
	if got != winnerID {
		t.Errorf("persist() (loser) = %q, want winner's id %q (first write wins)", got, winnerID)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	if string(data) != winnerID {
		t.Errorf("file content = %q, want winner's id %q (loser must not overwrite it)", data, winnerID)
	}
}
