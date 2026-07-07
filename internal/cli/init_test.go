package cli

import (
	"bytes"
	"context"
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
func TestInit_ScheduleRegistrationAccepted_InstallsSchedule(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, unmarkedReadme)
	work := cloneWorkdir(t, remote, "sched-accept")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	scheduleDest := filepath.Join(t.TempDir(), "schedule")

	schedDir := t.TempDir()
	statePath := filepath.Join(schedDir, "state")
	capturePath := filepath.Join(schedDir, "capture")
	launchctlBin := fakeLaunchctlBinary(t, statePath, capturePath)

	var stdout bytes.Buffer
	deps := InitDeps{
		Config:         config.Config{Breakdown: config.BreakdownPerModel, TargetRepo: work, ScheduleInterval: 4 * time.Hour},
		Client:         &agentsview.Client{BinaryName: bin},
		MachineID:      "machine-sched-accept",
		Now:            time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:        work,
		ScheduleDest:   scheduleDest,
		BinaryPath:     "/usr/local/bin/token-profile",
		ConfigPath:     "/config.json",
		Stdout:         &stdout,
		Stdin:          strings.NewReader("y\n"),
		PromptSchedule: true,
		Schedule: ScheduleDeps{
			GOOS:      "darwin",
			PlistPath: filepath.Join(schedDir, "schedule.plist"),
			Launchctl: launchctlBin,
		},
	}

	if err := Init(t.Context(), deps); err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}

	if !strings.Contains(stdout.String(), "Register the refresh schedule now?") {
		t.Errorf("Init() Stdout = %q, want the schedule-registration prompt to have been shown", stdout.String())
	}

	captured := readCaptureFile(t, capturePath)
	foundBootstrap := false
	for _, line := range captured {
		if strings.HasPrefix(line, "bootstrap ") {
			foundBootstrap = true
		}
	}
	if !foundBootstrap {
		t.Errorf("captured launchctl invocations = %v, want a bootstrap call", captured)
	}
}

// TestInit_ScheduleRegistrationAccepted_InstallFails_ReportsWarningExitsZero
// covers KTD17: a failed live install attempt (e.g. a permission-restricted
// LaunchAgents directory, simulated here by a launchctl fixture that always
// fails) must degrade to a reported warning rather than a non-zero exit —
// clone/config/first-publish have already succeeded by this point.
func TestInit_ScheduleRegistrationAccepted_InstallFails_ReportsWarningExitsZero(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, unmarkedReadme)
	work := cloneWorkdir(t, remote, "sched-fail")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	scheduleDest := filepath.Join(t.TempDir(), "schedule")
	capturePath := filepath.Join(t.TempDir(), "capture")
	launchctlBin := fakeLaunchctlBinaryAlwaysFails(t, capturePath)

	var stdout bytes.Buffer
	deps := InitDeps{
		Config:         config.Config{Breakdown: config.BreakdownPerModel, TargetRepo: work},
		Client:         &agentsview.Client{BinaryName: bin},
		MachineID:      "machine-sched-fail",
		Now:            time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:        work,
		ScheduleDest:   scheduleDest,
		BinaryPath:     "/usr/local/bin/token-profile",
		ConfigPath:     "/config.json",
		Stdout:         &stdout,
		Stdin:          strings.NewReader("y\n"),
		PromptSchedule: true,
		Schedule: ScheduleDeps{
			GOOS:      "darwin",
			PlistPath: filepath.Join(t.TempDir(), "schedule.plist"),
			Launchctl: launchctlBin,
		},
	}

	if err := Init(t.Context(), deps); err != nil {
		t.Fatalf("Init() error = %v, want nil (a failed schedule install must degrade to a warning, KTD17)", err)
	}
	if !strings.Contains(stdout.String(), "warning") {
		t.Errorf("Init() Stdout = %q, want a warning about the failed schedule install", stdout.String())
	}
}

