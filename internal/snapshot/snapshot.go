// Package snapshot persists each machine's resolved usage history to a
// per-machine file and merges every machine's snapshot present in a shared
// target repo into aggregate totals. Git (outside this package's concern,
// see U6) is the transport that carries these files between machines; this
// package only handles the local read/write/merge logic.
package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// validateMachineID rejects a machineID that isn't a clean, single path
// component. This is defense in depth alongside machineid.Load's own format
// validation: if a malformed id (path separators, "..") ever reached
// snapshotPath some other way, filepath.Join would resolve outside the
// snapshots directory (KTD6 path traversal).
func validateMachineID(machineID string) error {
	if machineID == "" {
		return errors.New("machine id must not be empty")
	}
	if strings.ContainsAny(machineID, `/\`) || strings.Contains(machineID, "..") {
		return fmt.Errorf("invalid machine id %q: must not contain path separators or \"..\"", machineID)
	}
	return nil
}

// Write persists rows as machineID's current resolve, merged into whatever
// snapshot already exists for this machine (see mergeRowsByKey): fresh rows
// override any existing row sharing a (date, agent, model) key — so
// re-running on the same day naturally overwrites rather than double-counts
// — while rows for days the current resolve no longer covers are preserved,
// so this machine's history accumulates across runs instead of rolling off
// with the trailing window.
func Write(targetRepo, machineID string, rows []Row) error {
	if err := validateMachineID(machineID); err != nil {
		return fmt.Errorf("writing snapshot: %w", err)
	}

	normalized := make([]Row, len(rows))
	for i, r := range rows {
		date, err := normalizeDate(r.Date)
		if err != nil {
			return fmt.Errorf("row %d: %w", i, err)
		}
		r.Date = date
		if r.Tokens < 0 || r.Cost < 0 {
			return fmt.Errorf("row %d: negative tokens (%d) or cost (%g) — a corrupted or malformed source value, not real usage", i, r.Tokens, r.Cost)
		}
		normalized[i] = r
	}

	dir := snapshotsDir(targetRepo)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating snapshots directory %s: %w", dir, err)
	}

	path := snapshotPath(targetRepo, machineID)
	existing, err := readSnapshotFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		// Deliberately fails rather than treating an unreadable file as
		// empty: merging fresh rows over a silently-emptied existing file
		// would permanently erase whatever history was still intact, unlike
		// Merge's read-time tolerance (skip one bad file out of many),
		// which risks nothing since it never writes anything back.
		return fmt.Errorf("reading existing snapshot %s: %w", path, err)
	}

	merged := mergeRowsByKey(existing, normalized)

	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding snapshot for machine %s: %w", machineID, err)
	}

	if err := writeFileAtomic(dir, path, data); err != nil {
		return fmt.Errorf("writing snapshot %s: %w", path, err)
	}
	return nil
}

// writeFileAtomic writes data to path via a temp file created in dir
// followed by a rename, so a process killed mid-write leaves either the
// previous complete file or the new complete file — never a torn/partial
// one (merge.go tolerates a corrupted file gracefully, but this avoids the
// data gap entirely rather than relying on that fallback).
func writeFileAtomic(dir, path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if err != nil {
			os.Remove(tmpPath)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// Read decodes machineID's snapshot file under targetRepo into rows.
func Read(targetRepo, machineID string) ([]Row, error) {
	if err := validateMachineID(machineID); err != nil {
		return nil, fmt.Errorf("reading snapshot: %w", err)
	}

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
