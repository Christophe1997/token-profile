package cli

import (
	"bytes"
	"context"
	"errors"
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

// TestLoadOrScaffoldConfig_NoConfigFile_ScaffoldsAndReturnsGuidedError covers
// a first-time adopter: no config file exists yet at configPath, so
// loadOrScaffoldConfig must scaffold a starter template and return a guided
// error pointing at it, rather than the generic errTargetRepoMissing.
func TestLoadOrScaffoldConfig_NoConfigFile_ScaffoldsAndReturnsGuidedError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	_, err := loadOrScaffoldConfig(path)
	if err == nil {
		t.Fatal("loadOrScaffoldConfig() error = nil, want a guided scaffold error")
	}
	if !strings.Contains(err.Error(), "created a starter config") {
		t.Errorf("loadOrScaffoldConfig() error = %q, want it to mention %q", err.Error(), "created a starter config")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("loadOrScaffoldConfig() error = %q, want it to mention the config path %q", err.Error(), path)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() on scaffolded config error = %v, want nil", err)
	}
	if cfg.TargetRepo != "" {
		t.Errorf("scaffolded TargetRepo = %q, want empty", cfg.TargetRepo)
	}
	if cfg.Breakdown != config.BreakdownPerModel {
		t.Errorf("scaffolded Breakdown = %q, want %q", cfg.Breakdown, config.BreakdownPerModel)
	}
}