// TestInit_ScheduleRegistrationDeclined_SnippetWrittenNoInstallAttempted
// covers the declined-prompt integration scenario: the reviewable snippet
// at --schedule-dest is still written regardless, but no live install is
// ever attempted.
func TestInit_ScheduleRegistrationDeclined_SnippetWrittenNoInstallAttempted(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, unmarkedReadme)
	work := cloneWorkdir(t, remote, "sched-decline")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	scheduleDest := filepath.Join(t.TempDir(), "schedule")

	var stdout bytes.Buffer
	deps := InitDeps{
		Config:         config.Config{Breakdown: config.BreakdownPerModel, TargetRepo: work},
		Client:         &agentsview.Client{BinaryName: bin},
		MachineID:      "machine-sched-decline",
		Now:            time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:        work,
		ScheduleDest:   scheduleDest,
		BinaryPath:     "/usr/local/bin/token-profile",
		ConfigPath:     "/config.json",
		Stdout:         &stdout,
		Stdin:          strings.NewReader("n\n"),
		PromptSchedule: true,
		Schedule: ScheduleDeps{
			// A deliberately-broken path: if InstallSchedule were ever
			// invoked despite the decline, resolving this would fail loudly
			// rather than silently succeeding.
			Launchctl: filepath.Join(t.TempDir(), "no-such-launchctl"),
		},
	}

	if err := Init(t.Context(), deps); err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}
	if _, err := os.Stat(scheduleDest); err != nil {
		t.Errorf("scheduling entry %s missing after decline: %v", scheduleDest, err)
	}
	if strings.Contains(stdout.String(), "warning") {
		t.Errorf("Init() Stdout = %q, want no install-failure warning when declined", stdout.String())
	}
}

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
// terminal, so the auto-clone shortcut must never activate against one.
func TestIsInteractive_NonFileReader_ReturnsFalse(t *testing.T) {
	if isInteractive(strings.NewReader("y\n")) {
		t.Error("isInteractive(strings.Reader) = true, want false")
	}
}

// TestIsInteractive_RegularFile_ReturnsFalse covers a real *os.File that
// isn't a character device (unlike a terminal) — e.g. redirecting stdin
// from a plain file, as a scheduled cron/launchd invocation effectively
// does.
func TestIsInteractive_RegularFile_ReturnsFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(path, []byte("y\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	regular, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer regular.Close()

	if isInteractive(regular) {
		t.Error("isInteractive(regular file) = true, want false")
	}
}

// TestGitGlobalUserName_Configured_ReturnsIt and
// TestGitGlobalUserName_Unset_ReturnsEmpty both point git's --global config
// at a scratch HOME (via t.Setenv, auto-restored) so they never read or
// mutate the real developer machine's ~/.gitconfig.
func TestGitGlobalUserName_Configured_ReturnsIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runGitT(t, "", "config", "--global", "user.name", "octocat")

	if got := gitGlobalUserName(t.Context()); got != "octocat" {
		t.Errorf("gitGlobalUserName() = %q, want %q", got, "octocat")
	}
}

func TestGitGlobalUserName_Unset_ReturnsEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if got := gitGlobalUserName(t.Context()); got != "" {
		t.Errorf("gitGlobalUserName() = %q, want empty", got)
	}
}

