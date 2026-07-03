package snapshot_test

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/Christophe1997/token-profile/internal/snapshot"
)

// TestWrite_ThenRead_RoundTrips covers the basic persistence contract: rows
// written for a machine can be read back unchanged (dates are already
// canonical YYYY-MM-DD, so normalization is a no-op here — see
// TestWrite_RejectsMalformedDate for the validation path).
func TestWrite_ThenRead_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	rows := []snapshot.Row{
		{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 100, Cost: 1.0},
		{Date: "2026-06-21", Agent: "codex", Model: "gpt-5.4", Tokens: 50, Cost: 0.5},
	}

	if err := snapshot.Write(dir, "machine-a", rows); err != nil {
		t.Fatalf("Write() error = %v, want nil", err)
	}

	got, err := snapshot.Read(dir, "machine-a")
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}
	if len(got) != len(rows) {
		t.Fatalf("Read() = %+v, want %+v", got, rows)
	}
	for i, want := range rows {
		if got[i] != want {
			t.Errorf("Read()[%d] = %+v, want %+v", i, got[i], want)
		}
	}
}

// TestWrite_PersistsUnderTargetRepoSnapshotsDir pins down the on-disk
// location: <targetRepo>/.token-profile/snapshots/<machineID>.json.
func TestWrite_PersistsUnderTargetRepoSnapshotsDir(t *testing.T) {
	dir := t.TempDir()
	rows := []snapshot.Row{{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 1, Cost: 0.1}}

	if err := snapshot.Write(dir, "machine-a", rows); err != nil {
		t.Fatalf("Write() error = %v, want nil", err)
	}

	want := filepath.Join(dir, ".token-profile", "snapshots", "machine-a.json")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected snapshot file at %s, stat error = %v", want, err)
	}
}

// TestWrite_RejectsMaliciousMachineID covers defense in depth (KTD6): even
// if a malformed machine id reaches this package some other way (bypassing
// machineid.Load's own validation), Write must reject anything that isn't a
// single clean path component rather than letting filepath.Join carry a
// "../../evil" style id outside the snapshots directory.
func TestWrite_RejectsMaliciousMachineID(t *testing.T) {
	dir := t.TempDir()
	rows := []snapshot.Row{{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 1, Cost: 0.1}}

	for _, machineID := range []string{"../../evil", "sub/dir", `sub\dir`, ".."} {
		t.Run(machineID, func(t *testing.T) {
			if err := snapshot.Write(dir, machineID, rows); err == nil {
				t.Fatalf("Write(%q) error = nil, want an error rejecting the malicious machine id", machineID)
			}
		})
	}

	// "../../evil" resolves to <dir>/evil.json (two levels above the
	// snapshots dir lands back at dir itself) — still inside the test's own
	// tempdir, so safe to assert against, and it must NOT have been created.
	if _, err := os.Stat(filepath.Join(dir, "evil.json")); !os.IsNotExist(err) {
		t.Errorf("Write() must not write outside the snapshots directory; stat(%s) error = %v", filepath.Join(dir, "evil.json"), err)
	}
}

// TestRead_RejectsMaliciousMachineID mirrors TestWrite_RejectsMaliciousMachineID
// for the read path.
func TestRead_RejectsMaliciousMachineID(t *testing.T) {
	dir := t.TempDir()
	if _, err := snapshot.Read(dir, "../../evil"); err == nil {
		t.Fatal("Read() error = nil, want an error rejecting the malicious machine id")
	}
}

// TestWrite_NoStrayTempFilesLeftBehind covers the atomic-write fix: after a
// successful Write, the snapshots directory must contain only the final
// <machine-id>.json file — no leftover temp file from the write-then-rename
// sequence.
func TestWrite_NoStrayTempFilesLeftBehind(t *testing.T) {
	dir := t.TempDir()
	rows := []snapshot.Row{{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 1, Cost: 0.1}}

	if err := snapshot.Write(dir, "machine-a", rows); err != nil {
		t.Fatalf("Write() error = %v, want nil", err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, ".token-profile", "snapshots"))
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	if len(names) != 1 || names[0] != "machine-a.json" {
		t.Errorf("snapshots dir contents = %v, want only [machine-a.json]", names)
	}
}

// TestWrite_RejectsMalformedDate covers proactive validation: a row whose
// Date isn't a parseable calendar date must fail Write rather than being
// silently persisted and corrupting later merges.
func TestWrite_RejectsMalformedDate(t *testing.T) {
	dir := t.TempDir()
	rows := []snapshot.Row{{Date: "not-a-date", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 1, Cost: 0.1}}

	if err := snapshot.Write(dir, "machine-a", rows); err == nil {
		t.Fatal("Write() error = nil, want an error for a malformed date")
	}
}

// mergedRow finds the row in ds matching (date, agent, model), failing the
// test if it's absent.
func mergedRow(t *testing.T, ds snapshot.MergedDataset, date, agent, model string) snapshot.Row {
	t.Helper()
	for _, r := range ds.Rows {
		if r.Date == date && r.Agent == agent && r.Model == model {
			return r
		}
	}
	t.Fatalf("MergedDataset has no row for (date=%s, agent=%s, model=%s); rows = %+v", date, agent, model, ds.Rows)
	return snapshot.Row{}
}

