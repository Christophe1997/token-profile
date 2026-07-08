package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

// cleanupWorkdir builds a git working tree with token-profile's on-disk
// footprint already committed — README.md scaffolded with the marker pair
// and injected card content, plus a couple of files under .token-profile/
// — mirroring what at least one successful `run`/`init` would have left
// behind.
func cleanupWorkdir(t *testing.T) string {
	t.Helper()
	remote := initBareRemote(t)
	seedRemote(t, remote, "# username\n")
	dir := cloneWorkdir(t, remote, "cleanup-work")

	writeFile(t, dir, readmeFile, "# username\n\n<!-- token-profile:start -->\ncard content here\n<!-- token-profile:end -->\n")
	writeNestedFile(t, dir, filepath.Join(".token-profile", "snapshots", "machine-a", "2026-06.json"), `{"rows":[]}`)
	writeNestedFile(t, dir, filepath.Join(".token-profile", "card-light.svg"), "<svg/>")
	runGitT(t, dir, "add", "README.md", ".token-profile")
	runGitT(t, dir, "commit", "-q", "-m", "seed footprint")
	return dir
}

// writeNestedFile writes a fixture file at a possibly-nested path inside a
// test working directory, creating any missing parent directories first
// (unlike writeFile, which assumes dir itself already exists).
func writeNestedFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	path := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", relPath, err)
	}
}

// registeredLaunchdDeps builds a ScheduleDeps exercising the launchctl
// branch against a fixture that reports the job as already registered,
// returning the capture file path so a test can assert exactly which
// launchctl subcommands were invoked.
func registeredLaunchdDeps(t *testing.T) (ScheduleDeps, string) {
	t.Helper()
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state")
	capturePath := filepath.Join(dir, "capture")
	if err := os.WriteFile(statePath, []byte("registered"), 0o644); err != nil {
		t.Fatalf("WriteFile(state) error = %v", err)
	}
	bin := fakeLaunchctlBinary(t, statePath, capturePath)
	return ScheduleDeps{GOOS: "darwin", Label: "dev.token-profile.refresh", Launchctl: bin}, capturePath
}

// unregisteredLaunchdDeps mirrors registeredLaunchdDeps for a fixture that
// reports the job as never registered.
func unregisteredLaunchdDeps(t *testing.T) (ScheduleDeps, string) {
	t.Helper()
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state") // never created -> not registered
	capturePath := filepath.Join(dir, "capture")
	bin := fakeLaunchctlBinary(t, statePath, capturePath)
	return ScheduleDeps{GOOS: "darwin", Label: "dev.token-profile.refresh", Launchctl: bin}, capturePath
}

