package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Christophe1997/token-profile/internal/agentsview"
	"github.com/Christophe1997/token-profile/internal/config"
	"github.com/Christophe1997/token-profile/internal/readme"
)

// TestEnsureReadmeMarkers_NoReadme_CreatesMinimalWithMarkers covers the case
// where the target repo has no README.md at all: ensureReadmeMarkers must
// create one containing just the markers, per the plan's "if the README
// doesn't exist at all, create a minimal one containing just the markers"
// instruction.
func TestEnsureReadmeMarkers_NoReadme_CreatesMinimalWithMarkers(t *testing.T) {
	dir := t.TempDir()

	if err := ensureReadmeMarkers(dir); err != nil {
		t.Fatalf("ensureReadmeMarkers() error = %v, want nil", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, readmeFile))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	if !strings.Contains(string(got), readme.StartMarker) || !strings.Contains(string(got), readme.EndMarker) {
		t.Errorf("README = %q, want it to contain both markers", got)
	}
	if _, err := readme.Inject(got, "probe"); err != nil {
		t.Errorf("readme.Inject() on freshly-created README error = %v, want nil", err)
	}
}

// TestEnsureReadmeMarkers_ReadmeWithoutMarkers_AppendsMarkers covers a README
// that already exists but has never been scaffolded: the existing content
// must be preserved, with the markers appended so a subsequent Inject call
// succeeds.
func TestEnsureReadmeMarkers_ReadmeWithoutMarkers_AppendsMarkers(t *testing.T) {
	dir := t.TempDir()
	const original = "# username\n\nSome bio text.\n"
	writeFile(t, dir, readmeFile, original)

	if err := ensureReadmeMarkers(dir); err != nil {
		t.Fatalf("ensureReadmeMarkers() error = %v, want nil", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, readmeFile))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	if !strings.HasPrefix(string(got), original) {
		t.Errorf("README = %q, want existing content %q preserved as a prefix", got, original)
	}
	if _, err := readme.Inject(got, "probe"); err != nil {
		t.Errorf("readme.Inject() after appending markers error = %v, want nil", err)
	}
}

// TestEnsureReadmeMarkers_AlreadyScaffolded_NoOp covers re-running init
// against an already-initialized repo: the README must come back
// byte-for-byte unchanged, with no duplicate marker pairs inserted.
func TestEnsureReadmeMarkers_AlreadyScaffolded_NoOp(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, readmeFile, markedReadme)

	if err := ensureReadmeMarkers(dir); err != nil {
		t.Fatalf("ensureReadmeMarkers() error = %v, want nil", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, readmeFile))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	if string(got) != markedReadme {
		t.Errorf("README = %q, want unchanged %q", got, markedReadme)
	}
	if n := strings.Count(string(got), readme.StartMarker); n != 1 {
		t.Errorf("README contains %d start markers, want exactly 1 (no duplication)", n)
	}
	if n := strings.Count(string(got), readme.EndMarker); n != 1 {
		t.Errorf("README contains %d end markers, want exactly 1 (no duplication)", n)
	}
}

// TestInit_EndToEnd_FreshRepo covers the happy path (R10, R11, F3): running
// init against a freshly-cloned repo with no markers yet must scaffold the
// markers, write a scheduling entry to the injectable destination, and
// perform a first run whose commit lands on the remote.
func TestInit_EndToEnd_FreshRepo(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, unmarkedReadme)

	work := cloneWorkdir(t, remote, "init-fresh")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	scheduleDest := filepath.Join(t.TempDir(), "schedule")

	deps := InitDeps{
		Config:       config.Config{Breakdown: config.BreakdownPerModel, TargetRepo: work},
		Client:       &agentsview.Client{BinaryName: bin},
		MachineID:    "machine-init",
		Now:          time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:      work,
		ScheduleDest: scheduleDest,
		BinaryPath:   "/usr/local/bin/token-profile",
		ConfigPath:   "/config.json",
	}

	if err := Init(t.Context(), deps); err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}

	readmeBytes, err := os.ReadFile(filepath.Join(work, readmeFile))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	if _, err := readme.Inject(readmeBytes, "probe"); err != nil {
		t.Errorf("readme.Inject() after Init() error = %v, want nil (markers must be present)", err)
	}

	if _, err := os.Stat(scheduleDest); err != nil {
		t.Errorf("scheduling entry %s does not exist after Init(): %v", scheduleDest, err)
	}

	// Verify against a *fresh* clone of the remote, proving the first run's
	// commit actually landed and was pushed, not just written locally.
	verify := cloneWorkdir(t, remote, "verify")
	log := runGitT(t, verify, "log", "--oneline")
	if !strings.Contains(log, "token-profile") {
		t.Errorf("git log = %q, want the first run's commit to have landed on the remote", log)
	}
}