// TestMerge_HappyPath_TwoMachinesCombinedTotals covers the core union
// behavior: two different machines' contributions to the same (date, agent,
// model) bucket must both count, summed together.
func TestMerge_HappyPath_TwoMachinesCombinedTotals(t *testing.T) {
	dir := t.TempDir()

	if err := snapshot.Write(dir, "machine-a", []snapshot.Row{
		{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 100, Cost: 1.0},
	}); err != nil {
		t.Fatalf("Write(machine-a) error = %v, want nil", err)
	}
	if err := snapshot.Write(dir, "machine-b", []snapshot.Row{
		{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 50, Cost: 0.5},
	}); err != nil {
		t.Fatalf("Write(machine-b) error = %v, want nil", err)
	}

	ds, err := snapshot.Merge(dir)
	if err != nil {
		t.Fatalf("Merge() error = %v, want nil", err)
	}

	got := mergedRow(t, ds, "2026-06-20", "claude-code", "claude-sonnet-5")
	if want := (snapshot.Row{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 150, Cost: 1.5}); got != want {
		t.Errorf("merged row = %+v, want %+v (summed across both machines)", got, want)
	}
}

// TestMerge_StaleSnapshot_ContributesHistoricalRows covers AE2: a machine
// whose snapshot only holds old-dated rows (i.e. it hasn't run in 40+ days)
// must still have those historical rows counted — inactivity doesn't zero
// out or drop a machine's prior contribution.
func TestMerge_StaleSnapshot_ContributesHistoricalRows(t *testing.T) {
	dir := t.TempDir()

	staleDate := "2026-05-01" // 40+ days before the fixture "today" of 2026-06-20+
	if err := snapshot.Write(dir, "stale-machine", []snapshot.Row{
		{Date: staleDate, Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 200, Cost: 2.0},
	}); err != nil {
		t.Fatalf("Write(stale-machine) error = %v, want nil", err)
	}
	if err := snapshot.Write(dir, "active-machine", []snapshot.Row{
		{Date: "2026-06-20", Agent: "codex", Model: "gpt-5.4", Tokens: 10, Cost: 0.1},
	}); err != nil {
		t.Fatalf("Write(active-machine) error = %v, want nil", err)
	}

	ds, err := snapshot.Merge(dir)
	if err != nil {
		t.Fatalf("Merge() error = %v, want nil", err)
	}

	got := mergedRow(t, ds, staleDate, "claude-code", "claude-sonnet-5")
	if want := (snapshot.Row{Date: staleDate, Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 200, Cost: 2.0}); got != want {
		t.Errorf("stale machine's row = %+v, want %+v (its inactivity must not zero out its prior contribution)", got, want)
	}
}

// TestMerge_MachineRerun_SecondWriteReplacesFirst covers the re-run
// overwrite semantics: Write always fully replaces a machine's file with
// its current complete dataset, so calling Write twice for the same machine
// with different row sets must leave only the second write's data in the
// merge — never a sum of both writes.
func TestMerge_MachineRerun_SecondWriteReplacesFirst(t *testing.T) {
	dir := t.TempDir()

	if err := snapshot.Write(dir, "machine-a", []snapshot.Row{
		{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 100, Cost: 1.0},
	}); err != nil {
		t.Fatalf("first Write() error = %v, want nil", err)
	}
	if err := snapshot.Write(dir, "machine-a", []snapshot.Row{
		{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 130, Cost: 1.3},
	}); err != nil {
		t.Fatalf("second Write() error = %v, want nil", err)
	}

	ds, err := snapshot.Merge(dir)
	if err != nil {
		t.Fatalf("Merge() error = %v, want nil", err)
	}

	got := mergedRow(t, ds, "2026-06-20", "claude-code", "claude-sonnet-5")
	if want := (snapshot.Row{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 130, Cost: 1.3}); got != want {
		t.Errorf("merged row = %+v, want %+v (only the second write's data, not summed with the first)", got, want)
	}
}

// TestMerge_CorruptedFileSkippedWithWarning covers the error path: a
// snapshot file that fails to parse must be skipped with a logged warning,
// while every other valid snapshot still merges successfully rather than
// the whole run aborting.
func TestMerge_CorruptedFileSkippedWithWarning(t *testing.T) {
	dir := t.TempDir()

	if err := snapshot.Write(dir, "good-machine", []snapshot.Row{
		{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 100, Cost: 1.0},
	}); err != nil {
		t.Fatalf("Write(good-machine) error = %v, want nil", err)
	}

	corruptPath := filepath.Join(dir, ".token-profile", "snapshots", "corrupt-machine.json")
	if err := os.WriteFile(corruptPath, []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("WriteFile(corrupt) error = %v, want nil", err)
	}

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	ds, err := snapshot.Merge(dir)
	if err != nil {
		t.Fatalf("Merge() error = %v, want nil (corrupted files should be skipped, not abort the merge)", err)
	}

	got := mergedRow(t, ds, "2026-06-20", "claude-code", "claude-sonnet-5")
	if want := (snapshot.Row{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 100, Cost: 1.0}); got != want {
		t.Errorf("good machine's row = %+v, want %+v (unaffected by the corrupted sibling file)", got, want)
	}

	if logBuf.Len() == 0 {
		t.Error("Merge() logged nothing, want a warning about the corrupted snapshot file")
	}
}
