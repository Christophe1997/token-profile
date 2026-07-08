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

// TestNewInitCmd_HasRegisterScheduleFlag covers the agent-native escape
// hatch: an unattended re-run of `init` (no TTY) needs a way to opt into
// InstallSchedule without going through the interactive Y/N prompt.
func TestNewInitCmd_HasRegisterScheduleFlag(t *testing.T) {
	cmd := NewInitCmd()
	if f := cmd.Flags().Lookup("register-schedule"); f == nil {
		t.Error("NewInitCmd() does not register a --register-schedule flag, want one")
	}
}