// TestInit_Success_PrintsHeadlineAndCommitHash covers the fix: a successful
// Init must print the same one-line confirmation as Run — the headline
// summary plus the just-published commit's short hash — through the shared
// run() core.
func TestInit_Success_PrintsHeadlineAndCommitHash(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, unmarkedReadme)

	work := cloneWorkdir(t, remote, "init-summary")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	scheduleDest := filepath.Join(t.TempDir(), "schedule")

	var buf bytes.Buffer
	deps := InitDeps{
		Config:       config.Config{Breakdown: config.BreakdownPerModel, TargetRepo: work},
		Client:       &agentsview.Client{BinaryName: bin},
		MachineID:    "machine-init",
		Now:          time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:      work,
		ScheduleDest: scheduleDest,
		BinaryPath:   "/usr/local/bin/token-profile",
		ConfigPath:   "/config.json",
		Stdout:       &buf,
	}

	if err := Init(t.Context(), deps); err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}

	hash := strings.TrimSpace(runGitT(t, work, "rev-parse", "--short", "HEAD"))
	got := buf.String()
	if !strings.Contains(got, "Tokens:") {
		t.Errorf("Init() Stdout = %q, want it to contain %q", got, "Tokens:")
	}
	if !strings.Contains(got, "Streak:") {
		t.Errorf("Init() Stdout = %q, want it to contain %q", got, "Streak:")
	}
	if !strings.Contains(got, "published as") {
		t.Errorf("Init() Stdout = %q, want it to contain %q", got, "published as")
	}
	if !strings.Contains(got, hash) {
		t.Errorf("Init() Stdout = %q, want it to contain the published commit's short hash %q", got, hash)
	}
	if !strings.Contains(got, scheduleDest) {
		t.Errorf("Init() Stdout = %q, want it to report the scheduling entry path %q", got, scheduleDest)
	}
}

// TestInit_Rerun_NoOp covers the edge case: re-running init against an
// already-initialized repo must not duplicate the README markers or the
// scheduling entry. Now differs between the two runs (mirroring a real
// second invocation some time later) so the second run's rendered "last
// updated" line actually changes and the second commit has something to
// publish.
func TestInit_Rerun_NoOp(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, unmarkedReadme)

	work := cloneWorkdir(t, remote, "init-rerun")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	scheduleDest := filepath.Join(t.TempDir(), "schedule")

	deps := InitDeps{
		Config:       config.Config{Breakdown: config.BreakdownPerModel, TargetRepo: work},
		Client:       &agentsview.Client{BinaryName: bin},
		MachineID:    "machine-init",
		Now:          time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:      work,
		ScheduleDest: scheduleDest,
		BinaryPath:   "/usr/local/bin/token-profile",
		ConfigPath:   "/config.json",
	}

	if err := Init(t.Context(), deps); err != nil {
		t.Fatalf("Init() first run error = %v, want nil", err)
	}

	deps.Now = deps.Now.Add(6 * time.Hour)
	if err := Init(t.Context(), deps); err != nil {
		t.Fatalf("Init() second run error = %v, want nil", err)
	}

	readmeBytes, err := os.ReadFile(filepath.Join(work, readmeFile))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	got := string(readmeBytes)
	if n := strings.Count(got, readme.StartMarker); n != 1 {
		t.Errorf("README contains %d start markers after two init runs, want exactly 1 (no duplication)", n)
	}
	if n := strings.Count(got, readme.EndMarker); n != 1 {
		t.Errorf("README contains %d end markers after two init runs, want exactly 1 (no duplication)", n)
	}

	scheduleBytes, err := os.ReadFile(scheduleDest)
	if err != nil {
		t.Fatalf("ReadFile(scheduling entry) error = %v", err)
	}
	if n := strings.Count(string(scheduleBytes), deps.BinaryPath); n != 1 {
		t.Errorf("scheduling entry contains %d references to the binary after two init runs, want exactly 1 (no duplication)", n)
	}
}

// TestSchedulingEntryContent_Darwin_UsesConfiguredInterval and
// TestSchedulingEntryContent_Cron_UsesConfiguredInterval cover the fix: the
// scheduling snippet must reflect the configured ScheduleInterval instead
// of the old hardcoded 6-hour cycle (KTD10 supersedes 21600/"0 */6 * * *").
func TestSchedulingEntryContent_Darwin_UsesConfiguredInterval(t *testing.T) {
	got := schedulingEntryContent("darwin", "/usr/local/bin/token-profile", "/config.json", 4*time.Hour)
	if !strings.Contains(got, "<integer>14400</integer>") {
		t.Errorf("schedulingEntryContent() = %q, want StartInterval 14400 for a 4h interval", got)
	}
	if strings.Contains(got, "<integer>21600</integer>") {
		t.Errorf("schedulingEntryContent() = %q, want the old hardcoded 21600 gone", got)
	}
}

