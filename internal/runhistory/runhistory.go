// Package runhistory persists a bounded, local record of each
// `token-profile run` invocation's outcome, so an adopter can later check
// whether their scheduled runs are actually succeeding. It has no
// awareness of the target repo or git — that's run.go's job — this
// package only reads and writes one machine-local JSON file.
package runhistory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultLimit is the number of most-recent records Append retains. Fixed
// rather than configurable — no current requirement to change it, and
// raising it later is a one-line, backward-compatible change.
const DefaultLimit = 20

// Record is one run invocation's recorded outcome.
type Record struct {
	Timestamp time.Time `json:"timestamp"`
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitzero"`
}

// Append reads path's existing records, appends rec, trims to the most
// recent DefaultLimit (oldest dropped first), and atomically writes the
// result back.
func Append(path string, rec Record) error {
	existing, err := Read(path)
	if err != nil {
		return fmt.Errorf("reading existing history %s: %w", path, err)
	}

	records := append(existing, rec)
	if len(records) > DefaultLimit {
		records = records[len(records)-DefaultLimit:]
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding history: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating history directory %s: %w", dir, err)
	}
	if err := writeFileAtomic(dir, path, data); err != nil {
		return fmt.Errorf("writing history %s: %w", path, err)
	}
	return nil
}

// Read decodes path's stored records, oldest first. A missing file returns
// an empty slice and a nil error, not an error — the store has no opinion
// on display order, that's the caller's job.
func Read(path string) ([]Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading history %s: %w", path, err)
	}

	var records []Record
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("decoding history %s: %w", path, err)
	}
	return records, nil
}

// writeFileAtomic writes data to path via a temp file created in dir
// followed by a rename, mirroring internal/snapshot's atomic-write pattern
// so a process killed mid-write leaves either the previous complete file
// or the new complete file — never a torn/partial one.
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
