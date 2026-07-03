package cli

import (
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