// TestLoadOrScaffoldConfig_ExistingBlankTargetRepo_ReturnsConfigUnmodified
// covers a deliberately-edited config that still has a blank targetRepo:
// loadOrScaffoldConfig must not scaffold over it, leaving the caller's
// existing errTargetRepoMissing check to fire unchanged.
func TestLoadOrScaffoldConfig_ExistingBlankTargetRepo_ReturnsConfigUnmodified(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.json", `{"breakdown":"per-model"}`)
	path := filepath.Join(dir, "config.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	cfg, err := loadOrScaffoldConfig(path)
	if err != nil {
		t.Fatalf("loadOrScaffoldConfig() error = %v, want nil", err)
	}
	if cfg.TargetRepo != "" {
		t.Errorf("TargetRepo = %q, want empty", cfg.TargetRepo)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("config file changed: before %q, after %q", before, after)
	}
}

// TestLoadOrScaffoldConfig_RerunAfterScaffold_ReturnsBlankTargetRepoNotGuidedError
// covers re-running init after a first scaffold: the second call must find
// the now-existing file via stat and return the loaded config plainly,
// rather than scaffolding (and erroring) again.
func TestLoadOrScaffoldConfig_RerunAfterScaffold_ReturnsBlankTargetRepoNotGuidedError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	if _, err := loadOrScaffoldConfig(path); err == nil {
		t.Fatal("loadOrScaffoldConfig() first call error = nil, want the guided scaffold error")
	}

	cfg, err := loadOrScaffoldConfig(path)
	if err != nil {
		t.Fatalf("loadOrScaffoldConfig() second call error = %v, want nil", err)
	}
	if cfg.TargetRepo != "" {
		t.Errorf("TargetRepo = %q, want empty", cfg.TargetRepo)
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
		protocol string
		want     string
		wantErr  bool
	}{
		{cloneProtocolSSH, "git@github.com:octocat/octocat.git", false},
		{cloneProtocolHTTPS, "https://github.com/octocat/octocat.git", false},
		{"bogus", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.protocol, func(t *testing.T) {
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

// TestCloneProfileRepo_CreatesMissingParentDirs covers the fix: dest's
// parent directory (~/.token-profile/repos, on a genuinely fresh machine)
// won't exist yet, so cloneProfileRepo must create it before cloning.
func TestCloneProfileRepo_CreatesMissingParentDirs(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, unmarkedReadme)
	dest := filepath.Join(t.TempDir(), "nested", "missing", "octocat")

	if err := cloneProfileRepo(t.Context(), remote, dest); err != nil {
		t.Fatalf("cloneProfileRepo() error = %v, want nil", err)
	}

	out := runGitT(t, dest, "rev-parse", "--is-inside-work-tree")
	if strings.TrimSpace(out) != "true" {
		t.Errorf("git rev-parse --is-inside-work-tree = %q, want %q", out, "true")
	}
}

func TestCloneProfileRepo_BadURL_ReturnsError(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "octocat")

	if err := cloneProfileRepo(t.Context(), filepath.Join(t.TempDir(), "no-such-remote"), dest); err == nil {
		t.Fatal("cloneProfileRepo() error = nil, want an error for a nonexistent remote")
	}
}

func TestConfirmAutoClone(t *testing.T) {
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
			got := confirmAutoClone(strings.NewReader(tt.input), &stdout, "git@github.com:octocat/octocat.git", "/home/adopter/.token-profile/repos/octocat")
			if got != tt.want {
				t.Errorf("confirmAutoClone(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestConfirmAutoClone_PromptContainsURLAndDest(t *testing.T) {
	var stdout bytes.Buffer
	confirmAutoClone(strings.NewReader("n\n"), &stdout, "git@github.com:octocat/octocat.git", "/home/adopter/.token-profile/repos/octocat")

	got := stdout.String()
	if !strings.Contains(got, "git@github.com:octocat/octocat.git") {
		t.Errorf("confirmAutoClone() prompt = %q, want it to mention the clone URL", got)
	}
	if !strings.Contains(got, "/home/adopter/.token-profile/repos/octocat") {
		t.Errorf("confirmAutoClone() prompt = %q, want it to mention the destination", got)
	}
}

// failIfCalledGitUserName and failIfCalledClone build hooks for
// bootstrapDeps that fail the test if invoked — used across the
// bootstrapConfig tests below to prove the auto-clone shortcut's hooks
// stay untouched whenever it shouldn't apply.
func failIfCalledGitUserName(t *testing.T) func(ctx context.Context) string {
	t.Helper()
	return func(ctx context.Context) string {
		t.Fatal("GitUserName was called, want it untouched")
		return ""
	}
}

func failIfCalledClone(t *testing.T) func(ctx context.Context, url, dest string) error {
	t.Helper()
	return func(ctx context.Context, url, dest string) error {
		t.Fatal("Clone was called, want it untouched")
		return nil
	}
}

// TestBootstrapConfig_ConfigAlreadyExists_DelegatesToLoadOrScaffold covers
// the case where a config file already sits at ConfigPath (even with a
// blank targetRepo): bootstrapConfig must not offer the auto-clone
// shortcut at all, regardless of Interactive.
func TestBootstrapConfig_ConfigAlreadyExists_DelegatesToLoadOrScaffold(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.json", `{"breakdown":"per-model"}`)
	path := filepath.Join(dir, "config.json")

	cfg, err := bootstrapConfig(t.Context(), bootstrapDeps{
		ConfigPath:  path,
		Interactive: true,
		Stdin:       strings.NewReader(""),
		Stdout:      &bytes.Buffer{},
		GitUserName: failIfCalledGitUserName(t),
		Clone:       failIfCalledClone(t),
	})
	if err != nil {
		t.Fatalf("bootstrapConfig() error = %v, want nil", err)
	}
	if cfg.TargetRepo != "" {
		t.Errorf("TargetRepo = %q, want empty", cfg.TargetRepo)
	}
}

// TestBootstrapConfig_NonInteractive_FallsBack covers a scheduled
// cron/launchd invocation: no config file, but Interactive is false, so the
// shortcut must never engage.
func TestBootstrapConfig_NonInteractive_FallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	_, err := bootstrapConfig(t.Context(), bootstrapDeps{
		ConfigPath:  path,
		Interactive: false,
		Stdin:       strings.NewReader(""),
		Stdout:      &bytes.Buffer{},
		GitUserName: failIfCalledGitUserName(t),
		Clone:       failIfCalledClone(t),
	})
	if err == nil || !strings.Contains(err.Error(), "created a starter config") {
		t.Errorf("bootstrapConfig() error = %v, want the scaffold-and-guide error", err)
	}
}

// TestBootstrapConfig_NoGitUserName_FallsBack covers an interactive session
// where git's global user.name is unset.
func TestBootstrapConfig_NoGitUserName_FallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	_, err := bootstrapConfig(t.Context(), bootstrapDeps{
		ConfigPath:  path,
		Interactive: true,
		Stdin:       strings.NewReader(""),
		Stdout:      &bytes.Buffer{},
		GitUserName: func(ctx context.Context) string { return "" },
		Clone:       failIfCalledClone(t),
	})
	if err == nil || !strings.Contains(err.Error(), "created a starter config") {
		t.Errorf("bootstrapConfig() error = %v, want the scaffold-and-guide error", err)
	}
}

// TestBootstrapConfig_UnsafeGitUserName_FallsBack proves validAutoCloneName
// is actually wired into bootstrapConfig, not just unit-tested standalone.
func TestBootstrapConfig_UnsafeGitUserName_FallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	_, err := bootstrapConfig(t.Context(), bootstrapDeps{
		ConfigPath:  path,
		Interactive: true,
		Stdin:       strings.NewReader(""),
		Stdout:      &bytes.Buffer{},
		GitUserName: func(ctx context.Context) string { return "../escape" },
		Clone:       failIfCalledClone(t),
	})
	if err == nil || !strings.Contains(err.Error(), "created a starter config") {
		t.Errorf("bootstrapConfig() error = %v, want the scaffold-and-guide error", err)
	}
}

// TestBootstrapConfig_UserDeclines_FallsBackButShowsPrompt covers a
// declined confirmation: the shortcut must still fall back to the plain
// scaffold, but only after actually showing the prompt (proving the
// decline was a real choice, not a short-circuit before asking).
func TestBootstrapConfig_UserDeclines_FallsBackButShowsPrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	var stdout bytes.Buffer

	_, err := bootstrapConfig(t.Context(), bootstrapDeps{
		ConfigPath:    path,
		Interactive:   true,
		Stdin:         strings.NewReader("n\n"),
		Stdout:        &stdout,
		CloneProtocol: cloneProtocolSSH,
		GitUserName:   func(ctx context.Context) string { return "octocat" },
		Clone:         failIfCalledClone(t),
	})
	if err == nil || !strings.Contains(err.Error(), "created a starter config") {
		t.Errorf("bootstrapConfig() error = %v, want the scaffold-and-guide error", err)
	}
	if !strings.Contains(stdout.String(), "git@github.com:octocat/octocat.git") {
		t.Errorf("Stdout = %q, want the auto-clone prompt to have been shown", stdout.String())
	}
}

