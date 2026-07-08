package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Christophe1997/token-profile/internal/runhistory"
)

// TestStatus_RegisteredWithHistory_PrintsBothMostRecentFirst covers the
// happy path: schedule registered plus two history records prints both the
// registration line and both records, most-recent-first.
func TestStatus_RegisteredWithHistory_PrintsBothMostRecentFirst(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state")
	capturePath := filepath.Join(dir, "capture")
	bin := fakeLaunchctlBinary(t, statePath, capturePath)
	if err := os.WriteFile(statePath, nil, 0o644); err != nil {
		t.Fatalf("seeding registered state: %v", err)
	}

	historyPath := filepath.Join(dir, "history.json")
	older := runhistory.Record{Timestamp: time.Date(2026, 7, 8, 6, 0, 0, 0, time.UTC), Success: true}
	newer := runhistory.Record{Timestamp: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC), Success: true}
	mustAppend(t, historyPath, older)
	mustAppend(t, historyPath, newer)

	var out bytes.Buffer
	deps := StatusDeps{
		Schedule:    ScheduleDeps{GOOS: "darwin", Label: "dev.token-profile.refresh", Launchctl: bin},
		HistoryPath: historyPath,
		Stdout:      &out,
	}

	if err := Status(t.Context(), deps); err != nil {
		t.Fatalf("Status() error = %v, want nil", err)
	}

	got := out.String()
	if !strings.Contains(got, "schedule: registered") || strings.Contains(got, "not registered") {
		t.Errorf("output = %q, want it to report the schedule as registered (not \"not registered\")", got)
	}
	newerIdx := strings.Index(got, newer.Timestamp.Format(time.RFC3339))
	olderIdx := strings.Index(got, older.Timestamp.Format(time.RFC3339))
	if newerIdx == -1 || olderIdx == -1 {
		t.Fatalf("output = %q, want both record timestamps present", got)
	}
	if newerIdx > olderIdx {
		t.Errorf("output = %q, want the newer record printed before the older one (most-recent-first)", got)
	}
}

// TestStatus_NoHistory_PrintsNoRunsYet covers AE1: no history file present
// at all prints the explicit "no runs yet" message and Status returns nil.
func TestStatus_NoHistory_PrintsNoRunsYet(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state")
	capturePath := filepath.Join(dir, "capture")
	bin := fakeLaunchctlBinary(t, statePath, capturePath)

	var out bytes.Buffer
	deps := StatusDeps{
		Schedule:    ScheduleDeps{GOOS: "darwin", Label: "dev.token-profile.refresh", Launchctl: bin},
		HistoryPath: filepath.Join(dir, "history.json"),
		Stdout:      &out,
	}

	if err := Status(t.Context(), deps); err != nil {
		t.Fatalf("Status() error = %v, want nil", err)
	}

	got := out.String()
	if !strings.Contains(got, "no runs") {
		t.Errorf("output = %q, want an explicit \"no runs\" message", got)
	}
	if !strings.Contains(got, "not registered") {
		t.Errorf("output = %q, want it to report the schedule as not registered", got)
	}
}

// TestStatus_NotRegisteredWithHistory_ReportsBothIndependently covers the
// case where the schedule and the history disagree: not registered plus
// history present prints "not registered" alongside the history — the two
// facts are reported independently.
func TestStatus_NotRegisteredWithHistory_ReportsBothIndependently(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state")
	capturePath := filepath.Join(dir, "capture")
	bin := fakeLaunchctlBinary(t, statePath, capturePath)

	historyPath := filepath.Join(dir, "history.json")
	mustAppend(t, historyPath, runhistory.Record{Timestamp: time.Date(2026, 7, 8, 6, 0, 0, 0, time.UTC), Success: true})

	var out bytes.Buffer
	deps := StatusDeps{
		Schedule:    ScheduleDeps{GOOS: "darwin", Label: "dev.token-profile.refresh", Launchctl: bin},
		HistoryPath: historyPath,
		Stdout:      &out,
	}

	if err := Status(t.Context(), deps); err != nil {
		t.Fatalf("Status() error = %v, want nil", err)
	}

	got := out.String()
	if !strings.Contains(got, "not registered") {
		t.Errorf("output = %q, want it to report the schedule as not registered", got)
	}
	if !strings.Contains(got, "2026-07-08T06:00:00Z") {
		t.Errorf("output = %q, want the recorded run to still be shown", got)
	}
}