// TestCleanup_HappyPath_ScheduleRemovedReadmeStrippedDirDeleted covers the
// primary flow: a registered schedule and a valid repo with its footprint
// present — confirmation shown, schedule removed, README stripped,
// .token-profile/ deleted, working tree left uncommitted for the Adopter to
// review (R10-R14).
func TestCleanup_HappyPath_ScheduleRemovedReadmeStrippedDirDeleted(t *testing.T) {
	dir := cleanupWorkdir(t)
	schedule, capturePath := registeredLaunchdDeps(t)
	var out bytes.Buffer

	result, err := Cleanup(t.Context(), CleanupDeps{
		RepoDir:     dir,
		Schedule:    schedule,
		Interactive: true,
		Accessible:  true,
		Input:       scriptedInput("y"),
		Output:      &out,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v, want nil", err)
	}
	if result.Declined {
		t.Error("Cleanup() result.Declined = true, want false")
	}
	if !result.RepoValid {
		t.Error("Cleanup() result.RepoValid = false, want true")
	}
	if result.Schedule != ScheduleRegistered {
		t.Errorf("Cleanup() result.Schedule = %v, want ScheduleRegistered (found registered, then removed)", result.Schedule)
	}
	if !result.ReadmeStripped {
		t.Error("Cleanup() result.ReadmeStripped = false, want true")
	}
	if !result.DirRemoved {
		t.Error("Cleanup() result.DirRemoved = false, want true")
	}

	readmeGot, err := os.ReadFile(filepath.Join(dir, readmeFile))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	if strings.Contains(string(readmeGot), "card content here") {
		t.Errorf("README.md = %q, want card content cleared", readmeGot)
	}
	if !strings.Contains(string(readmeGot), "<!-- token-profile:start -->") {
		t.Errorf("README.md = %q, want marker lines left in place", readmeGot)
	}

	if _, statErr := os.Stat(filepath.Join(dir, ".token-profile")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("Stat(.token-profile) error = %v, want os.ErrNotExist after cleanup", statErr)
	}

	sawBootout := false
	for _, call := range readCaptureFile(t, capturePath) {
		if strings.HasPrefix(call, "bootout ") {
			sawBootout = true
		}
	}
	if !sawBootout {
		t.Error("launchctl bootout never invoked, want the registered schedule to be deregistered")
	}

	status := runGitT(t, dir, "status", "--porcelain")
	if !strings.Contains(status, "README.md") {
		t.Errorf("git status --porcelain = %q, want README.md listed as modified (working tree left uncommitted)", status)
	}
	if !strings.Contains(status, ".token-profile") {
		t.Errorf("git status --porcelain = %q, want .token-profile listed as deleted (working tree left uncommitted)", status)
	}
}

// TestCleanup_ScheduleAlreadyAbsent_StillProceedsWithRepoCleanup covers the
// edge case where no schedule was ever installed: cleanup must report
// nothing-to-remove for the schedule (never invoking bootout) while still
// performing the repo-side steps.
func TestCleanup_ScheduleAlreadyAbsent_StillProceedsWithRepoCleanup(t *testing.T) {
	dir := cleanupWorkdir(t)
	schedule, capturePath := unregisteredLaunchdDeps(t)
	var out bytes.Buffer

	result, err := Cleanup(t.Context(), CleanupDeps{
		RepoDir:     dir,
		Schedule:    schedule,
		Interactive: true,
		Accessible:  true,
		Input:       scriptedInput("y"),
		Output:      &out,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v, want nil", err)
	}
	if result.Schedule != ScheduleNotRegistered {
		t.Errorf("Cleanup() result.Schedule = %v, want ScheduleNotRegistered", result.Schedule)
	}
	if !result.ReadmeStripped || !result.DirRemoved {
		t.Errorf("Cleanup() result = %+v, want repo-side cleanup to still proceed", result)
	}

	for _, call := range readCaptureFile(t, capturePath) {
		if strings.HasPrefix(call, "bootout ") {
			t.Errorf("launchctl bootout invoked (%q) though nothing was registered", call)
		}
	}
}

// TestCleanup_RerunOnAlreadyCleanedRepo_NoOp covers AE5: running cleanup a
// second time against an already-cleaned repo must report nothing-to-remove
// for every piece, without erroring.
func TestCleanup_RerunOnAlreadyCleanedRepo_NoOp(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, "# username\n\n<!-- token-profile:start -->\n<!-- token-profile:end -->\n")
	dir := cloneWorkdir(t, remote, "already-clean")
	schedule, _ := unregisteredLaunchdDeps(t)
	var out bytes.Buffer

	result, err := Cleanup(t.Context(), CleanupDeps{
		RepoDir:     dir,
		Schedule:    schedule,
		Interactive: true,
		Accessible:  true,
		Input:       scriptedInput("y"),
		Output:      &out,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v, want nil", err)
	}
	if result.Schedule != ScheduleNotRegistered {
		t.Errorf("result.Schedule = %v, want ScheduleNotRegistered", result.Schedule)
	}
	if result.ReadmeStripped {
		t.Error("result.ReadmeStripped = true, want false (README already clean)")
	}
	if result.DirRemoved {
		t.Error("result.DirRemoved = true, want false (.token-profile/ already absent)")
	}
	if !result.RepoValid {
		t.Error("result.RepoValid = false, want true")
	}
}

// TestCleanup_TargetRepoMissing_ScheduleStillDeregistered_NoDirResurrected
// covers the edge case where targetRepo points at a path that doesn't exist
// at all: schedule deregistration must still happen, lock acquisition must
// be skipped entirely, and RepoDir must never be created as a side effect
// (KTD5, KTD6).
func TestCleanup_TargetRepoMissing_ScheduleStillDeregistered_NoDirResurrected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	schedule, capturePath := registeredLaunchdDeps(t)
	var out bytes.Buffer

	result, err := Cleanup(t.Context(), CleanupDeps{
		RepoDir:     dir,
		Schedule:    schedule,
		Interactive: true,
		Accessible:  true,
		Input:       scriptedInput("y"),
		Output:      &out,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v, want nil", err)
	}
	if result.RepoValid {
		t.Error("result.RepoValid = true, want false")
	}
	if result.Schedule != ScheduleRegistered {
		t.Errorf("result.Schedule = %v, want ScheduleRegistered (deregistered despite missing repo)", result.Schedule)
	}
	if result.ReadmeStripped || result.DirRemoved {
		t.Errorf("result = %+v, want repo-side steps reported as a no-op", result)
	}
	if _, statErr := os.Stat(dir); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("Stat(RepoDir) error = %v, want os.ErrNotExist (must not be resurrected by lock acquisition)", statErr)
	}

	sawBootout := false
	for _, call := range readCaptureFile(t, capturePath) {
		if strings.HasPrefix(call, "bootout ") {
			sawBootout = true
		}
	}
	if !sawBootout {
		t.Error("launchctl bootout never invoked, want schedule deregistration regardless of the missing repo")
	}
}

// TestCleanup_TargetRepoNotGitRepo_NoOp covers the other "corrupted
// targetRepo" shape: an existing directory that isn't a git working tree.
// Repo-side steps must degrade to a no-op without ever acquiring the
// run-lock (which would otherwise scaffold a .token-profile.lock file into
// an unrelated directory).
func TestCleanup_TargetRepoNotGitRepo_NoOp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "somefile"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	schedule, _ := unregisteredLaunchdDeps(t)
	var out bytes.Buffer

	result, err := Cleanup(t.Context(), CleanupDeps{
		RepoDir:     dir,
		Schedule:    schedule,
		Interactive: true,
		Accessible:  true,
		Input:       scriptedInput("y"),
		Output:      &out,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v, want nil", err)
	}
	if result.RepoValid {
		t.Error("result.RepoValid = true, want false (not a git repository)")
	}
	if result.ReadmeStripped || result.DirRemoved {
		t.Errorf("result = %+v, want repo-side steps reported as a no-op", result)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".token-profile.lock")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("Stat(.token-profile.lock) error = %v, want os.ErrNotExist (lock never acquired against an invalid repo)", statErr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(dir) error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "somefile" {
		t.Errorf("ReadDir(dir) = %v, want only the original somefile (nothing created)", entries)
	}
}

// removeOnReadReader wraps r, os.RemoveAll'ing target the first time Read
// is called — used to simulate a filesystem change happening while a
// blocking confirmation prompt (backed by r) is still awaiting input,
// without needing real concurrency or timing.
type removeOnReadReader struct {
	r       io.Reader
	target  string
	removed bool
}

func (rr *removeOnReadReader) Read(p []byte) (int, error) {
	if !rr.removed {
		rr.removed = true
		os.RemoveAll(rr.target)
	}
	return rr.r.Read(p)
}

// TestCleanup_RepoDirRemovedDuringConfirmation_TreatedAsNoOpNotResurrected
// covers RepoDir disappearing out from under Cleanup while its interactive
// confirmation prompt is still blocked reading input. The repoValid
// snapshot taken before the prompt must be re-checked afterward — trusting
// it would let acquireRunLock's MkdirAll silently resurrect the
// deliberately-removed directory, exactly the resurrection Cleanup's own
// design says must never happen (KTD5).
func TestCleanup_RepoDirRemovedDuringConfirmation_TreatedAsNoOpNotResurrected(t *testing.T) {
	dir := cleanupWorkdir(t)
	schedule, capturePath := registeredLaunchdDeps(t)

	input := &removeOnReadReader{r: scriptedInput("y"), target: dir}

	result, err := Cleanup(t.Context(), CleanupDeps{
		RepoDir:     dir,
		Schedule:    schedule,
		Interactive: true,
		Accessible:  true,
		Input:       input,
		Output:      &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v, want nil (a repo removed mid-confirmation degrades to a no-op)", err)
	}
	if result.RepoValid {
		t.Error("result.RepoValid = true, want false — RepoDir was removed before repo-side steps ran")
	}
	if _, statErr := os.Stat(dir); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("Stat(dir) error = %v, want os.ErrNotExist — RepoDir must not be resurrected by acquireRunLock", statErr)
	}

	sawBootout := false
	for _, call := range readCaptureFile(t, capturePath) {
		if strings.HasPrefix(call, "bootout ") {
			sawBootout = true
		}
	}
	if !sawBootout {
		t.Error("launchctl bootout never invoked, want schedule deregistration regardless of the repo disappearing mid-confirmation")
	}
}

// TestInspectFootprint_ReadmeReadFails_DegradesToWarning covers a footprint
// sub-check failing (README.md replaced by a directory, so os.ReadFile
// fails with something other than os.ErrNotExist): inspectFootprint must
// record it as a warning rather than aborting, so Cleanup's confirmation
// prompt — and, downstream, schedule deregistration — is always reachable
// regardless of a read-only preview step failing.
func TestInspectFootprint_ReadmeReadFails_DegradesToWarning(t *testing.T) {
	dir := cleanupWorkdir(t)
	readmePath := filepath.Join(dir, readmeFile)
	if err := os.Remove(readmePath); err != nil {
		t.Fatalf("Remove(README) error = %v", err)
	}
	if err := os.Mkdir(readmePath, 0o755); err != nil {
		t.Fatalf("Mkdir(README) error = %v", err)
	}

	footprint := inspectFootprint(t.Context(), CleanupDeps{RepoDir: dir, Schedule: ScheduleDeps{Label: "test"}}, true)

	if len(footprint.warnings) == 0 {
		t.Fatal("footprint.warnings is empty, want a warning recorded for the unreadable README")
	}
	if !strings.Contains(footprint.String(), "warning") {
		t.Errorf("footprint.String() = %q, want the confirmation prompt to surface the inspection warning", footprint.String())
	}
	if footprint.fileCount == 0 {
		t.Error("footprint.fileCount = 0, want the unrelated .token-profile count to still be populated despite the README warning")
	}
}

// TestCleanup_FootprintInspectionWarning_StillDeregistersSchedule covers
// the end-to-end effect of the fix above: even with a footprint-inspection
// sub-check failing (the same broken README as above), Cleanup must still
// reach the confirmation prompt and deregister the schedule — previously,
// inspectFootprint's hard error short-circuited Cleanup before either ever
// ran, silently leaving the schedule registered.
func TestCleanup_FootprintInspectionWarning_StillDeregistersSchedule(t *testing.T) {
	dir := cleanupWorkdir(t)
	readmePath := filepath.Join(dir, readmeFile)
	if err := os.Remove(readmePath); err != nil {
		t.Fatalf("Remove(README) error = %v", err)
	}
	if err := os.Mkdir(readmePath, 0o755); err != nil {
		t.Fatalf("Mkdir(README) error = %v", err)
	}
	schedule, capturePath := registeredLaunchdDeps(t)

	result, _ := Cleanup(t.Context(), CleanupDeps{
		RepoDir:     dir,
		Schedule:    schedule,
		Interactive: true,
		Accessible:  true,
		Input:       scriptedInput("y"),
		Output:      &bytes.Buffer{},
	})
	// The later README-stripping step still fails on the deliberately
	// broken README (it's a directory), so Cleanup's overall error is not
	// asserted here — only that schedule deregistration, which runs before
	// that step, was not skipped.
	if result.Schedule != ScheduleRegistered {
		t.Errorf("result.Schedule = %v, want ScheduleRegistered", result.Schedule)
	}
	sawBootout := false
	for _, call := range readCaptureFile(t, capturePath) {
		if strings.HasPrefix(call, "bootout ") {
			sawBootout = true
		}
	}
	if !sawBootout {
		t.Error("launchctl bootout never invoked, want schedule deregistration to run despite the footprint-inspection warning")
	}
}

// TestCleanup_UncommittedChanges_NamedInConfirmationPrompt covers the case
// where README.md/.token-profile/ already carry uncommitted changes (e.g.
// left over from a prior --dry-run that was never committed): the
// confirmation prompt must name them explicitly rather than folding them
// silently into the plain file count, and deletion still proceeds.
func TestCleanup_UncommittedChanges_NamedInConfirmationPrompt(t *testing.T) {
	dir := cleanupWorkdir(t)
	// Simulate a leftover uncommitted change from a prior --dry-run:
	// rewrite the already-committed README without committing again.
	writeFile(t, dir, readmeFile, "# username\n\n<!-- token-profile:start -->\ncard content here (dry-run update)\n<!-- token-profile:end -->\n")
	schedule, _ := unregisteredLaunchdDeps(t)
	var out bytes.Buffer

	result, err := Cleanup(t.Context(), CleanupDeps{
		RepoDir:     dir,
		Schedule:    schedule,
		Interactive: true,
		Accessible:  true,
		Input:       scriptedInput("y"),
		Output:      &out,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v, want nil", err)
	}
	if !result.ReadmeStripped {
		t.Error("result.ReadmeStripped = false, want true (deletion still proceeds despite pre-existing uncommitted changes)")
	}

	printed := out.String()
	if !strings.Contains(printed, "uncommitted changes") {
		t.Errorf("confirmation output = %q, want it to name the pre-existing uncommitted changes explicitly", printed)
	}
	if !strings.Contains(printed, "README.md") {
		t.Errorf("confirmation output = %q, want the uncommitted README.md change named specifically", printed)
	}
}

// TestCleanup_ScheduleCheckFails_ReportedDistinctly covers AE4/KTD7: a live
// schedule-state check failure (for a reason other than "not found") must
// be reported as a distinct check-failed outcome, never collapsed into
// nothing-to-remove — and must not block the (independent) repo-side
// cleanup from proceeding (KTD6).
func TestCleanup_ScheduleCheckFails_ReportedDistinctly(t *testing.T) {
	dir := cleanupWorkdir(t)
	capturePath := filepath.Join(t.TempDir(), "capture")
	bin := fakeLaunchctlBinaryAlwaysFails(t, capturePath)
	schedule := ScheduleDeps{GOOS: "darwin", Label: "dev.token-profile.refresh", Launchctl: bin}
	var out bytes.Buffer

	result, err := Cleanup(t.Context(), CleanupDeps{
		RepoDir:     dir,
		Schedule:    schedule,
		Interactive: true,
		Accessible:  true,
		Input:       scriptedInput("y"),
		Output:      &out,
	})
	if err == nil {
		t.Fatal("Cleanup() error = nil, want non-nil when the schedule-state check fails")
	}
	if result.Schedule != ScheduleCheckFailed {
		t.Errorf("result.Schedule = %v, want ScheduleCheckFailed", result.Schedule)
	}
	if result.Schedule == ScheduleNotRegistered {
		t.Error("Cleanup() collapsed a check failure into ScheduleNotRegistered")
	}
	if !result.ReadmeStripped || !result.DirRemoved {
		t.Errorf("result = %+v, want repo-side cleanup unaffected by the schedule check failure", result)
	}
}

// TestCleanup_RunningAsRoot_ScheduleDeregistrationRefused covers KTD16's own
// claim ("never sudo, never the system domain") being enforced on the
// cleanup side too: running under an effective UID of 0 must refuse the
// schedule-deregistration attempt entirely (never even checking state, let
// alone bootout/crontab), rather than silently operating against gui/0 or
// root's own crontab. Repo-side cleanup is independent of the schedule
// step (KTD6) and must still proceed.
func TestCleanup_RunningAsRoot_ScheduleDeregistrationRefused(t *testing.T) {
	dir := cleanupWorkdir(t)
	schedule, capturePath := registeredLaunchdDeps(t)

	geteuid = func() int { return 0 }
	defer func() { geteuid = os.Geteuid }()

	result, err := Cleanup(t.Context(), CleanupDeps{
		RepoDir:     dir,
		Schedule:    schedule,
		Interactive: true,
		Accessible:  true,
		Input:       scriptedInput("y"),
		Output:      &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("Cleanup() error = nil, want an error refusing schedule deregistration while running as root")
	}
	if !strings.Contains(err.Error(), "root") {
		t.Errorf("Cleanup() error = %q, want it to mention root/sudo", err.Error())
	}
	if result.Schedule != ScheduleCheckFailed {
		t.Errorf("result.Schedule = %v, want ScheduleCheckFailed", result.Schedule)
	}
	if !result.ReadmeStripped || !result.DirRemoved {
		t.Errorf("result = %+v, want repo-side cleanup unaffected by the root refusal (KTD6)", result)
	}
	if captured := readCaptureFile(t, capturePath); len(captured) != 0 {
		t.Errorf("launchctl invocations = %v, want none — schedule state must never be checked while running as root", captured)
	}
}

// TestCleanup_NoTTY_FailsFast covers KTD12: cleanup requires an interactive
// terminal and fails fast with no non-interactive override, touching
// nothing at all.
func TestCleanup_NoTTY_FailsFast(t *testing.T) {
	dir := cleanupWorkdir(t)
	schedule, capturePath := registeredLaunchdDeps(t)

	_, err := Cleanup(t.Context(), CleanupDeps{
		RepoDir:     dir,
		Schedule:    schedule,
		Interactive: false,
	})
	if !errors.Is(err, errCleanupRequiresTTY) {
		t.Errorf("Cleanup() error = %v, want errCleanupRequiresTTY", err)
	}

	readmeGot, readErr := os.ReadFile(filepath.Join(dir, readmeFile))
	if readErr != nil {
		t.Fatalf("ReadFile(README.md) error = %v", readErr)
	}
	if !strings.Contains(string(readmeGot), "card content here") {
		t.Errorf("README.md = %q, want it untouched", readmeGot)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".token-profile")); statErr != nil {
		t.Errorf("Stat(.token-profile) error = %v, want it to still exist untouched", statErr)
	}
	if calls := readCaptureFile(t, capturePath); len(calls) != 0 {
		t.Errorf("launchctl invoked %v, want no invocations at all", calls)
	}
}

// TestCleanup_ConfirmationDeclined_NoChanges covers a declined confirmation:
// no schedule change, no file changes, clean exit — but the footprint
// preview must still have been printed before the decline.
func TestCleanup_ConfirmationDeclined_NoChanges(t *testing.T) {
	dir := cleanupWorkdir(t)
	schedule, capturePath := registeredLaunchdDeps(t)
	var out bytes.Buffer

	result, err := Cleanup(t.Context(), CleanupDeps{
		RepoDir:     dir,
		Schedule:    schedule,
		Interactive: true,
		Accessible:  true,
		Input:       scriptedInput("n"),
		Output:      &out,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v, want nil", err)
	}
	if !result.Declined {
		t.Error("result.Declined = false, want true")
	}

	readmeGot, readErr := os.ReadFile(filepath.Join(dir, readmeFile))
	if readErr != nil {
		t.Fatalf("ReadFile(README.md) error = %v", readErr)
	}
	if !strings.Contains(string(readmeGot), "card content here") {
		t.Errorf("README.md = %q, want it untouched after a declined confirmation", readmeGot)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".token-profile")); statErr != nil {
		t.Errorf("Stat(.token-profile) error = %v, want it to still exist untouched", statErr)
	}
	if calls := readCaptureFile(t, capturePath); len(calls) != 0 {
		t.Errorf("launchctl invoked %v, want no invocations after a declined confirmation", calls)
	}

	if !strings.Contains(out.String(), "cleanup will attempt") {
		t.Errorf("output = %q, want the footprint preview printed even though the confirmation was declined", out.String())
	}
}

// TestCleanup_ConfirmationAborted_TreatedIdenticallyToDeclined covers huh's
// ctrl+c path (KTD2, mirroring U3's own documented limitation): accessible
// mode can only exercise the "declined" shape, since huh's runAccessible
// discards each field's own RunAccessible error and unconditionally returns
// nil — a real interactive ctrl+c is a production-only path this package's
// accessible-mode tests can't reach. Immediate EOF (no answer given at all)
// falls back to the confirm field's zero-value default (false), reading
// identically to an explicit decline.
func TestCleanup_ConfirmationAborted_TreatedIdenticallyToDeclined(t *testing.T) {
	dir := cleanupWorkdir(t)
	schedule, capturePath := registeredLaunchdDeps(t)
	var out bytes.Buffer

	result, err := Cleanup(t.Context(), CleanupDeps{
		RepoDir:     dir,
		Schedule:    schedule,
		Interactive: true,
		Accessible:  true,
		Input:       iotest.OneByteReader(strings.NewReader("")),
		Output:      &out,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v, want nil", err)
	}
	if !result.Declined {
		t.Error("result.Declined = false, want true (EOF/interrupted reads identically to a decline)")
	}
	if calls := readCaptureFile(t, capturePath); len(calls) != 0 {
		t.Errorf("launchctl invoked %v, want no invocations", calls)
	}
}

// TestCleanup_ConcurrentLockHeld_FailsOnAcquisition covers the run-lock
// integration: a concurrent process already holding the lock must make
// cleanup fail on acquisition rather than racing it for README.md/
// .token-profile — while schedule deregistration, which never depends on
// the lock (KTD5, KTD6), still proceeds.
func TestCleanup_ConcurrentLockHeld_FailsOnAcquisition(t *testing.T) {
	dir := cleanupWorkdir(t)
	release, err := acquireRunLock(dir) // simulates a concurrent run/init/cleanup holding the lock
	if err != nil {
		t.Fatalf("acquireRunLock() setup error = %v, want nil", err)
	}
	defer release()

	schedule, capturePath := registeredLaunchdDeps(t)
	var out bytes.Buffer

	result, err := Cleanup(t.Context(), CleanupDeps{
		RepoDir:     dir,
		Schedule:    schedule,
		Interactive: true,
		Accessible:  true,
		Input:       scriptedInput("y"),
		Output:      &out,
	})
	if err == nil {
		t.Fatal("Cleanup() error = nil, want an error while the run-lock is held by a concurrent process")
	}
	if !strings.Contains(err.Error(), "already in progress") {
		t.Errorf("Cleanup() error = %q, want it to explain the lock contention", err.Error())
	}

	if result.Schedule != ScheduleRegistered {
		t.Errorf("result.Schedule = %v, want ScheduleRegistered (deregistration proceeds regardless of lock contention)", result.Schedule)
	}
	sawBootout := false
	for _, call := range readCaptureFile(t, capturePath) {
		if strings.HasPrefix(call, "bootout ") {
			sawBootout = true
		}
	}
	if !sawBootout {
		t.Error("launchctl bootout never invoked, want schedule deregistration to proceed despite the lock contention")
	}

	readmeGot, readErr := os.ReadFile(filepath.Join(dir, readmeFile))
	if readErr != nil {
		t.Fatalf("ReadFile(README.md) error = %v", readErr)
	}
	if !strings.Contains(string(readmeGot), "card content here") {
		t.Errorf("README.md = %q, want it untouched (no racing mutation while the lock is contended)", readmeGot)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".token-profile")); statErr != nil {
		t.Errorf("Stat(.token-profile) error = %v, want it to still exist untouched", statErr)
	}
}

// TestCleanup_LockFileSurvivesTokenProfileDirDeletion_BlocksConcurrentAcquire
// covers KTD15's integration guarantee directly against lock.go's own
// primitives (independent of Cleanup's full plumbing): after
// .token-profile/ is deleted while the run-lock is held, the lock file
// (now a sibling at <repoDir>/.token-profile.lock) must still exist on
// disk until release() runs, and a concurrently-attempted acquireRunLock
// during that window must still correctly block.
func TestCleanup_LockFileSurvivesTokenProfileDirDeletion_BlocksConcurrentAcquire(t *testing.T) {
	dir := t.TempDir()

	release, err := acquireRunLock(dir)
	if err != nil {
		t.Fatalf("acquireRunLock() error = %v, want nil", err)
	}

	tokenProfileDir := filepath.Join(dir, ".token-profile")
	if err := os.MkdirAll(tokenProfileDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", tokenProfileDir, err)
	}
	if err := os.RemoveAll(tokenProfileDir); err != nil { // mirrors cleanup's own deletion step
		t.Fatalf("RemoveAll(%s) error = %v", tokenProfileDir, err)
	}

	lockPath := filepath.Join(dir, ".token-profile.lock")
	if _, statErr := os.Stat(lockPath); statErr != nil {
		t.Fatalf("Stat(lock) error = %v, want the lock file to survive .token-profile/'s deletion (KTD15)", statErr)
	}

	if _, err := acquireRunLock(dir); err == nil {
		t.Error("acquireRunLock() during the held window error = nil, want it to still block a concurrent attempt")
	}

	release()

	release2, err := acquireRunLock(dir)
	if err != nil {
		t.Fatalf("acquireRunLock() after release error = %v, want nil", err)
	}
	release2()
}

// TestPrintCleanupResult covers printCleanupResult's branches directly: the
// declined short-circuit, each ScheduleState value, and the repo-invalid
// short-circuit versus the two repo-side outcome flags — this function had
// no direct test coverage at all before this table.
func TestPrintCleanupResult(t *testing.T) {
	tests := []struct {
		name   string
		result CleanupResult
		want   []string
	}{
		{
			name:   "declined",
			result: CleanupResult{Declined: true},
			want:   []string{"cleanup cancelled — nothing changed"},
		},
		{
			name:   "repo invalid, schedule removed",
			result: CleanupResult{RepoValid: false, Schedule: ScheduleRegistered},
			want:   []string{"schedule: removed", "target repo: missing or not a git repository"},
		},
		{
			name:   "schedule check failed, repo valid, nothing stripped or removed",
			result: CleanupResult{RepoValid: true, Schedule: ScheduleCheckFailed},
			want:   []string{"schedule: could not determine live state", "README.md: nothing to strip", ".token-profile/: nothing to remove"},
		},
		{
			name:   "schedule not registered, repo valid, readme stripped and dir removed",
			result: CleanupResult{RepoValid: true, Schedule: ScheduleNotRegistered, ReadmeStripped: true, DirRemoved: true},
			want:   []string{"schedule: nothing to remove", "README.md: markers stripped", ".token-profile/: removed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			printCleanupResult(&out, tt.result)
			got := out.String()
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("printCleanupResult() output = %q, want it to contain %q", got, want)
				}
			}
		})
	}
}

// TestReportCleanupOutcome_NoTTYError_SkipsPrinting covers the one case
// where nothing was ever attempted: printing the zero-value CleanupResult
// here would be actively misleading (e.g. falsely implying the target repo
// is invalid), so no output should be produced.
func TestReportCleanupOutcome_NoTTYError_SkipsPrinting(t *testing.T) {
	var out bytes.Buffer
	err := reportCleanupOutcome(&out, CleanupResult{}, errCleanupRequiresTTY)
	if !errors.Is(err, errCleanupRequiresTTY) {
		t.Errorf("reportCleanupOutcome() error = %v, want errCleanupRequiresTTY", err)
	}
	if out.Len() != 0 {
		t.Errorf("output = %q, want empty (nothing was attempted)", out.String())
	}
}

// TestReportCleanupOutcome_ScheduleErrorWithRepoSuccess_StillPrintsSummary
// is the direct regression test for the bug this fixes: NewCleanupCmd's
// RunE previously returned Cleanup's error before ever calling
// printCleanupResult, so an operator hitting a schedule-only failure never
// learned that README.md was actually stripped and .token-profile/ was
// actually deleted.
func TestReportCleanupOutcome_ScheduleErrorWithRepoSuccess_StillPrintsSummary(t *testing.T) {
	var out bytes.Buffer
	scheduleErr := errors.New("removing schedule: launchctl bootout: exit status 1")
	result := CleanupResult{RepoValid: true, Schedule: ScheduleCheckFailed, ReadmeStripped: true, DirRemoved: true}

	err := reportCleanupOutcome(&out, result, scheduleErr)
	if !errors.Is(err, scheduleErr) {
		t.Errorf("reportCleanupOutcome() error = %v, want %v", err, scheduleErr)
	}
	for _, want := range []string{"README.md: markers stripped", ".token-profile/: removed"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output = %q, want it to contain %q despite the schedule error", out.String(), want)
		}
	}
}