// TestBootstrapConfig_UserConfirms_ClonesAndWritesConfig covers the happy
// path: a confirmed auto-clone must write a config whose targetRepo is the
// clone destination, with a fully-populated MachineIDPath (pinning the
// bug where an earlier draft hand-built the returned Config and dropped
// it), and that config must already be on disk, not just in memory.
func TestBootstrapConfig_UserConfirms_ClonesAndWritesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(t.TempDir(), "config.json")

	var clonedURL, clonedDest string
	cfg, err := bootstrapConfig(t.Context(), bootstrapDeps{
		ConfigPath:    path,
		Interactive:   true,
		Stdin:         strings.NewReader("y\n"),
		Stdout:        &bytes.Buffer{},
		CloneProtocol: cloneProtocolSSH,
		GitUserName:   func(ctx context.Context) string { return "octocat" },
		Clone: func(ctx context.Context, url, dest string) error {
			clonedURL, clonedDest = url, dest
			return nil
		},
	})
	if err != nil {
		t.Fatalf("bootstrapConfig() error = %v, want nil", err)
	}

	wantDest := filepath.Join(home, ".token-profile", "repos", "octocat")
	if clonedURL != "git@github.com:octocat/octocat.git" {
		t.Errorf("Clone was called with url = %q, want %q", clonedURL, "git@github.com:octocat/octocat.git")
	}
	if clonedDest != wantDest {
		t.Errorf("Clone was called with dest = %q, want %q", clonedDest, wantDest)
	}
	if cfg.TargetRepo != wantDest {
		t.Errorf("TargetRepo = %q, want %q", cfg.TargetRepo, wantDest)
	}
	if cfg.MachineIDPath == "" {
		t.Error("MachineIDPath is empty, want it populated (bootstrapConfig must reload via config.Load, not hand-build a Config)")
	}

	onDisk, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if onDisk != cfg {
		t.Errorf("config.Load(path) = %+v, want it to match the returned config %+v (WriteTemplate must have actually landed on disk)", onDisk, cfg)
	}
}

// TestBootstrapConfig_CloneFails_FallsBackAndReports covers a clone
// failure (bad guess, no access, network error): the shortcut must fall
// back to the plain scaffold, after reporting the failure.
func TestBootstrapConfig_CloneFails_FallsBackAndReports(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	var stdout bytes.Buffer

	_, err := bootstrapConfig(t.Context(), bootstrapDeps{
		ConfigPath:    path,
		Interactive:   true,
		Stdin:         strings.NewReader("y\n"),
		Stdout:        &stdout,
		CloneProtocol: cloneProtocolSSH,
		GitUserName:   func(ctx context.Context) string { return "octocat" },
		Clone: func(ctx context.Context, url, dest string) error {
			return errors.New("clone failed: repository not found")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "created a starter config") {
		t.Errorf("bootstrapConfig() error = %v, want the scaffold-and-guide error", err)
	}
	if !strings.Contains(stdout.String(), "auto-clone failed") {
		t.Errorf("Stdout = %q, want it to report the clone failure", stdout.String())
	}
}

// TestBootstrapConfig_InvalidCloneProtocol_ErrorsBeforePrompting covers an
// invalid --clone-protocol value: it must error immediately, before the
// prompt is ever shown (Stdout untouched) or Clone is invoked.
func TestBootstrapConfig_InvalidCloneProtocol_ErrorsBeforePrompting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	var stdout bytes.Buffer

	_, err := bootstrapConfig(t.Context(), bootstrapDeps{
		ConfigPath:    path,
		Interactive:   true,
		Stdin:         strings.NewReader("y\n"),
		Stdout:        &stdout,
		CloneProtocol: "bogus",
		GitUserName:   func(ctx context.Context) string { return "octocat" },
		Clone:         failIfCalledClone(t),
	})
	if err == nil {
		t.Fatal("bootstrapConfig() error = nil, want an error for an invalid --clone-protocol")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("bootstrapConfig() error = %q, want it to mention the invalid protocol", err.Error())
	}
	if stdout.String() != "" {
		t.Errorf("Stdout = %q, want empty (prompt must never show before protocol validation)", stdout.String())
	}
}