func TestValidAutoCloneName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"", false},
		{".", false},
		{"..", false},
		{"a/b", false},
		{`a\b`, false},
		{"a..b", false},
		{"octocat", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validAutoCloneName(tt.name); got != tt.want {
				t.Errorf("validAutoCloneName(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestProfileRepoURL(t *testing.T) {
	tests := []struct {
		protocol config.CloneProtocol
		want     string
		wantErr  bool
	}{
		{config.CloneProtocolSSH, "git@github.com:octocat/octocat.git", false},
		{config.CloneProtocolHTTPS, "https://github.com/octocat/octocat.git", false},
		{"bogus", "", true},
	}
	for _, tt := range tests {
		t.Run(string(tt.protocol), func(t *testing.T) {
			got, err := profileRepoURL(tt.protocol, "octocat")
			if (err != nil) != tt.wantErr {
				t.Fatalf("profileRepoURL() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("profileRepoURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConfirmYesNo(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"lowercase y", "y\n", true},
		{"uppercase Y", "Y\n", true},
		{"lowercase yes", "yes\n", true},
		{"uppercase YES", "YES\n", true},
		{"y with no trailing newline", "y", true},
		{"n", "n\n", false},
		{"empty line", "\n", false},
		{"garbage", "maybe\n", false},
		{"no input at all", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			got := confirmYesNo(strings.NewReader(tt.input), &stdout, "Proceed?")
			if got != tt.want {
				t.Errorf("confirmYesNo(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestConfirmYesNo_NilStdin_ReturnsFalse covers offerScheduleRegistration's
// own defensive use: a nil deps.Stdin (e.g. a test InitDeps literal that
// never sets it) must read as a plain "no" rather than panicking on a nil
// io.Reader.
func TestConfirmYesNo_NilStdin_ReturnsFalse(t *testing.T) {
	if confirmYesNo(nil, &bytes.Buffer{}, "Proceed?") {
		t.Error("confirmYesNo(nil stdin) = true, want false")
	}
}

func TestConfirmYesNo_PromptContainsQuestion(t *testing.T) {
	var stdout bytes.Buffer
	confirmYesNo(strings.NewReader("n\n"), &stdout, "Register the refresh schedule now?")

	if !strings.Contains(stdout.String(), "Register the refresh schedule now?") {
		t.Errorf("confirmYesNo() prompt = %q, want it to contain the question", stdout.String())
	}
}

// TestRequireConfigOrTTY_NoConfigNoTTY_ReturnsActionableErrorNothingWritten
// covers R5/AE1: a config-needing command (run, or the guided-init entry
// point) invoked with no config file yet and no interactive terminal must
// fail immediately, naming the missing path and pointing at interactive
// init, without writing anything to disk. requireConfigOrTTY is tested
// directly (rather than through NewRunCmd's cobra wiring) since a plain `go
// test` run can't deterministically control the real process's os.Stdin
// TTY-ness.
func TestRequireConfigOrTTY_NoConfigNoTTY_ReturnsActionableErrorNothingWritten(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")

	err := requireConfigOrTTY(configPath, false)
	if err == nil {
		t.Fatal("requireConfigOrTTY() error = nil, want an error")
	}
	if !strings.Contains(err.Error(), configPath) {
		t.Errorf("requireConfigOrTTY() error = %q, want it to mention the config path %q", err.Error(), configPath)
	}
	if !strings.Contains(err.Error(), "init") {
		t.Errorf("requireConfigOrTTY() error = %q, want it to point at interactive init", err.Error())
	}
	if _, statErr := os.Stat(configPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("Stat(config) error = %v, want os.ErrNotExist (nothing written)", statErr)
	}
}

func TestRequireConfigOrTTY_ConfigExists_ReturnsNilRegardlessOfTTY(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.json", `{}`)
	configPath := filepath.Join(dir, "config.json")

	if err := requireConfigOrTTY(configPath, false); err != nil {
		t.Errorf("requireConfigOrTTY() error = %v, want nil when a config file already exists", err)
	}
}

func TestRequireConfigOrTTY_NoConfigButInteractive_ReturnsNil(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")

	if err := requireConfigOrTTY(configPath, true); err != nil {
		t.Errorf("requireConfigOrTTY() error = %v, want nil when interactive", err)
	}
}

// TestResolveInitConfig_ConfigAlreadyExists_DelegatesStraightToLoad covers
// the edge case: a config file already sitting at ConfigPath must be loaded
// as-is, with the wizard never invoked at all — regardless of Interactive.
func TestResolveInitConfig_ConfigAlreadyExists_DelegatesStraightToLoad(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.json", `{"targetRepo":"/some/repo","breakdown":"per-model"}`)
	configPath := filepath.Join(dir, "config.json")

	cfg, err := resolveInitConfig(t.Context(), initConfigDeps{
		ConfigPath:  configPath,
		Interactive: true,
		Wizard: WizardDeps{
			GitUserName: func(context.Context) string {
				t.Fatal("GitUserName was called, want the wizard untouched when a config already exists")
				return ""
			},
		},
		ResolveCloneURL: func(config.CloneProtocol, string) (string, error) {
			t.Fatal("ResolveCloneURL was called, want the wizard untouched when a config already exists")
			return "", nil
		},
	})
	if err != nil {
		t.Fatalf("resolveInitConfig() error = %v, want nil", err)
	}
	if cfg.TargetRepo != "/some/repo" {
		t.Errorf("cfg.TargetRepo = %q, want %q", cfg.TargetRepo, "/some/repo")
	}
}

// TestResolveInitConfig_NoConfigNoTTY_FailsFastNothingWritten covers R5/AE1
// on the init path directly.
func TestResolveInitConfig_NoConfigNoTTY_FailsFastNothingWritten(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")

	_, err := resolveInitConfig(t.Context(), initConfigDeps{
		ConfigPath:  configPath,
		Interactive: false,
		Stdout:      &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("resolveInitConfig() error = nil, want a fail-fast error")
	}
	if !strings.Contains(err.Error(), configPath) {
		t.Errorf("resolveInitConfig() error = %q, want it to mention the config path %q", err.Error(), configPath)
	}
	if _, statErr := os.Stat(configPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("Stat(config) error = %v, want os.ErrNotExist (nothing written)", statErr)
	}
}

// TestResolveInitConfig_WizardCancelled_NoConfigWrittenNoCloneAttempted
// covers the edge case: declining the wizard's trailing confirm must leave
// no partial config on disk and never attempt a clone.
func TestResolveInitConfig_WizardCancelled_NoConfigWrittenNoCloneAttempted(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	cloneDest := filepath.Join(dir, "clone-dest")

	_, err := resolveInitConfig(t.Context(), initConfigDeps{
		ConfigPath:  configPath,
		Interactive: true,
		Wizard: WizardDeps{
			GitUserName: func(context.Context) string { return "octocat" },
			Accessible:  true,
			Input:       scriptedInput("octocat", "1", cloneDest, "n"),
			Output:      &bytes.Buffer{},
		},
		ResolveCloneURL: func(config.CloneProtocol, string) (string, error) {
			t.Fatal("ResolveCloneURL was called, want it untouched when the wizard is cancelled")
			return "", nil
		},
	})
	if !errors.Is(err, ErrWizardCancelled) {
		t.Errorf("resolveInitConfig() error = %v, want ErrWizardCancelled", err)
	}
	if _, statErr := os.Stat(configPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("Stat(config) error = %v, want os.ErrNotExist (nothing written after a cancelled wizard)", statErr)
	}
	if _, statErr := os.Stat(cloneDest); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("Stat(clone dest) error = %v, want os.ErrNotExist (no clone side effects after a cancelled wizard)", statErr)
	}
}

// TestResolveInitConfig_FreshMachine_WizardConfirmed_ClonesAndWritesConfig
// covers the wizard-confirmed happy path end to end: real local git
// fixtures stand in for the remote (matching this repo's no-mocked-git
// convention), with ResolveCloneURL substituting the fixture's URL for the
// real github.com one profileRepoURL would otherwise construct.
func TestResolveInitConfig_FreshMachine_WizardConfirmed_ClonesAndWritesConfig(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, unmarkedReadme)

	home := t.TempDir()
	configPath := filepath.Join(home, "config.json")
	localPath := filepath.Join(home, "clone")

	var stdout bytes.Buffer
	cfg, err := resolveInitConfig(t.Context(), initConfigDeps{
		ConfigPath:  configPath,
		Interactive: true,
		Wizard: WizardDeps{
			GitUserName: func(context.Context) string { return "" },
			Accessible:  true,
			Input:       scriptedInput("octocat", "1", localPath, "y"),
			Output:      &bytes.Buffer{},
		},
		Stdout: &stdout,
		ResolveCloneURL: func(protocol config.CloneProtocol, repoName string) (string, error) {
			if repoName != "octocat" {
				t.Errorf("ResolveCloneURL called with repoName = %q, want %q", repoName, "octocat")
			}
			if protocol != config.CloneProtocolHTTPS {
				t.Errorf("ResolveCloneURL called with protocol = %q, want %q", protocol, config.CloneProtocolHTTPS)
			}
			return remote, nil
		},
	})
	if err != nil {
		t.Fatalf("resolveInitConfig() error = %v, want nil", err)
	}

	if cfg.TargetRepo != localPath {
		t.Errorf("cfg.TargetRepo = %q, want %q", cfg.TargetRepo, localPath)
	}
	if cfg.RemoteRepo != remote {
		t.Errorf("cfg.RemoteRepo = %q, want %q", cfg.RemoteRepo, remote)
	}
	if cfg.CloneProtocol != config.CloneProtocolHTTPS {
		t.Errorf("cfg.CloneProtocol = %q, want %q", cfg.CloneProtocol, config.CloneProtocolHTTPS)
	}

	out := runGitT(t, localPath, "rev-parse", "--is-inside-work-tree")
	if strings.TrimSpace(out) != "true" {
		t.Errorf("localPath %s is not a git working tree after resolveInitConfig()", localPath)
	}
	if !strings.Contains(stdout.String(), "cloned") {
		t.Errorf("Stdout = %q, want it to report the clone status", stdout.String())
	}

	onDisk, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config.Load() error = %v, want nil", err)
	}
	if onDisk != cfg {
		t.Errorf("config.Load(configPath) = %+v, want it to match the returned config %+v", onDisk, cfg)
	}
}

// TestNewInitCmd_NoCloneProtocolFlag covers KTD14: the --clone-protocol
// flag is obsoleted by the wizard's own protocol field and must no longer
// be registered on `init`.
func TestNewInitCmd_NoCloneProtocolFlag(t *testing.T) {
	cmd := NewInitCmd()
	if f := cmd.Flags().Lookup("clone-protocol"); f != nil {
		t.Errorf("NewInitCmd() still registers a --clone-protocol flag = %+v, want it removed (KTD14)", f)
	}
}

// TestGuidedInit_FreshMachine_EndToEnd_ClonesConfigsSchedulesAndPublishes is
// the plan's headline happy-path scenario (F1) exercised end to end: the
// wizard-driven config resolution feeds straight into Init, cloning the
// repo, writing config, ensuring README markers, performing the first
// publish, and — on accepting the schedule-registration offer — installing
// the schedule. A second fresh clone of the remote proves the commit
// actually landed, mirroring TestInit_EndToEnd_FreshRepo's own verification
// style.
func TestGuidedInit_FreshMachine_EndToEnd_ClonesConfigsSchedulesAndPublishes(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, unmarkedReadme)

	home := t.TempDir()
	configPath := filepath.Join(home, "config.json")
	localPath := filepath.Join(home, "clone")

	cfg, err := resolveInitConfig(t.Context(), initConfigDeps{
		ConfigPath:  configPath,
		Interactive: true,
		Wizard: WizardDeps{
			GitUserName: func(context.Context) string { return "" },
			Accessible:  true,
			Input:       scriptedInput("octocat", "1", localPath, "y"),
			Output:      &bytes.Buffer{},
		},
		Stdout: &bytes.Buffer{},
		ResolveCloneURL: func(config.CloneProtocol, string) (string, error) {
			return remote, nil
		},
	})
	if err != nil {
		t.Fatalf("resolveInitConfig() error = %v, want nil", err)
	}

	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	scheduleDest := filepath.Join(t.TempDir(), "schedule")
	schedDir := t.TempDir()
	statePath := filepath.Join(schedDir, "state")
	capturePath := filepath.Join(schedDir, "capture")
	launchctlBin := fakeLaunchctlBinary(t, statePath, capturePath)

	var stdout bytes.Buffer
	deps := InitDeps{
		Config:         cfg,
		Client:         &agentsview.Client{BinaryName: bin},
		MachineID:      "machine-guided",
		Now:            time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:        cfg.TargetRepo,
		ScheduleDest:   scheduleDest,
		BinaryPath:     "/usr/local/bin/token-profile",
		ConfigPath:     configPath,
		Stdout:         &stdout,
		Stdin:          strings.NewReader("y\n"),
		PromptSchedule: true,
		Schedule: ScheduleDeps{
			GOOS:      "darwin",
			PlistPath: filepath.Join(schedDir, "schedule.plist"),
			Launchctl: launchctlBin,
		},
	}

	if err := Init(t.Context(), deps); err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}

	readmeBytes, err := os.ReadFile(filepath.Join(cfg.TargetRepo, readmeFile))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	if _, err := readme.Inject(readmeBytes, "probe"); err != nil {
		t.Errorf("readme.Inject() after Init() error = %v, want nil (markers must be present)", err)
	}

	verify := cloneWorkdir(t, remote, "guided-verify")
	log := runGitT(t, verify, "log", "--oneline")
	if !strings.Contains(log, "token-profile") {
		t.Errorf("git log = %q, want the first run's commit to have landed on the remote", log)
	}

	captured := readCaptureFile(t, capturePath)
	foundBootstrap := false
	for _, line := range captured {
		if strings.HasPrefix(line, "bootstrap ") {
			foundBootstrap = true
		}
	}
	if !foundBootstrap {
		t.Errorf("captured launchctl invocations = %v, want a bootstrap call", captured)
	}
}

// TestInit_DryRun_FreshMachine_ClonesConfigsWritesNoCommitNoSchedulePrompt
// covers AE2/R8: a fresh-machine `init --dry-run` must still clone the
// repo, write config, and ensure README markers on disk (the same real
// writes as a non-dry-run init), but must never show the
// schedule-registration prompt — even though PromptSchedule is true and
// Stdin would answer "yes" — and must never commit/push.
func TestInit_DryRun_FreshMachine_ClonesConfigsWritesNoCommitNoSchedulePrompt(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, unmarkedReadme)

	home := t.TempDir()
	configPath := filepath.Join(home, "config.json")
	localPath := filepath.Join(home, "clone")

	cfg, err := resolveInitConfig(t.Context(), initConfigDeps{
		ConfigPath:  configPath,
		Interactive: true,
		Wizard: WizardDeps{
			GitUserName: func(context.Context) string { return "" },
			Accessible:  true,
			Input:       scriptedInput("octocat", "1", localPath, "y"),
			Output:      &bytes.Buffer{},
		},
		Stdout: &bytes.Buffer{},
		ResolveCloneURL: func(config.CloneProtocol, string) (string, error) {
			return remote, nil
		},
	})
	if err != nil {
		t.Fatalf("resolveInitConfig() error = %v, want nil", err)
	}

	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	scheduleDest := filepath.Join(t.TempDir(), "schedule")

	var stdout bytes.Buffer
	deps := InitDeps{
		Config:         cfg,
		Client:         &agentsview.Client{BinaryName: bin},
		MachineID:      "machine-dry-init",
		Now:            time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:        cfg.TargetRepo,
		ScheduleDest:   scheduleDest,
		BinaryPath:     "/usr/local/bin/token-profile",
		ConfigPath:     configPath,
		Stdout:         &stdout,
		Stdin:          strings.NewReader("y\n"),
		PromptSchedule: true,
		DryRun:         true,
		Schedule: ScheduleDeps{
			// A deliberately-broken path: if InstallSchedule were ever
			// invoked despite the dry-run gate, resolving this would fail
			// loudly rather than silently succeeding.
			Launchctl: filepath.Join(t.TempDir(), "no-such-launchctl"),
		},
	}

	if err := Init(t.Context(), deps); err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}

	// Repo cloned to disk.
	out := runGitT(t, cfg.TargetRepo, "rev-parse", "--is-inside-work-tree")
	if strings.TrimSpace(out) != "true" {
		t.Errorf("localPath %s is not a git working tree after Init(), want it cloned", cfg.TargetRepo)
	}

	// Config written to disk.
	if _, err := os.Stat(configPath); err != nil {
		t.Errorf("Stat(config) error = %v, want the config written to disk", err)
	}

	// README markers ensured.
	readmeBytes, err := os.ReadFile(filepath.Join(cfg.TargetRepo, readmeFile))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	if _, err := readme.Inject(readmeBytes, "probe"); err != nil {
		t.Errorf("readme.Inject() after Init() error = %v, want nil (markers must be present)", err)
	}

	// The schedule-registration prompt must never be shown, and no install
	// ever attempted, regardless of PromptSchedule/Stdin.
	if strings.Contains(stdout.String(), "Register the refresh schedule now?") {
		t.Errorf("Init() Stdout = %q, want the schedule-registration prompt never shown in dry-run mode", stdout.String())
	}
	if strings.Contains(stdout.String(), "warning") {
		t.Errorf("Init() Stdout = %q, want no schedule-install warning either, since the prompt was never shown", stdout.String())
	}

	// No commit/push landed on the remote.
	verify2 := cloneWorkdir(t, remote, "dry-run-init-verify")
	log2 := runGitT(t, verify2, "log", "--oneline")
	if strings.Contains(log2, "token-profile") {
		t.Errorf("git log = %q, want no commit landed on the remote in dry-run mode", log2)
	}
}

// TestNewInitCmd_HasDryRunFlag covers R8: `init` must expose a --dry-run
// flag.
func TestNewInitCmd_HasDryRunFlag(t *testing.T) {
	cmd := NewInitCmd()
	if f := cmd.Flags().Lookup("dry-run"); f == nil {
		t.Error("NewInitCmd() does not register a --dry-run flag, want one (R8)")
	}
}