// TestReportCleanupOutcome_Success_PrintsSummaryNoError covers the ordinary
// success path unchanged.
func TestReportCleanupOutcome_Success_PrintsSummaryNoError(t *testing.T) {
	var out bytes.Buffer
	result := CleanupResult{RepoValid: true, Schedule: ScheduleRegistered, ReadmeStripped: true, DirRemoved: true}

	if err := reportCleanupOutcome(&out, result, nil); err != nil {
		t.Errorf("reportCleanupOutcome() error = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "schedule: removed") {
		t.Errorf("output = %q, want it to report the schedule removal", out.String())
	}
}

// TestNewCleanupCmd_RegistersConfigFlag is a flag-registration smoke test
// mirroring this repo's own convention for cobra-command-level tests
// (TestNewInitCmd_HasDryRunFlag, TestNewRunCmd_HasDryRunFlag) — NewCleanupCmd's
// RunE itself resolves os.Stdin/config.Load internally with no injectable
// seams, so it isn't unit-testable beyond its flag wiring; behavior is
// covered by calling Cleanup directly, as every other test in this file does.
func TestNewCleanupCmd_RegistersConfigFlag(t *testing.T) {
	cmd := NewCleanupCmd()
	if f := cmd.Flags().Lookup("config"); f == nil {
		t.Error("NewCleanupCmd() does not register a --config flag, want one")
	}
	if cmd.Use != "cleanup" {
		t.Errorf("NewCleanupCmd().Use = %q, want %q", cmd.Use, "cleanup")
	}
}
