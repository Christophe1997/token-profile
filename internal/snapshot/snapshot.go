// Package snapshot persists each machine's resolved usage history to a
// per-machine file and merges every machine's snapshot present in a shared
// target repo into aggregate totals. Git (outside this package's concern,
// see U6) is the transport that carries these files between machines; this
// package only handles the local read/write/merge logic.
package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Row is one (date, agent, model) usage observation. It intentionally
// mirrors agentsview.Row's shape rather than importing that package, so
// snapshot stays a generic persistence/merge concern decoupled from
// agentsview's specific types; callers convert at the call site.
type Row struct {
	Date   string  `json:"date"`
	Agent  string  `json:"agent"`
	Model  string  `json:"model"`
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
}

// snapshotsDir returns the directory holding every machine's snapshot file
// under targetRepo.
func snapshotsDir(targetRepo string) string {
	return filepath.Join(targetRepo, ".token-profile", "snapshots")
}

// snapshotPath returns machineID's snapshot file path under targetRepo.
func snapshotPath(targetRepo, machineID string) string {
	return filepath.Join(snapshotsDir(targetRepo), machineID+".json")
}

// Write persists rows as machineID's complete current snapshot under
// targetRepo, fully replacing any prior snapshot for this machine. Resolve
// always produces this machine's complete window each run, so a full
// replace (rather than an append/delta) is correct: re-running on the same
// day naturally overwrites rather than double-counts (see merge.go).
func Write(targetRepo, machineID string, rows []Row) error {
	normalized := make([]Row, len(rows))
	for i, r := range rows {
		date, err := normalizeDate(r.Date)
		if err != nil {
			return fmt.Errorf("row %d: %w", i, err)
		}
		r.Date = date
		normalized[i] = r
	}

	dir := snapshotsDir(targetRepo)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating snapshots directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding snapshot for machine %s: %w", machineID, err)
	}

	path := snapshotPath(targetRepo, machineID)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing snapshot %s: %w", path, err)
	}
	return nil
}

// Read decodes machineID's snapshot file under targetRepo into rows.
func Read(targetRepo, machineID string) ([]Row, error) {
	path := snapshotPath(targetRepo, machineID)
	rows, err := readSnapshotFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading snapshot %s: %w", path, err)
	}
	return rows, nil
}

// readSnapshotFile decodes a single snapshot file's rows, shared by Read
// (one known-good machine) and Merge (every file, tolerating failures).
func readSnapshotFile(path string) ([]Row, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows []Row
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// normalizeDate validates date as a bare calendar date and returns its
// canonical form (KTD5).
//
// Design note: agentsview already buckets daily usage by its own
// --timezone flag into a plain "YYYY-MM-DD" string with no time-of-day or
// UTC-offset component (confirmed against every DailyRow fixture in
// internal/agentsview) — there is no embedded zone to shift here. Parsing
// with time.DateOnly (which time.Parse defaults to UTC when the layout
// carries no zone) and reformatting is therefore validation, not
// conversion: it guards against agentsview ever emitting a different shape
// and guarantees a canonical form on disk, satisfying KTD5's "normalized to
// UTC" requirement by construction rather than by an actual timezone
// shift.
func normalizeDate(date string) (string, error) {
	t, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return "", fmt.Errorf("invalid date %q (want %s): %w", date, time.DateOnly, err)
	}
	return t.Format(time.DateOnly), nil
}