func TestSchedulingEntryContent_Cron_UsesConfiguredInterval(t *testing.T) {
	got := schedulingEntryContent("linux", "/usr/local/bin/token-profile", "/config.json", 4*time.Hour)
	if !strings.Contains(got, "0 */4 * * *") {
		t.Errorf("schedulingEntryContent() = %q, want cron field */4 for a 4h interval", got)
	}
	if strings.Contains(got, "0 */6 * * *") {
		t.Errorf("schedulingEntryContent() = %q, want the old hardcoded */6 gone", got)
	}
}

// TestSchedulingEntryContent_ZeroInterval_DefaultsToConfigDefault covers an
// InitDeps literal built directly by a test, bypassing config.Load's
// Default() layering: a zero interval must still render a sane cadence
// rather than a nonsensical StartInterval 0 / "*/0" cron field.
func TestSchedulingEntryContent_ZeroInterval_DefaultsToConfigDefault(t *testing.T) {
	got := schedulingEntryContent("darwin", "/usr/local/bin/token-profile", "/config.json", 0)
	want := fmt.Sprintf("<integer>%d</integer>", scheduleIntervalSeconds(config.DefaultScheduleInterval))
	if !strings.Contains(got, want) {
		t.Errorf("schedulingEntryContent(interval=0) = %q, want it to default to %q", got, want)
	}
}

// TestInit_ScheduleRegistrationAccepted_InstallsSchedule covers R4's happy
// path: after a successful init, accepting the schedule-registration prompt
// must actually install it (via U4's InstallSchedule), not just write the
// reviewable snippet.
// TestInit_TargetRepoNotGitRepo_FailsFast covers Fix 3: a RepoDir that
// exists but isn't a git working tree must fail fast with an actionable
// error, before Init scaffolds anything (README markers, scheduling entry,
// or a .token-profile directory) into it.
func TestInit_TargetRepoNotGitRepo_FailsFast(t *testing.T) {
	dir := t.TempDir()
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	scheduleDest := filepath.Join(t.TempDir(), "schedule")

	deps := InitDeps{
		Config:       config.Config{Breakdown: config.BreakdownPerModel, TargetRepo: dir},
		Client:       &agentsview.Client{BinaryName: bin},
		MachineID:    "machine-init",
		Now:          time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:      dir,
		ScheduleDest: scheduleDest,
		BinaryPath:   "/usr/local/bin/token-profile",
		ConfigPath:   "/config.json",
	}

	err := Init(t.Context(), deps)
	if err == nil {
		t.Fatal("Init() error = nil, want an error when RepoDir isn't a git repository")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("Init() error = %q, want it to explain RepoDir isn't a git repository", err.Error())
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".token-profile")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("Stat(.token-profile) error = %v, want os.ErrNotExist (no writes before the git-repo check runs)", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, readmeFile)); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("Stat(README.md) error = %v, want os.ErrNotExist (no README scaffolding before the git-repo check runs)", statErr)
	}
}

// TestInit_TargetRepoDoesNotExist_FailsFast covers Fix 3's other edge case
// for Init: a RepoDir that doesn't exist at all must fail the same way.
func TestInit_TargetRepoDoesNotExist_FailsFast(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	scheduleDest := filepath.Join(t.TempDir(), "schedule")

	deps := InitDeps{
		Config:       config.Config{Breakdown: config.BreakdownPerModel, TargetRepo: dir},
		Client:       &agentsview.Client{BinaryName: bin},
		MachineID:    "machine-init",
		Now:          time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:      dir,
		ScheduleDest: scheduleDest,
		BinaryPath:   "/usr/local/bin/token-profile",
		ConfigPath:   "/config.json",
	}

	err := Init(t.Context(), deps)
	if err == nil {
		t.Fatal("Init() error = nil, want an error when RepoDir doesn't exist")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("Init() error = %q, want it to explain RepoDir isn't a git repository", err.Error())
	}
	if _, statErr := os.Stat(dir); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("Stat(RepoDir) error = %v, want os.ErrNotExist (RepoDir itself must not be created as a side effect)", statErr)
	}
}

// TestInit_TargetRepoEmpty_FailsFast covers the error path: an unconfigured
// TargetRepo must fail fast with a clear, actionable message rather than
// panicking or guessing a location.
func TestInit_TargetRepoEmpty_FailsFast(t *testing.T) {
	deps := InitDeps{
		Config: config.Config{Breakdown: config.BreakdownPerModel},
	}

	err := Init(t.Context(), deps)
	if err == nil {
		t.Fatal("Init() error = nil, want an error when Config.TargetRepo is empty")
	}
	if !strings.Contains(err.Error(), "targetRepo") {
		t.Errorf("Init() error = %q, want actionable guidance mentioning %q", err.Error(), "targetRepo")
	}
}

// TestIsInteractive_NonFileReader_ReturnsFalse covers every test fixture's
// shape (strings.Reader, bytes.Buffer, ...): none of them are a real