// TestStatus_ScheduleCheckFailed_ReportsCheckFailed_StillExitsNil covers
// KTD5: CheckScheduleState returning ScheduleCheckFailed prints "check
// failed" as the registration line and Status still returns nil.
func TestStatus_ScheduleCheckFailed_ReportsCheckFailed_StillExitsNil(t *testing.T) {
	dir := t.TempDir()
	capturePath := filepath.Join(dir, "capture")
	bin := fakeLaunchctlBinaryAlwaysFails(t, capturePath)

	var out bytes.Buffer
	deps := StatusDeps{
		Schedule:    ScheduleDeps{GOOS: "darwin", Label: "dev.token-profile.refresh", Launchctl: bin},
		HistoryPath: filepath.Join(dir, "history.json"),
		Stdout:      &out,
	}

	if err := Status(t.Context(), deps); err != nil {
		t.Fatalf("Status() error = %v, want nil (a failed live check is reported as data, not a command failure)", err)
	}

	got := out.String()
	if !strings.Contains(got, "check failed") {
		t.Errorf("output = %q, want it to report \"check failed\"", got)
	}
	if !strings.Contains(got, "no runs recorded yet") {
		t.Errorf("output = %q, want it to still report \"no runs recorded yet\" alongside the check-failed schedule line", got)
	}
}

// TestStatus_FailedRecord_PrintsErrorTextVerbatim is the integration
// scenario: a record with Success:false and a populated Error prints that
// error text verbatim, not a generic "failed".
func TestStatus_FailedRecord_PrintsErrorTextVerbatim(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state")
	capturePath := filepath.Join(dir, "capture")
	bin := fakeLaunchctlBinary(t, statePath, capturePath)

	historyPath := filepath.Join(dir, "history.json")
	mustAppend(t, historyPath, runhistory.Record{
		Timestamp: time.Date(2026, 7, 8, 6, 0, 0, 0, time.UTC),
		Success:   false,
		Error:     "publishing: git push failed: connection refused",
	})

	var out bytes.Buffer
	deps := StatusDeps{
		Schedule:    ScheduleDeps{GOOS: "darwin", Label: "dev.token-profile.refresh", Launchctl: bin},
		HistoryPath: historyPath,
		Stdout:      &out,
	}

	if err := Status(t.Context(), deps); err != nil {
		t.Fatalf("Status() error = %v, want nil", err)
	}

	got := out.String()
	if !strings.Contains(got, "publishing: git push failed: connection refused") {
		t.Errorf("output = %q, want the record's exact error text", got)
	}
}

// TestStatus_CorruptedHistory_ReportsUnavailable_StillExitsNil extends
// KTD5's treatment to a corrupted history file: a runhistory.Read error is
// printed as a non-fatal "history unavailable" line and Status still
// returns nil, rather than propagating the error.
func TestStatus_CorruptedHistory_ReportsUnavailable_StillExitsNil(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state")
	capturePath := filepath.Join(dir, "capture")
	bin := fakeLaunchctlBinary(t, statePath, capturePath)

	historyPath := filepath.Join(dir, "history.json")
	if err := os.WriteFile(historyPath, []byte("not json"), 0o644); err != nil {
		t.Fatalf("seeding corrupt history file: %v", err)
	}

	var out bytes.Buffer
	deps := StatusDeps{
		Schedule:    ScheduleDeps{GOOS: "darwin", Label: "dev.token-profile.refresh", Launchctl: bin},
		HistoryPath: historyPath,
		Stdout:      &out,
	}

	if err := Status(t.Context(), deps); err != nil {
		t.Fatalf("Status() error = %v, want nil (an unreadable history file is reported as data, not a command failure)", err)
	}

	got := out.String()
	if !strings.Contains(got, "history unavailable") {
		t.Errorf("output = %q, want a \"history unavailable\" line", got)
	}
}

// TestStatus_NilStdout_DoesNotPanic covers the fix: a nil Stdout must
// silently discard the report, mirroring RunDeps.Stdout's own
// nil-is-a-no-op convention, rather than panicking on the first Fprintf.
func TestStatus_NilStdout_DoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state")
	capturePath := filepath.Join(dir, "capture")
	bin := fakeLaunchctlBinary(t, statePath, capturePath)
	if err := os.WriteFile(statePath, nil, 0o644); err != nil {
		t.Fatalf("seeding registered state: %v", err)
	}

	deps := StatusDeps{
		Schedule:    ScheduleDeps{GOOS: "darwin", Label: "dev.token-profile.refresh", Launchctl: bin},
		HistoryPath: filepath.Join(dir, "history.json"),
	}

	if err := Status(t.Context(), deps); err != nil {
		t.Fatalf("Status() error = %v, want nil", err)
	}
}

// mustAppend is a small test helper wrapping runhistory.Append with
// t.Fatalf on error, since most Status tests need to seed a history file
// without asserting on the write itself.
func mustAppend(t *testing.T, path string, rec runhistory.Record) {
	t.Helper()
	if err := runhistory.Append(path, rec); err != nil {
		t.Fatalf("runhistory.Append() error = %v, want nil", err)
	}
}
