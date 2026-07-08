package runhistory_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Christophe1997/token-profile/internal/runhistory"
)

// TestAppend_ThenRead_RoundTrips covers the basic persistence contract: a
// record appended to a path that doesn't exist yet creates the file (and
// its parent directory) and reads back unchanged.
func TestAppend_ThenRead_RoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "history.json")
	rec := runhistory.Record{Timestamp: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC), Success: true}

	if err := runhistory.Append(path, rec); err != nil {
		t.Fatalf("Append() error = %v, want nil", err)
	}

	got, err := runhistory.Read(path)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != rec {
		t.Fatalf("Read() = %+v, want [%+v]", got, rec)
	}
}

// TestAppend_TrimsToDefaultLimit covers AE3: once more than DefaultLimit
// records have been appended, Read returns exactly DefaultLimit records
// with the oldest ones dropped.
func TestAppend_TrimsToDefaultLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	base := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)

	for i := range runhistory.DefaultLimit + 1 {
		rec := runhistory.Record{Timestamp: base.Add(time.Duration(i) * time.Hour), Success: true}
		if err := runhistory.Append(path, rec); err != nil {
			t.Fatalf("Append() #%d error = %v, want nil", i, err)
		}
	}

	got, err := runhistory.Read(path)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}
	if len(got) != runhistory.DefaultLimit {
		t.Fatalf("Read() returned %d records, want %d", len(got), runhistory.DefaultLimit)
	}
	wantOldest := base.Add(1 * time.Hour)
	if !got[0].Timestamp.Equal(wantOldest) {
		t.Errorf("Read()[0].Timestamp = %v, want %v (the very first append should have been dropped)", got[0].Timestamp, wantOldest)
	}
}

// TestRead_MissingFile_ReturnsEmptyNotError covers the data-layer half of
// R5's "no runs yet" contract: a path that has never been written to is not
// an error condition.
func TestRead_MissingFile_ReturnsEmptyNotError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")

	got, err := runhistory.Read(path)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("Read() = %+v, want empty", got)
	}
}

// TestAppend_RoundTripsSuccessAndFailureRecords covers both outcome shapes:
// a success record with no error text, and a failure record whose Error
// field survives the JSON round trip unchanged.
func TestAppend_RoundTripsSuccessAndFailureRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	ok := runhistory.Record{Timestamp: time.Date(2026, 7, 8, 6, 0, 0, 0, time.UTC), Success: true}
	fail := runhistory.Record{Timestamp: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC), Success: false, Error: "publishing: git push failed"}

	if err := runhistory.Append(path, ok); err != nil {
		t.Fatalf("Append(ok) error = %v, want nil", err)
	}
	if err := runhistory.Append(path, fail); err != nil {
		t.Fatalf("Append(fail) error = %v, want nil", err)
	}

	got, err := runhistory.Read(path)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}
	if len(got) != 2 || got[0] != ok || got[1] != fail {
		t.Fatalf("Read() = %+v, want [%+v %+v]", got, ok, fail)
	}
}

// TestAppend_UncreatableParentDir_ReturnsError covers the error path: when
// the parent directory can't be created (a plain file sits where a
// directory component is expected), Append fails loudly rather than
// silently dropping the record. The "never fails the caller" contract from
// R6 is verified at the internal/cli layer, not here — this package is
// allowed to fail; its caller isn't.
func TestAppend_UncreatableParentDir_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("seeding blocker file: %v", err)
	}
	path := filepath.Join(blocker, "history.json")

	err := runhistory.Append(path, runhistory.Record{Timestamp: time.Now(), Success: true})
	if err == nil {
		t.Fatal("Append() error = nil, want non-nil")
	}
}

// TestAppend_Read_UnmarshalError covers a corrupted history file: Read
// surfaces the decode error rather than silently treating malformed
// content as empty, so callers (status) can choose how to react.
func TestAppend_Read_UnmarshalError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("seeding corrupt file: %v", err)
	}

	_, err := runhistory.Read(path)
	if err == nil {
		t.Fatal("Read() error = nil, want non-nil")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("Read() error = %v, want a decode error, not ErrNotExist", err)
	}
}

// TestAppend_TwoSequentialCalls_BothSurvive is the integration scenario: a
// second Append call reads back what the first call wrote rather than
// clobbering it blind.
func TestAppend_TwoSequentialCalls_BothSurvive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	first := runhistory.Record{Timestamp: time.Date(2026, 7, 8, 6, 0, 0, 0, time.UTC), Success: true}
	second := runhistory.Record{Timestamp: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC), Success: true}

	if err := runhistory.Append(path, first); err != nil {
		t.Fatalf("Append(first) error = %v, want nil", err)
	}
	if err := runhistory.Append(path, second); err != nil {
		t.Fatalf("Append(second) error = %v, want nil", err)
	}

	got, err := runhistory.Read(path)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("Read() returned %d records, want 2 (second Append must not clobber the first)", len(got))
	}
}
